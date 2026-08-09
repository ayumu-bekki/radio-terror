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
	rng       *rand.Rand
	rngMu     sync.Mutex

	mu sync.RWMutex
	// bindingByBridge は bridge_id → device_id の動的バインド (§5)
	bindingByBridge map[string]string
	// sessionByDevice は device_id → 進行中セッション
	sessionByDevice map[string]*GameSession
}

func NewGameCoordinator(
	devices *DeviceRegistry,
	bridges *BridgeRegistry,
	builder *ScenarioBuilder,
	store SessionStore,
	rng *rand.Rand,
) *GameCoordinator {
	return &GameCoordinator{
		devices:         devices,
		bridges:         bridges,
		builder:         builder,
		store:           store,
		rng:             rng,
		bindingByBridge: make(map[string]string),
		sessionByDevice: make(map[string]*GameSession),
	}
}

// SetNavigatorSpeaker はナビゲーターの発話生成器を設定する。
func (c *GameCoordinator) SetNavigatorSpeaker(speaker NavigatorSpeaker) {
	c.speaker = speaker
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
	c.mu.Lock()
	c.bindingByBridge[bridgeID] = deviceID
	c.sessionByDevice[deviceID] = session
	c.mu.Unlock()

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

	// 混線のスケジュールを開始する
	if c.crosstalk != nil {
		c.crosstalk.Start(ctx, session, sender)
	}

	// 5. 開始直後、ナビゲーターから初回の声掛けを TTS でプッシュする
	// (プレイヤーの発話待ちにせず行動を促す)
	return c.speak(ctx, sender, session, "session_start", "")
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

	c.mu.Lock()
	delete(c.sessionByDevice, deviceID)
	c.mu.Unlock()

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

	session.mu.Lock()
	previousStage := session.StageIndex
	session.StageIndex = msg.StageIndex
	session.RemainingMS = msg.RemainingMS
	if msg.State != "" {
		session.State = msg.State
	}
	stageChanged := msg.StageIndex != previousStage
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
		c.mu.Lock()
		delete(c.sessionByDevice, msg.DeviceID)
		c.mu.Unlock()
		return

	case msgStageCleared:
		// msg.StageIndex はクリアしたステージの番号。次のステージへ進む
		c.logEvent(session, EventStageCleared,
			fmt.Sprintf("✓ ステージ%d クリア: %s", msg.StageIndex+1, c.stageName(session, msg.StageIndex)),
			msg.StageIndex, msg.RemainingMS)
		if name := c.stageName(session, msg.StageIndex+1); name != "" {
			c.logEvent(session, EventStageStart,
				fmt.Sprintf("ステージ%d開始: %s", msg.StageIndex+2, name),
				msg.StageIndex+1, msg.RemainingMS)
		}

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
		c.speakAsync(ctx, sender, session, "exploded",
			"解体は失敗し、装置が起動してしまった。失敗を受け止めるメッセージを返す。")

		// 他チームのCoreの爆発を契機に「別現場の通信」を流す (§5.1 イベント駆動)
		if c.crosstalk != nil {
			c.crosstalk.NotifyExplosion(ctx, session.DeviceID, c.playingSessions(session.DeviceID))
		}

	case msgDefused:
		log.Printf("[game] defused: device=%s remaining=%dms", msg.DeviceID, msg.RemainingMS)
		c.logEvent(session, EventDefused,
			fmt.Sprintf("★ 解除成功 — スコア(残り時間) %.1f秒", float64(msg.RemainingMS)/1000),
			msg.StageIndex, msg.RemainingMS)
		c.finishSession(ctx, session, msg.RemainingMS)
		c.speakAsync(ctx, sender, session, "defused",
			fmt.Sprintf("解除に成功した!残り時間%d秒でクリア。祝福する。", msg.RemainingMS/1000))
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

// SessionForBridge は bridge にバインドされたセッションを返す。
// プレイヤーの発話に応答する際、どのセッションの文脈で話すかの解決に使う。
func (c *GameCoordinator) SessionForBridge(bridgeID string) *GameSession {
	c.mu.RLock()
	deviceID, ok := c.bindingByBridge[bridgeID]
	c.mu.RUnlock()

	if !ok {
		return nil
	}
	return c.sessionFor(deviceID)
}

// Sessions は進行中の全セッションを返す (マネージャー向け Web 画面用)。
func (c *GameCoordinator) Sessions() []*GameSession {
	c.mu.RLock()
	defer c.mu.RUnlock()

	list := make([]*GameSession, 0, len(c.sessionByDevice))
	for _, session := range c.sessionByDevice {
		list = append(list, session)
	}
	return list
}

// Bindings は現在のバインド表を返す (Web画面用)。
func (c *GameCoordinator) Bindings() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]string, len(c.bindingByBridge))
	for bridgeID, deviceID := range c.bindingByBridge {
		result[bridgeID] = deviceID
	}
	return result
}

// Restore は Valkey から復元したセッションをレジストリへ戻す。
// 進行状態は Core からの device_status で再同期する (docs/scenario_design.md §6)。
func (c *GameCoordinator) Restore(sessions []*GameSession) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, session := range sessions {
		session.progress.Reset(time.Now())
		c.sessionByDevice[session.DeviceID] = session
		if session.BridgeID != "" {
			c.bindingByBridge[session.BridgeID] = session.DeviceID
		}
		log.Printf("[game] restored session %s device=%s bridge=%s",
			session.SessionID, session.DeviceID, session.BridgeID)
	}
}

// sessionFor は device_id に対応する進行中セッションを返す。
func (c *GameCoordinator) sessionFor(deviceID string) *GameSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionByDevice[deviceID]
}

// playingSessions は指定デバイス以外で Playing 中のセッションを返す
// (混線のイベント駆動配信の宛先解決)。
func (c *GameCoordinator) playingSessions(excludeDeviceID string) []*GameSession {
	c.mu.RLock()
	defer c.mu.RUnlock()

	list := make([]*GameSession, 0, len(c.sessionByDevice))
	for deviceID, session := range c.sessionByDevice {
		if deviceID == excludeDeviceID {
			continue
		}
		session.mu.Lock()
		playing := session.State == deviceStatePlaying
		session.mu.Unlock()
		if playing {
			list = append(list, session)
		}
	}
	return list
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
	if c.speaker == nil {
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[game] speak panic: %v", rec)
			}
		}()
		if err := c.speak(context.WithoutCancel(ctx), sender, session, trigger, event); err != nil {
			log.Printf("[game] speak error (%s): %v", trigger, err)
		}
	}()
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

// describeWrongAction は誤操作の内容をログ用の日本語にする (§7.2 の detail)。
func describeWrongAction(msg *deviceMessage) string {
	line := ""
	if msg.Line != "" {
		if name, ok := colorNameJA[msg.Line]; ok {
			line = fmt.Sprintf("(%s)", name)
		} else {
			line = fmt.Sprintf("(%s)", msg.Line)
		}
	}

	switch msg.Detail {
	case "wrong_line":
		return "誤切断" + line
	case "precondition_unmet":
		return "手順未達のまま切断" + line
	case "forbidden_rotary":
		return "禁止位置でダイヤルを停止"
	case "push_seq":
		return "ボタン列の入力ミス"
	default:
		return "誤操作" + line
	}
}

// describePenalty はペナルティをログ用の文字列にする。
func describePenalty(penaltyMS int) string {
	if penaltyMS <= 0 {
		return ""
	}
	return fmt.Sprintf(" −%d秒", penaltyMS/1000)
}

// describeExplodeReason は爆発理由をログ用の日本語にする (§7.2 の reason)。
func describeExplodeReason(msg *deviceMessage) string {
	switch msg.Reason {
	case "timeout":
		return "時間切れ"
	case "forced":
		return "マネージャーによる強制破裂"
	case "wrong_cut":
		return describeWrongAction(msg)
	default:
		return msg.Reason
	}
}
