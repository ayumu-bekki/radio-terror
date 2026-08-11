package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"
	"time"
)

// fakeDeviceConn は送信されたメッセージを記録するだけの接続。
type fakeDeviceConn struct {
	sent []map[string]any
}

func (c *fakeDeviceConn) SendJSON(v any) error {
	// 実際の送信と同じく JSON 化を通す (型のタグ漏れもここで拾える)
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	c.sent = append(c.sent, decoded)
	return nil
}

// newDetonateTestFixture は進行中セッションを1つ持つ Coordinator を組む。
func newDetonateTestFixture(t *testing.T, state string) (*GameCoordinator, *fakeDeviceConn) {
	t.Helper()

	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))
	game.SetSessionLogStore(NewSessionLogStore(store))

	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)

	session := &GameSession{
		SessionID:   "s-1",
		DeviceID:    "0001",
		BridgeID:    "bridge-1",
		State:       state,
		StageIndex:  1,
		RemainingMS: 120000,
		StartedAt:   time.Now(),
	}
	game.binder.Bind("bridge-1", "0001", session)

	return game, conn
}

// 進行中セッションへ force_detonate が届くこと。
func TestForceDetonateSendsCommand(t *testing.T) {
	game, conn := newDetonateTestFixture(t, deviceStatePlaying)

	if err := game.ForceDetonate(context.Background(), "0001"); err != nil {
		t.Fatalf("ForceDetonate: %v", err)
	}

	if len(conn.sent) != 1 {
		t.Fatalf("送信数 = %d, want 1", len(conn.sent))
	}
	msg := conn.sent[0]
	if msg["type"] != msgForceDetonate {
		t.Errorf("type = %v, want %s", msg["type"], msgForceDetonate)
	}
	// 宛先の取り違えが誤爆発に直結するため device_id は必須 (§7.1)
	if msg["device_id"] != "0001" {
		t.Errorf("device_id = %v, want 0001", msg["device_id"])
	}

	// 「マネージャーが指示した」ことがログに残る
	entries := game.logs.Entries("s-1")
	found := false
	for _, e := range entries {
		if e.Event == EventForced {
			found = true
		}
	}
	if !found {
		t.Error("強制破裂の記録がログに無い")
	}
}

// セッションは解放しない。破裂後の exploded を受けて既存の演出が動くため。
func TestForceDetonateKeepsSession(t *testing.T) {
	game, _ := newDetonateTestFixture(t, deviceStatePlaying)

	if err := game.ForceDetonate(context.Background(), "0001"); err != nil {
		t.Fatalf("ForceDetonate: %v", err)
	}

	if game.sessionFor("0001") == nil {
		t.Error("セッションが解放されている (exploded を受け取れなくなる)")
	}
}

// Playing 以外では送信しない。届かないコマンドで記録だけ残るのを避ける。
func TestForceDetonateRejectsNonPlaying(t *testing.T) {
	for _, state := range []string{
		deviceStateSetup, deviceStateReady, deviceStateDefused, deviceStateExploded,
	} {
		game, conn := newDetonateTestFixture(t, state)

		if err := game.ForceDetonate(context.Background(), "0001"); err != errNotPlaying {
			t.Errorf("state=%s: err = %v, want errNotPlaying", state, err)
		}
		if len(conn.sent) != 0 {
			t.Errorf("state=%s: 送信してはいけない (%d件)", state, len(conn.sent))
		}
	}
}

// 進行中セッションが無ければ何もしない。
func TestForceDetonateWithoutSession(t *testing.T) {
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))

	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)

	if err := game.ForceDetonate(context.Background(), "0001"); err != errNoActiveSession {
		t.Errorf("err = %v, want errNoActiveSession", err)
	}
	if len(conn.sent) != 0 {
		t.Errorf("送信してはいけない (%d件)", len(conn.sent))
	}
}
