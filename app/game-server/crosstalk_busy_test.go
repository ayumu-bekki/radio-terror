package main

import (
	"math/rand"
	"testing"
	"time"
)

func newTestScheduler() *CrosstalkScheduler {
	return NewCrosstalkScheduler(nil, nil, rand.New(rand.NewSource(1)))
}

// TestBusyTracksPlaybackTime は、無線が塞がっている時間を
// 再生時間ぶん保持することを確かめる。
//
// 以前は「発話中か」の bool だったため、送出完了と同時にフラグが落ち、
// bridge がこれから再生する十数秒の間に混線が割り込んでいた
// (docs/operation_flow.md §5.1)。
func TestBusyTracksPlaybackTime(t *testing.T) {
	s := newTestScheduler()

	if s.isSpeaking("0001") {
		t.Fatal("初期状態で塞がっている")
	}

	s.SetBusy("0001", 500*time.Millisecond)
	if !s.isSpeaking("0001") {
		t.Error("再生中に塞がっていない扱いになっている")
	}
	if remain := s.BusyFor("0001"); remain <= 0 || remain > 500*time.Millisecond {
		t.Errorf("残り時間 = %v, want 0〜500ms", remain)
	}

	// 鳴り終われば解放される
	time.Sleep(600 * time.Millisecond)
	if s.isSpeaking("0001") {
		t.Error("再生終了後も塞がったままになっている")
	}
	if remain := s.BusyFor("0001"); remain != 0 {
		t.Errorf("終了後の残り時間 = %v, want 0", remain)
	}
}

// TestMarkBusyDoesNotShorten は MarkBusy が既存の予定を縮めないことを確かめる。
// 長い発話の再生中に短い効果音を足しても、発話の終わりより手前に
// 戻ってはいけない。
func TestMarkBusyDoesNotShorten(t *testing.T) {
	s := newTestScheduler()

	s.SetBusy("0001", 10*time.Second)
	s.MarkBusy("0001", 1*time.Second)

	if remain := s.BusyFor("0001"); remain < 9*time.Second {
		t.Errorf("残り時間 = %v, want 9秒以上 (短い予定で上書きされている)", remain)
	}
}

// TestMarkBusyExtends は MarkBusy が長い方向には延長することを確かめる。
func TestMarkBusyExtends(t *testing.T) {
	s := newTestScheduler()

	s.MarkBusy("0001", 200*time.Millisecond)
	s.MarkBusy("0001", 5*time.Second)

	if remain := s.BusyFor("0001"); remain < 4*time.Second {
		t.Errorf("残り時間 = %v, want 4秒以上 (延長されていない)", remain)
	}
}

// TestSetBusyCanShorten は SetBusy が短くする方向にも効くことを確かめる。
// 発話の生成中は見込み (長め) で押さえ、送出後に実際の再生時間へ
// 差し替えるため、この方向が必要になる。
func TestSetBusyCanShorten(t *testing.T) {
	s := newTestScheduler()

	s.MarkBusy("0001", navigatorSpeakReserve) // 見込み 30秒
	s.SetBusy("0001", 3*time.Second)          // 実測 3秒

	remain := s.BusyFor("0001")
	if remain > 3*time.Second {
		t.Errorf("残り時間 = %v, want 3秒以下 (実測値に置き換わっていない)", remain)
	}
	if remain <= 0 {
		t.Errorf("残り時間 = %v, want 0より大きい", remain)
	}
}

// TestClearBusy は予約の取り消しを確かめる (発話生成に失敗した場合)。
func TestClearBusy(t *testing.T) {
	s := newTestScheduler()

	s.MarkBusy("0001", navigatorSpeakReserve)
	s.ClearBusy("0001")

	if s.isSpeaking("0001") {
		t.Error("ClearBusy 後も塞がったままになっている")
	}
}

// TestBusyIsPerDevice は塞がり状態がデバイスごとに独立していることを確かめる。
// 複数チームが同時進行するため、片方の発話が他方の混線を止めてはいけない。
func TestBusyIsPerDevice(t *testing.T) {
	s := newTestScheduler()

	s.SetBusy("0001", 5*time.Second)

	if !s.isSpeaking("0001") {
		t.Error("0001 が塞がっていない")
	}
	if s.isSpeaking("0002") {
		t.Error("別デバイス 0002 まで塞がっている")
	}
}

// TestStopClearsBusy はセッション終了で塞がり状態も消えることを確かめる。
func TestStopClearsBusy(t *testing.T) {
	s := newTestScheduler()

	s.SetBusy("0001", 10*time.Second)
	s.Stop("0001")

	if s.isSpeaking("0001") {
		t.Error("Stop 後も塞がったままになっている")
	}
}
