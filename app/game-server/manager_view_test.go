package main

import (
	"testing"
	"time"
)

func TestFmtRemaining(t *testing.T) {
	cases := []struct {
		ms   int
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{42300, "42.3s"},
		{1000, "1.0s"},
	}
	for _, tc := range cases {
		if got := fmtRemaining(tc.ms); got != tc.want {
			t.Errorf("fmtRemaining(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestFmtProgress(t *testing.T) {
	// StageIndex は0起点、表示は1起点
	if got := fmtProgress(1, 3); got != "2 / 3" {
		t.Errorf("fmtProgress(1, 3) = %q, want %q", got, "2 / 3")
	}
}

func TestBatteryClass(t *testing.T) {
	cases := []struct {
		volts float64
		low   bool
		want  string
	}{
		{4.1, false, ""},
		{3.7, false, ""},
		{3.5, false, "warn"}, // Deep Sleep 閾値 3.4V の手前で気付ける
		{3.5, true, "err"},   // 低電圧フラグが優先
		{4.1, true, "err"},
	}
	for _, tc := range cases {
		if got := batteryClass(tc.volts, tc.low); got != tc.want {
			t.Errorf("batteryClass(%.1f, %v) = %q, want %q", tc.volts, tc.low, got, tc.want)
		}
	}
}

func TestEventClass(t *testing.T) {
	cases := map[string]string{
		EventStageCleared: "ev-ok",
		EventWhackDone:    "ev-ok",
		EventWrongAction:  "ev-ng",
		EventExploded:     "ev-ng",
		EventDefused:      "ev-end",
		EventAborted:      "ev-end",
		EventSessionStart: "ev-end",
		EventPushProgress: "ev",
	}
	for event, want := range cases {
		if got := eventClass(event); got != want {
			t.Errorf("eventClass(%q) = %q, want %q", event, got, want)
		}
	}
}

func TestResultKindAndClass(t *testing.T) {
	cases := []struct {
		state     string
		wantKind  string
		wantClass string
	}{
		{deviceStateDefused, "ok", "ev-ok"},
		{deviceStateExploded, "ng", "ev-ng"},
		{deviceStateDetonating, "ng", "ev-ng"},
		{deviceStatePlaying, "other", "muted"},
		{deviceStateSetup, "other", "muted"},
	}
	for _, tc := range cases {
		if got := resultKind(tc.state); got != tc.wantKind {
			t.Errorf("resultKind(%q) = %q, want %q", tc.state, got, tc.wantKind)
		}
		if got := resultClass(tc.state); got != tc.wantClass {
			t.Errorf("resultClass(%q) = %q, want %q", tc.state, got, tc.wantClass)
		}
	}
}

// ステージの到達状況。解除成功なら全ステージが到達済みになる。
func TestStageClass(t *testing.T) {
	cases := []struct {
		name       string
		i          int
		stageIndex int
		state      string
		want       string
	}{
		{"進行中: 通過済み", 0, 1, deviceStatePlaying, "done"},
		{"進行中: 現在地", 1, 1, deviceStatePlaying, "current"},
		{"進行中: 未到達", 2, 1, deviceStatePlaying, ""},
		{"爆発: 通過済み", 0, 1, deviceStateExploded, "done"},
		{"爆発: 到達したが失敗", 1, 1, deviceStateExploded, ""},
		{"爆発: 未到達", 2, 1, deviceStateExploded, ""},
		{"解除成功: 全て通過", 2, 2, deviceStateDefused, "done"},
		{"解除成功: 先頭も通過", 0, 2, deviceStateDefused, "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stageClass(tc.i, tc.stageIndex, tc.state); got != tc.want {
				t.Errorf("stageClass(%d, %d, %q) = %q, want %q",
					tc.i, tc.stageIndex, tc.state, got, tc.want)
			}
		})
	}
}

// ナビゲーター知識は決まった順に並べ、未知のキーは後ろへ回す。
func TestBuildNaviFields(t *testing.T) {
	fields := buildNaviFields(map[string]string{
		"hint_l2":  "ヒント2",
		"answer":   "正解だ",
		"briefing": "説明",
		"zzz_未知":   "未知の項目",
		"hint_l1":  "ヒント1",
	})

	wantLabels := []string{"ブリーフィング", "正解", "ヒント L1", "ヒント L2", "zzz_未知"}
	if len(fields) != len(wantLabels) {
		t.Fatalf("len = %d, want %d", len(fields), len(wantLabels))
	}
	for i, want := range wantLabels {
		if fields[i].Label != want {
			t.Errorf("fields[%d].Label = %q, want %q", i, fields[i].Label, want)
		}
	}

	// 正解だけ強調する
	for _, f := range fields {
		wantAnswer := f.Label == "正解"
		if f.IsAnswer != wantAnswer {
			t.Errorf("%q: IsAnswer = %v, want %v", f.Label, f.IsAnswer, wantAnswer)
		}
	}
}

// 配線は色順に並べ、切断された線に印を付ける。
func TestBuildDeviceViews(t *testing.T) {
	// 0001 は接続中、0002 は切断済み
	connected := func(deviceID string) bool { return deviceID == "0001" }

	views := buildDeviceViews([]*DeviceStatus{
		{DeviceID: "0002", State: deviceStateReady, Battery: 4.05},
		{
			DeviceID: "0001",
			State:    deviceStatePlaying,
			Battery:  3.5,
			Lines:    map[string]bool{"C": true, "A": false, "B": true},
		},
	}, connected)

	// Core ID 順
	if views[0].DeviceID != "0001" || views[1].DeviceID != "0002" {
		t.Fatalf("並び順が Core ID 順でない: %s, %s", views[0].DeviceID, views[1].DeviceID)
	}

	// 色順 (A-E の並び) に、日本語色名で出る。A だけ切断済み
	lines := views[0].Lines
	if len(lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3", len(lines))
	}
	for i, want := range []struct {
		color string
		cut   bool
	}{{"赤", true}, {"黄", false}, {"緑", false}} {
		if lines[i].Color != want.color || lines[i].Cut != want.cut {
			t.Errorf("Lines[%d] = %+v, want {%s %v}", i, lines[i], want.color, want.cut)
		}
	}

	if views[0].BatteryClass != "warn" {
		t.Errorf("BatteryClass = %q, want warn", views[0].BatteryClass)
	}
	// 電圧が無い場合は「—」
	if views[1].Battery != "4.05V" {
		t.Errorf("Battery = %q, want 4.05V", views[1].Battery)
	}

	// 接続状態が反映される (切断済みは最後の状態が残っているだけ)
	if !views[0].Connected {
		t.Error("0001 は接続中のはず")
	}
	if views[1].Connected {
		t.Error("0002 は切断済みのはず")
	}
}

// 発話はプレイヤーとナビゲーターで色を分ける。
func TestBuildEntryViews(t *testing.T) {
	views := buildEntryViews([]ConversationEntry{
		{Sender: senderPlayer, Message: "確認した", At: 1000},
		{Sender: "コード", Message: "了解", At: 1010},
		{Kind: EntryKindEvent, Event: EventDefused, Message: "解除成功", At: 1020},
		{Sender: "誰か", Message: "時刻不明", At: 0},
	})

	if views[0].WhoClass != "" {
		t.Errorf("プレイヤーの WhoClass = %q, want 空", views[0].WhoClass)
	}
	if views[1].WhoClass != "navi" {
		t.Errorf("ナビの WhoClass = %q, want navi", views[1].WhoClass)
	}
	if !views[2].IsEvent || views[2].Class != "ev-end" {
		t.Errorf("イベント行 = %+v", views[2])
	}
	if views[3].Time != "--:--:--" {
		t.Errorf("時刻不明の表示 = %q, want --:--:--", views[3].Time)
	}
}

// 無線表はバインド状況を列に分けて表示する。
//
// 未バインドは CoreID 欄を空にせず「—」で埋める (他の表と揃える)。
// 空欄だと列が抜けて見え、バインド済みの行と縦位置がずれる。
func TestBuildBridgeViews(t *testing.T) {
	views := buildBridgeViews(
		[]string{"BR02", "BR01"},
		map[string]string{"BR01": "0001"},
	)

	if len(views) != 2 {
		t.Fatalf("views = %d, want 2", len(views))
	}
	// ID順に並ぶこと
	if views[0].BridgeID != "BR01" || views[1].BridgeID != "BR02" {
		t.Errorf("並び順 = %q, %q", views[0].BridgeID, views[1].BridgeID)
	}

	bound := views[0]
	if bound.DeviceID != "0001" {
		t.Errorf("バインド済みの CoreID = %q, want 0001", bound.DeviceID)
	}
	if !bound.Bound {
		t.Error("バインド済みの Bound が false")
	}
	if bound.Status != "バインド済み" {
		t.Errorf("Status = %q", bound.Status)
	}

	free := views[1]
	if free.DeviceID != "—" {
		t.Errorf("未バインドの CoreID = %q, want —", free.DeviceID)
	}
	if free.Bound {
		t.Error("未バインドの Bound が true")
	}
	if free.Status != "未バインド" {
		t.Errorf("Status = %q", free.Status)
	}
}

func TestDateKey(t *testing.T) {
	at := time.Date(2026, 8, 11, 23, 30, 0, 0, time.Local)
	if got := dateKey(at); got != "2026-08-11" {
		t.Errorf("dateKey = %q, want 2026-08-11", got)
	}
	if got := dateKey(time.Time{}); got != "" {
		t.Errorf("ゼロ値の dateKey = %q, want 空", got)
	}
}

// 表示できない値でも画面を壊さない。
func TestIndentJSONFallback(t *testing.T) {
	// チャネルは JSON にできない
	if got := indentJSON(map[string]any{"bad": make(chan int)}); got == "" {
		t.Error("エラー時に空文字を返している")
	}
}

// 配線は日本語の色名で表示する。
// 現場は色で配線を扱うため、色コード (A-E) では読み替えが要る。
func TestColorLabel(t *testing.T) {
	cases := map[string]string{
		"A": "赤",
		"B": "黄",
		"C": "緑",
		"D": "青",
		"E": "白",
	}
	for code, want := range cases {
		if got := colorLabel(code); got != want {
			t.Errorf("colorLabel(%q) = %q, want %q", code, got, want)
		}
	}

	// 想定外のコードでも表示を壊さない
	if got := colorLabel("Z"); got != "Z" {
		t.Errorf("未知のコードは素通しする: got %q", got)
	}
}

// セッション詳細の切断線も色名で表示する。
func TestBuildStageViewsUsesColorName(t *testing.T) {
	views := buildStageViews([]*BuiltStage{
		{TemplateID: "102", Name: "シグナル", Cut: "A"},
		{TemplateID: "203", Name: "暗号電文", Cut: "C"},
	}, 0, deviceStatePlaying)

	if views[0].Cut != "赤" {
		t.Errorf("Cut = %q, want 赤", views[0].Cut)
	}
	if views[1].Cut != "緑" {
		t.Errorf("Cut = %q, want 緑", views[1].Cut)
	}
}

// TestRotaryViewDistinguishesZeroFromUnreported は、
// ロータリー位置0と未報告が別物として表示されることを確かめる。
//
// 0 は正当な位置なので、未報告 (rotary を送らない旧ファーム) と
// 混同すると画面が現場と食い違う。
func TestRotaryViewDistinguishesZeroFromUnreported(t *testing.T) {
	zero := 0
	four := 4

	cases := []struct {
		name   string
		rotary *int
		want   string
	}{
		{"未報告", nil, "—"},
		{"位置0", &zero, "0"},
		{"位置4", &four, "4"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fmtRotary(c.rotary); got != c.want {
				t.Errorf("fmtRotary() = %q, want %q", got, c.want)
			}
		})
	}

	// ビュー全体でも同じこと
	views := buildDeviceViews([]*DeviceStatus{
		{DeviceID: "0001", State: deviceStatePlaying, Rotary: &zero},
		{DeviceID: "0002", State: deviceStatePlaying},
	}, func(string) bool { return true })

	if len(views) != 2 {
		t.Fatalf("views = %d, want 2", len(views))
	}
	byID := map[string]deviceView{}
	for _, v := range views {
		byID[v.DeviceID] = v
	}
	if got := byID["0001"].Rotary; got != "0" {
		t.Errorf("位置0 の表示 = %q, want \"0\"", got)
	}
	if got := byID["0002"].Rotary; got != "—" {
		t.Errorf("未報告の表示 = %q, want \"—\"", got)
	}
}

// TestUpdateStatusCopiesRotary は、受信メッセージのポインタを
// 共有せず値をコピーしていることを確かめる。
//
// 共有すると、次のメッセージで msg が書き換わったときに
// 保存済みの状態まで変わってしまう。
func TestUpdateStatusCopiesRotary(t *testing.T) {
	registry := NewDeviceRegistry()

	pos := 3
	msg := &deviceMessage{
		Type: "device_status", DeviceID: "0001",
		State: deviceStatePlaying, Rotary: &pos,
	}
	status := registry.UpdateStatus(msg)

	if status.Rotary == nil || *status.Rotary != 3 {
		t.Fatalf("Rotary = %v, want 3", status.Rotary)
	}
	if status.Rotary == msg.Rotary {
		t.Error("ポインタが共有されている (値をコピーすべき)")
	}

	// 送信元を書き換えても保存済みの値は変わらないこと
	pos = 5
	if *status.Rotary != 3 {
		t.Errorf("送信元の変更が保存済み状態に波及した: %d", *status.Rotary)
	}
}
