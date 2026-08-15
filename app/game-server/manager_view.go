package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 画面表示用のビューモデル。
//
// 表示の判断 (色分け・書式・並び) はすべてここでサーバー側に寄せ、
// テンプレートは受け取った値を並べるだけにする。ブラウザ側で組み立てると
// 同じ整形を複数ページのJSへ書き写すことになり、食い違いの温床になるため
// (docs/game_session_design.md §10 決定32)。

// lineView は配線1本の状態。切断されていれば Cut が真になる。
type lineView struct {
	// Color は日本語の色名 (赤/黄/緑/青/白)。
	// 現場は色で配線を扱うため、色コード (A-E) では読み替えが要る。
	Color string
	// Code は色コードの小文字 (a-e)。CSS クラス (line-a 等) で
	// **実際の配線色をチップとして描く**ために使う。
	// 色名の文字だけだと現場の配線と目で照合しにくい。
	Code string
	Cut  bool
}

// deviceView はデバイス表の1行。
type deviceView struct {
	DeviceID  string
	State     string
	Remaining string
	Battery   string
	// BatteryClass は電圧に応じた警告色 ("" / "warn" / "err")
	BatteryClass string
	Lines        []lineView
	// Rotary はロータリースイッチの現在位置。
	// 未報告 (rotary を送らない旧ファーム) は "—" になる。
	// **位置0と未報告は別物**なので、0 を未報告扱いにしてはいけない。
	Rotary    string
	UpdatedAt string

	// Connected は現在 WS 接続中か。
	//
	// 切断されても最後の状態 (State) は残るため、これを見ないと
	// 電池切れ・電源断の Core が「ready のまま」に見える。
	Connected bool
}

// sessionView は進行中セッション表の1行。
type sessionView struct {
	SessionID  string
	DeviceID   string
	BridgeID   string
	Difficulty string
	Character  string
	State      string
	StageIndex int
	StageCount int
	StageName  string
	// Progress は「2 / 3」形式の進行表示
	Progress string
	// Answer は現在ステージの正解 (マネージャー向け。画面の向きに注意)
	Answer    string
	Remaining string
}

// bridgeView は無線1台の接続状況 (表の1行)。
type bridgeView struct {
	BridgeID string
	// DeviceID はバインド先の CoreID。未バインドなら "—"
	DeviceID string
	// Bound はバインド済みか。未バインド行を薄く表示するのに使う
	Bound bool
	// Status は「状態」列の表示文字列 ("バインド済み" / "未バインド")。
	// デバイス表・セッション表の「状態」がゲームの進行状態を指すのに対し、
	// こちらは Core と紐付いているかを指す。
	Status string
}

// healthView は外部APIの状況。
type healthView struct {
	LastSuccess string
	LastError   string
	ErrorCount  int
	LastMessage string
	// HasError は直近にエラーがあったか (テンプレートの色分け用)
	HasError bool

	Jamming int
	Ambient int
	Uneasy  int
}

// entryView は交信ログ1行。
type entryView struct {
	Time    string
	IsEvent bool
	// Class はイベントの色分け ("ev" / "ev-ok" / "ev-ng" / "ev-end")
	Class string
	// Who は発話者。IsEvent が偽のときのみ使う
	Who string
	// WhoClass は発話者の色分け (プレイヤーは無色、ナビは "navi")
	WhoClass string
	Message  string
}

// historyView は履歴一覧の1行。
type historyView struct {
	SessionID  string
	DeviceID   string
	Difficulty string
	Character  string
	Progress   string
	Result     string
	// ResultClass は結果の色分け ("ev-ok" / "ev-ng" / "muted")
	ResultClass string
	StartedAt   string
	// DateKey は日付絞り込みのキー (YYYY-MM-DD)
	DateKey string
}

// stageView はセッション詳細のステージ1件。
type stageView struct {
	No         int
	TemplateID string
	Name       string
	// Cut は正解の切断線 (日本語の色名)
	Cut string
	// Class は到達状況 ("" / "done" / "current")
	Class string
	// Navigator はナビゲーター知識を表示順に並べたもの
	Navigator []naviFieldView
	// CoreJSON は Core へ送った定義 (整形済み)
	CoreJSON string
}

// naviFieldView はナビゲーター知識の1項目。
type naviFieldView struct {
	Label string
	Value string
	// IsAnswer は正解行か (テンプレートで強調する)
	IsAnswer bool
}

// --- 整形ヘルパ ---

// fmtRemaining は残り時間を「12.3s」形式にする。0以下は「—」。
func fmtRemaining(ms int) string {
	if ms <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// fmtRotary はロータリー位置を表示用にする。未報告 (nil) は「—」。
//
// **位置0を未報告扱いにしない**。0 は正当な位置なので、
// 「0 なら空表示」にすると現場と画面が食い違う。
func fmtRotary(pos *int) string {
	if pos == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *pos)
}

// fmtClock は時刻を「15:04:05」にする。ゼロ値は「—」。
func fmtClock(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("15:04:05")
}

// fmtStamp はログ行の時刻。0 は時刻不明 (導入前のログ)。
func fmtStamp(unix int64) string {
	if unix <= 0 {
		return "--:--:--"
	}
	return time.Unix(unix, 0).Format("15:04:05")
}

// fmtDateTime は履歴一覧の開始時刻。
func fmtDateTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("01/02 15:04")
}

// fmtProgress は「2 / 3」形式の進行表示。
func fmtProgress(index, count int) string {
	return fmt.Sprintf("%d / %d", index+1, count)
}

// indentJSON は Core へ送った定義を読める形に整形する。
// 表示専用なので、失敗しても画面を壊さずエラー文言を返す。
func indentJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("(表示できません: %v)", err)
	}
	return string(data)
}

// colorLabel は色コード (A-E) を日本語の色名にする。
// 未知のコードはそのまま返す (デバイスが想定外の値を送っても表示は壊さない)。
func colorLabel(code string) string {
	if name, ok := colorNameJA[code]; ok {
		return name
	}
	return code
}

// batteryClass は電圧に応じた警告色を返す。
// 低電圧フラグが立っていれば赤、3.6V未満は橙 (Deep Sleep 閾値 3.4V の手前で気付けるように)。
func batteryClass(volts float64, low bool) string {
	switch {
	case low:
		return "err"
	case volts < 3.6:
		return "warn"
	default:
		return ""
	}
}

// eventClass はイベント種別の色分け (成功=緑 / 失敗=赤 / 区切り=黄)。
func eventClass(event string) string {
	switch event {
	case EventStageCleared, EventWhackDone:
		return "ev-ok"
	case EventWrongAction, EventExploded, EventForced:
		return "ev-ng"
	case EventDefused, EventAborted, EventSessionStart:
		return "ev-end"
	default:
		return "ev"
	}
}

// resultClass は結末の色分け。describeSessionResult の出力を分類する。
func resultClass(state string) string {
	switch state {
	case deviceStateDefused:
		return "ev-ok"
	case deviceStateExploded, deviceStateDetonating:
		return "ev-ng"
	default:
		return "muted"
	}
}

// resultKind は履歴の絞り込み用に結末を3種へ畳む。
// クエリの result= と対応する ("ok" / "ng" / "other")。
func resultKind(state string) string {
	switch state {
	case deviceStateDefused:
		return "ok"
	case deviceStateExploded, deviceStateDetonating:
		return "ng"
	default:
		return "other"
	}
}

// --- 組み立て ---

// buildDeviceViews はデバイス表を組み立てる (Core ID 順)。
//
// connected は device_id → WS接続中か。切断済みの Core を
// 「最後に見えていた状態」のまま表示しないために要る。
func buildDeviceViews(devices []*DeviceStatus, connected func(string) bool) []deviceView {
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		battery := "—"
		if d.Battery > 0 {
			battery = fmt.Sprintf("%.2fV", d.Battery)
		}

		colors := make([]string, 0, len(d.Lines))
		for color := range d.Lines {
			colors = append(colors, color)
		}
		sort.Strings(colors)

		lines := make([]lineView, 0, len(colors))
		for _, color := range colors {
			lines = append(lines, lineView{
				Color: colorLabel(color),
				Code:  strings.ToLower(color),
				Cut:   !d.Lines[color],
			})
		}

		views = append(views, deviceView{
			DeviceID:     d.DeviceID,
			State:        d.State,
			Remaining:    fmtRemaining(d.RemainingMS),
			Battery:      battery,
			BatteryClass: batteryClass(d.Battery, d.LowBattery),
			Lines:        lines,
			Rotary:       fmtRotary(d.Rotary),
			UpdatedAt:    fmtStamp(d.UpdatedAt),
			Connected:    connected(d.DeviceID),
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].DeviceID < views[j].DeviceID })
	return views
}

// buildSessionViews は進行中セッション表を組み立てる (Core ID 順)。
func buildSessionViews(sessions []*GameSession) []sessionView {
	views := make([]sessionView, 0, len(sessions))
	for _, session := range sessions {
		session.mu.Lock()
		view := sessionView{
			SessionID:  session.SessionID,
			DeviceID:   session.DeviceID,
			BridgeID:   session.BridgeID,
			Difficulty: session.Difficulty,
			Character:  session.Character.Name,
			State:      session.State,
			StageIndex: session.StageIndex,
			Remaining:  fmtRemaining(session.RemainingMS),
		}
		if session.Built != nil {
			view.StageCount = len(session.Built.Stages)
			if session.StageIndex >= 0 && session.StageIndex < len(session.Built.Stages) {
				stage := session.Built.Stages[session.StageIndex]
				view.StageName = stage.Name
				view.Answer = stage.Navigator["answer"]
			}
		}
		view.Progress = fmtProgress(view.StageIndex, view.StageCount)
		session.mu.Unlock()
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].DeviceID < views[j].DeviceID })
	return views
}

// buildBridgeViews は無線の接続状況を組み立てる (ID順)。
func buildBridgeViews(ids []string, bindings map[string]string) []bridgeView {
	views := make([]bridgeView, 0, len(ids))
	for _, id := range ids {
		deviceID := bindings[id]
		view := bridgeView{
			BridgeID: id,
			DeviceID: deviceID,
			Bound:    deviceID != "",
			Status:   "バインド済み",
		}
		if !view.Bound {
			// 未バインドは「まだ開始申告が来ていない」状態。
			// 空欄にすると列が抜けて見えるので他の表と同じ「—」で埋める。
			view.DeviceID = "—"
			view.Status = "未バインド"
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].BridgeID < views[j].BridgeID })
	return views
}

// buildHealthView は外部APIとアセットの状況を組み立てる。
func buildHealthView(h APIHealthSnapshot, assets map[string]int) healthView {
	return healthView{
		LastSuccess: fmtClock(h.LastSuccess),
		LastError:   fmtClock(h.LastError),
		ErrorCount:  h.ErrorCount,
		LastMessage: h.LastMessage,
		HasError:    h.ErrorCount > 0,
		Jamming:     assets[crosstalkJamming],
		Ambient:     assets[crosstalkAmbient],
		Uneasy:      assets[crosstalkUneasy],
	}
}

// buildEntryViews は交信ログを組み立てる。
func buildEntryViews(entries []ConversationEntry) []entryView {
	views := make([]entryView, 0, len(entries))
	for _, e := range entries {
		view := entryView{
			Time:    fmtStamp(e.At),
			IsEvent: e.IsEvent(),
			Message: e.Message,
		}
		if view.IsEvent {
			view.Class = eventClass(e.Event)
		} else {
			view.Who = e.Sender
			// プレイヤー以外 (ナビゲーター・混線) は青系で区別する
			if e.Sender != senderPlayer {
				view.WhoClass = "navi"
			}
		}
		views = append(views, view)
	}
	return views
}

// buildStageViews はセッション詳細のステージ構成を組み立てる。
//
// 到達しなかったステージの正解も出す。履歴は終了後の振り返りであり、
// 「どこで詰まり、その先に何があったか」を読むための画面のため (§9)。
func buildStageViews(stages []*BuiltStage, stageIndex int, state string) []stageView {
	views := make([]stageView, 0, len(stages))
	for i, stage := range stages {
		views = append(views, stageView{
			No:         i + 1,
			TemplateID: stage.TemplateID,
			Name:       stage.Name,
			Cut:        colorLabel(stage.Cut),
			Class:      stageClass(i, stageIndex, state),
			Navigator:  buildNaviFields(stage.Navigator),
			CoreJSON:   indentJSON(stage.Core),
		})
	}
	return views
}

// stageClass はステージの到達状況を返す。
// 解除成功なら全ステージ到達済み、進行中は現在地を黄で示す。
func stageClass(i, stageIndex int, state string) string {
	switch {
	case state == deviceStateDefused:
		return "done"
	case i < stageIndex:
		return "done"
	case i == stageIndex && state == deviceStatePlaying:
		return "current"
	default:
		return ""
	}
}

// naviFieldLabels はナビゲーター知識のキーの表示名。
var naviFieldLabels = map[string]string{
	"briefing":  "ブリーフィング",
	"answer":    "正解",
	"procedure": "手順",
	"hint_l1":   "ヒント L1",
	"hint_l2":   "ヒント L2",
	"hint_l3":   "ヒント L3",
}

// naviFieldOrder は表示順。ここに無いキーは後ろへ回し、キー名のまま出す。
var naviFieldOrder = []string{"briefing", "answer", "procedure", "hint_l1", "hint_l2", "hint_l3"}

// buildNaviFields はナビゲーター知識を表示順に並べる。
func buildNaviFields(navi map[string]string) []naviFieldView {
	rank := make(map[string]int, len(naviFieldOrder))
	for i, key := range naviFieldOrder {
		rank[key] = i
	}
	rankOf := func(key string) int {
		if r, ok := rank[key]; ok {
			return r
		}
		return len(naviFieldOrder)
	}

	keys := make([]string, 0, len(navi))
	for key := range navi {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := rankOf(keys[i]), rankOf(keys[j])
		if ri != rj {
			return ri < rj
		}
		// 未知のキー同士は名前順で安定させる
		return keys[i] < keys[j]
	})

	fields := make([]naviFieldView, 0, len(keys))
	for _, key := range keys {
		label, ok := naviFieldLabels[key]
		if !ok {
			label = key
		}
		fields = append(fields, naviFieldView{
			Label:    label,
			Value:    navi[key],
			IsAnswer: key == "answer",
		})
	}
	return fields
}
