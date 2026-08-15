package main

import (
	"encoding/json"
	"testing"
)

// TestProgressEventsKeepDeviceStatus は進行イベント (stage_cleared など) が
// **device_status で得た情報を消さない**ことを確かめる。
//
// 進行イベントは state / battery / lines / rotary を含まない。
// これらを含む前提で状態を丸ごと差し替えると、マネージャー画面の表示が
// 一斉に「—」へ落ちる(ダイヤル表示が消える不具合として実際に現れた)。
// 加えて state が空になるため、`IsPlaying()` によるバインド競合の判定も
// 効かなくなる。
func TestProgressEventsKeepDeviceStatus(t *testing.T) {
	reg := NewDeviceRegistry()

	parse := func(raw string) *deviceMessage {
		t.Helper()
		var msg deviceMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return &msg
	}

	// device_status で全項目が入る
	reg.UpdateStatus(parse(`{"type":"device_status","device_id":"3701",` +
		`"state":"playing","session_id":"s-1","stage_index":0,"remaining_ms":90000,` +
		`"battery":3.9,"low_battery":false,"rotary":3,` +
		`"lines":{"A":true,"B":true,"C":true,"D":true,"E":true}}`))

	// 進行イベントが届いても、状態表示に使う項目は保たれる
	reg.UpdateStatus(parse(`{"type":"stage_cleared","device_id":"3701",` +
		`"stage_index":1,"remaining_ms":50000}`))

	status := reg.Status("3701")
	if status.Rotary == nil {
		t.Fatal("stage_cleared がロータリー位置を消した")
	}
	if *status.Rotary != 3 {
		t.Errorf("Rotary = %d, want 3", *status.Rotary)
	}
	if status.State != deviceStatePlaying {
		t.Errorf("State = %q, want %q (バインド競合の判定に使う)",
			status.State, deviceStatePlaying)
	}
	if status.Battery != 3.9 {
		t.Errorf("Battery = %v, want 3.9", status.Battery)
	}
	if len(status.Lines) != 5 {
		t.Errorf("len(Lines) = %d, want 5", len(status.Lines))
	}
	if status.SessionID != "s-1" {
		t.Errorf("SessionID = %q, want s-1", status.SessionID)
	}

	// 進行に関わる項目は進行イベントで更新される
	if status.StageIndex != 1 {
		t.Errorf("StageIndex = %d, want 1", status.StageIndex)
	}
	if status.RemainingMS != 50000 {
		t.Errorf("RemainingMS = %d, want 50000", status.RemainingMS)
	}

	// 画面表示まで通して確認する
	views := buildDeviceViews(reg.AllStatus(), func(string) bool { return true })
	if views[0].Rotary != "3" {
		t.Errorf("画面のダイヤル表示 = %q, want 3", views[0].Rotary)
	}
}

// TestProgressEventBeforeDeviceStatus は device_status より先に進行イベントが
// 届いた場合でも落ちないことを確かめる (未報告項目は未報告のまま)。
func TestProgressEventBeforeDeviceStatus(t *testing.T) {
	reg := NewDeviceRegistry()

	var msg deviceMessage
	if err := json.Unmarshal([]byte(
		`{"type":"stage_cleared","device_id":"3702","stage_index":2,"remaining_ms":10000}`,
	), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	status := reg.UpdateStatus(&msg)
	if status == nil {
		t.Fatal("UpdateStatus returned nil")
	}
	if status.DeviceID != "3702" {
		t.Errorf("DeviceID = %q, want 3702", status.DeviceID)
	}
	// ロータリーは未報告のまま (0 と誤表示しない)
	if status.Rotary != nil {
		t.Errorf("Rotary = %v, want nil (未報告)", *status.Rotary)
	}
	views := buildDeviceViews(reg.AllStatus(), func(string) bool { return true })
	if views[0].Rotary != "—" {
		t.Errorf("未報告のダイヤル表示 = %q, want —", views[0].Rotary)
	}
}
