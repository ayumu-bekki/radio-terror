package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/redis/go-redis/v9"
)

// SessionStore はセッション状態・履歴・会話ログ・バインドの永続化先。
//
// **`session:*` と `history:*` は役割が違う。**
// `session:*` は「今バインド中のセッション」専用で、再起動時の復元と
// 進行中ダッシュボード表示にしか使わない。マネージャーがリセットするたび
// (終了後の定型リセットも含め) 無条件に消える (P-4b)。
// `history:*` は Management Console の履歴一覧・詳細ページ専用で、
// セッションが終了した瞬間に一度だけ書き、以後リセットされても消えない (§9)。
// 両方に同じ `*GameSession` の JSON を保存する
// (詳細ページが `Built.Stages` まで使うため)。
//
// キー設計 (docs/scenario_design.md §6):
//
//	session:{session_id}       … 進行中セッション本体 (Core向けJSON・ナビゲーター知識・
//	                              difficulty・bridge_id ⇔ device_id・状態・
//	                              キャラクター割当・スコア)
//	history:{session_id}       … 終了時点のスナップショット (履歴閲覧用。内容は session と同形)
//	session:{session_id}:log   … 会話・イベントのログ (リスト)
//	binding:{bridge_id}        … 現在のバインド先 device_id
type SessionStore interface {
	SaveSession(ctx context.Context, session *GameSession) error
	LoadSessions(ctx context.Context) ([]*GameSession, error)
	// DeleteSession はセッション本体とバインドを消す。**ログは残す**。
	//
	// リセットのたびに (終了後の定型リセットも含め) 無条件に呼んでよい。
	// 履歴は `SaveHistory` が別途 `history:{id}` へ残しているため、
	// ここで消しても Management Console の履歴からは消えない (§9)。
	DeleteSession(ctx context.Context, session *GameSession) error
	// SaveHistory はセッション終了時点のスナップショットを `history:{id}` へ保存する。
	// `session:{id}` とは独立したキーなので、その後のリセットで消えない。
	SaveHistory(ctx context.Context, session *GameSession) error
	// LoadHistories は保存済みの履歴を全件返す (Management Console の履歴一覧用)。
	LoadHistories(ctx context.Context) ([]*GameSession, error)
	AppendLog(sessionID string, entry ConversationEntry) error
	LoadLog(ctx context.Context, sessionID string) ([]ConversationEntry, error)
}

// ValkeyStore は Valkey (Redis互換) を使う永続化実装。
type ValkeyStore struct {
	client *redis.Client
	ctx    context.Context
}

// NewValkeyStore は Valkey へ接続する。接続できない場合はエラーを返すので、
// 呼び出し側でメモリ実装へフォールバックする。
func NewValkeyStore(ctx context.Context, addr string) (*ValkeyStore, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("valkey ping: %w", err)
	}
	return &ValkeyStore{client: client, ctx: ctx}, nil
}

func sessionKey(sessionID string) string    { return "session:" + sessionID }
func sessionLogKey(sessionID string) string { return "session:" + sessionID + ":log" }
func bindingKey(bridgeID string) string     { return "binding:" + bridgeID }
func historyKey(sessionID string) string    { return "history:" + sessionID }

func (s *ValkeyStore) SaveSession(ctx context.Context, session *GameSession) error {
	session.mu.Lock()
	data, err := json.Marshal(session)
	bridgeID, deviceID := session.BridgeID, session.DeviceID
	sessionID := session.SessionID
	session.mu.Unlock()

	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	if err := s.client.Set(ctx, sessionKey(sessionID), data, 0).Err(); err != nil {
		return fmt.Errorf("set session: %w", err)
	}
	if bridgeID != "" {
		if err := s.client.Set(ctx, bindingKey(bridgeID), deviceID, 0).Err(); err != nil {
			return fmt.Errorf("set binding: %w", err)
		}
	}
	return nil
}

// DeleteSession はセッション本体・ログ・バインドを Valkey から消す。
//
// これが無いと、**リセットしたセッションが再起動で復活する**。
// AbortSession はメモリ上の binder を外すだけなので、Valkey に残った
// セッションを LoadSessions が読み戻してしまう (実運用で発生)。
func (s *ValkeyStore) DeleteSession(ctx context.Context, session *GameSession) error {
	session.mu.Lock()
	sessionID, bridgeID := session.SessionID, session.BridgeID
	session.mu.Unlock()

	// **ログは消さない。** 「なぜ終わったか」を後から追えるようにするため
	// (中断イベントもここに記録されている)。復活するのはセッション本体なので、
	// それとバインドだけ消せば足りる。
	keys := []string{sessionKey(sessionID)}
	// バインドのキーも消しておく (掃除)。
	//
	// 復元はセッション本体から行うので `binding:` は現状**読まれていない**が、
	// 消したセッションを指したまま残るのは紛らわしい。
	// なお**メモリ上の bridge → device の紐付けは残す** — 再接続で継続できる
	// ようにする設計 (SessionBinder.Release のコメント参照)。
	if bridgeID != "" {
		keys = append(keys, bindingKey(bridgeID))
	}
	if err := s.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("del session: %w", err)
	}
	return nil
}

// LoadSessions は保存済みの全セッションを読み出す。
// サーバー再起動時に呼び、進行状態は Core からの device_status で再同期する。
func (s *ValkeyStore) LoadSessions(ctx context.Context) ([]*GameSession, error) {
	keys, err := s.client.Keys(ctx, "session:*").Result()
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}

	var sessions []*GameSession
	for _, key := range keys {
		// ログのキー (session:{id}:log) は除外する
		if len(key) > 4 && key[len(key)-4:] == ":log" {
			continue
		}

		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			log.Printf("[store] get %s: %v", key, err)
			continue
		}

		var session GameSession
		if err := json.Unmarshal(data, &session); err != nil {
			log.Printf("[store] unmarshal %s: %v", key, err)
			continue
		}
		sessions = append(sessions, &session)
	}
	return sessions, nil
}

// SaveHistory はセッション終了時点のスナップショットを `history:{id}` へ保存する。
//
// `session:{id}` とは独立したキーに書く。前者はリセットのたびに無条件で
// 消える (DeleteSession) が、履歴はそれとは無関係に残す必要があるため (§9)。
func (s *ValkeyStore) SaveHistory(ctx context.Context, session *GameSession) error {
	session.mu.Lock()
	data, err := json.Marshal(session)
	sessionID := session.SessionID
	session.mu.Unlock()

	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if err := s.client.Set(ctx, historyKey(sessionID), data, 0).Err(); err != nil {
		return fmt.Errorf("set history: %w", err)
	}
	return nil
}

// LoadHistories は保存済みの履歴を全件読み出す (Management Console の履歴一覧・詳細用)。
func (s *ValkeyStore) LoadHistories(ctx context.Context) ([]*GameSession, error) {
	keys, err := s.client.Keys(ctx, "history:*").Result()
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}

	var sessions []*GameSession
	for _, key := range keys {
		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			log.Printf("[store] get %s: %v", key, err)
			continue
		}

		var session GameSession
		if err := json.Unmarshal(data, &session); err != nil {
			log.Printf("[store] unmarshal %s: %v", key, err)
			continue
		}
		sessions = append(sessions, &session)
	}
	return sessions, nil
}

func (s *ValkeyStore) AppendLog(sessionID string, entry ConversationEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	return s.client.RPush(s.ctx, sessionLogKey(sessionID), data).Err()
}

func (s *ValkeyStore) LoadLog(ctx context.Context, sessionID string) ([]ConversationEntry, error) {
	items, err := s.client.LRange(ctx, sessionLogKey(sessionID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("lrange: %w", err)
	}

	entries := make([]ConversationEntry, 0, len(items))
	for _, item := range items {
		var entry ConversationEntry
		if err := json.Unmarshal([]byte(item), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// MemoryStore は Valkey が使えない環境向けのメモリ実装。
// 再起動での復帰はできないが、開発・単体動作を止めないためのフォールバック。
type MemoryStore struct {
	mu        sync.RWMutex
	sessions  map[string]*GameSession
	histories map[string]*GameSession
	logs      map[string][]ConversationEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:  make(map[string]*GameSession),
		histories: make(map[string]*GameSession),
		logs:      make(map[string][]ConversationEntry),
	}
}

func (s *MemoryStore) SaveSession(ctx context.Context, session *GameSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.SessionID] = session
	return nil
}

func (s *MemoryStore) DeleteSession(ctx context.Context, session *GameSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, session.SessionID)
	// ログは残す (ValkeyStore と同じ理由)
	return nil
}

func (s *MemoryStore) SaveHistory(ctx context.Context, session *GameSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histories[session.SessionID] = session
	return nil
}

func (s *MemoryStore) LoadHistories(ctx context.Context) ([]*GameSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*GameSession, 0, len(s.histories))
	for _, session := range s.histories {
		list = append(list, session)
	}
	return list, nil
}

func (s *MemoryStore) LoadSessions(ctx context.Context) ([]*GameSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*GameSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		list = append(list, session)
	}
	return list, nil
}

func (s *MemoryStore) AppendLog(sessionID string, entry ConversationEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs[sessionID] = append(s.logs[sessionID], entry)
	return nil
}

func (s *MemoryStore) LoadLog(ctx context.Context, sessionID string) ([]ConversationEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]ConversationEntry, len(s.logs[sessionID]))
	copy(entries, s.logs[sessionID])
	return entries, nil
}

// logAppendError は会話ログの永続化失敗を記録する (進行は止めない)。
func logAppendError(sessionID string, err error) {
	log.Printf("[store] append log failed (session=%s): %v", sessionID, err)
}
