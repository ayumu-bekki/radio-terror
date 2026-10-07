package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// writeReaskAssets は指定のファイル名で聞き直し音声 (1秒の無音) を作る。
func writeReaskAssets(t *testing.T, names ...string) string {
	t.Helper()

	assetDir := t.TempDir()
	dir := filepath.Join(assetDir, reaskAssetDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ogg, err := encodePCMToOggOpus(make([]int16, sampleRate))
	if err != nil {
		t.Fatalf("encodePCMToOggOpus: %v", err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), ogg, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return assetDir
}

var reaskTestOwl = NavigatorCharacter{
	ID: "owl", Name: "フクロウ",
	ReaskLines: []string{"こちらフクロウ。もう一度頼む。どうぞ", "こちらフクロウ。聞こえなかった。どうぞ"},
}

func newReaskTestSession(character NavigatorCharacter) *GameSession {
	return &GameSession{
		SessionID: "s1", DeviceID: "core-1", BridgeID: "bridge-1",
		Character: character,
	}
}

// 音声が揃ったキャラクターだけが読み込まれ、揃わなくても起動は止まらないこと。
func TestReaskLoadsOnlyExistingClips(t *testing.T) {
	heron := NavigatorCharacter{ID: "heron", Name: "アオサギ", ReaskLines: []string{"こちらアオサギ。どうぞ"}}
	assetDir := writeReaskAssets(t, "owl_1.ogg") // owl_2 と heron_1 は無い

	r := NewReaskPlayer(assetDir, []NavigatorCharacter{reaskTestOwl, heron}, nil, nil, rand.New(rand.NewSource(1)))
	if got := len(r.clips["owl"]); got != 1 {
		t.Errorf("owl clips = %d, want 1", got)
	}
	if got := len(r.clips["heron"]); got != 0 {
		t.Errorf("heron clips = %d, want 0", got)
	}

	// 音声が無いキャラクターでも Play が落ちず、false を返す
	if r.Play(NewAudioSender(NewBridgeRegistry(), "bridge-1"), newReaskTestSession(heron)) {
		t.Error("音声の無いキャラクターで再生できたことになっている")
	}
}

// 流したら、bridge へ音声が届き、会話ログへ同じ文が残り、無線が塞がること。
func TestReaskPlaysAndLogs(t *testing.T) {
	bridges := NewBridgeRegistry()
	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	logs := NewSessionLogStore(nil)
	crosstalk := NewCrosstalkScheduler(nil, bridges, rand.New(rand.NewSource(1)))
	assetDir := writeReaskAssets(t, "owl_1.ogg", "owl_2.ogg")
	r := NewReaskPlayer(assetDir, []NavigatorCharacter{reaskTestOwl}, logs, crosstalk, rand.New(rand.NewSource(1)))

	session := newReaskTestSession(reaskTestOwl)
	if !r.Play(NewAudioSender(bridges, "bridge-1"), session) {
		t.Fatal("Play が false")
	}

	select {
	case <-ch:
	default:
		t.Error("bridge へ音声が届いていない")
	}

	entries := logs.Entries("s1")
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Sender != "フクロウ" || e.Receiver != senderPlayer {
		t.Errorf("log entry = %+v, want フクロウ → プレイヤー", e)
	}
	if e.Message != reaskTestOwl.ReaskLines[0] && e.Message != reaskTestOwl.ReaskLines[1] {
		t.Errorf("log message = %q, want one of the reask lines", e.Message)
	}
	if crosstalk.BusyFor("core-1") <= 0 {
		t.Error("再生中なのに無線が塞がっていない (混線が被る)")
	}
}

// 直前の聞き直しが鳴っている間は重ねて流さないこと (書き起こしが続けて失敗した場合)。
func TestReaskDoesNotOverlap(t *testing.T) {
	bridges := NewBridgeRegistry()
	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	assetDir := writeReaskAssets(t, "owl_1.ogg", "owl_2.ogg")
	r := NewReaskPlayer(assetDir, []NavigatorCharacter{reaskTestOwl}, nil, nil, rand.New(rand.NewSource(1)))
	sender := NewAudioSender(bridges, "bridge-1")
	session := newReaskTestSession(reaskTestOwl)

	if !r.Play(sender, session) {
		t.Fatal("1回目が流れない")
	}
	if r.Play(sender, session) {
		t.Error("鳴っている最中に重ねて流れた")
	}

	// 再生が終わった後 (予定時刻を過ぎた後) は流せる。連続する場合は台詞を替える
	first := r.last["core-1"]
	r.mu.Lock()
	r.until["core-1"] = time.Now().Add(-time.Second)
	r.mu.Unlock()
	if !r.Play(sender, session) {
		t.Fatal("再生後に流れない")
	}
	if r.last["core-1"] == first {
		t.Error("同じ台詞が連続した")
	}
}

// 終了済み・コンソールモードのセッションには流さないこと。
func TestReaskSkipsFinishedAndConsole(t *testing.T) {
	bridges := NewBridgeRegistry()
	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	assetDir := writeReaskAssets(t, "owl_1.ogg", "owl_2.ogg")
	r := NewReaskPlayer(assetDir, []NavigatorCharacter{reaskTestOwl}, nil, nil, rand.New(rand.NewSource(1)))
	sender := NewAudioSender(bridges, "bridge-1")

	finished := newReaskTestSession(reaskTestOwl)
	finished.Finished = true
	if r.Play(sender, finished) {
		t.Error("終了済みのセッションへ流れた")
	}
	console := newReaskTestSession(reaskTestOwl)
	console.ConsoleMode = true
	if r.Play(sender, console) {
		t.Error("コンソールモードへ流れた")
	}
	select {
	case <-ch:
		t.Error("音声が送出されている")
	default:
	}
}

// nil の ReaskPlayer は何もしないこと (未配線でも落ちない)。
func TestReaskNilIsNoop(t *testing.T) {
	var r *ReaskPlayer
	if r.Play(NewAudioSender(NewBridgeRegistry(), "b"), newReaskTestSession(reaskTestOwl)) {
		t.Error("nil で true")
	}
}

// 実際のキャラクター定義の全員が、聞き直しの台詞を持つこと。
// 台詞が無いキャラクターは聞き直しが無言になる。
func TestEveryCharacterHasReaskLines(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	for _, c := range cfg.Characters {
		if len(c.ReaskLines) == 0 {
			t.Errorf("%s: reask_lines が無い", c.ID)
		}
		for i, line := range c.ReaskLines {
			if line == "" {
				t.Errorf("%s: reask_lines[%d] が空", c.ID, i)
			}
		}
	}
}

type droppingSpeaker struct{ err error }

func (d droppingSpeaker) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger string, event string) error {
	return d.err
}

// プレイヤー発話以外の発話 (開始・イベント・声掛け) で音声にできなくても、
// 従来どおりエラーにせず続行すること (聞き直しは流さない)。
func TestCoordinatorSpeakToleratesDroppedSpeech(t *testing.T) {
	c := &GameCoordinator{speaker: droppingSpeaker{err: errSpeechDropped}}
	if err := c.speak(context.Background(), nil, newReaskTestSession(reaskTestOwl), "session_start", ""); err != nil {
		t.Errorf("speak = %v, want nil", err)
	}

	other := &GameCoordinator{speaker: droppingSpeaker{err: os.ErrInvalid}}
	if err := other.speak(context.Background(), nil, newReaskTestSession(reaskTestOwl), "session_start", ""); err == nil {
		t.Error("他のエラーまで握りつぶしている")
	}
}

// 実際に置いてある聞き直し音声が、全キャラクターの台詞ぶん読み込めること。
// 抜けたキャラクターは聞き直しが無言になる。尺が極端に短い・長い音声
// (生成失敗・台詞の読み飛ばし) も見つける。
func TestReaskRealAssetsCoverEveryLine(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	r := NewReaskPlayer("assets", cfg.Characters, nil, nil, rand.New(rand.NewSource(1)))
	for _, c := range cfg.Characters {
		clips := r.clips[c.ID]
		if len(clips) != len(c.ReaskLines) {
			t.Errorf("%s: 音声 %d 本 != 台詞 %d 行 (assets/reask/%s_<n>.ogg が足りない)",
				c.ID, len(clips), len(c.ReaskLines), c.ID)
		}
		for i, clip := range clips {
			if clip.duration < 2*time.Second || clip.duration > 12*time.Second {
				t.Errorf("%s_%d.ogg: 尺 %v が想定外 (2〜12秒)", c.ID, i+1, clip.duration)
			}
		}
	}
}

// カラスの聞き直し: 実際に置いてある音声が台詞ぶん読み込めること。
// 抜けると、開始・リセットの失敗や疎通確認の失敗が無言になる。
func TestReaskRealAssetsCoverCrow(t *testing.T) {
	r := NewReaskPlayer("assets", nil, nil, nil, rand.New(rand.NewSource(1)))
	clips := r.clips[crowReaskID]
	if len(clips) != len(testResponderReaskLines) {
		t.Fatalf("カラス: 音声 %d 本 != 台詞 %d 行 (assets/reask/crow_<n>.ogg が足りない)",
			len(clips), len(testResponderReaskLines))
	}
	for i, clip := range clips {
		if clip.duration < 2*time.Second || clip.duration > 12*time.Second {
			t.Errorf("crow_%d.ogg: 尺 %v が想定外 (2〜12秒)", i+1, clip.duration)
		}
	}
}

// crosstalk.toml の [[reask]] crow_<n> が、カラスの台詞・声と一致していること。
// 声や台詞が違えば、会話ログ・疎通確認の声と食い違う。
func TestCrowReaskMatchesCrosstalkConfig(t *testing.T) {
	var cfg struct {
		Reask []struct {
			Name  string `toml:"name"`
			Voice string `toml:"voice"`
			Text  string `toml:"text"`
		} `toml:"reask"`
	}
	if _, err := toml.DecodeFile("../crosstalk-gen/crosstalk.toml", &cfg); err != nil {
		t.Fatalf("decode crosstalk.toml: %v", err)
	}
	got := map[string][2]string{}
	for _, v := range cfg.Reask {
		if strings.HasPrefix(v.Name, crowReaskID+"_") {
			got[v.Name] = [2]string{v.Voice, v.Text}
		}
	}
	if len(got) != len(testResponderReaskLines) {
		t.Errorf("crosstalk.toml の crow_* %d 件 != 台詞 %d 行", len(got), len(testResponderReaskLines))
	}
	for i, line := range testResponderReaskLines {
		name := fmt.Sprintf("%s_%d", crowReaskID, i+1)
		v, ok := got[name]
		if !ok {
			t.Errorf("crosstalk.toml の [[reask]] に %q が無い", name)
			continue
		}
		if v[0] != testResponderTTSVoice {
			t.Errorf("%s: voice=%q だがカラスは %q", name, v[0], testResponderTTSVoice)
		}
		if v[1] != line {
			t.Errorf("%s: 台詞が違う\n  crosstalk.toml: %q\n  testResponderReaskLines: %q", name, v[1], line)
		}
	}
}

// カラスの聞き直しは bridge ごとに管理し、鳴っている間は重ねない。
func TestPlayCrowDoesNotOverlap(t *testing.T) {
	assetDir := writeReaskAssets(t, "crow_1.ogg", "crow_2.ogg", "crow_3.ogg")
	r := NewReaskPlayer(assetDir, nil, nil, nil, rand.New(rand.NewSource(1)))
	if len(r.clips[crowReaskID]) != 3 {
		t.Fatalf("crow clips = %d, want 3", len(r.clips[crowReaskID]))
	}
	registry := NewBridgeRegistry()
	sender := NewAudioSender(registry, "bridge-1")
	// 送信先が無くても、2回目は「鳴っている間」で弾かれることは pick で確かめる
	if _, ok := r.pick("bridge:bridge-1", crowReaskID); !ok {
		t.Fatal("1回目は選べるはず")
	}
	r.until["bridge:bridge-1"] = time.Now().Add(time.Minute)
	if r.PlayCrow(sender) {
		t.Error("鳴っている間に重ねて流した")
	}
}
