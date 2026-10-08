package main

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// preparingSpeaker は第一声を先に用意する NavigatorSpeaker。
type preparingSpeaker struct {
	mu         sync.Mutex
	events     []string
	prepareErr error
	prepareDur time.Duration
}

func (p *preparingSpeaker) note(e string) {
	p.mu.Lock()
	p.events = append(p.events, e)
	p.mu.Unlock()
}

func (p *preparingSpeaker) log() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

func (p *preparingSpeaker) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	p.note("speak:" + trigger)
	return nil
}

func (p *preparingSpeaker) Prepare(ctx context.Context, session *GameSession, trigger, event string) (*PreparedSpeech, error) {
	time.Sleep(p.prepareDur)
	p.note("prepare:" + trigger)
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	return &PreparedSpeech{trigger: trigger}, nil
}

func (p *preparingSpeaker) Play(sender *AudioSender, session *GameSession, prepared *PreparedSpeech) error {
	p.note("play:" + prepared.trigger)
	return nil
}

func newStartTestFixture(t *testing.T, speaker NavigatorSpeaker) (*GameCoordinator, *fakeDeviceConn, *AudioSender) {
	t.Helper()
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), builder, NewMemoryStore(),
		rand.New(rand.NewSource(1)))
	game.SetNavigatorConfig(&NavigatorConfig{Characters: []NavigatorCharacter{{ID: "owl", Name: "フクロウ"}}})
	game.SetNavigatorSpeaker(speaker)

	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)
	devices.UpdateStatus(&deviceMessage{Type: msgDeviceStatus, DeviceID: "0001", State: deviceStateReady})
	return game, conn, NewAudioSender(game.bridges, "bridge-1")
}

func sentTypes(conn *fakeDeviceConn) []string {
	var types []string
	for _, m := range conn.sent {
		if s, ok := m["type"].(string); ok {
			types = append(types, s)
		}
	}
	return types
}

// 第一声を先に用意し、**Core の session_accepted を受けてから**流すこと。
// 流す前に session_start が送られていること、用意が終わるまで session_start を送らないこと。
func TestFirstSpeechPlaysAfterSessionAccepted(t *testing.T) {
	speaker := &preparingSpeaker{prepareDur: 100 * time.Millisecond}
	game, conn, sender := newStartTestFixture(t, speaker)

	done := make(chan error, 1)
	go func() { done <- game.StartSession(context.Background(), sender, "0001", difficultyEasy) }()

	// 用意が終わるまで session_start は送られない
	time.Sleep(20 * time.Millisecond)
	for _, ty := range sentTypes(conn) {
		if ty == "session_start" {
			t.Fatal("第一声の用意前に session_start を送った")
		}
	}

	// session_start が送られたあと、accepted を返すまでは流れない
	deadline := time.Now().Add(countdownStartDelay + 2*time.Second)
	for time.Now().Before(deadline) {
		started := false
		for _, ty := range sentTypes(conn) {
			started = started || ty == "session_start"
		}
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	for _, e := range speaker.log() {
		if e == "play:session_start" {
			t.Fatal("session_accepted の前に第一声を流した")
		}
	}

	game.notifyAccepted("0001")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartSession: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accepted 後も StartSession が戻らない")
	}

	events := speaker.log()
	if events[len(events)-1] != "play:session_start" {
		t.Errorf("最後が第一声の再生になっていない: %v", events)
	}
}

// 第一声を用意できなかったときは**開始せず**、session_abort で Core を戻すこと。
func TestStartCancelledWhenFirstSpeechFails(t *testing.T) {
	speaker := &preparingSpeaker{prepareErr: errors.New("tts down")}
	game, conn, sender := newStartTestFixture(t, speaker)

	if err := game.StartSession(context.Background(), sender, "0001", difficultyEasy); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	types := sentTypes(conn)
	for _, ty := range types {
		if ty == "session_start" {
			t.Fatalf("第一声が無いのに session_start を送った: %v", types)
		}
	}
	if types[len(types)-1] != msgSessionAbort {
		t.Errorf("session_abort で戻していない: %v", types)
	}
	if game.sessionFor("0001") != nil {
		t.Error("取りやめたのにセッションが残っている")
	}
}

// 猶予のあと firstSpeechMaxWait を超えても用意できなければ、開始を取りやめること。
func TestStartCancelledWhenFirstSpeechTooSlow(t *testing.T) {
	old := firstSpeechMaxWait
	firstSpeechMaxWait = 200 * time.Millisecond
	defer func() { firstSpeechMaxWait = old }()

	speaker := &preparingSpeaker{prepareDur: countdownStartDelay + 5*time.Second}
	game, conn, sender := newStartTestFixture(t, speaker)

	if err := game.StartSession(context.Background(), sender, "0001", difficultyEasy); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	for _, ty := range sentTypes(conn) {
		if ty == "session_start" {
			t.Fatal("間に合わなかったのに session_start を送った")
		}
	}
	if game.sessionFor("0001") != nil {
		t.Error("取りやめたのにセッションが残っている")
	}
}
