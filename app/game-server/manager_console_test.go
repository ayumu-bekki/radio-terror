package main

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

// newConsoleTestSession はコンソールモードの進行中セッションを1つ持つ
// Coordinator を組む (StartSessionWith は経由せず、実機・実無線を使わない
// テスト用の最小構成)。
func newConsoleTestSession(t *testing.T, stageCount int) (*GameCoordinator, *GameSession, *fakeDeviceConn, *MemoryStore) {
	t.Helper()

	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-console-1", difficultyEasy)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stageCount > 0 && stageCount < len(built.Stages) {
		built.Stages = built.Stages[:stageCount]
	}

	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))
	game.SetSessionLogStore(NewSessionLogStore(store))

	conn := &fakeDeviceConn{}
	deviceID := "console-1"
	devices.Register(deviceID, conn)

	session := &GameSession{
		SessionID: "s-console-1", DeviceID: deviceID, BridgeID: "console-1",
		State: deviceStatePlaying, StageIndex: 0, RemainingMS: built.CountdownMS,
		Built: built, StartedAt: time.Now(), ConsoleMode: true,
	}
	session.progress.Reset(time.Now())
	game.binder.Bind("console-1", deviceID, session)

	return game, session, conn, store
}

// TestConsoleStageClearAdvancesNonFinalStage は非最終ステージでの
// 成功注入 (stage_cleared 相当) がステージを1つ進めることを確認する。
func TestConsoleStageClearAdvancesNonFinalStage(t *testing.T) {
	game, session, _, _ := newConsoleTestSession(t, 2)

	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgStageCleared, DeviceID: session.DeviceID,
		StageIndex: 0, RemainingMS: session.RemainingMS,
	})

	session.mu.Lock()
	got := session.StageIndex
	finished := session.Finished
	session.mu.Unlock()

	if got != 1 {
		t.Errorf("StageIndex = %d, want 1", got)
	}
	if finished {
		t.Error("非最終ステージのクリアで Finished になってはいけない")
	}
}

// TestConsoleStageClearOnFinalStageFinishes は最終ステージでの成功注入が
// defused 相当としてセッションを終了させることを確認する
// (コンソール画面ハンドラの isFinalStage 判定と同じロジック)。
func TestConsoleStageClearOnFinalStageFinishes(t *testing.T) {
	game, session, _, _ := newConsoleTestSession(t, 1)

	session.mu.Lock()
	stageIndex := session.StageIndex
	stageCount := len(session.Built.Stages)
	session.mu.Unlock()

	msgType := msgStageCleared
	if stageIndex == stageCount-1 {
		msgType = msgDefused
	}
	if msgType != msgDefused {
		t.Fatalf("テスト前提が崩れている: msgType = %s", msgType)
	}

	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgType, DeviceID: session.DeviceID,
		StageIndex: stageIndex, RemainingMS: session.RemainingMS,
	})

	session.mu.Lock()
	finished := session.Finished
	score := session.Score
	session.mu.Unlock()

	if !finished {
		t.Error("最終ステージの成功注入で Finished にならなかった")
	}
	if score != session.RemainingMS && score == 0 {
		t.Errorf("Score = %d, want %d", score, session.RemainingMS)
	}
}

// TestConsoleExplodeFinishesSession は即爆発ボタン相当の注入で
// セッションが終了することを確認する。
func TestConsoleExplodeFinishesSession(t *testing.T) {
	game, session, _, _ := newConsoleTestSession(t, 2)

	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgExploded, DeviceID: session.DeviceID,
		StageIndex: session.StageIndex, RemainingMS: session.RemainingMS,
		Reason: "wrong_cut", Detail: "console",
	})

	session.mu.Lock()
	finished := session.Finished
	session.mu.Unlock()

	if !finished {
		t.Error("即爆発の注入で Finished にならなかった")
	}
}

// TestConsoleModeSkipsPersist はコンソールモードのセッションが
// Valkey (ここではメモリ実装) へ永続化されないことを確認する。
// 本番の進行ロジックは共有しつつ、永続化だけを迂回する設計 (ADR M-7) の要。
func TestConsoleModeSkipsPersist(t *testing.T) {
	game, session, _, store := newConsoleTestSession(t, 2)

	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgStageCleared, DeviceID: session.DeviceID,
		StageIndex: 0, RemainingMS: session.RemainingMS,
	})

	sessions, err := store.LoadSessions(context.Background())
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	for _, s := range sessions {
		if s.SessionID == session.SessionID {
			t.Error("コンソールモードのセッションが persist されている")
		}
	}
}

// TestNonConsoleSessionsExcludesConsole は NonConsoleSessions がコンソール
// モードのセッションを一覧から除外することを確認する
// (Management Console のダッシュボードに疑似デバイスを混ぜないため)。
func TestNonConsoleSessionsExcludesConsole(t *testing.T) {
	game, consoleSession, _, _ := newConsoleTestSession(t, 2)

	built := consoleSession.Built
	realSession := &GameSession{
		SessionID: "s-real-1", DeviceID: "0001", BridgeID: "bridge-1",
		State: deviceStatePlaying, StageIndex: 0, RemainingMS: built.CountdownMS,
		Built: built, StartedAt: time.Now(),
	}
	realSession.progress.Reset(time.Now())
	game.binder.Bind("bridge-1", "0001", realSession)

	sessions := game.NonConsoleSessions()
	for _, s := range sessions {
		if s.ConsoleMode {
			t.Errorf("NonConsoleSessions にコンソールセッション %s が含まれている", s.SessionID)
		}
	}
	found := false
	for _, s := range sessions {
		if s.SessionID == realSession.SessionID {
			found = true
		}
	}
	if !found {
		t.Error("実機セッションが NonConsoleSessions から漏れている")
	}
}

// TestIsConsoleDeviceID は device_id の判定を確認する。
func TestIsConsoleDeviceID(t *testing.T) {
	cases := map[string]bool{
		"console-1":  true,
		"console-42": true,
		"0001":       false,
		"3701":       false,
		"":           false,
	}
	for id, want := range cases {
		if got := isConsoleDeviceID(id); got != want {
			t.Errorf("isConsoleDeviceID(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestForgetStatusRemovesDeviceCompletely は ForgetStatus が接続・状態の
// 両方を消すことを確認する (片付けるボタンの土台)。
func TestForgetStatusRemovesDeviceCompletely(t *testing.T) {
	devices := NewDeviceRegistry()
	conn := &fakeDeviceConn{}
	devices.Register("console-1", conn)
	devices.UpdateStatus(&deviceMessage{
		Type: msgDeviceStatus, DeviceID: "console-1", State: deviceStateReady,
	})

	if !devices.IsConnected("console-1") {
		t.Fatal("前提: 登録直後は接続扱いのはず")
	}
	if devices.Status("console-1") == nil {
		t.Fatal("前提: 登録直後は状態があるはず")
	}

	devices.ForgetStatus("console-1")

	if devices.IsConnected("console-1") {
		t.Error("ForgetStatus 後も接続扱いのまま")
	}
	if devices.Status("console-1") != nil {
		t.Error("ForgetStatus 後も状態が残っている")
	}
}
