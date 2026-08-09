package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// APIHealth は外部API (生成AI・TTS) の直近の状況。
//
// 生成AIの障害時は自動フォールバックを設けずマネージャー介入で運用するため、
// 障害を Web 画面で検知できるようにする (docs/game_session_design.md §9)。
type APIHealth struct {
	mu sync.Mutex

	lastSuccess time.Time
	lastError   time.Time
	lastMessage string
	errorCount  int
}

// APIHealthSnapshot は APIHealth の複製 (JSON化・受け渡し用)。
// ロックを含まないため安全にコピーできる。
type APIHealthSnapshot struct {
	LastSuccess time.Time `json:"last_success"`
	LastError   time.Time `json:"last_error"`
	LastMessage string    `json:"last_message"`
	ErrorCount  int       `json:"error_count"`
}

func (h *APIHealth) NoteSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSuccess = time.Now()
}

func (h *APIHealth) NoteError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastError = time.Now()
	h.lastMessage = err.Error()
	h.errorCount++
}

func (h *APIHealth) Snapshot() APIHealthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return APIHealthSnapshot{
		LastSuccess: h.lastSuccess,
		LastError:   h.lastError,
		LastMessage: h.lastMessage,
		ErrorCount:  h.errorCount,
	}
}

// ManagerWeb はマネージャー向けの簡易 Web 画面を提供する。
// セッション状況 (Core状態・進行・バインド)、会話・イベントログ、
// 外部APIの状況を確認できる (docs/game_session_design.md §9)。
type ManagerWeb struct {
	devices   *DeviceRegistry
	bridges   *BridgeRegistry
	game      *GameCoordinator
	logs      *SessionLogStore
	crosstalk *CrosstalkLibrary
	health    *APIHealth

	// store は終了済みセッションの履歴を読むための永続化層。
	// 進行中セッションはメモリから消えるため、履歴はここから引く。
	store SessionStore
}

func NewManagerWeb(
	devices *DeviceRegistry,
	bridges *BridgeRegistry,
	game *GameCoordinator,
	logs *SessionLogStore,
	crosstalk *CrosstalkLibrary,
	health *APIHealth,
	store SessionStore,
) *ManagerWeb {
	return &ManagerWeb{
		devices:   devices,
		bridges:   bridges,
		game:      game,
		logs:      logs,
		crosstalk: crosstalk,
		health:    health,
		store:     store,
	}
}

// Register は HTTP ハンドラを登録する。
func (w *ManagerWeb) Register(mux *http.ServeMux) {
	mux.HandleFunc("/manager", w.handleIndex)
	mux.HandleFunc("/manager/api/status", w.handleStatus)
	mux.HandleFunc("/manager/api/log", w.handleLog)
	mux.HandleFunc("/manager/api/abort", w.handleAbort)
	mux.HandleFunc("/manager/api/history", w.handleHistory)
	mux.HandleFunc("/manager/api/transcript", w.handleTranscript)
}

// statusResponse は Web 画面が表示する情報一式。
type statusResponse struct {
	Devices   []*DeviceStatus   `json:"devices"`
	Bridges   []string          `json:"bridges"`
	Bindings  map[string]string `json:"bindings"`
	Sessions  []sessionSummary  `json:"sessions"`
	Assets    map[string]int    `json:"assets"`
	Health    APIHealthSnapshot `json:"health"`
	Timestamp time.Time         `json:"timestamp"`
}

// sessionSummary は進行中セッションの要約 (正解は含めるが表示側で伏せる)。
type sessionSummary struct {
	SessionID   string `json:"session_id"`
	DeviceID    string `json:"device_id"`
	BridgeID    string `json:"bridge_id"`
	Difficulty  string `json:"difficulty"`
	Character   string `json:"character"`
	State       string `json:"state"`
	StageIndex  int    `json:"stage_index"`
	StageCount  int    `json:"stage_count"`
	StageName   string `json:"stage_name"`
	Answer      string `json:"answer"`
	RemainingMS int    `json:"remaining_ms"`
	Score       int    `json:"score"`
}

func (w *ManagerWeb) handleStatus(rw http.ResponseWriter, r *http.Request) {
	sessions := make([]sessionSummary, 0)
	for _, session := range w.game.Sessions() {
		session.mu.Lock()
		summary := sessionSummary{
			SessionID:   session.SessionID,
			DeviceID:    session.DeviceID,
			BridgeID:    session.BridgeID,
			Difficulty:  session.Difficulty,
			Character:   session.Character.Name,
			State:       session.State,
			StageIndex:  session.StageIndex,
			RemainingMS: session.RemainingMS,
			Score:       session.Score,
		}
		if session.Built != nil {
			summary.StageCount = len(session.Built.Stages)
			if session.StageIndex >= 0 && session.StageIndex < len(session.Built.Stages) {
				stage := session.Built.Stages[session.StageIndex]
				summary.StageName = stage.Name
				summary.Answer = stage.Navigator["answer"]
			}
		}
		session.mu.Unlock()
		sessions = append(sessions, summary)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].DeviceID < sessions[j].DeviceID })

	devices := w.devices.AllStatus()
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceID < devices[j].DeviceID })

	bridges := w.bridges.IDs()
	sort.Strings(bridges)

	resp := statusResponse{
		Devices:   devices,
		Bridges:   bridges,
		Bindings:  w.game.Bindings(),
		Sessions:  sessions,
		Assets:    w.crosstalk.AssetSummary(),
		Health:    w.health.Snapshot(),
		Timestamp: time.Now(),
	}

	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(resp); err != nil {
		log.Printf("[manager-web] encode error: %v", err)
	}
}

func (w *ManagerWeb) handleLog(rw http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(rw, "session_id is required", http.StatusBadRequest)
		return
	}

	entries := w.entriesFor(r.Context(), sessionID)

	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(entries); err != nil {
		log.Printf("[manager-web] encode error: %v", err)
	}
}

// entriesFor はセッションのログを返す。メモリに無ければ永続化層から読む
// (サーバー再起動後・終了済みセッションの履歴閲覧に対応するため)。
func (w *ManagerWeb) entriesFor(ctx context.Context, sessionID string) []ConversationEntry {
	if entries := w.logs.Entries(sessionID); len(entries) > 0 {
		return entries
	}
	if w.store == nil {
		return []ConversationEntry{}
	}
	entries, err := w.store.LoadLog(ctx, sessionID)
	if err != nil {
		log.Printf("[manager-web] load log %s: %v", sessionID, err)
		return []ConversationEntry{}
	}
	return entries
}

// historyItem は履歴一覧の1件。
type historyItem struct {
	SessionID  string `json:"session_id"`
	DeviceID   string `json:"device_id"`
	Difficulty string `json:"difficulty"`
	Character  string `json:"character"`
	State      string `json:"state"`
	StageIndex int    `json:"stage_index"`
	StageCount int    `json:"stage_count"`
	Score      int    `json:"score"`
	StartedAt  int64  `json:"started_at"`
	Result     string `json:"result"`
}

// handleHistory は保存済みセッションを新しい順に返す。
func (w *ManagerWeb) handleHistory(rw http.ResponseWriter, r *http.Request) {
	items := make([]historyItem, 0)

	if w.store != nil {
		sessions, err := w.store.LoadSessions(r.Context())
		if err != nil {
			log.Printf("[manager-web] load sessions: %v", err)
		}
		for _, session := range sessions {
			session.mu.Lock()
			item := historyItem{
				SessionID:  session.SessionID,
				DeviceID:   session.DeviceID,
				Difficulty: session.Difficulty,
				Character:  session.Character.Name,
				State:      session.State,
				StageIndex: session.StageIndex,
				Score:      session.Score,
				StartedAt:  session.StartedAt.Unix(),
			}
			if session.Built != nil {
				item.StageCount = len(session.Built.Stages)
			}
			item.Result = describeSessionResult(session.State, session.Score)
			session.mu.Unlock()
			items = append(items, item)
		}
	}

	// 新しい順
	sort.Slice(items, func(i, j int) bool { return items[i].StartedAt > items[j].StartedAt })

	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(items); err != nil {
		log.Printf("[manager-web] encode error: %v", err)
	}
}

// describeSessionResult はセッションの結末を短い日本語にする。
func describeSessionResult(state string, score int) string {
	switch state {
	case deviceStateDefused:
		return fmt.Sprintf("解除成功 (残り%.1fs)", float64(score)/1000)
	case deviceStateExploded, deviceStateDetonating:
		return "爆発"
	case deviceStatePlaying:
		return "進行中"
	default:
		return state
	}
}

// handleTranscript はセッションのログをテキストで返す (保存・共有用)。
func (w *ManagerWeb) handleTranscript(rw http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(rw, "session_id is required", http.StatusBadRequest)
		return
	}

	entries := w.entriesFor(r.Context(), sessionID)

	var b strings.Builder
	fmt.Fprintf(&b, "RADIO TERROR 交信ログ\nセッション: %s\n\n", sessionID)
	for _, e := range entries {
		stamp := "--:--:--"
		if e.At > 0 {
			stamp = time.Unix(e.At, 0).Format("15:04:05")
		}
		if e.IsEvent() {
			fmt.Fprintf(&b, "%s [装置] %s\n", stamp, e.Message)
			continue
		}
		fmt.Fprintf(&b, "%s %s → %s: %s\n", stamp, e.Sender, e.Receiver, e.Message)
	}

	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", sessionID+".txt"))
	rw.Write([]byte(b.String()))
}

// handleAbort は Web 画面からの強制リセット。無線が使えない場合の代替手段。
func (w *ManagerWeb) handleAbort(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "POST required", http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		http.Error(rw, "device_id is required", http.StatusBadRequest)
		return
	}

	// デバイス未接続でもサーバー側のリセットは完了しているため、
	// 画面側はエラー扱いにせず、届かなかったことだけを伝える。
	if err := w.game.AbortSession(r.Context(), nil, deviceID); err != nil {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(rw, "サーバー側のセッションは終了しました。ただしデバイスへは届いていません: %v", err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

func (w *ManagerWeb) handleIndex(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Write([]byte(managerIndexHTML))
}

// managerIndexHTML はマネージャー向け簡易画面。
//
// HTML/CSS/JS は manager_page.html に置き、ビルド時に埋め込む。
// Go の文字列リテラルに埋めるとバッククォート(JSのテンプレートリテラル)が
// 使えず可読性を損なうため、別ファイルにしている。
//
//go:embed manager_page.html
var managerIndexHTML string
