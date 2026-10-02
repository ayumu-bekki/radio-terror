package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var endingTestOwl = NavigatorCharacter{
	ID: "owl", Name: "フクロウ",
	DefusedLines: []string{
		"[relieved] 止まったな。見事だ。",
		"[warm] 装置は止まった。無事で何よりだ。",
		"[relieved] ……止まった。よくやった。",
	},
	ExplodedLines: []string{
		"[urgent] おい、返事をしろ! 現場へ向かう。",
		"[urgent] どうした! 聞こえているなら応答しろ。",
		"[tense] 返事をしろ! 救護を回す。",
	},
}

func endingTestLines(kind string) []string {
	if kind == endingDefused {
		return endingTestOwl.DefusedLines
	}
	return endingTestOwl.ExplodedLines
}

// writeEndingAssets は終幕音声 (1秒の無音) と効果音を作り、アセットのルートを返す。
// 効果音は success.ogg / failure.ogg (0.5秒) を置く。
func writeEndingAssets(t *testing.T, c NavigatorCharacter) string {
	t.Helper()

	assetDir := t.TempDir()
	for _, d := range []string{endingAssetDir, "sfx"} {
		if err := os.MkdirAll(filepath.Join(assetDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	speech, err := encodePCMToOggOpus(make([]int16, sampleRate))
	if err != nil {
		t.Fatalf("encodePCMToOggOpus: %v", err)
	}
	for kind, lines := range map[string][]string{endingDefused: c.DefusedLines, endingExploded: c.ExplodedLines} {
		for i := range lines {
			name := strings.Join([]string{c.ID, kind, string(rune('1' + i))}, "_") + ".ogg"
			if err := os.WriteFile(filepath.Join(assetDir, endingAssetDir, name), speech, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	sfx, err := encodePCMToOggOpus(make([]int16, sampleRate/2))
	if err != nil {
		t.Fatalf("encodePCMToOggOpus: %v", err)
	}
	for _, name := range []string{sfxSuccessFile, sfxFailureFile} {
		if err := os.WriteFile(filepath.Join(assetDir, "sfx", name), sfx, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return assetDir
}

func newEndingTestSession() *GameSession {
	return &GameSession{
		SessionID: "s1", DeviceID: "core-1", BridgeID: "bridge-1",
		Character: endingTestOwl,
	}
}

// 解除成功と爆発で、それぞれのキャラクターの台詞が読み込まれること。
// 揃っていなくても起動は止まらないこと。
func TestEndingLoadsClipsPerKind(t *testing.T) {
	heron := NavigatorCharacter{ID: "heron", Name: "アオサギ",
		DefusedLines: []string{"[calm] 解除を確認しました。"}} // 音声は無い
	assetDir := writeEndingAssets(t, endingTestOwl)

	e := NewEndingPlayer(assetDir, []NavigatorCharacter{endingTestOwl, heron}, nil, nil, rand.New(rand.NewSource(1)))
	if got := len(e.clips["owl"][endingDefused]); got != 3 {
		t.Errorf("owl defused = %d, want 3", got)
	}
	if got := len(e.clips["owl"][endingExploded]); got != 3 {
		t.Errorf("owl exploded = %d, want 3", got)
	}
	if got := len(e.clips["heron"][endingDefused]); got != 0 {
		t.Errorf("heron defused = %d, want 0 (音声なし)", got)
	}
	// 会話ログ用の本文は表情タグを除いてある
	for _, c := range e.clips["owl"][endingDefused] {
		if strings.Contains(c.text, "[") {
			t.Errorf("本文に表情タグが残っている: %q", c.text)
		}
	}
}

// 効果音と台詞が1本に連結されて届き、会話ログへ残り、無線が塞がること。
func TestEndingPlaysSfxAndSpeechAsOne(t *testing.T) {
	bridges := NewBridgeRegistry()
	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	logs := NewSessionLogStore(nil)
	crosstalk := NewCrosstalkScheduler(nil, bridges, rand.New(rand.NewSource(1)))
	e := NewEndingPlayer(writeEndingAssets(t, endingTestOwl), []NavigatorCharacter{endingTestOwl},
		logs, crosstalk, rand.New(rand.NewSource(1)))

	if !e.Play(NewAudioSender(bridges, "bridge-1"), newEndingTestSession(), endingDefused) {
		t.Fatal("Play が false")
	}

	var sent outgoingAudio
	select {
	case sent = <-ch:
	default:
		t.Fatal("bridge へ音声が届いていない")
	}
	// 台詞1秒 + 効果音0.5秒 が連結されている (台詞だけなら1秒)
	if d := oggOpusDuration(sent.Data); d < 1400*time.Millisecond {
		t.Errorf("再生時間 %v — 効果音が連結されていない (1.5秒前後のはず)", d)
	}
	select {
	case <-ch:
		t.Error("音声が2本に分かれて届いた (効果音と台詞は1本にする)")
	default:
	}

	entries := logs.Entries("s1")
	if len(entries) != 1 || entries[0].Receiver != senderPlayer || strings.Contains(entries[0].Message, "[") {
		t.Errorf("会話ログ = %+v, want 表情タグなしの台詞が1件", entries)
	}
	if crosstalk.BusyFor("core-1") <= 0 {
		t.Error("再生中なのに無線が塞がっていない (混線が被る)")
	}
}

// 3本からランダムに選ばれ、解除成功と爆発で別の台詞が流れること。
func TestEndingPicksFromAllClipsOfTheKind(t *testing.T) {
	e := NewEndingPlayer(writeEndingAssets(t, endingTestOwl), []NavigatorCharacter{endingTestOwl},
		nil, nil, rand.New(rand.NewSource(1)))

	for kind, lines := range map[string][]string{endingDefused: endingTestOwl.DefusedLines, endingExploded: endingTestOwl.ExplodedLines} {
		valid := map[string]bool{}
		for _, l := range lines {
			valid[stripTTSTags(l)] = true
		}
		seen := map[string]bool{}
		for i := 0; i < 60; i++ {
			clip, ok := e.pick("owl", kind)
			if !ok {
				t.Fatalf("%s: pick できない", kind)
			}
			if !valid[clip.text] {
				t.Fatalf("%s: 別の種類の台詞が選ばれた: %q", kind, clip.text)
			}
			seen[clip.text] = true
		}
		if len(seen) != 3 {
			t.Errorf("%s: 60回引いて %d 種類しか出ない (3種類出るはず)", kind, len(seen))
		}
	}
}

// 台詞の音声が無いキャラクターでも、効果音だけは流れて落ちないこと。
func TestEndingFallsBackToSfxOnly(t *testing.T) {
	bridges := NewBridgeRegistry()
	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	// 効果音は置くが、台詞の音声は無い
	assetDir := writeEndingAssets(t, NavigatorCharacter{ID: "other"})
	e := NewEndingPlayer(assetDir, []NavigatorCharacter{endingTestOwl}, nil, nil, rand.New(rand.NewSource(1)))

	if e.Play(NewAudioSender(bridges, "bridge-1"), newEndingTestSession(), endingExploded) {
		t.Error("台詞が無いのに true を返した")
	}
	select {
	case sent := <-ch:
		if len(sent.Data) == 0 {
			t.Error("効果音が空")
		}
	default:
		t.Error("台詞が無いとき、効果音だけでも流れるはず")
	}
}

// コンソールモードは音声を送らず、会話ログへ台詞だけを残すこと。
func TestEndingConsoleModeLogsOnly(t *testing.T) {
	bridges := NewBridgeRegistry()
	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	logs := NewSessionLogStore(nil)
	e := NewEndingPlayer(writeEndingAssets(t, endingTestOwl), []NavigatorCharacter{endingTestOwl},
		logs, nil, rand.New(rand.NewSource(1)))
	session := newEndingTestSession()
	session.ConsoleMode = true

	e.Play(NewAudioSender(bridges, "bridge-1"), session, endingExploded)

	select {
	case <-ch:
		t.Error("コンソールモードなのに音声が送られた")
	default:
	}
	if got := len(logs.Entries("s1")); got != 1 {
		t.Errorf("会話ログ %d 件, want 1", got)
	}
}

// 本番のキャラクター定義が、全員 解除成功3本・爆発3本を持ち、終幕に「どうぞ」が
// 入っていないこと (交信を終える場面。ADR 決定46)。
func TestEndingLinesOfRealCharacters(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	if len(cfg.Characters) == 0 {
		t.Fatal("キャラクターが無い")
	}
	for _, c := range cfg.Characters {
		for kind, lines := range map[string][]string{endingDefused: c.DefusedLines, endingExploded: c.ExplodedLines} {
			if len(lines) != 3 {
				t.Errorf("%s: %s_lines が %d 本 (3本のはず)", c.ID, kind, len(lines))
			}
			for _, l := range lines {
				if strings.Contains(l, "どうぞ") {
					t.Errorf("%s/%s: 終幕に「どうぞ」: %q", c.ID, kind, l)
				}
				// 交信の締めくくりであって新しい呼びかけではない (名乗らない)
				if strings.Contains(l, "こちら") {
					t.Errorf("%s/%s: 終幕で名乗っている: %q", c.ID, kind, l)
				}
				// ナビは無線の向こうにいる。現場の様子・身体の被害は語れない (決定90)
				for _, w := range []string{"耳鳴り", "煙が"} {
					if strings.Contains(l, w) {
						t.Errorf("%s/%s: %q は現場にいないナビが言えない: %q", c.ID, kind, w, l)
					}
				}
				if kind == endingExploded {
					// 爆発は言わない (結果だけ告げる)。次がある前提・無事に終わった前提の
					// 言葉も使わない (ADR N-25)。ヒバリの「次は、どこに仕掛けようか」は
					// 黒幕の一言として意図したもので、「次は」は検査しない
					for _, w := range []string{"爆発", "お疲れ", "また挑戦", "再挑戦"} {
						if strings.Contains(l, w) {
							t.Errorf("%s/%s: 爆発の締めに %q: %q", c.ID, kind, w, l)
						}
					}
				}
				if !strings.HasPrefix(l, "[") {
					t.Errorf("%s/%s: 先頭に表情タグが無い: %q", c.ID, kind, l)
				}
				for _, tag := range extractTTSTags(l) {
					if !allowedTTSTags[tag] {
						t.Errorf("%s/%s: 未許可の表情タグ [%s]", c.ID, kind, tag)
					}
				}
			}
		}
	}
}

// 実際に置いてある終幕音声が、全キャラクターの台詞ぶん読み込めること。
// 抜けたキャラクターは終幕が効果音だけになる。尺が極端に短い・長い音声
// (生成失敗・台詞の読み飛ばし) も見つける。
//
// 効果音と連結できること (24kHz mono) も確かめる。連結できないと効果音が欠ける。
func TestEndingRealAssetsCoverEveryLine(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	e := NewEndingPlayer("assets", cfg.Characters, nil, nil, rand.New(rand.NewSource(1)))
	for _, c := range cfg.Characters {
		for kind, lines := range map[string][]string{endingDefused: c.DefusedLines, endingExploded: c.ExplodedLines} {
			clips := e.clips[c.ID][kind]
			if len(clips) != len(lines) {
				t.Errorf("%s/%s: 音声 %d 本 != 台詞 %d 行 (assets/ending/%s_%s_<n>.ogg が足りない)",
					c.ID, kind, len(clips), len(lines), c.ID, kind)
			}
			for i, clip := range clips {
				if clip.duration < 2*time.Second || clip.duration > 20*time.Second {
					t.Errorf("%s_%s_%d.ogg: 尺 %v が想定外 (2〜20秒)", c.ID, kind, i+1, clip.duration)
				}
				if _, err := decodeOggOpusToPCM(clip.data); err != nil {
					t.Errorf("%s_%s_%d.ogg: 効果音と連結できない: %v", c.ID, kind, i+1, err)
				}
			}
		}
	}
}

// extractTTSTags は発話に含まれる表情タグ名を取り出す。
func extractTTSTags(reply string) []string {
	tags := make([]string, 0)
	rest := reply
	for {
		open := strings.Index(rest, "[")
		if open < 0 {
			break
		}
		close := strings.Index(rest[open:], "]")
		if close < 0 {
			break
		}
		tags = append(tags, rest[open+1:open+close])
		rest = rest[open+close:]
	}
	return tags
}
