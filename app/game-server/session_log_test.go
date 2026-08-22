package main

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// newTestCoordinator は永続化にメモリ実装を使う GameCoordinator を組み立てる。
func newTestCoordinator(t *testing.T) (*GameCoordinator, *SessionLogStore, *MemoryStore) {
	t.Helper()

	lib := loadTestLibrary(t)
	store := NewMemoryStore()
	logs := NewSessionLogStore(store)

	game := NewGameCoordinator(
		NewDeviceRegistry(),
		NewBridgeRegistry(),
		NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1))),
		store,
		rand.New(rand.NewSource(1)),
	)
	game.SetSessionLogStore(logs)
	return game, logs, store
}

// newTestSession は組み立て済みセッションを直接レジストリへ入れる
// (デバイス接続・音声申告を経由せずイベント処理だけを試験する)。
func newTestSession(t *testing.T, game *GameCoordinator) *GameSession {
	t.Helper()

	built, err := game.builder.Build("s-log-test", difficultyNormal)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	session := &GameSession{
		SessionID:  "s-log-test",
		DeviceID:   "3701",
		BridgeID:   "BR01",
		Difficulty: difficultyNormal,
		Character:  loadTestNavigator(t).Characters[0],
		Built:      built,
		State:      deviceStatePlaying,
		StartedAt:  time.Now(),
	}
	session.progress.Reset(time.Now())

	game.binder.Bind(session.BridgeID, session.DeviceID, session)

	return session
}

// TestSessionTimelineRecordsEvents は進行イベントが発話と同じタイムラインへ
// 時系列で記録されることを確かめる (docs/game_session_design.md §9)。
func TestSessionTimelineRecordsEvents(t *testing.T) {
	game, logs, _ := newTestCoordinator(t)
	session := newTestSession(t, game)
	ctx := context.Background()

	// ステージ1クリア → ステージ2開始
	game.HandleDeviceMessage(ctx, &deviceMessage{
		Type: msgStageCleared, DeviceID: "3701", StageIndex: 0, RemainingMS: 108300,
	})
	// プレイヤーの発話
	logs.Append(session.SessionID, ConversationEntry{
		Sender: "プレイヤー", Receiver: "フクロウ", Message: "ランプが2つ光ってます",
	})
	// 誤操作
	game.HandleDeviceMessage(ctx, &deviceMessage{
		Type: msgWrongAction, DeviceID: "3701", Detail: "wrong_line",
		Line: "D", PenaltyMS: 30000, StageIndex: 1, RemainingMS: 70000,
	})
	// 爆発
	game.HandleDeviceMessage(ctx, &deviceMessage{
		Type: msgExploded, DeviceID: "3701", Reason: "timeout",
		StageIndex: 1, RemainingMS: 0,
	})

	entries := logs.Entries(session.SessionID)
	if len(entries) < 5 {
		t.Fatalf("記録が少なすぎる: %d件", len(entries))
	}

	// 期待するイベントがすべて出ていること
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsEvent() {
			seen[e.Event] = true
		}
		// 全件に時刻が入っていること
		if e.At == 0 {
			t.Errorf("時刻が記録されていない: %+v", e)
		}
	}
	for _, want := range []string{
		EventStageCleared, EventStageStart, EventWrongAction, EventExploded,
	} {
		if !seen[want] {
			t.Errorf("イベント %q が記録されていない", want)
		}
	}

	// 発話とイベントが混在していること
	var speeches, events int
	for _, e := range entries {
		if e.IsEvent() {
			events++
		} else {
			speeches++
		}
	}
	if speeches == 0 || events == 0 {
		t.Errorf("発話=%d イベント=%d: 両方が1本のログに入っているべき", speeches, events)
	}

	// 時系列が単調であること
	for i := 1; i < len(entries); i++ {
		if entries[i].At < entries[i-1].At {
			t.Errorf("時系列が逆転している: %d件目", i)
		}
	}

	for _, e := range entries {
		t.Logf("[%s] %s", e.Kind, e.Message)
	}
}

// TestSessionTimelinePersisted は記録が永続化層へも残り、
// 終了済みセッションのログを後から読めることを確かめる。
func TestSessionTimelinePersisted(t *testing.T) {
	game, logs, store := newTestCoordinator(t)
	session := newTestSession(t, game)
	ctx := context.Background()

	logs.Append(session.SessionID, ConversationEntry{
		Sender: "フクロウ", Receiver: "プレイヤー", Message: "慌てるな、死ぬぞ。",
	})
	game.HandleDeviceMessage(ctx, &deviceMessage{
		Type: msgDefused, DeviceID: "3701", StageIndex: 2, RemainingMS: 45000,
	})

	// 永続化層から読み直せること (メモリのログを経由しない)
	persisted, err := store.LoadLog(ctx, session.SessionID)
	if err != nil {
		t.Fatalf("LoadLog: %v", err)
	}
	if len(persisted) == 0 {
		t.Fatal("永続化層にログが残っていない")
	}

	var foundDefused bool
	for _, e := range persisted {
		if e.Event == EventDefused {
			foundDefused = true
			if !strings.Contains(e.Message, "45.0") {
				t.Errorf("解除成功のスコアが記録されていない: %s", e.Message)
			}
		}
	}
	if !foundDefused {
		t.Error("解除成功イベントが永続化されていない")
	}
}

// TestAbortRecordsEvent は強制リセットが記録されることを確かめる。
// リセット後はセッションがメモリから消えるため、記録が残らないと
// 「なぜ終わったか」が追えなくなる。
func TestAbortRecordsEvent(t *testing.T) {
	game, _, store := newTestCoordinator(t)
	session := newTestSession(t, game)
	ctx := context.Background()

	// デバイス未接続でも中断イベントは記録される必要がある
	_ = game.AbortSession(ctx, nil, session.DeviceID)

	persisted, err := store.LoadLog(ctx, session.SessionID)
	if err != nil {
		t.Fatalf("LoadLog: %v", err)
	}

	var found bool
	for _, e := range persisted {
		if e.Event == EventAborted {
			found = true
		}
	}
	if !found {
		t.Error("強制リセットが記録されていない")
	}
}

// TestRenderIncludesEvents はプロンプトへ渡す文脈にイベントが含まれることを
// 確かめる (ナビゲーターが直前の出来事を踏まえて話せるようにするため)。
func TestRenderIncludesEvents(t *testing.T) {
	log := NewConversationLog(0)
	log.Append(ConversationEntry{Sender: "プレイヤー", Receiver: "フクロウ", Message: "切りました"})
	log.Append(ConversationEntry{Kind: EntryKindEvent, Event: EventStageCleared, Message: "✓ ステージ1 クリア"})

	rendered := log.Render()
	if !strings.Contains(rendered, "(装置)") {
		t.Errorf("イベントが文脈に含まれていない:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[プレイヤー] -> [フクロウ]") {
		t.Errorf("発話の形式が崩れている:\n%s", rendered)
	}
}

// TestAbortedSessionDoesNotSurviveRestart は、リセットしたセッションが
// **サーバー再起動で復活しない**ことを確かめる。
//
// `docker compose down` → `up` でリセット済みのセッションが戻ってくる
// 不具合があった。AbortSession は binder (メモリ) を外すだけで
// **Valkey のセッションを消していなかった**ため、起動時の LoadSessions が
// 読み戻していた。
func TestAbortedSessionDoesNotSurviveRestart(t *testing.T) {
	game, _, store := newTestCoordinator(t)
	session := newTestSession(t, game)
	ctx := context.Background()

	// 保存されている状態を作る (通常は開始時・進行時に persist される)
	if err := store.SaveSession(ctx, session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	_ = game.AbortSession(ctx, nil, session.DeviceID)

	// **再起動を模す** — 新しい Coordinator へ保存済みセッションを復元する
	restored, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	for _, s := range restored {
		if s.SessionID == session.SessionID {
			t.Fatalf("リセットしたセッションが復元された: %s", s.SessionID)
		}
	}

	// **ログは残っていること** — 「なぜ終わったか」を追えなくなるため
	entries, err := store.LoadLog(ctx, session.SessionID)
	if err != nil {
		t.Fatalf("LoadLog: %v", err)
	}
	var foundAbort bool
	for _, e := range entries {
		if e.Event == EventAborted {
			foundAbort = true
		}
	}
	if !foundAbort {
		t.Error("中断イベントのログまで消えている — 終了理由が追えなくなる")
	}
}

// TestFinishedSessionSurvivesRestart は、終了 (爆発・解除) したセッションが
// **`session:{id}` にも残る**ことを確かめる。
//
// これは Management Console の進行表 (終了直後、リセット前の結果表示) 用。
// リセットが来れば `session:{id}` は消えてよい — 履歴側は
// `TestReleaseAfterFinishWritesHistory` が別途保証する (§9)。
func TestFinishedSessionSurvivesRestart(t *testing.T) {
	game, _, store := newTestCoordinator(t)
	session := newTestSession(t, game)
	ctx := context.Background()

	if err := store.SaveSession(ctx, session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// 解除成功として終了させる (AbortSession は通さない)
	game.finishSession(ctx, session, 12345)
	game.releaseAfterFinish(ctx, session)
	if err := store.SaveSession(ctx, session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	restored, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	var found bool
	for _, s := range restored {
		if s.SessionID == session.SessionID {
			found = true
		}
	}
	if !found {
		t.Error("終了したセッションが消えている — リセット前に進行表で結果を確認できなくなる")
	}
}

// TestReleaseAfterFinishWritesHistory は、セッション終了の瞬間に
// `history:{id}` が書かれることを確かめる。
//
// `session:{id}` はその後のリセットで無条件に消える (P-4b) ため、
// 履歴はリセットのタイミングに関係なくここで確定させる必要がある (§9)。
func TestReleaseAfterFinishWritesHistory(t *testing.T) {
	game, _, store := newTestCoordinator(t)
	session := newTestSession(t, game)
	ctx := context.Background()

	game.finishSession(ctx, session, 12345)
	game.releaseAfterFinish(ctx, session)

	histories, err := store.LoadHistories(ctx)
	if err != nil {
		t.Fatalf("LoadHistories: %v", err)
	}
	var found bool
	for _, s := range histories {
		if s.SessionID == session.SessionID {
			found = true
		}
	}
	if !found {
		t.Error("終了時に history:{id} が書かれていない — Web履歴に出てこなくなる")
	}

	// リセット (AbortSession) しても履歴は消えない。
	// デバイス未接続はここでは無視してよい (状態整理とログ記録は必ず行われる。
	// AbortSession 自身のコメント参照)。
	_ = game.AbortSession(ctx, nil, session.DeviceID)
	histories, err = store.LoadHistories(ctx)
	if err != nil {
		t.Fatalf("LoadHistories (after reset): %v", err)
	}
	found = false
	for _, s := range histories {
		if s.SessionID == session.SessionID {
			found = true
		}
	}
	if !found {
		t.Error("終了後のリセットで履歴まで消えている")
	}
}
