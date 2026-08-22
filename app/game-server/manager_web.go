package main

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
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

// APIHealthSnapshot は APIHealth の複製 (受け渡し用)。
// ロックを含まないため安全にコピーできる。
type APIHealthSnapshot struct {
	LastSuccess time.Time
	LastError   time.Time
	LastMessage string
	ErrorCount  int
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

// ManagerWeb はマネージャー向けの Web 画面を提供する。
//
// 画面はサーバー側で描画する (html/template)。表示の判断はすべて
// ビューモデル (manager_view.go) に寄せ、テンプレートは値を並べるだけにする
// (docs/game_session_design.md §10 決定32)。
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
//
// 画面は3ページに分かれる (docs/game_session_design.md §9):
//
//	/manager                        … ダッシュボード (進行中の監視)
//	/manager/history                … 履歴一覧
//	/manager/history/{session_id}   … セッション詳細
//
// 表示用のJSON APIは持たない (ページがサーバー側で完成して返るため)。
// 残る2つは画面の描画ではなく操作・書き出しの口。
func (w *ManagerWeb) Register(mux *http.ServeMux) {
	mux.HandleFunc("/manager", w.handleIndex)
	mux.HandleFunc("/manager/manager.css", w.handleCSS)
	mux.HandleFunc("/manager/history", w.handleHistoryPage)
	// 末尾スラッシュのパターンは前方一致。{session_id} をここで受ける
	mux.HandleFunc("/manager/history/", w.handleSessionPage)
	mux.HandleFunc("/manager/api/abort", w.handleAbort)
	mux.HandleFunc("/manager/api/detonate", w.handleDetonate)
	mux.HandleFunc("/manager/api/transcript", w.handleTranscript)
}

// --- ダッシュボード ---

// dashboardData はダッシュボードのテンプレートへ渡す値。
type dashboardData struct {
	Sessions []sessionView
	Devices  []deviceView
	Bridges  []bridgeView

	// Tabs は交信ログのタブ (進行中セッション)。
	Tabs []logTabView
	// SelectedSession は表示中のログのセッションID
	SelectedSession string
	Entries         []entryView
}

// logTabView は交信ログのタブ1つ。
type logTabView struct {
	SessionID string
	DeviceID  string
	Selected  bool
}

// handleIndex はダッシュボードを描画する。
//
// partial=live のときは進行中の部分だけを返す。画面側が2秒ごとに取得して
// 差し替えるため、ページ全体をリロードせずスクロール位置を保てる。
func (w *ManagerWeb) handleIndex(rw http.ResponseWriter, r *http.Request) {
	sessions := buildSessionViews(w.game.Sessions())

	data := dashboardData{
		Sessions: sessions,
		Devices:  buildDeviceViews(w.devices.AllStatus(), w.devices.IsConnected),
		Bridges:  buildBridgeViews(w.bridges.IDs(), w.game.Bindings()),
	}

	// 表示するログのセッションを決める。指定が無ければ先頭の進行中セッション
	selected := r.URL.Query().Get("session_id")
	if !hasSession(sessions, selected) {
		selected = ""
		if len(sessions) > 0 {
			selected = sessions[0].SessionID
		}
	}
	data.SelectedSession = selected

	for _, s := range sessions {
		data.Tabs = append(data.Tabs, logTabView{
			SessionID: s.SessionID,
			DeviceID:  s.DeviceID,
			Selected:  s.SessionID == selected,
		})
	}
	if selected != "" {
		data.Entries = buildEntryViews(w.entriesFor(r.Context(), selected))
	}

	// 差し替え用の部分描画 (進行中の状態だけ)
	if r.URL.Query().Get("partial") == "live" {
		w.render(rw, managerPageTmpl, "live", data)
		return
	}

	w.render(rw, managerPageTmpl, "page", data)
}

// hasSession は指定IDが進行中セッションに含まれるかを返す。
func hasSession(sessions []sessionView, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return true
		}
	}
	return false
}

// --- 履歴一覧 ---

// historyFilter は履歴一覧の絞り込み条件 (クエリパラメータ)。
// 空文字は「すべて」を意味する。
type historyFilter struct {
	Date   string
	Device string
	Result string
}

// matches は1件が条件に合うかを返す。
func (f historyFilter) matches(item historyView, state string) bool {
	if f.Date != "" && item.DateKey != f.Date {
		return false
	}
	if f.Device != "" && item.DeviceID != f.Device {
		return false
	}
	if f.Result != "" && resultKind(state) != f.Result {
		return false
	}
	return true
}

// historyPageData は履歴一覧のテンプレートへ渡す値。
type historyPageData struct {
	Items  []historyView
	Filter historyFilter

	// Dates / Devices は絞り込みの選択肢 (実データから作る)
	Dates   []string
	Devices []string

	Shown int
	Total int
}

func (w *ManagerWeb) handleHistoryPage(rw http.ResponseWriter, r *http.Request) {
	filter := historyFilter{
		Date:   r.URL.Query().Get("date"),
		Device: r.URL.Query().Get("device"),
		Result: r.URL.Query().Get("result"),
	}

	all := w.historyViews(r.Context(), historyFilter{}, 0)
	shown := w.historyViews(r.Context(), filter, 0)

	// 選択肢は絞り込み前の全件から作る (絞った結果で選択肢が消えないように)
	dates := make([]string, 0)
	devices := make([]string, 0)
	seenDate := map[string]bool{}
	seenDevice := map[string]bool{}
	for _, item := range all {
		if item.DateKey != "" && !seenDate[item.DateKey] {
			seenDate[item.DateKey] = true
			dates = append(dates, item.DateKey)
		}
		if item.DeviceID != "" && !seenDevice[item.DeviceID] {
			seenDevice[item.DeviceID] = true
			devices = append(devices, item.DeviceID)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates))) // 新しい日付を上に
	sort.Strings(devices)

	w.render(rw, managerHistoryTmpl, "page", historyPageData{
		Items:   shown,
		Filter:  filter,
		Dates:   dates,
		Devices: devices,
		Shown:   len(shown),
		Total:   len(all),
	})
}

// historyViews は保存済みセッションを新しい順に返す。
//
// filter が空なら全件。limit が正なら先頭N件へ絞る。
func (w *ManagerWeb) historyViews(ctx context.Context, filter historyFilter, limit int) []historyView {
	items := make([]historyView, 0)
	if w.store == nil {
		return items
	}

	sessions, err := w.store.LoadHistories(ctx)
	if err != nil {
		log.Printf("[manager-web] load histories: %v", err)
		return items
	}

	// 並べ替えに使う開始時刻を view と一緒に持つ (view は整形済み文字列のため)
	type row struct {
		view      historyView
		startedAt time.Time
	}
	rows := make([]row, 0, len(sessions))

	for _, session := range sessions {
		session.mu.Lock()
		state := session.State
		item := historyView{
			SessionID:   session.SessionID,
			DeviceID:    session.DeviceID,
			Difficulty:  session.Difficulty,
			Character:   session.Character.Name,
			Result:      describeSessionResult(state, session.Score),
			ResultClass: resultClass(state),
			StartedAt:   fmtDateTime(session.StartedAt),
			DateKey:     dateKey(session.StartedAt),
		}
		stageCount := 0
		if session.Built != nil {
			stageCount = len(session.Built.Stages)
		}
		item.Progress = fmtProgress(session.StageIndex, stageCount)
		startedAt := session.StartedAt
		session.mu.Unlock()

		if !filter.matches(item, state) {
			continue
		}
		rows = append(rows, row{view: item, startedAt: startedAt})
	}

	// 新しい順
	sort.Slice(rows, func(i, j int) bool { return rows[i].startedAt.After(rows[j].startedAt) })

	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	for _, r := range rows {
		items = append(items, r.view)
	}
	return items
}

// dateKey は日付絞り込みのキー (YYYY-MM-DD)。ゼロ値は空。
//
// displayLocation (現場のタイムゾーン) の暦日で区切る。UTCのまま区切ると、
// 表示時刻 (JST) と日付の選択肢がずれる (UTC 15:00〜23:59 は JST では翌日)。
func dateKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(displayLocation).Format("2006-01-02")
}

// --- セッション詳細 ---

// sessionPageData はセッション詳細のテンプレートへ渡す値。
type sessionPageData struct {
	SessionID  string
	DeviceID   string
	BridgeID   string
	Difficulty string
	Character  string
	State      string
	Progress   string
	Result     string
	// ResultClass は結果の色分け
	ResultClass string
	// Score は残り時間 (解除成功時のみ。それ以外は「—」)
	Score     string
	StartedAt string

	// Live が真なら進行中。画面を自動更新する
	Live bool

	Stages  []stageView
	Entries []entryView
}

// handleSessionPage は /manager/history/{session_id} を描画する。
func (w *ManagerWeb) handleSessionPage(rw http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/manager/history/")
	// ID無しは一覧へ寄せる
	if sessionID == "" {
		http.Redirect(rw, r, "/manager/history", http.StatusFound)
		return
	}

	session := w.sessionFor(r.Context(), sessionID)
	if session == nil {
		http.Error(rw, "session not found", http.StatusNotFound)
		return
	}

	session.mu.Lock()
	data := sessionPageData{
		SessionID:   session.SessionID,
		DeviceID:    session.DeviceID,
		BridgeID:    session.BridgeID,
		Difficulty:  session.Difficulty,
		Character:   session.Character.Name,
		State:       session.State,
		Result:      describeSessionResult(session.State, session.Score),
		ResultClass: resultClass(session.State),
		Score:       fmtRemaining(session.Score),
		StartedAt:   fmtDateTimeFull(session.StartedAt),
		Live:        session.State == deviceStatePlaying,
	}
	stageCount := 0
	if session.Built != nil {
		stageCount = len(session.Built.Stages)
		data.Stages = buildStageViews(session.Built.Stages, session.StageIndex, session.State)
	}
	data.Progress = fmtProgress(session.StageIndex, stageCount)
	session.mu.Unlock()

	data.Entries = buildEntryViews(w.entriesFor(r.Context(), sessionID))

	w.render(rw, managerSessionTmpl, "page", data)
}

// fmtDateTimeFull は詳細ページの開始時刻 (年まで出す)。表示は displayLocation。
func fmtDateTimeFull(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(displayLocation).Format("2006/01/02 15:04:05")
}

// --- 共通 ---

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

// sessionFor はセッション本体を返す。進行中はメモリ、終了済みは履歴から引く
// (entriesFor と同じ方針)。見つからなければ nil。
func (w *ManagerWeb) sessionFor(ctx context.Context, sessionID string) *GameSession {
	for _, session := range w.game.Sessions() {
		session.mu.Lock()
		match := session.SessionID == sessionID
		session.mu.Unlock()
		if match {
			return session
		}
	}

	if w.store == nil {
		return nil
	}
	sessions, err := w.store.LoadHistories(ctx)
	if err != nil {
		log.Printf("[manager-web] load histories: %v", err)
		return nil
	}
	for _, session := range sessions {
		if session.SessionID == sessionID {
			return session
		}
	}
	return nil
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

// render はテンプレートを描画する。
//
// 描画中のエラーは応答の途中で起きうる (すでに書き出したバイトは戻せない)。
// 画面が途中で切れてもサーバーは止めず、ログに残して運営が気付けるようにする。
func (w *ManagerWeb) render(rw http.ResponseWriter, tmpl *template.Template, name string, data any) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(rw, name, data); err != nil {
		log.Printf("[manager-web] render %s: %v", name, err)
	}
}

func (w *ManagerWeb) handleCSS(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/css; charset=utf-8")
	rw.Write([]byte(managerCSS))
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
		stamp := fmtStamp(e.At)
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

// handleDetonate は Web 画面からの強制破裂。**風船が実際に割れる**。
//
// 進行中セッションが無い場合は 409 を返す。誤操作の影響が大きいため、
// abort と違って「届かなかったが状態は整理した」という緩い扱いはしない。
func (w *ManagerWeb) handleDetonate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "POST required", http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		http.Error(rw, "device_id is required", http.StatusBadRequest)
		return
	}

	if err := w.game.ForceDetonate(r.Context(), deviceID); err != nil {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.WriteHeader(http.StatusConflict)
		fmt.Fprintf(rw, "強制破裂できませんでした: %v", err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// マネージャー向け画面のテンプレートと共通CSS。
//
// テンプレートは別ファイルに置き、ビルド時に埋め込む。Go の文字列リテラルへ
// 埋めるとテンプレート記法とエスケープが読みづらくなるため。
// CSS は3ページで共有するので独立させている。
//
// 拡張子が `.gohtml` なのは、中身が素のHTMLではなく **html/template の
// テンプレート**だから。`{{define}}` で始まり単体では表示できないため、
// `.html` だと開いた人を誤解させる。
var (
	//go:embed manager_page.gohtml
	managerPageHTML string

	//go:embed manager_history.gohtml
	managerHistoryHTML string

	//go:embed manager_session.gohtml
	managerSessionHTML string

	//go:embed manager.css
	managerCSS string
)

// テンプレートは起動時に一度だけ解析する。
// 解析失敗はテンプレートの書き間違いであり、起動時に落として気付けるようにする。
var (
	managerPageTmpl    = template.Must(template.New("page").Parse(managerPageHTML))
	managerHistoryTmpl = template.Must(template.New("history").Parse(managerHistoryHTML))
	managerSessionTmpl = template.Must(template.New("session").Parse(managerSessionHTML))
)
