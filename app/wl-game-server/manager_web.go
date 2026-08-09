package main

import (
	"context"
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
// 外部依存なしの1ファイル構成で、2秒ごとに /manager/api/status をポーリングする。
const managerIndexHTML = `<!doctype html>
<html lang="ja">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RADIO TERROR — マネージャー画面</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; padding: 1rem; background: #12141a; color: #e6e8ee;
         font-family: system-ui, -apple-system, "Hiragino Sans", sans-serif; }
  h1 { font-size: 1.1rem; margin: 0 0 1rem; letter-spacing: .04em; }
  h2 { font-size: .85rem; margin: 1.5rem 0 .5rem; color: #9aa3b5;
       text-transform: uppercase; letter-spacing: .08em; }
  table { width: 100%; border-collapse: collapse; font-size: .85rem; }
  th, td { text-align: left; padding: .4rem .6rem; border-bottom: 1px solid #262a35; }
  th { color: #9aa3b5; font-weight: 600; }
  .state { padding: .1rem .5rem; border-radius: 999px; font-size: .75rem; }
  .setup { background: #4a3f10; color: #f5d76e; }
  .ready { background: #10304a; color: #6ec1f5; }
  .playing { background: #4a1010; color: #f56e6e; }
  .defused { background: #103a1c; color: #6ef58f; }
  .exploded, .detonating { background: #4a1010; color: #f5a26e; }
  .warn { color: #f5a26e; }
  .err { color: #f56e6e; }
  .muted { color: #6b7383; }
  .answer { font-family: ui-monospace, monospace; font-size: .8rem; color: #f5d76e; }
  .log { font-size: .8rem; line-height: 1.7; max-height: 22rem; overflow-y: auto;
         background: #0d0f14; border: 1px solid #262a35; border-radius: 6px; padding: .6rem .8rem; }
  .log .t { color: #5a6272; font-family: ui-monospace, monospace; margin-right: .5rem; }
  .log .ev { color: #9aa3b5; }
  .log .ev-ok { color: #6ef58f; }
  .log .ev-ng { color: #f56e6e; }
  .log .ev-end { color: #f5d76e; font-weight: 600; }
  .log .navi { color: #6ec1f5; }
  .log .who { color: #9aa3b5; margin-right: .4rem; }
  .tabs { display: flex; gap: .4rem; margin-bottom: .6rem; flex-wrap: wrap; }
  .tab { background: #1c2029; border: 1px solid #2e3543; color: #9aa3b5; border-radius: 4px;
         padding: .25rem .7rem; cursor: pointer; font-size: .78rem; }
  .tab:hover { background: #262c38; }
  .tab.on { background: #2f4a63; color: #cfe6f7; border-color: #3f6a8f; }
  .row-actions a { color: #6ec1f5; font-size: .75rem; text-decoration: none; }
  button { background: #2a2f3d; color: #e6e8ee; border: 1px solid #3a4152;
           border-radius: 4px; padding: .2rem .6rem; cursor: pointer; font-size: .75rem; }
  button:hover { background: #3a4152; }
</style>
</head>
<body>
<h1>RADIO TERROR — マネージャー画面</h1>

<h2>セッション</h2>
<table><thead><tr>
  <th>Core</th><th>難易度</th><th>ナビ</th><th>状態</th><th>進行</th>
  <th>残り</th><th>現在の正解</th><th>無線</th><th></th>
</tr></thead><tbody id="sessions"></tbody></table>

<h2>デバイス</h2>
<table><thead><tr>
  <th>Core</th><th>状態</th><th>残り</th><th>電池</th><th>配線</th><th>最終報告</th>
</tr></thead><tbody id="devices"></tbody></table>

<h2>無線 (bridge)</h2>
<div id="bridges" class="muted"></div>

<h2>外部API / アセット</h2>
<div id="health" class="muted"></div>

<h2>交信ログ / 進行記録</h2>
<div class="tabs" id="logtabs"></div>
<div class="log" id="logs">—</div>

<h2>履歴 (過去のセッション)</h2>
<table><thead><tr>
  <th>開始時刻</th><th>Core</th><th>難易度</th><th>ナビ</th>
  <th>進行</th><th>結果</th><th></th>
</tr></thead><tbody id="history"></tbody></table>

<script>
const fmtMs = ms => ms > 0 ? (ms / 1000).toFixed(1) + 's' : '—';
const fmtTime = t => t && !t.startsWith('0001') ? new Date(t).toLocaleTimeString('ja-JP') : '—';

async function refresh() {
  let data;
  try {
    data = await (await fetch('/manager/api/status')).json();
  } catch (e) {
    document.getElementById('health').innerHTML = '<span class="err">サーバーに接続できません</span>';
    return;
  }

  document.getElementById('sessions').innerHTML = (data.sessions || []).map(s => ` + "`" + `
    <tr>
      <td>${s.device_id}</td>
      <td>${s.difficulty}</td>
      <td>${s.character}</td>
      <td><span class="state ${s.state}">${s.state}</span></td>
      <td>${s.stage_index + 1} / ${s.stage_count}<br><span class="muted">${s.stage_name || ''}</span></td>
      <td>${fmtMs(s.remaining_ms)}</td>
      <td class="answer">${s.answer || ''}</td>
      <td>${s.bridge_id}</td>
      <td><button onclick="abort('${s.device_id}')">リセット</button></td>
    </tr>` + "`" + `).join('') || '<tr><td colspan="9" class="muted">進行中のセッションはありません</td></tr>';

  document.getElementById('devices').innerHTML = (data.devices || []).map(d => {
    const lines = Object.entries(d.lines || {})
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => v ? k : '<span class="err">' + k + '</span>').join(' ');
    const battery = d.battery > 0
      ? (d.low_battery ? '<span class="err">' : (d.battery < 3.6 ? '<span class="warn">' : '<span>'))
        + d.battery.toFixed(2) + 'V</span>'
      : '<span class="muted">—</span>';
    return ` + "`" + `<tr>
      <td>${d.device_id}</td>
      <td><span class="state ${d.state}">${d.state}</span>${d.i2c_error ? ' <span class="err">I2C</span>' : ''}</td>
      <td>${fmtMs(d.remaining_ms)}</td>
      <td>${battery}</td>
      <td>${lines}</td>
      <td class="muted">${new Date(d.updated_at * 1000).toLocaleTimeString('ja-JP')}</td>
    </tr>` + "`" + `;
  }).join('') || '<tr><td colspan="6" class="muted">接続中のデバイスはありません</td></tr>';

  const bindings = data.bindings || {};
  document.getElementById('bridges').textContent = (data.bridges || []).length
    ? (data.bridges || []).map(b => b + (bindings[b] ? ' → Core ' + bindings[b] : ' (未バインド)')).join(' / ')
    : '接続中の無線はありません';

  const h = data.health || {};
  const assets = data.assets || {};
  document.getElementById('health').innerHTML =
    '最終成功: ' + fmtTime(h.last_success) +
    ' / 最終エラー: ' + (h.error_count ? '<span class="err">' + fmtTime(h.last_error) +
      ' (' + h.error_count + '件) ' + (h.last_message || '') + '</span>' : '—') +
    '<br>混線アセット: 邪魔者 ' + (assets.jamming || 0) +
    ' / 環境 ' + (assets.ambient || 0) + ' / 不穏 ' + (assets.uneasy || 0);

  // 進行中セッションをタブに出す。選択中が終了しても表示は保つ
  const live = (data.sessions || []).map(s => ({ id: s.session_id, label: 'Core ' + s.device_id }));
  renderTabs(live);
  await renderLog();
  await renderHistory();
}

// --- 交信ログ ---

let selectedSession = null;   // 表示中のセッションID
let tabList = [];             // [{id, label}]

function renderTabs(live) {
  // 選択中のセッションがタブに無ければ (履歴から開いた場合) 先頭に足す
  tabList = live.slice();
  if (selectedSession && !tabList.some(t => t.id === selectedSession)) {
    tabList.unshift({ id: selectedSession, label: '履歴: ' + selectedSession });
  }
  if (!selectedSession && tabList.length) selectedSession = tabList[0].id;

  document.getElementById('logtabs').innerHTML = tabList.map(t =>
    '<button class="tab' + (t.id === selectedSession ? ' on' : '') +
    '" onclick="selectSession(\'' + t.id + '\')">' + t.label + '</button>'
  ).join('') + (selectedSession
    ? '<a class="tab" href="/manager/api/transcript?session_id=' + selectedSession + '">テキストで保存</a>'
    : '');
}

async function selectSession(id) {
  selectedSession = id;
  renderTabs(tabList.filter(t => !t.label.startsWith('履歴: ')));
  await renderLog();
}

// イベント種別ごとの見た目 (成功=緑 / 失敗=赤 / 終了=黄)
function eventClass(ev) {
  if (ev === 'stage_cleared' || ev === 'whack_done') return 'ev-ok';
  if (ev === 'wrong_action' || ev === 'exploded') return 'ev-ng';
  if (ev === 'defused' || ev === 'aborted' || ev === 'session_start') return 'ev-end';
  return 'ev';
}

async function renderLog() {
  const el = document.getElementById('logs');
  if (!selectedSession) { el.textContent = '—'; return; }

  let entries;
  try {
    entries = await (await fetch('/manager/api/log?session_id=' + selectedSession)).json();
  } catch (e) { el.textContent = 'ログを取得できません'; return; }

  if (!entries || !entries.length) { el.textContent = 'まだ記録がありません'; return; }

  const esc = t => String(t == null ? '' : t)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  el.innerHTML = entries.map(e => {
    const t = e.at ? new Date(e.at * 1000).toLocaleTimeString('ja-JP') : '--:--:--';
    const stamp = '<span class="t">' + t + '</span>';
    if (e.kind === 'event') {
      return stamp + '<span class="' + eventClass(e.event) + '">' + esc(e.message) + '</span>';
    }
    const who = esc(e.sender);
    const cls = who === 'プレイヤー' ? '' : 'navi';
    return stamp + '<span class="who ' + cls + '">' + who + '</span>' + esc(e.message);
  }).join('<br>');

  // 最新が見えるよう末尾へスクロール
  el.scrollTop = el.scrollHeight;
}

// --- 履歴一覧 ---

async function renderHistory() {
  let items;
  try {
    items = await (await fetch('/manager/api/history')).json();
  } catch (e) { return; }

  const esc = t => String(t == null ? '' : t)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  document.getElementById('history').innerHTML = (items || []).map(h => {
    const started = h.started_at
      ? new Date(h.started_at * 1000).toLocaleString('ja-JP', { month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit' })
      : '—';
    const cls = h.result.startsWith('解除成功') ? 'ev-ok'
              : h.result === '爆発' ? 'ev-ng' : 'muted';
    return '<tr>' +
      '<td class="muted">' + started + '</td>' +
      '<td>' + esc(h.device_id) + '</td>' +
      '<td>' + esc(h.difficulty) + '</td>' +
      '<td>' + esc(h.character) + '</td>' +
      '<td>' + (h.stage_index + 1) + ' / ' + h.stage_count + '</td>' +
      '<td class="' + cls + '">' + esc(h.result) + '</td>' +
      '<td class="row-actions">' +
        '<button onclick="selectSession(\'' + h.session_id + '\')">ログを見る</button> ' +
        '<a href="/manager/api/transcript?session_id=' + h.session_id + '">TXT</a>' +
      '</td>' +
    '</tr>';
  }).join('') || '<tr><td colspan="7" class="muted">履歴はまだありません</td></tr>';
}

async function abort(deviceId) {
  if (!confirm('Core ' + deviceId + ' をリセットしますか?')) return;
  const res = await fetch('/manager/api/abort?device_id=' + deviceId, { method: 'POST' });
  // 202 = サーバー側は終了したがデバイスへ届いていない
  if (res.status === 202) alert(await res.text());
  refresh();
}

refresh();
setInterval(refresh, 2000);
</script>
</body>
</html>`
