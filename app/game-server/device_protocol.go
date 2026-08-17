package main

// デバイス (core-system) ⇔ サーバー の WebSocket メッセージ定義。
// 仕様は docs/game_session_design.md §7。

// --- デバイス → サーバー (§7.2) ---

// deviceMessage はデバイスから届く全メッセージの共通部分。
// type と device_id で振り分けたあと、type ごとのフィールドを読む。
type deviceMessage struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`

	// device_status
	State      string          `json:"state"`
	SessionID  string          `json:"session_id"`
	Battery    float64         `json:"battery"`
	LowBattery bool            `json:"low_battery"`
	Lines      map[string]bool `json:"lines"`
	// Rotary はロータリースイッチの現在位置 (0-5)。
	// ポインタなのは**位置0と未報告を区別する**ため。0 は正当な位置であり、
	// 値が来ない場合 (rotary を送らない旧ファーム) と混同すると
	// 画面に「ダイヤル: 0」と嘘を表示してしまう。
	Rotary *int `json:"rotary"`

	// 進行イベント共通
	StageIndex  int `json:"stage_index"`
	RemainingMS int `json:"remaining_ms"`

	// push_progress
	SeqIndex int `json:"seq_index"`

	// wrong_action / exploded
	Detail    string `json:"detail"`
	Line      string `json:"line"`
	PenaltyMS int    `json:"penalty_ms"`
	Reason    string `json:"reason"`
}

// デバイス → サーバー のメッセージ type (§7.2)
const (
	msgDeviceStatus    = "device_status"
	msgSessionAccepted = "session_accepted"
	msgSessionRejected = "session_rejected"
	msgStageCleared    = "stage_cleared"
	msgWhackCompleted  = "whack_completed"
	msgPushProgress    = "push_progress"
	msgWrongAction     = "wrong_action"
	msgExploded        = "exploded"
	msgDefused         = "defused"
)

// --- サーバー → デバイス (§7.1) ---

// サーバー → デバイス のメッセージ type
const (
	msgSessionStart   = "session_start"
	msgSessionPending = "session_pending"
	msgSessionAbort   = "session_abort"
	msgForceDetonate  = "force_detonate"
)

// sessionPendingCommand は開始申告が通ったことの通知 (§4.2)。
//
// カウントダウンはまだ始まらない。ナビゲーターがマネージャーへ応答し、
// 鳴り終わってから session_start が届く。その間デバイスは青点滅で
// 「開始申告は通った、まもなく始まる」を示す。
type sessionPendingCommand struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
}

// sessionAbortCommand は安全に中断して Setup へ戻す指示。
type sessionAbortCommand struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
}

// forceDetonateCommand は即座に爆発シーケンスへ入る指示。
type forceDetonateCommand struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
}

// デバイスの状態名 (§4)。device_status.state に載る値。
const (
	deviceStateSetup      = "setup"
	deviceStateReady      = "ready"
	deviceStatePending    = "pending"
	deviceStatePlaying    = "playing"
	deviceStateDetonating = "detonating"
	deviceStateExploded   = "exploded"
	deviceStateDefused    = "defused"
)

// DeviceStatus はサーバーが保持するデバイスの最新報告。
// WS 再接続時の再同期・マネージャー向け Web 画面の表示に使う (§7.3)。
type DeviceStatus struct {
	DeviceID    string          `json:"device_id"`
	State       string          `json:"state"`
	SessionID   string          `json:"session_id"`
	StageIndex  int             `json:"stage_index"`
	RemainingMS int             `json:"remaining_ms"`
	Battery     float64         `json:"battery"`
	LowBattery  bool            `json:"low_battery"`
	Lines       map[string]bool `json:"lines"`
	// Rotary はロータリースイッチの現在位置 (0-5)。未報告なら nil。
	Rotary    *int  `json:"rotary,omitempty"`
	UpdatedAt int64 `json:"updated_at"`
}

// IsReady は session_start を受理できる状態かを返す (§4)。
//
// **pending も受理できる** — 開始申告が通ってナビゲーターの応答を
// 待っている状態で、その直後に session_start が届く (§4.2)。
// ここを ready だけにすると、開始申告のあと5秒間だけ
// 「自分が送った session_start を自分で拒否する」状態になる。
func (s *DeviceStatus) IsReady() bool {
	return s != nil && (s.State == deviceStateReady || s.State == deviceStatePending)
}

// IsPlaying はゲーム進行中かを返す。バインド競合の判定に使う (§5)。
func (s *DeviceStatus) IsPlaying() bool {
	return s != nil && s.State == deviceStatePlaying
}
