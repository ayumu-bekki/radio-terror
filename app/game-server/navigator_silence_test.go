package main

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// recordingSpeaker は呼び出しを記録する NavigatorSpeaker。
//
// fakeSpeaker と違い**ロックを持つ**。SilenceWatcher はゴルーチンから
// 発話するため、記録側を守らないと -race で落ちる。
type recordingSpeaker struct {
	mu       sync.Mutex
	triggers []string
	spoke    chan string
}

func newRecordingSpeaker() *recordingSpeaker {
	return &recordingSpeaker{spoke: make(chan string, 16)}
}

func (r *recordingSpeaker) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	r.mu.Lock()
	r.triggers = append(r.triggers, trigger)
	r.mu.Unlock()

	select {
	case r.spoke <- trigger:
	default:
	}
	return nil
}

func (r *recordingSpeaker) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.triggers)
}

func newTestWatcher(speaker NavigatorSpeaker, min, max time.Duration) *SilenceWatcher {
	return NewSilenceWatcher(speaker, NewBridgeRegistry(), nil, min, max,
		rand.New(rand.NewSource(1)))
}

func newSilenceTestSession() *GameSession {
	return &GameSession{
		SessionID: "s-silence",
		DeviceID:  "core-1",
		BridgeID:  "bridge-1",
	}
}

// TestSilenceWatcherSpeaksAfterNoReply は、応答が無いまま待ち時間を
// 超えたらナビゲーターが声を掛けることを確かめる。
func TestSilenceWatcherSpeaksAfterNoReply(t *testing.T) {
	speaker := newRecordingSpeaker()
	w := newTestWatcher(speaker, 30*time.Millisecond, 30*time.Millisecond)
	session := newSilenceTestSession()

	w.Start(context.Background(), session)
	defer w.Stop(session.DeviceID)

	select {
	case trigger := <-speaker.spoke:
		if trigger != "silence" {
			t.Errorf("trigger = %q, want %q", trigger, "silence")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("待ち時間を過ぎても声を掛けなかった")
	}
}

// TestSilenceWatcherResetsOnPlayerReply は、プレイヤーの発話で計測が
// やり直しになることを確かめる。
//
// **ここが本体**。応答があっても計測が続くと、喋っている最中に
// 「どうした?」と割り込む。無線は半二重なので割り込みは送信を潰す。
func TestSilenceWatcherResetsOnPlayerReply(t *testing.T) {
	speaker := newRecordingSpeaker()
	w := newTestWatcher(speaker, 120*time.Millisecond, 120*time.Millisecond)
	session := newSilenceTestSession()

	w.Start(context.Background(), session)
	defer w.Stop(session.DeviceID)

	// 待ち時間より短い間隔で喋り続ける間は、一度も声を掛けないはず
	deadline := time.Now().Add(360 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
		w.Notice(session.DeviceID)
	}

	if n := speaker.count(); n != 0 {
		t.Errorf("応答が続いている間に %d 回発話した (0 のはず)", n)
	}
}

// TestSilenceWatcherStopsAfterFinish は、終了したセッションでは
// 声を掛けないことを確かめる。
//
// 爆発・解除の最終メッセージのあとに「どうした?」と続くと、
// 締めた交信が台無しになる。
func TestSilenceWatcherStopsAfterFinish(t *testing.T) {
	speaker := newRecordingSpeaker()
	w := newTestWatcher(speaker, 30*time.Millisecond, 30*time.Millisecond)
	session := newSilenceTestSession()

	session.mu.Lock()
	session.Finished = true
	session.mu.Unlock()

	w.Start(context.Background(), session)
	defer w.Stop(session.DeviceID)

	time.Sleep(200 * time.Millisecond)
	if n := speaker.count(); n != 0 {
		t.Errorf("終了済みセッションで %d 回発話した (0 のはず)", n)
	}
}

// TestSilenceWatcherStopEndsWatch は Stop 後に発話しないことを確かめる。
func TestSilenceWatcherStopEndsWatch(t *testing.T) {
	speaker := newRecordingSpeaker()
	w := newTestWatcher(speaker, 40*time.Millisecond, 40*time.Millisecond)
	session := newSilenceTestSession()

	w.Start(context.Background(), session)
	w.Stop(session.DeviceID)

	time.Sleep(200 * time.Millisecond)
	if n := speaker.count(); n != 0 {
		t.Errorf("停止後に %d 回発話した (0 のはず)", n)
	}
}

// TestSilenceWatcherNextWaitInRange は待ち時間が設定の幅に収まることを確かめる。
//
// **幅から引く**のが要点。固定間隔だと「一定時間黙ると必ず鳴る」と読まれる。
func TestSilenceWatcherNextWaitInRange(t *testing.T) {
	min := 40 * time.Second
	max := 60 * time.Second
	w := newTestWatcher(newRecordingSpeaker(), min, max)

	varied := false
	first := w.nextWait()
	for i := 0; i < 200; i++ {
		got := w.nextWait()
		if got < min || got > max {
			t.Fatalf("nextWait() = %v, want %v..%v", got, min, max)
		}
		if got != first {
			varied = true
		}
	}
	if !varied {
		t.Error("待ち時間が毎回同じだった (幅から引けていない)")
	}
}

// TestSilenceWatcherNilSafe は未設定 (nil) でも落ちないことを確かめる。
// 設定を持たない構成やテストから呼ばれる。
func TestSilenceWatcherNilSafe(t *testing.T) {
	var w *SilenceWatcher
	w.Start(context.Background(), newSilenceTestSession())
	w.Notice("core-1")
	w.Stop("core-1")
}

// TestNavigatorSilenceConfigDefaults は prompt.toml の既定値を確かめる。
func TestNavigatorSilenceConfigDefaults(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	if cfg.Prompt.SilenceMinMS != 40000 {
		t.Errorf("silence_min_ms = %d, want 40000", cfg.Prompt.SilenceMinMS)
	}
	if cfg.Prompt.SilenceMaxMS != 60000 {
		t.Errorf("silence_max_ms = %d, want 60000", cfg.Prompt.SilenceMaxMS)
	}
	if cfg.Prompt.Triggers["silence"] == "" {
		t.Error("silence トリガーの指示が空")
	}
}
