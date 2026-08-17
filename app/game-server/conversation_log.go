package main

import (
	"strings"
	"sync"
)

// ログ種別 (docs/game_session_design.md §9)。
//
// 発話とゲーム進行イベントを**1本のタイムライン**に時系列で残す。
// 「ナビが何を言った直後に誤切断したか」を1本の流れで追えるようにするため。
const (
	// EntryKindSpeech はプレイヤー・ナビゲーターの発話
	EntryKindSpeech = "speech"
	// EntryKindEvent はゲーム進行イベント (ステージ開始・クリア・誤操作・終了)
	EntryKindEvent = "event"
)

// イベントの細分 (EntryKindEvent のときの Event フィールド)。
const (
	EventSessionStart   = "session_start"    // セッション開始
	EventStageStart     = "stage_start"      // ステージ開始
	EventStageCleared   = "stage_cleared"    // ステージクリア
	EventColorMatchDone = "color_match_done" // 色合わせ完了
	EventPushProgress   = "push_progress"    // ボタン列の進捗
	EventWrongAction    = "wrong_action"     // 誤操作
	EventDefused        = "defused"          // 解除成功
	EventExploded       = "exploded"         // 爆発 (失敗)
	EventAborted        = "aborted"          // マネージャーによる強制リセット
	EventForced         = "forced"           // マネージャーによる強制破裂の指示
)

// senderPlayer はプレイヤー発話の Sender 名。
// マネージャー画面はこの名前でナビゲーター側の発話と表示を分けるため、
// 記録側と表示側で同じ定数を参照する。
const senderPlayer = "プレイヤー"

// ConversationEntry はログ1件を表す。発話とイベントの両方を扱う。
//
// JSONタグは永続化(Valkey)とマネージャー向けWeb画面の両方で使う。
// 保存済みデータとの互換のため、既存フィールドのタグは変更しないこと。
type ConversationEntry struct {
	Sender   string `json:"sender"`   // 発言者 (発話時)
	Receiver string `json:"receiver"` // 対象 (発話時)
	Message  string `json:"message"`  // 発話内容 / イベントの説明文

	// Kind は種別。空文字は EntryKindSpeech とみなす
	// (この項目を導入する前に保存されたログとの互換のため)。
	Kind string `json:"kind,omitempty"`

	// Event は Kind == EntryKindEvent のときの細分
	Event string `json:"event,omitempty"`

	// StageIndex / RemainingMS はイベント発生時点のゲーム状態
	StageIndex  int `json:"stage_index,omitempty"`
	RemainingMS int `json:"remaining_ms,omitempty"`

	// At は記録時刻 (Unix秒)。0 は時刻不明 (導入前のログ)。
	At int64 `json:"at,omitempty"`
}

// IsEvent はこの記録がゲーム進行イベントかを返す。
func (e ConversationEntry) IsEvent() bool { return e.Kind == EntryKindEvent }

// ConversationLog は全接続で共有する単一の会話ログ。
// 無線は単一周波数のブロードキャストなので、全プレイヤー・全NPCの発話を時系列で1本に記録する。
// 各NPCはこのログ全体を文脈として参照するため、自分が直接関与しない交信も「聞こえている」前提になる。
type ConversationLog struct {
	mu      sync.RWMutex
	entries []ConversationEntry
	max     int // 0=無制限。将来のウィンドウ化(直近N件のみ保持)用
}

// NewConversationLog は共有会話ログを生成する。max=0 で無制限。
func NewConversationLog(max int) *ConversationLog {
	return &ConversationLog{max: max}
}

// Append は発話を1件追記する。max>0 の場合は古い発話を切り捨てて直近 max 件のみ保持する。
func (l *ConversationLog) Append(e ConversationEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	if l.max > 0 && len(l.entries) > l.max {
		l.entries = l.entries[len(l.entries)-l.max:]
	}
}

// Render は全履歴を改行区切り文字列に整形する。
// NPC応答生成時にプロンプトの文脈として渡す。
//
// 発話は "[Sender] -> [Receiver] Message"、イベントは "(装置) Message" とする。
// イベントも文脈に含めることで、ナビゲーターは「直前に何が起きたか」を
// 踏まえた発話ができる。
func (l *ConversationLog) Render() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var b strings.Builder
	for i, e := range l.entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		if e.IsEvent() {
			b.WriteString("(装置) ")
			b.WriteString(e.Message)
			continue
		}
		b.WriteString("[")
		b.WriteString(e.Sender)
		b.WriteString("] -> [")
		b.WriteString(e.Receiver)
		b.WriteString("] ")
		b.WriteString(e.Message)
	}
	return b.String()
}
