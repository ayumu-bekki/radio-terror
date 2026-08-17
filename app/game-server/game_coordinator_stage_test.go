package main

import (
	"context"
	"errors"
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

// fakeSpeaker は発話せずに呼び出しを記録するだけの NavigatorSpeaker。
type fakeSpeaker struct {
	triggers []string
}

func (f *fakeSpeaker) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	f.triggers = append(f.triggers, trigger)
	return nil
}

// TestNavigatorReleasedAfterGameEnd は爆発・解除のあとに
// **ナビゲーターのバインドが解放される**ことを確かめる。
//
// 解放しないと終了後もナビゲーターが応答し続け、マネージャーのリセット申告にまで
// 「リセットだな、了解。ランプの状態を教えてくれ」と反応する
// (実運用で発生)。ゲームはもう進行しないので、開始前と同じくカラスが
// 引き継ぐのが正しい (docs/operation_flow.md §6)。
//
// **最終メッセージを流し終えてから**解放すること。先に解放すると、
// その最終メッセージ自体が流れなくなる。
func TestNavigatorReleasedAfterGameEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		msgType string
		trigger string
	}{
		{"爆発", msgExploded, "exploded"},
		{"解除成功", msgDefused, "defused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryStore()
			devices := NewDeviceRegistry()
			game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
				rand.New(rand.NewSource(1)))
			game.SetSessionLogStore(NewSessionLogStore(store))

			speaker := &fakeSpeaker{}
			game.SetNavigatorSpeaker(speaker)

			conn := &fakeDeviceConn{}
			devices.Register("0001", conn)

			session := &GameSession{
				SessionID: "s-1", DeviceID: "0001", BridgeID: "bridge-1",
				State: deviceStatePlaying, StageIndex: 0, RemainingMS: 10000,
				StartedAt: time.Now(),
			}
			session.progress.Reset(time.Now())
			game.binder.Bind("bridge-1", "0001", session)

			if game.SessionForBridge("bridge-1") == nil {
				t.Fatal("前提: バインドされているはず")
			}

			game.HandleDeviceMessage(context.Background(), &deviceMessage{
				Type: tc.msgType, DeviceID: "0001", RemainingMS: 10000,
			})

			// 発話は非同期なので、解放されるまで待つ
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if game.SessionForBridge("bridge-1") == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}

			if game.SessionForBridge("bridge-1") != nil {
				t.Fatal("終了後もバインドが残っている — ナビゲーターが応答し続ける")
			}
			// 最終メッセージは解放前に流れていること
			if len(speaker.triggers) == 0 || speaker.triggers[0] != tc.trigger {
				t.Errorf("最終メッセージ %q が流れていない: %v", tc.trigger, speaker.triggers)
			}
		})
	}
}

// TestFinalStageClearedDoesNotSpeak は**最終ステージのクリアで発話しない**ことを
// 確かめる。
//
// デバイスは最後の1本を切ると stage_cleared に続けて defused を送る。
// stage_cleared で「次の課題へ進む」と促すと、次のステージが無いため
// プロンプトにステージ知識が入らず、**生成AIが課題を捏造する**。
// 実運用では解除成功の直後に
// 「あと60秒!もう一本、赤の線を切ってください!」と、
// 既に解除済みの装置へ存在しない指示を出した (docs/navigator_design.md §6 決定26)。
func TestFinalStageClearedDoesNotSpeak(t *testing.T) {
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

	speaker := &fakeSpeaker{}
	game.SetNavigatorSpeaker(speaker)

	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)

	session := &GameSession{
		SessionID: "s-1", DeviceID: "0001", BridgeID: "bridge-1",
		State: deviceStatePlaying, StageIndex: last, RemainingMS: 54700,
		Built: built, StartedAt: time.Now(),
	}
	session.progress.Reset(time.Now())
	game.binder.Bind("bridge-1", "0001", session)

	// 最終ステージのクリア
	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgStageCleared, DeviceID: "0001",
		StageIndex: last, RemainingMS: 54700,
	})

	// 非同期発話が走らないことを確かめるため少し待つ
	time.Sleep(200 * time.Millisecond)

	for _, trigger := range speaker.triggers {
		if trigger == "stage_cleared" {
			t.Fatal("最終ステージのクリアで発話した — 存在しない課題を促す危険がある")
		}
	}

	// 中間ステージのクリアでは従来どおり発話すること (抑制しすぎていない)
	if last > 0 {
		speaker.triggers = nil
		session.StageIndex = 0
		game.HandleDeviceMessage(context.Background(), &deviceMessage{
			Type: msgStageCleared, DeviceID: "0001",
			StageIndex: 0, RemainingMS: 100000,
		})
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && len(speaker.triggers) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		if len(speaker.triggers) == 0 || speaker.triggers[0] != "stage_cleared" {
			t.Errorf("中間ステージのクリアで発話していない: %v", speaker.triggers)
		}
	}
}

// TestFinishedSessionStaysVisibleButHandsOverRadio は終了後のセッションが
// **Management Console には残り、無線の応答だけカラスへ移る**ことを確かめる。
//
// 当初はバインドごと解放していたが、それだと終了直後に進行表からセッションが
// 消えて**結果を確認できなくなる**(実運用で発生)。表示用の保持と
// 「誰が無線に応答するか」は別の関心事なので、印 (Finished) で分ける。
func TestFinishedSessionStaysVisibleButHandsOverRadio(t *testing.T) {
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	game := NewGameCoordinator(devices, NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))
	game.SetSessionLogStore(NewSessionLogStore(store))
	game.SetNavigatorSpeaker(&fakeSpeaker{})

	conn := &fakeDeviceConn{}
	devices.Register("0001", conn)

	session := &GameSession{
		SessionID: "s-1", DeviceID: "0001", BridgeID: "bridge-1",
		State: deviceStatePlaying, RemainingMS: 54700, StartedAt: time.Now(),
	}
	session.progress.Reset(time.Now())
	game.binder.Bind("bridge-1", "0001", session)

	game.HandleDeviceMessage(context.Background(), &deviceMessage{
		Type: msgDefused, DeviceID: "0001", RemainingMS: 54700,
	})

	// 無線の応答相手がカラスへ移るまで待つ
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if game.SessionForBridge("bridge-1") == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// ナビゲーターは応答しない (カラスへ引き継ぐ)
	if game.SessionForBridge("bridge-1") != nil {
		t.Error("終了後もナビゲーターが応答する状態になっている")
	}

	// Management Console からは見える
	found := false
	for _, s := range game.Sessions() {
		if s.SessionID == "s-1" {
			found = true
		}
	}
	if !found {
		t.Error("終了したセッションが Management Console から消えている — 結果を確認できない")
	}

	// 進行表に出す情報が壊れていないこと
	views := buildSessionViews(game.Sessions())
	if len(views) != 1 {
		t.Fatalf("進行表の件数 = %d, want 1", len(views))
	}
	if views[0].DeviceID != "0001" {
		t.Errorf("進行表の DeviceID = %q, want 0001", views[0].DeviceID)
	}
}

// TestAnnounceReadyWaitsBeforeCountdown は、カウントダウン開始前に
// ナビゲーターが名乗り、**発話が終わってから猶予を置く**ことを確かめる。
//
// 以前は session_start を先に送っており、**返答が無いままいきなり
// カウントダウンが始まって**いた。プレイヤーは誰と交信するのかも、
// 始まったことも分からないまま時間だけが減る
// (docs/navigator_design.md 決定36)。
func TestAnnounceReadyWaitsBeforeCountdown(t *testing.T) {
	speaker := &fakeSpeaker{}
	game := &GameCoordinator{speaker: speaker}
	session := &GameSession{DeviceID: "3701"}

	// countdownStartDelay の実測を避けるため、待ちを打ち切れる ctx を使う。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	game.announceReady(ctx, nil, session)

	if len(speaker.triggers) != 1 || speaker.triggers[0] != "session_ready" {
		t.Fatalf("session_ready が発話されていない: %v", speaker.triggers)
	}
}

// TestAnnounceReadySurvivesSpeakFailure は、発話に失敗しても
// **セッション開始を止めない**ことを確かめる。
//
// 無線が無言になるのは痛いが、装置の前にプレイヤーが立っている以上、
// 開始できない方が困る (§9 はマネージャー介入で運用する方針)。
func TestAnnounceReadySurvivesSpeakFailure(t *testing.T) {
	game := &GameCoordinator{speaker: &failingSpeaker{}}
	session := &GameSession{DeviceID: "3701"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		game.announceReady(context.Background(), nil, session)
	}()

	select {
	case <-done:
		// 失敗時は待たずに戻る (countdownStartDelay を消費しない)
	case <-time.After(countdownStartDelay):
		t.Fatal("発話失敗時に猶予を待ってしまっている — 開始が無用に遅れる")
	}
}

// failingSpeaker は必ず失敗する NavigatorSpeaker。
type failingSpeaker struct{}

func (f *failingSpeaker) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	return errors.New("TTS unavailable")
}

// TestPendingStateAcceptsSessionStart は、pending のデバイスが
// **session_start を受理できる**ことを確かめる (§4.2)。
//
// 開始申告 → session_pending → (応答 + 5秒) → session_start という順序なので、
// session_start が届くときデバイスは既に pending になっている。
// IsReady が ready しか通さないと、**自分が送る session_start を
// 自分で拒否する**状態が5秒間できてしまう。
func TestPendingStateAcceptsSessionStart(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{deviceStateReady, true},
		{deviceStatePending, true},
		{deviceStateSetup, false},
		{deviceStatePlaying, false},
	} {
		got := (&DeviceStatus{State: tc.state}).IsReady()
		if got != tc.want {
			t.Errorf("IsReady(%s) = %v, want %v", tc.state, got, tc.want)
		}
	}
}
