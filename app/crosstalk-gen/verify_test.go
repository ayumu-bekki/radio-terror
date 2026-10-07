package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// 合成PCM (440Hz正弦波1秒) をエンコードして Ogg Opus になることを確認する。
func TestEncodeProducesValidOggOpus(t *testing.T) {
	pcm := make([]int16, sampleRate)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
	}
	data, err := encodePCMToOggOpus(pcm, 16000)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if string(data[:4]) != "OggS" {
		t.Errorf("magic = %q, want OggS", data[:4])
	}
	if !strings.Contains(string(data[:200]), "OpusHead") {
		t.Error("OpusHead not found")
	}
	t.Logf("encoded %d bytes from 1.0s PCM", len(data))
}

func TestEncodeRejectsTooShort(t *testing.T) {
	if _, err := encodePCMToOggOpus(make([]int16, 10), 16000); err == nil {
		t.Error("want error for sub-frame PCM")
	}
}

func TestBuildWAVRoundTrip(t *testing.T) {
	pcm := []int16{100, -200, 300, -400}
	got, err := parsePCM16(stripWAVHeader(buildWAV(pcm)))
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if len(got) != len(pcm) {
		t.Fatalf("len = %d, want %d", len(got), len(pcm))
	}
	for i := range pcm {
		if got[i] != pcm[i] {
			t.Errorf("sample %d = %d, want %d", i, got[i], pcm[i])
		}
	}
}

// 設定から展開したファイル名が docs の規約どおりか。
func TestJobsMatchAssetConvention(t *testing.T) {
	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	jobs, err := cfg.BuildJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	count := map[string]int{}
	for _, j := range jobs {
		count[j.Category]++
	}
	for cat, want := range map[string]int{catJamming: 15, catAmbient: 13, catUneasy: 2} {
		if count[cat] != want {
			t.Errorf("%s = %d, want %d", cat, count[cat], want)
		}
	}

	// jamming は {name}_{A-E} でなければサーバーが色を解釈できない
	valid := map[string]bool{"A": true, "B": true, "C": true, "D": true, "E": true}
	for _, j := range jobs {
		if j.Category != catJamming {
			continue
		}
		i := strings.LastIndex(j.Name, "_")
		if i < 0 || !valid[j.Name[i+1:]] {
			t.Errorf("jamming %q: 色サフィックスが不正", j.Name)
		}
	}

	// イベント駆動再生で名前指定される固定ファイル
	found := false
	for _, j := range jobs {
		if j.Category == catUneasy && j.Name == "uneasy_bessgenba" {
			found = true
		}
	}
	if !found {
		t.Error("uneasy_bessgenba が定義されていない (crosstalk.go が名前で参照する)")
	}

	// 非言語タグが閉じていること。
	// 閉じ忘れると TTS がタグを言葉として読み上げてしまう。
	for _, j := range jobs {
		if strings.Count(j.Text, "[") != strings.Count(j.Text, "]") {
			t.Errorf("%s: タグの括弧が対応していない: %q", j.Name, j.Text)
		}
		// 空タグ・全角括弧は読み上げ事故のもと
		if strings.Contains(j.Text, "[]") {
			t.Errorf("%s: 空のタグがある: %q", j.Name, j.Text)
		}
		if strings.ContainsAny(j.Text, "［］") {
			t.Errorf("%s: 全角の角括弧が使われている: %q", j.Name, j.Text)
		}
	}

	// タグを除いた本文が空にならないこと (タグだけの発話は無音になる)
	for _, j := range jobs {
		if strings.TrimSpace(stripTags(j.Text)) == "" {
			t.Errorf("%s: タグを除くと本文が空: %q", j.Name, j.Text)
		}
	}
}

func TestStripTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[whispering] ……赤だ……", "……赤だ……"},
		{"今すぐ[pause]切れ", "今すぐ切れ"},
		{"タグなし", "タグなし"},
		// タグ後の区切り空白は畳む (日本語なので語間空白は残さない)
		{"はやく! [shouting] 逃げろ", "はやく! 逃げろ"},
		{"[a] [b] 本文", "本文"},
	}
	for _, c := range cases {
		if got := strings.TrimSpace(stripTags(c.in)); got != c.want {
			t.Errorf("stripTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 混線の声が、ナビゲーター4キャラと疎通確認のカラスと重複していないこと。
//
// 重複すると「自称犯人がナビゲーターと同じ声で『ナビゲーターを信じるな』と言う」
// といった演出の破綻が起きる。混線は本物の声と区別がつくことが前提。
//
// **announce・reask・ending は対象外**。自動送信局アナウンスはカラス本人が名乗る放送なので、
// カラスと同じ声であることが正しい (TestAnnounceUsesCrowVoice で逆を担保する)。
// 聞き直しはナビゲーター本人の声で流すので、同じ声であることが正しい
// (TestReaskMatchesCharacters で担保する)。終幕も同じ (TestEndingMatchesCharacters)。
func TestVoicesDoNotCollideWithNavigators(t *testing.T) {
	protected := map[string]string{}

	// ナビゲーター: navigator/characters/*.toml の tts_voice
	chars, err := filepath.Glob("../game-server/navigator/characters/*.toml")
	if err != nil || len(chars) == 0 {
		t.Fatalf("キャラクター定義が見つからない: %v", err)
	}
	for _, path := range chars {
		var c struct {
			Name  string `toml:"name"`
			Voice string `toml:"tts_voice"`
		}
		if _, err := toml.DecodeFile(path, &c); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if c.Voice == "" {
			t.Errorf("%s: tts_voice が空", path)
			continue
		}
		protected[c.Voice] = "ナビゲーター:" + c.Name
	}

	// カラス: test_responder.go の定数
	src, err := os.ReadFile("../game-server/test_responder.go")
	if err != nil {
		t.Fatalf("read test_responder.go: %v", err)
	}
	m := regexp.MustCompile(`testResponderTTSVoice = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("testResponderTTSVoice が見つからない")
	}
	protected[string(m[1])] = "疎通確認:カラス"

	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if owner, ok := protected[cfg.Defaults.Voice]; ok {
		t.Errorf("defaults.voice=%q が %s と重複", cfg.Defaults.Voice, owner)
	}
	jobs, err := cfg.BuildJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, j := range jobs {
		// announce はカラス本人、reask・ending はナビゲーター本人の声で正しい
		if j.Category == catAnnounce || j.Category == catReask || j.Category == catEnding {
			continue
		}
		if owner, ok := protected[j.VoiceID]; ok {
			t.Errorf("%s/%s: voice=%q が %s と重複", j.Category, j.Name, j.VoiceID, owner)
		}
	}
}

// アナウンスの声が疎通確認のカラスと**一致していること**。
//
// 「こちらはカラス」と名乗る放送なので、疎通確認で応答するカラスと
// 同じ声でなければ別人に聞こえる。混線の重複禁止とは逆向きの要求で、
// 声を変えたときに気づけるようにテストで固定する。
func TestAnnounceUsesCrowVoice(t *testing.T) {
	src, err := os.ReadFile("../game-server/test_responder.go")
	if err != nil {
		t.Fatalf("read test_responder.go: %v", err)
	}
	m := regexp.MustCompile(`testResponderTTSVoice = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("testResponderTTSVoice が見つからない")
	}
	crowVoice := string(m[1])

	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	jobs, err := cfg.BuildJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var found int
	for _, j := range jobs {
		if j.Category != catAnnounce {
			continue
		}
		found++
		if j.VoiceID != crowVoice {
			t.Errorf("%s/%s: voice=%q だがカラスは %q。同一人物に聞こえなくなる",
				j.Category, j.Name, j.VoiceID, crowVoice)
		}
	}
	if found == 0 {
		t.Error("announce のジョブが1件も無い")
	}
}

// アナウンスのファイル名が game-server 側の参照と一致していること。
//
// announce.go の announceFile が名前で参照するため、
// 片方だけ変えると**無言でスキップ**され、鳴らない理由が分かりにくい。
func TestAnnounceFileNameMatchesServer(t *testing.T) {
	src, err := os.ReadFile("../game-server/announce.go")
	if err != nil {
		t.Fatalf("read announce.go: %v", err)
	}
	m := regexp.MustCompile(`announceFile = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("announceFile が見つからない")
	}
	want := strings.TrimSuffix(string(m[1]), ".ogg")

	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	jobs, err := cfg.BuildJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, j := range jobs {
		if j.Category == catAnnounce && j.Name == want {
			return
		}
	}
	t.Errorf("announce に %q が無い (announce.go は %q を読む)", want, string(m[1]))
}

func TestOnlyRejectsUnknownName(t *testing.T) {
	cfg, _ := LoadConfig("crosstalk.toml")
	jobs, _ := cfg.BuildJobs()
	if _, err := filterJobs(jobs, "typo_name", ""); err == nil {
		t.Error("存在しない名前は明示エラーにすべき")
	}
}

func TestWriteToRealAssetLayout(t *testing.T) {
	cfg, _ := LoadConfig("crosstalk.toml")
	jobs, _ := cfg.BuildJobs()
	dir := t.TempDir()
	pcm := make([]int16, sampleRate/2)
	data, _ := encodePCMToOggOpus(pcm, 16000)
	for _, j := range jobs {
		p := filepath.Join(dir, j.Category, j.Name+".ogg")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, data, 0o644)
	}
	for cat, want := range map[string]int{"jamming": 15, "ambient": 13, "uneasy": 2} {
		es, _ := os.ReadDir(filepath.Join(dir, cat))
		if len(es) != want {
			t.Errorf("%s: %d files, want %d", cat, len(es), want)
		}
	}
	t.Logf("asset tree written to %s", dir)
}

// 聞き直しの音声が、ナビゲーターの定義 (声・台詞) と一致していること。
//
// game-server は assets/reask/<キャラクターID>_<n>.ogg を、キャラクター定義の
// reask_lines の n 番目として再生し、会話ログへ同じ文を残す。声が違えば別人に
// 聞こえ、台詞が違えば会話ログと音声が食い違う。
func TestReaskMatchesCharacters(t *testing.T) {
	chars, err := filepath.Glob("../game-server/navigator/characters/*.toml")
	if err != nil || len(chars) == 0 {
		t.Fatalf("キャラクター定義が見つからない: %v", err)
	}

	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	jobs, err := cfg.BuildJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	reask := map[string]Job{}
	for _, j := range jobs {
		// crow_<n> はカラス (ナビゲーターではない)。台詞は game-server の
		// testResponderReaskLines と一致させ、game-server 側のテストが検査する。
		if j.Category == catReask && !strings.HasPrefix(j.Name, "crow_") {
			reask[j.Name] = j
		}
	}

	want := 0
	for _, path := range chars {
		var c struct {
			ID         string   `toml:"id"`
			Voice      string   `toml:"tts_voice"`
			ReaskLines []string `toml:"reask_lines"`
		}
		if _, err := toml.DecodeFile(path, &c); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if len(c.ReaskLines) == 0 {
			t.Errorf("%s: reask_lines が無い", c.ID)
		}
		for i, line := range c.ReaskLines {
			want++
			name := fmt.Sprintf("%s_%d", c.ID, i+1)
			j, ok := reask[name]
			if !ok {
				t.Errorf("crosstalk.toml の [[reask]] に %q が無い", name)
				continue
			}
			if j.VoiceID != c.Voice {
				t.Errorf("%s: voice=%q だがキャラクターは %q", name, j.VoiceID, c.Voice)
			}
			if j.Text != line {
				t.Errorf("%s: 台詞が違う\n  crosstalk.toml: %q\n  キャラクター定義: %q", name, j.Text, line)
			}
		}
	}
	if len(reask) != want {
		t.Errorf("reask のジョブ %d 件 != キャラクター定義の台詞 %d 件 (余分な定義が無いか)", len(reask), want)
	}
}

// 終幕の音声が、ナビゲーターの定義 (声・台詞) と一致していること。
//
// game-server は assets/ending/<キャラクターID>_<defused|exploded>_<n>.ogg を、
// キャラクター定義の defused_lines / exploded_lines の n 番目として再生し、
// 会話ログへ (表情タグを除いた) 同じ文を残す。声が違えば別人に聞こえ、台詞が
// 違えば会話ログと音声が食い違う。
func TestEndingMatchesCharacters(t *testing.T) {
	chars, err := filepath.Glob("../game-server/navigator/characters/*.toml")
	if err != nil || len(chars) == 0 {
		t.Fatalf("キャラクター定義が見つからない: %v", err)
	}

	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	jobs, err := cfg.BuildJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ending := map[string]Job{}
	for _, j := range jobs {
		if j.Category == catEnding {
			ending[j.Name] = j
		}
	}

	want := 0
	for _, path := range chars {
		var c struct {
			ID            string   `toml:"id"`
			Voice         string   `toml:"tts_voice"`
			DefusedLines  []string `toml:"defused_lines"`
			ExplodedLines []string `toml:"exploded_lines"`
		}
		if _, err := toml.DecodeFile(path, &c); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		for kind, lines := range map[string][]string{"defused": c.DefusedLines, "exploded": c.ExplodedLines} {
			if len(lines) != 3 {
				t.Errorf("%s: %s_lines が %d 本 (3本のはず)", c.ID, kind, len(lines))
			}
			for i, line := range lines {
				want++
				name := fmt.Sprintf("%s_%s_%d", c.ID, kind, i+1)
				j, ok := ending[name]
				if !ok {
					t.Errorf("crosstalk.toml の [[ending]] に %q が無い", name)
					continue
				}
				if j.VoiceID != c.Voice {
					t.Errorf("%s: voice=%q だがキャラクターは %q", name, j.VoiceID, c.Voice)
				}
				if j.Text != line {
					t.Errorf("%s: 台詞が違う\n  crosstalk.toml: %q\n  キャラクター定義: %q", name, j.Text, line)
				}
				// 「どうぞ」は応答を求める言葉。終幕では付けない (ADR 決定46)
				if strings.Contains(line, "どうぞ") {
					t.Errorf("%s: 終幕に「どうぞ」が入っている: %q", name, line)
				}
			}
		}
	}
	if len(ending) != want {
		t.Errorf("ending のジョブ %d 件 != キャラクター定義の台詞 %d 件 (余分な定義が無いか)", len(ending), want)
	}
}
