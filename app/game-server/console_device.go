package main

// コンソールモード用の疑似デバイス。実機 (Core) を用意せず、キーボード操作
// だけでステージ進行をデバッグするための口 (docs/adr.md ADR M-7)。

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// consoleIDPrefix は疑似 device_id / bridge_id の名前空間。
// 実機・実無線の識別子と衝突しないようにする。
const consoleIDPrefix = "console-"

// consoleDeviceSentinel は `/manager/debug` の Core 選択肢のうち
// 「コンソールモード」を示す特別な値 (manager_debug.gohtml と対応)。
const consoleDeviceSentinel = "__console__"

// consoleDeviceConn は DeviceConn の疑似実装。
//
// StartSessionWith が送る session_start 等をそのまま握りつぶす —
// コンソール画面は Core への実送信内容を見るのではなく、ボタン操作で
// 進行イベント (stage_cleared 等) を直接注入して進行を作るため、
// 送信内容自体を確認する必要が無い。
type consoleDeviceConn struct{}

func (consoleDeviceConn) SendJSON(v any) error { return nil }

// consoleSeq はコンソールセッションの device_id / bridge_id を一意にするための連番。
var consoleSeq int64

// newConsoleIDs は疑似 device_id と bridge_id の組を発行する。
// 各コンソールセッションは実機と衝突しない専用の名前空間 ("console-N") を持つ。
func newConsoleIDs() (deviceID, bridgeID string) {
	n := atomic.AddInt64(&consoleSeq, 1)
	id := fmt.Sprintf("%s%d", consoleIDPrefix, n)
	return id, id
}

// isConsoleDeviceID は device_id がコンソールモードの疑似デバイスかを返す。
// 実機一覧 (Management Console の /manager, /manager/debug) から除外するために使う。
func isConsoleDeviceID(deviceID string) bool {
	return strings.HasPrefix(deviceID, consoleIDPrefix)
}

// nonConsoleDeviceStatus はコンソールモードの疑似デバイスを除いた
// DeviceStatus 一覧を返す。
func nonConsoleDeviceStatus(all []*DeviceStatus) []*DeviceStatus {
	filtered := make([]*DeviceStatus, 0, len(all))
	for _, status := range all {
		if status == nil || isConsoleDeviceID(status.DeviceID) {
			continue
		}
		filtered = append(filtered, status)
	}
	return filtered
}
