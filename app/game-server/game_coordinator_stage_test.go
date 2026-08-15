package main

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

// TestStageClearedAdvancesStageIndex は stage_cleared を受けたときに
// **次のステージ**を指すことを確かめる。
//
// stage_cleared の stage_index は「クリアした」ステージの番号で、デバイスは
// 送信後に AdvanceStage する (game_task.cc)。そのまま session.StageIndex へ
// 代入すると、ナビゲーターがクリア済みステージの briefing / hint_l1 を持って
// 喋ることになり、**新しい課題の導入ヒントが一度も出ない**
// (実運用で発生: ステージ2 ホールド&カットで「点灯・点滅しているランプが
// あることに気づかせる」L1ヒントが出ず、プレイヤーがボタンの存在に
// 気づけないまま時間切れになった)。
func TestStageClearedAdvancesStageIndex(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-1", difficultyEasy)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(built.Stages) < 2 {
		t.Fatalf("イージーは2ステージのはず: %d", len(built.Stages))
	}

	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))
	game.SetSessionLogStore(NewSessionLogStore(store))

	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)

	session := &GameSession{
		SessionID: "s-1", DeviceID: "0001", BridgeID: "bridge-1",
		State: deviceStatePlaying, StageIndex: 0, RemainingMS: 250000,
		Built: built, StartedAt: time.Now(),
	}
	session.progress.Reset(time.Now())
	game.binder.Bind("bridge-1", "0001", session)

	// ステージ0をクリア → デバイスは stage_cleared{stage_index:0} を送る
	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgStageCleared, DeviceID: "0001",
		StageIndex: 0, RemainingMS: 200000,
	})

	session.mu.Lock()
	got := session.StageIndex
	session.mu.Unlock()

	if got != 1 {
		t.Fatalf("session.StageIndex = %d, want 1 (次のステージ)", got)
	}

	// ナビゲーターが参照するステージ知識が次の課題になっていること
	in := NavigatorPromptInput{Session: built, StageIndex: got}
	stage := in.currentStage()
	if stage == nil {
		t.Fatal("currentStage が nil")
	}
	if stage.TemplateID != built.Stages[1].TemplateID {
		t.Errorf("ナビが参照するステージ = %s, want %s",
			stage.TemplateID, built.Stages[1].TemplateID)
	}
}

// TestStageClearedOnFinalStageDoesNotPanic は最終ステージのクリアで
// 範囲外参照にならないことを確かめる (+1 が末尾を超える)。
func TestStageClearedOnFinalStageDoesNotPanic(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-1", difficultyEasy)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	last := len(built.Stages) - 1

	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))
	game.SetSessionLogStore(NewSessionLogStore(store))
	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)

	session := &GameSession{
		SessionID: "s-1", DeviceID: "0001", BridgeID: "bridge-1",
		State: deviceStatePlaying, StageIndex: last, RemainingMS: 50000,
		Built: built, StartedAt: time.Now(),
	}
	session.progress.Reset(time.Now())
	game.binder.Bind("bridge-1", "0001", session)

	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgStageCleared, DeviceID: "0001",
		StageIndex: last, RemainingMS: 40000,
	})

	session.mu.Lock()
	got := session.StageIndex
	session.mu.Unlock()

	// 末尾+1 を指すが、参照側は範囲チェックで nil を返す
	in := NavigatorPromptInput{Session: built, StageIndex: got}
	if stage := in.currentStage(); stage != nil {
		t.Errorf("最終ステージクリア後に stage=%s が返った (nil のはず)", stage.TemplateID)
	}
}
