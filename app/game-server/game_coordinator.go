package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// GameSession はサーバー側が保持する1セッションの状態。
//
// 組み立て済みセッション (正解を含む)・会話ログ・スコアは Valkey へ永続化し、
// サーバー再起動後もナビゲーターが正解・文脈を保って復帰できるようにする
// (docs/game_session_design.md §9)。
type GameSession struct {
	SessionID  string             `json:"session_id"`
	DeviceID   string             `json:"device_id"`
	BridgeID   string             `json:"bridge_id"`
	Difficulty string             `json:"difficulty"`
	Character  NavigatorCharacter `json:"character"`
	Built      *BuiltSession      `json:"built"`

	// --- Core からの最新報告 ---
	StageIndex  int    `json:"stage_index"`
	RemainingMS int    `json:"remaining_ms"`
	State       string `json:"state"`

	// Score は defused 時の残り時間 (§9)
	Score int `json:"score"`

	// Finished はゲームが終了した (爆発・解除) ことを示す。
	//
	// **セッション自体は残す** — Management Console の進行表に結果を
	// 表示し続けるため。バインドを外して消してしまうと、終了直後に
	// 画面からセッションが消えて結果が確認できない (実運用で発生)。
	// 無線の応答相手だけをカラスへ戻すのに使う (docs/operation_flow.md §6)。
	Finished bool `json:"finished"`

	StartedAt time.Time `json:"started_at"`

	// progress はヒントレベル判定用のステージ内進捗 (永続化しない)
	progress StageProgress

	mu sync.Mutex
}

// NavigatorSpeaker はナビゲーターの発話を生成して無線へ送出する。
type NavigatorSpeaker interface {
	Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger string, event string) error
}

// GameCoordinator はバインド・セッション開始・デバイスイベントの演出接続を束ねる。
type GameCoordinator struct {
	devices   *DeviceRegistry
	bridges   *BridgeRegistry
	builder   *ScenarioBuilder
	store     SessionStore
	speaker   NavigatorSpeaker
	crosstalk *CrosstalkScheduler
	logs      *SessionLogStore
	navigator *NavigatorConfig

	// testResponder は疎通確認応答 (カラス)。セッション開始時に文脈を破棄する。
	testResponder *TestResponder
	rng           *rand.Rand
	rngMu         sync.Mutex

	// binder は bridge ⇔ device のバインドと進行中セッションを管理する
	binder *SessionBinder
}

func NewGameCoordinator(
	devices *DeviceRegistry,
	bridges *BridgeRegistry,
	builder *ScenarioBuilder,
	store SessionStore,
	rng *rand.Rand,
) *GameCoordinator {
	return &GameCoordinator{
		devices: devices,
		bridges: bridges,
		builder: builder,
		store:   store,
		rng:     rng,
		binder:  NewSessionBinder(),
	}
}

// SetNavigatorSpeaker はナビゲーターの発話生成器を設定する。
func (c *GameCoordinator) SetNavigatorSpeaker(speaker NavigatorSpeaker) {
	c.speaker = speaker
}

// SetTestResponder は疎通確認応答の相手を設定する。
// セッションが始まったら、その bridge の疎通確認の文脈は破棄する。
func (c *GameCoordinator) SetTestResponder(responder *TestResponder) {
	c.testResponder = responder
}

// SetNavigatorConfig はキャラクター割当に使う設定を渡す。
func (c *GameCoordinator) SetNavigatorConfig(cfg *NavigatorConfig) {
	c.navigator = cfg
}

// SetSessionLogStore はイベント記録先を設定する。
// 発話と同じタイムラインへ進行イベントを残す (docs/game_session_design.md §9)。
func (c *GameCoordinator) SetSessionLogStore(logs *SessionLogStore) {
	c.logs = logs
}

// logEvent はゲーム進行イベントをセッションのログへ記録する。
func (c *GameCoordinator) logEvent(session *GameSession, event, message string, stageIndex, remainingMS int) {
	if c.logs == nil {
		return
	}
	c.logs.AppendEvent(session.SessionID, event, message, stageIndex, remainingMS)
}

// stageName は指定ステージの名前を返す (ログの可読性のため)。
func (c *GameCoordinator) stageName(session *GameSession, stageIndex int) string {
	if session.Built == nil || stageIndex < 0 || stageIndex >= len(session.Built.Stages) {
		return ""
	}
	return session.Built.Stages[stageIndex].Name
}

// SetCrosstalkScheduler は混線演出のスケジューラを設定する。
func (c *GameCoordinator) SetCrosstalkScheduler(scheduler *CrosstalkScheduler) {
	c.crosstalk = scheduler
}

// StartSession はマネージャーの開始申告を受けてセッションを開始する
// (docs/bridge_connection_design.md §5 のバインド・開始フロー)。
func (c *GameCoordinator) StartSession(ctx context.Context, sender *AudioSender, deviceID, difficulty string) error {
	bridgeID := sender.BridgeID()

	// 3. 検証: 該当 device_id の Core が WS 接続中かつ Ready 状態か
	if !c.devices.IsConnected(deviceID) {
		log.Printf("[game] start rejected: device %s not connected", deviceID)
		return c.replyStartRejected(ctx, sender, deviceID, "接続されていません")
	}

	status := c.devices.Status(deviceID)
	if !status.IsReady() {
		log.Printf("[game] start rejected: device %s is %s (not ready)", deviceID, status.State)
		return c.replyStartRejected(ctx, sender, deviceID, "準備が完了していません")
	}

	// 競合: 既に他 bridge にバインドされ Playing 中なら拒否する (§5)
	if existing := c.sessionFor(deviceID); existing != nil {
		if existing.BridgeID != bridgeID && status.IsPlaying() {
			log.Printf("[game] start rejected: device %s in use by bridge %s", deviceID, existing.BridgeID)
			return c.replyStartRejected(ctx, sender, deviceID, "他のチームが使用中です")
		}
	}

	// 4. 難易度に対応するシナリオテンプレートからセッションJSONを組み立てる
	sessionID := fmt.Sprintf("s-%s-%d", deviceID, time.Now().Unix())

	c.rngMu.Lock()
	built, err := c.builder.Build(sessionID, difficulty)
	character := c.navigator.Pick(c.rng)
	c.rngMu.Unlock()

	if err != nil {
		return fmt.Errorf("build session: %w", err)
	}

	session := &GameSession{
		SessionID:  sessionID,
		DeviceID:   deviceID,
		BridgeID:   bridgeID,
		Difficulty: difficulty,
		Character:  character,
		Built:      built,
		State:      deviceStatePlaying,
		StartedAt:  time.Now(),
	}
	session.progress.Reset(time.Now())

	// バインドを確立する (後勝ち。明示的な解除は設けない)
	c.binder.Bind(bridgeID, deviceID, session)

	// session_start を送信する
	payload := built.SessionStartPayload(deviceID)
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session_start: %w", err)
	}
	if err := c.devices.SendSessionStart(deviceID, raw); err != nil {
		return fmt.Errorf("send session_start: %w", err)
	}

	log.Printf("[game] session started: %s device=%s bridge=%s difficulty=%s character=%s stages=%d",
		sessionID, deviceID, bridgeID, difficulty, character.Name, len(built.Stages))

	c.logEvent(session, EventSessionStart,
		fmt.Sprintf("セッション開始 (Core %s / 難易度 %s / ナビゲーター %s / 全%dステージ)",
			deviceID, difficulty, character.Name, len(built.Stages)),
		0, built.CountdownMS)
	if name := c.stageName(session, 0); name != "" {
		c.logEvent(session, EventStageStart,
			fmt.Sprintf("ステージ1開始: %s", name), 0, built.CountdownMS)
	}

	// 両方 (セッション本体・バインド) を Valkey に保存する (§2 手順4)
	c.persist(ctx, session)

	// 疎通確認の交信ログは本番の文脈に混ぜない
	if c.testResponder != nil {
		c.testResponder.Reset(bridgeID)
	}

	// 混線のスケジュールを開始する
	if c.crosstalk != nil {
		c.crosstalk.Start(ctx, session, sender)
	}

	// 5. 開始直後、ナビゲーターから初回の声掛けを TTS でプッシュする
	// (プレイヤーの発話待ちにせず行動を促す)
	return c.speak(ctx, sender, session, "session_start", "")
}

// ForceDetonate はマネージャーの判断で爆発シーケンスへ入れる
// (docs/operation_flow.md §7)。**風船が実際に破裂する**。
//
// AbortSession と違いセッションは解放しない。デバイスが破裂後に `exploded` を
// 返し、既存の失敗演出・スコア確定・混線通知がそのまま動くため
// (二重に演出を鳴らさない)。
func (c *GameCoordinator) ForceDetonate(ctx context.Context, deviceID string) error {
	session := c.sessionFor(deviceID)
	if session == nil {
		return errNoActiveSession
	}

	session.mu.Lock()
	state := session.State
	stageIndex, remaining := session.StageIndex, session.RemainingMS
	session.mu.Unlock()

	// 進行中以外は何もしない。デバイス側も Playing 以外は無視するが、
	// 届かないコマンドを送って記録だけ残るのを避ける。
	if state != deviceStatePlaying {
		return errNotPlaying
	}

	if err := c.devices.SendForceDetonate(deviceID); err != nil {
		log.Printf("[game] force_detonate failed: device=%s: %v", deviceID, err)
		return fmt.Errorf("send force_detonate: %w", err)
	}

	// 送信できた場合のみ記録する。破裂の事実は device からの exploded で
	// 別途残るが、「マネージャーが指示した」ことはここにしか現れない。
	c.logEvent(session, EventForced,
		"マネージャーによる強制破裂を指示", stageIndex, remaining)
	c.persist(ctx, session)

	log.Printf("[game] force detonate: device=%s", deviceID)
	return nil
}

// AbortSession は強制リセット(キルスイッチ)で Core を Setup へ戻す
// (docs/operation_flow.md §7)。
func (c *GameCoordinator) AbortSession(ctx context.Context, sender *AudioSender, deviceID string) error {
	// デバイスへの送信可否にかかわらず、サーバー側の状態整理とログ記録は必ず行う。
	// デバイスが切断されている状況こそマネージャーがリセットしたい場面であり、
	// ここで早期リターンすると「なぜセッションが終わったか」が記録に残らない。
	sendErr := c.devices.SendSessionAbort(deviceID)

	if session := c.sessionFor(deviceID); session != nil {
		session.mu.Lock()
		stageIndex, remaining := session.StageIndex, session.RemainingMS
		session.mu.Unlock()

		message := "マネージャーによる強制リセット (セッション中断)"
		if sendErr != nil {
			message += " ※デバイス未接続のため通知は届いていない"
		}
		c.logEvent(session, EventAborted, message, stageIndex, remaining)
		c.persist(ctx, session)
	}

	c.binder.Release(deviceID)

	if c.crosstalk != nil {
		c.crosstalk.Stop(deviceID)
	}

	if sendErr != nil {
		log.Printf("[game] session aborted (device unreachable): device=%s: %v", deviceID, sendErr)
		return fmt.Errorf("send session_abort: %w", sendErr)
	}
	log.Printf("[game] session aborted: device=%s", deviceID)
	return nil
}

// HandleDeviceMessage は Core からの進行イベントを受け、ナビゲーター演出へ接続する
// (docs/game_session_design.md §7.2 / docs/navigator_design.md §3.5)。
func (c *GameCoordinator) HandleDeviceMessage(ctx context.Context, msg *deviceMessage) {
	session := c.sessionFor(msg.DeviceID)
	if session == nil {
		// バインド前・復元前のデバイスからの報告 (device_status など) は状態更新のみ
		return
	}

	sender := NewAudioSender(c.bridges, session.BridgeID)

	// stage_cleared の stage_index は「**クリアした**ステージ」の番号
	// (デバイスは送信後に AdvanceStage する)。そのまま代入すると
	// session.StageIndex がクリア済みのステージを指したままになり、
	// ナビゲーターが**次の課題の知識を持たずに喋る**
	// (実運用で発生: ステージ2でランプに気づかせるヒントが出なかった)。
	// この1件だけ +1 して次のステージを指す。
	nextStage := msg.StageIndex
	if msg.Type == msgStageCleared {
		nextStage = msg.StageIndex + 1
	}

	session.mu.Lock()
	previousStage := session.StageIndex
	session.StageIndex = nextStage
	session.RemainingMS = msg.RemainingMS
	if msg.State != "" {
		session.State = msg.State
	}
	stageChanged := nextStage != previousStage
	session.mu.Unlock()

	// ステージが切り替わったらヒントレベルを L1 にリセットする
	// (docs/navigator_design.md §3.2)
	if stageChanged {
		session.mu.Lock()
		session.progress.Reset(time.Now())
		session.mu.Unlock()
	}

	switch msg.Type {
	case msgDeviceStatus:
		// 再同期のみ。演出は行わない (§7.3)
		return

	case msgSessionAccepted:
		log.Printf("[game] session accepted by device %s", msg.DeviceID)
		return

	case msgSessionRejected:
		log.Printf("[game] session REJECTED by device %s: reason=%s detail=%s",
			msg.DeviceID, msg.Reason, msg.Detail)
		c.binder.Release(msg.DeviceID)
		return

	case msgStageCleared:
		// msg.StageIndex はクリアしたステージの番号。次のステージへ進む
		c.logEvent(session, EventStageCleared,
			fmt.Sprintf("✓ ステージ%d クリア: %s", msg.StageIndex+1, c.stageName(session, msg.StageIndex)),
			msg.StageIndex, msg.RemainingMS)

		// **最終ステージのクリアでは何も喋らない。**
		//
		// デバイスは最後の1本を切ると stage_cleared に続けて defused を送る。
		// ここで「次の課題へ進む」と促すと、次のステージが無いためプロンプトに
		// ステージ知識が入らず、**生成AIが課題を捏造する**
		// (実運用で「あと60秒!もう一本、赤の線を切ってください!」と、
		// 解除済みの装置に対して存在しない指示を出した)。
		// 完了の演出は直後に届く defused が担当する。
		nextName := c.stageName(session, msg.StageIndex+1)
		if nextName == "" {
			log.Printf("[game] final stage cleared: device=%s (defused を待つ)", msg.DeviceID)
			break
		}

		c.logEvent(session, EventStageStart,
			fmt.Sprintf("ステージ%d開始: %s", msg.StageIndex+2, nextName),
			msg.StageIndex+1, msg.RemainingMS)

		c.speakAsync(ctx, sender, session, "stage_cleared",
			fmt.Sprintf("プレイヤーが%d番目の課題を突破した。次の課題へ進む。", msg.StageIndex+1))

	case msgWhackCompleted:
		c.logEvent(session, EventWhackDone, "モグラ叩き完了", msg.StageIndex, msg.RemainingMS)
		c.speakAsync(ctx, sender, session, "whack_completed",
			"プレイヤーがモグラ叩きを完了した。最後に押した色が次の手がかりになる。")

	case msgPushProgress:
		// ログは毎回残す (後から入力の進み方を追えるようにする)
		c.logEvent(session, EventPushProgress,
			fmt.Sprintf("ボタン入力 %d個目まで正解", msg.SeqIndex), msg.StageIndex, msg.RemainingMS)

		// 発話は毎回でなく間引く (§3.5)
		if msg.SeqIndex%2 == 1 {
			c.speakAsync(ctx, sender, session, "push_progress",
				fmt.Sprintf("プレイヤーがボタン入力を%d個目まで正しく進めた。短く反応する。", msg.SeqIndex))
		}

	case msgWrongAction:
		session.mu.Lock()
		session.progress.WrongActions++
		session.mu.Unlock()

		event := "プレイヤーが誤操作をした。"
		if msg.Detail == "precondition_unmet" {
			event = "プレイヤーが手順を満たさないまま線を切ってしまった。"
		} else if msg.Detail == "wrong_line" {
			event = "プレイヤーが違う線を切ってしまった。"
		}
		if msg.PenaltyMS > 0 {
			event += fmt.Sprintf("ペナルティで残り時間が%d秒減った。", msg.PenaltyMS/1000)
		}

		c.logEvent(session, EventWrongAction,
			fmt.Sprintf("✗ %s%s", describeWrongAction(msg), describePenalty(msg.PenaltyMS)),
			msg.StageIndex, msg.RemainingMS)

		c.speakAsync(ctx, sender, session, "wrong_action", event+"叱咤しつつ励まし、注意を促す。")

	case msgExploded:
		log.Printf("[game] exploded: device=%s reason=%s", msg.DeviceID, msg.Reason)
		c.logEvent(session, EventExploded,
			fmt.Sprintf("✗✗ 爆発 (%s) — 解体失敗", describeExplodeReason(msg)),
			msg.StageIndex, msg.RemainingMS)
		c.finishSession(ctx, session, 0)
		// 最終メッセージを流し終えてからバインドを解放し、以後はカラスに引き継ぐ
		c.speakAsyncThen(ctx, sender, session, "exploded",
			"解体は失敗し、装置が起動してしまった。失敗を受け止めるメッセージを返す。",
			func() { c.releaseAfterFinish(session) })

		// 他チームのCoreの爆発を契機に「別現場の通信」を流す (§5.1 イベント駆動)
		if c.crosstalk != nil {
			c.crosstalk.NotifyExplosion(ctx, session.DeviceID, c.binder.PlayingSessions(session.DeviceID))
		}

	case msgDefused:
		log.Printf("[game] defused: device=%s remaining=%dms", msg.DeviceID, msg.RemainingMS)
		c.logEvent(session, EventDefused,
			fmt.Sprintf("★ 解除成功 — スコア(残り時間) %.1f秒", float64(msg.RemainingMS)/1000),
			msg.StageIndex, msg.RemainingMS)
		c.finishSession(ctx, session, msg.RemainingMS)
		// 最終メッセージを流し終えてからバインドを解放し、以後はカラスに引き継ぐ
		c.speakAsyncThen(ctx, sender, session, "defused",
			fmt.Sprintf("解除に成功した!残り時間%d秒でクリア。祝福する。", msg.RemainingMS/1000),
			func() { c.releaseAfterFinish(session) })
	}

	c.persist(ctx, session)
}

// NoteQuestion はプレイヤーの質問回数を1つ数える (ヒントレベルの前倒し用)。
func (c *GameCoordinator) NoteQuestion(deviceID string) {
	session := c.sessionFor(deviceID)
	if session == nil {
		return
	}
	session.mu.Lock()
	session.progress.Questions++
	session.mu.Unlock()
}

// SessionForBridge は bridge にバインドされた**進行中の**セッションを返す。
//
// 終了済み (爆発・解除) のセッションは nil を返す。プレイヤーの発話を
// ナビゲーターへ流すかの判定に使うため、終了後はカラスへ引き継ぐ
// (セッション自体は Management Console 用に binder へ残っている)。
func (c *GameCoordinator) SessionForBridge(bridgeID string) *GameSession {
	session := c.binder.SessionForBridge(bridgeID)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	finished := session.Finished
	session.mu.Unlock()
	if finished {
		return nil
	}
	return session
}

// Sessions は進行中の全セッションを返す (マネージャー向け Web 画面用)。
func (c *GameCoordinator) Sessions() []*GameSession { return c.binder.Sessions() }

// Bindings は現在のバインド表を返す (Web画面用)。
func (c *GameCoordinator) Bindings() map[string]string { return c.binder.Bindings() }

// Restore は Valkey から復元したセッションをレジストリへ戻す。
func (c *GameCoordinator) Restore(sessions []*GameSession) {
	for _, session := range sessions {
		session.progress.Reset(time.Now())
	}
	c.binder.Restore(sessions)
}

// sessionFor は device_id に対応する進行中セッションを返す。
func (c *GameCoordinator) sessionFor(deviceID string) *GameSession {
	return c.binder.SessionFor(deviceID)
}

// finishSession はセッション終了時のスコア確定と混線停止を行う。
func (c *GameCoordinator) finishSession(ctx context.Context, session *GameSession, score int) {
	session.mu.Lock()
	session.Score = score
	session.mu.Unlock()

	if c.crosstalk != nil {
		c.crosstalk.Stop(session.DeviceID)
	}
}

// speak はナビゲーターの発話を生成して送出する。
func (c *GameCoordinator) speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	if c.speaker == nil {
		return nil
	}
	return c.speaker.Speak(ctx, sender, session, trigger, event)
}

// speakAsync は発話生成をバックグラウンドで行う。
// デバイスイベントの処理 (WS 読み取りループ) を TTS 生成でブロックしないため。
func (c *GameCoordinator) speakAsync(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) {
	c.speakAsyncThen(ctx, sender, session, trigger, event, nil)
}

// speakAsyncThen は発話の**送出が終わってから** done を呼ぶ。
//
// 成功・失敗の最終メッセージのあとにバインドを解放する用途で使う。
// 先に解放すると、その最終メッセージ自体がナビゲーター不在で流れなくなる。
func (c *GameCoordinator) speakAsyncThen(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string, done func()) {
	if c.speaker == nil {
		if done != nil {
			done()
		}
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[game] speak panic: %v", rec)
			}
			if done != nil {
				done()
			}
		}()
		if err := c.speak(context.WithoutCancel(ctx), sender, session, trigger, event); err != nil {
			log.Printf("[game] speak error (%s): %v", trigger, err)
		}
	}()
}

// releaseAfterFinish はゲーム終了後、無線の応答相手をカラスへ戻す。
//
// 印を付けるだけで**セッションは binder に残す**。消してしまうと
// Management Console の進行表から消えて結果を確認できなくなり、
// マネージャーが状況を把握できない (実運用で発生)。
//
// 印を付けないと**終了後もナビゲーターが応答し続ける**。実運用では爆発後に
// 「もう一度ランプの状態を教えてくれ」と促し続け、マネージャーのリセット申告にも
// ナビゲーターが反応していた (docs/operation_flow.md §6)。
func (c *GameCoordinator) releaseAfterFinish(session *GameSession) {
	session.mu.Lock()
	session.Finished = true
	session.mu.Unlock()
	log.Printf("[game] session finished, navigator handed over to crow: device=%s",
		session.DeviceID)
}

// replyStartRejected は開始申告を拒否した旨を無線で返す。
func (c *GameCoordinator) replyStartRejected(ctx context.Context, sender *AudioSender, deviceID, reason string) error {
	if c.speaker == nil {
		return nil
	}
	// セッションがまだ無いため、キャラクター未確定の簡易応答として扱う
	log.Printf("[game] reply start rejected: device=%s reason=%s", deviceID, reason)
	return nil
}

// persist はセッション状態を Valkey へ保存する。
func (c *GameCoordinator) persist(ctx context.Context, session *GameSession) {
	if c.store == nil {
		return
	}
	if err := c.store.SaveSession(ctx, session); err != nil {
		log.Printf("[game] persist error: %v", err)
	}
}
