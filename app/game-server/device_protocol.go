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
	msgSessionStart  = "session_start"
	msgSessionAbort  = "session_abort"
	msgForceDetonate = "force_detonate"
	msgTimeAdjust    = "time_adjust"
)

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

// timeAdjustCommand は残り時間を増減させる (ナビゲーター演出用)。
type timeAdjustCommand struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
	DeltaMS  int    `json:"delta_ms"`
}

// デバイスの状態名 (§4)。device_status.state に載る値。
const (
	deviceStateSetup      = "setup"
	deviceStateReady      = "ready"
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
	UpdatedAt   int64           `json:"updated_at"`
}

// IsReady は session_start を受理できる状態かを返す (§4)。
func (s *DeviceStatus) IsReady() bool {
	return s != nil && s.State == deviceStateReady
}

// IsPlaying はゲーム進行中かを返す。バインド競合の判定に使う (§5)。
func (s *DeviceStatus) IsPlaying() bool {
	return s != nil && s.State == deviceStatePlaying
}
