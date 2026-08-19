package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeAnnounceAsset はテスト用のアナウンス音声を作る。
// 中身は本物の Ogg Opus にしておく (再生時間の算出が走るため)。
func writeAnnounceAsset(t *testing.T) string {
	t.Helper()

	assetDir := t.TempDir()
	dir := filepath.Join(assetDir, "announce")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1秒ぶんの無音を Ogg Opus にする
	pcm := make([]int16, sampleRate)
	ogg, err := encodePCMToOggOpus(pcm)
	if err != nil {
		t.Fatalf("encodePCMToOggOpus: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, announceFile), ogg, 0o644); err != nil {
		t.Fatal(err)
	}
	return assetDir
}

// 音声が未配置なら無効になること (起動は妨げない)。
//
// 混線アセットと同じ方針。無いなら黙ってスキップする方が運用しやすい。
func TestAnnounceDisabledWhenAssetMissing(t *testing.T) {
	s := NewAnnounceScheduler(t.TempDir(), NewSessionBinder(), NewBridgeRegistry(), time.Minute)
	if s.Enabled() {
		t.Error("音声が無いのに有効になっている")
	}

	// 無効でも broadcast が落ちないこと (Run は即 return する)
	s.broadcast()
}

// 音声があれば有効になること。
func TestAnnounceEnabledWhenAssetPresent(t *testing.T) {
	s := NewAnnounceScheduler(writeAnnounceAsset(t), NewSessionBinder(), NewBridgeRegistry(), time.Minute)
	if !s.Enabled() {
		t.Fatal("音声を置いたのに無効のまま")
	}
	if s.Interval() != time.Minute {
		t.Errorf("Interval() = %v, want 1m", s.Interval())
	}
}

// 未設定・0以下の周期は既定値 (15分) に倒れること。
func TestAnnounceIntervalDefault(t *testing.T) {
	assetDir := writeAnnounceAsset(t)
	for _, given := range []time.Duration{0, -time.Minute} {
		s := NewAnnounceScheduler(assetDir, NewSessionBinder(), NewBridgeRegistry(), given)
		if s.Interval() != announceIntervalDefault {
			t.Errorf("Interval(%v) = %v, want %v", given, s.Interval(), announceIntervalDefault)
		}
	}

	// 設定側 (分指定) も同じ既定値に倒れること
	if got := (AnnounceConfig{}).Interval(); got != announceIntervalDefault {
		t.Errorf("AnnounceConfig{}.Interval() = %v, want %v", got, announceIntervalDefault)
	}
	if got := (AnnounceConfig{IntervalMin: 5}).Interval(); got != 5*time.Minute {
		t.Errorf("IntervalMin=5 => %v, want 5m", got)
	}
}

// **体験中の無線には流さないこと。** これが最も重要な性質。
//
// セッションが紐づいた bridge へ自動送信が割り込むと、カウントダウン中の
// 体験を壊す (ADR N-26 と同じ理由 — 装置の前にいるプレイヤーの邪魔をしない)。
func TestAnnounceSkipsBoundBridge(t *testing.T) {
	binder := NewSessionBinder()
	bridges := NewBridgeRegistry()
	s := NewAnnounceScheduler(writeAnnounceAsset(t), binder, bridges, time.Minute)

	ch := bridges.Register("bridge-playing")
	defer bridges.Unregister("bridge-playing", ch)

	// セッションをバインドする = 体験中
	binder.Bind("bridge-playing", "core-1", &GameSession{
		SessionID: "s1", DeviceID: "core-1", BridgeID: "bridge-playing",
	})

	if s.shouldSend("bridge-playing") {
		t.Error("体験中の bridge へ送ろうとしている")
	}

	s.broadcast()
	select {
	case got := <-ch:
		t.Errorf("体験中の bridge に音声が届いた (%d bytes)", len(got.Data))
	default:
	}
}

// 未バインドの bridge には流すこと。
func TestAnnounceSendsToIdleBridge(t *testing.T) {
	binder := NewSessionBinder()
	bridges := NewBridgeRegistry()
	s := NewAnnounceScheduler(writeAnnounceAsset(t), binder, bridges, time.Minute)

	ch := bridges.Register("bridge-idle")
	defer bridges.Unregister("bridge-idle", ch)

	if !s.shouldSend("bridge-idle") {
		t.Fatal("未バインドの bridge に送れる判定になっていない")
	}

	s.broadcast()
	select {
	case got := <-ch:
		if len(got.Data) == 0 {
			t.Error("空の音声が送られた")
		}
	default:
		t.Error("未バインドの bridge に音声が届いていない")
	}
}

// 体験中と待機中が混在していても、待機中にだけ流すこと。
func TestAnnounceSendsOnlyToIdleAmongMany(t *testing.T) {
	binder := NewSessionBinder()
	bridges := NewBridgeRegistry()
	s := NewAnnounceScheduler(writeAnnounceAsset(t), binder, bridges, time.Minute)

	idle := bridges.Register("bridge-idle")
	defer bridges.Unregister("bridge-idle", idle)
	playing := bridges.Register("bridge-playing")
	defer bridges.Unregister("bridge-playing", playing)

	binder.Bind("bridge-playing", "core-1", &GameSession{
		SessionID: "s1", DeviceID: "core-1", BridgeID: "bridge-playing",
	})

	s.broadcast()

	select {
	case <-idle:
	default:
		t.Error("待機中の bridge に届いていない")
	}
	select {
	case <-playing:
		t.Error("体験中の bridge に届いてしまった")
	default:
	}
}

// 直前に送った音声を再生中なら、次の周期まで見送ること。
//
// アナウンスは急ぐ性質のものではないので、重ねるより待つ。
func TestAnnounceSkipsWhileStillPlaying(t *testing.T) {
	bridges := NewBridgeRegistry()
	s := NewAnnounceScheduler(writeAnnounceAsset(t), NewSessionBinder(), bridges, time.Minute)

	ch := bridges.Register("bridge-idle")
	defer bridges.Unregister("bridge-idle", ch)

	s.broadcast()
	<-ch // 1回目は届く

	if s.shouldSend("bridge-idle") {
		t.Error("再生中なのに送れる判定になっている")
	}

	s.broadcast()
	select {
	case got := <-ch:
		t.Errorf("再生中に重ねて送ってしまった (%d bytes)", len(got.Data))
	default:
	}
}

// 再生が終われば再び送れること。
func TestAnnounceResumesAfterPlayback(t *testing.T) {
	bridges := NewBridgeRegistry()
	s := NewAnnounceScheduler(writeAnnounceAsset(t), NewSessionBinder(), bridges, time.Minute)

	ch := bridges.Register("bridge-idle")
	defer bridges.Unregister("bridge-idle", ch)

	s.broadcast()
	<-ch

	// 再生終了予定を過去へ倒す (実時間を待たない)
	s.mu.Lock()
	s.busyUntil["bridge-idle"] = time.Now().Add(-time.Second)
	s.mu.Unlock()

	if !s.shouldSend("bridge-idle") {
		t.Fatal("再生が終わったのに送れないままになっている")
	}

	s.broadcast()
	select {
	case <-ch:
	default:
		t.Error("再生終了後に送れていない")
	}
}

// セッションが終わって解放されたら、また流れるようになること。
//
// Release はバインド (bridge → device) を残しセッションだけ消すため、
// SessionForBridge が nil を返すことを確認する。
func TestAnnounceResumesAfterSessionRelease(t *testing.T) {
	binder := NewSessionBinder()
	bridges := NewBridgeRegistry()
	s := NewAnnounceScheduler(writeAnnounceAsset(t), binder, bridges, time.Minute)

	ch := bridges.Register("bridge-1")
	defer bridges.Unregister("bridge-1", ch)

	binder.Bind("bridge-1", "core-1", &GameSession{
		SessionID: "s1", DeviceID: "core-1", BridgeID: "bridge-1",
	})
	if s.shouldSend("bridge-1") {
		t.Fatal("体験中に送れる判定になっている")
	}

	binder.Release("core-1")
	if !s.shouldSend("bridge-1") {
		t.Error("セッション解放後も送れないままになっている")
	}
}

// 配置済みのアナウンス音声が**サーバー側のデコーダで正しく読めること**。
//
// crosstalk-gen が報告する秒数と oggOpusDuration の結果がずれていたら、
// granule position の基準レートを疑う (ADR T-7)。実際に 24kHz で書いていた
// バグがあり、実尺の半分と誤認していた。
//
// アセット未配置の環境ではスキップする (CI・他の開発者の手元)。
func TestAnnounceAssetDecodes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("assets", "announce", announceFile))
	if err != nil {
		t.Skipf("アセット未配置: %v", err)
	}

	d := oggOpusDuration(data)
	t.Logf("%s: %d bytes, %.2fs", announceFile, len(data), d.Seconds())

	if d <= 0 {
		t.Fatal("再生時間が算出できない (Ogg として壊れている)")
	}
	// 送出周期に対して十分短いこと。ここを超えるなら文言か生成を疑う。
	if d >= announceIntervalDefault {
		t.Errorf("再生時間 %.1fs が送出周期 %v 以上", d.Seconds(), announceIntervalDefault)
	}
	// 無線を長時間占有しないこと。文言は2文なので1分もあれば十分に収まる。
	if d > time.Minute {
		t.Errorf("長すぎる (%.1fs)。無線を占有しすぎる", d.Seconds())
	}
}
