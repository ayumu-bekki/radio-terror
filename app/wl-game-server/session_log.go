package main

import (
	"sync"
	"time"
)

// sessionLogWindow はプロンプトへ渡す直近の交信件数。
// 無線の文脈として必要十分な範囲に絞り、プロンプト長を抑える。
const sessionLogWindow = 30

// SessionLogStore はセッションごとの会話ログを保持する。
//
// 会話ログは Valkey へも永続化し、サーバー再起動後もナビゲーターが文脈を保って
// 復帰できるようにする (docs/game_session_design.md §9)。
type SessionLogStore struct {
	mu   sync.RWMutex
	logs map[string]*ConversationLog

	// store は永続化先。nil の場合はメモリのみ。
	store SessionStore
}

func NewSessionLogStore(store SessionStore) *SessionLogStore {
	return &SessionLogStore{
		logs:  make(map[string]*ConversationLog),
		store: store,
	}
}

// Append はセッションのログへ1件追記する。
// 記録時刻はここで一括して付ける (呼び出し側で付け忘れないようにする)。
func (s *SessionLogStore) Append(sessionID string, entry ConversationEntry) {
	if entry.Kind == "" {
		entry.Kind = EntryKindSpeech
	}
	if entry.At == 0 {
		entry.At = time.Now().Unix()
	}

	s.logFor(sessionID).Append(entry)

	if s.store != nil {
		if err := s.store.AppendLog(sessionID, entry); err != nil {
			// ログの永続化失敗はゲーム進行を止めない
			logAppendError(sessionID, err)
		}
	}
}

// AppendEvent はゲーム進行イベントを1件追記する。
//
// 発話と同じタイムラインへ時系列で残すことで、後から
// 「何を言った直後に何が起きたか」を1本の流れで追えるようにする。
func (s *SessionLogStore) AppendEvent(sessionID, event, message string, stageIndex, remainingMS int) {
	s.Append(sessionID, ConversationEntry{
		Kind:        EntryKindEvent,
		Event:       event,
		Message:     message,
		StageIndex:  stageIndex,
		RemainingMS: remainingMS,
	})
}

// Render はプロンプトへ渡す直近の交信を整形して返す。
func (s *SessionLogStore) Render(sessionID string) string {
	return s.logFor(sessionID).Render()
}

// Entries はセッションの全交信を返す (マネージャー向け Web 画面用)。
func (s *SessionLogStore) Entries(sessionID string) []ConversationEntry {
	log := s.logFor(sessionID)
	log.mu.RLock()
	defer log.mu.RUnlock()

	entries := make([]ConversationEntry, len(log.entries))
	copy(entries, log.entries)
	return entries
}

// Restore は永続化から読み出したログをメモリへ戻す。
func (s *SessionLogStore) Restore(sessionID string, entries []ConversationEntry) {
	log := s.logFor(sessionID)
	for _, entry := range entries {
		log.Append(entry)
	}
}

func (s *SessionLogStore) logFor(sessionID string) *ConversationLog {
	s.mu.RLock()
	log, ok := s.logs[sessionID]
	s.mu.RUnlock()
	if ok {
		return log
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if log, ok := s.logs[sessionID]; ok {
		return log
	}
	log = NewConversationLog(sessionLogWindow)
	s.logs[sessionID] = log
	return log
}
