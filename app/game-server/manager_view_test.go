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

	// 色順に並び、A だけ切断済み
	lines := views[0].Lines
	if len(lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3", len(lines))
	}
	for i, want := range []struct {
		color string
		cut   bool
	}{{"A", true}, {"B", false}, {"C", false}} {
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

// 無線はバインド状況を含めて表示する。
func TestBridgeViewLabel(t *testing.T) {
	bound := bridgeView{BridgeID: "bridge-1", DeviceID: "0001"}
	if got := bound.Label(); got != "bridge-1 → Core 0001" {
		t.Errorf("Label() = %q", got)
	}

	free := bridgeView{BridgeID: "bridge-2"}
	if got := free.Label(); got != "bridge-2 (未バインド)" {
		t.Errorf("Label() = %q", got)
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
