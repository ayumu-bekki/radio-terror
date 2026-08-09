package main

import (
	"log"
	"sync"
)

// SessionBinder は「どの無線がどの Core を担当しているか」を管理する。
//
// マネージャーの音声申告で bridge ⇔ device のバインドが確立し、以降その Core の
// ゲームイベントはその bridge への無線演出へ接続される
// (docs/bridge_connection_design.md §5)。
//
// 進行中セッションの保持もここに集約する。GameCoordinator は
// この対応表を引きながら演出を組み立てることに専念する。
type SessionBinder struct {
	mu sync.RWMutex

	// bindingByBridge は bridge_id → device_id の動的バインド
	bindingByBridge map[string]string
	// sessionByDevice は device_id → 進行中セッション
	sessionByDevice map[string]*GameSession
}

func NewSessionBinder() *SessionBinder {
	return &SessionBinder{
		bindingByBridge: make(map[string]string),
		sessionByDevice: make(map[string]*GameSession),
	}
}

// Bind は bridge と device を紐付け、セッションを登録する。
// 明示的な解除は設けず、新しい申告による上書きで運用する (後勝ち。§5)。
func (b *SessionBinder) Bind(bridgeID, deviceID string, session *GameSession) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bindingByBridge[bridgeID] = deviceID
	b.sessionByDevice[deviceID] = session
}

// Release はデバイスの進行中セッションを解除する。
// バインド自体 (bridge → device) は残し、再接続で継続できるようにする。
func (b *SessionBinder) Release(deviceID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessionByDevice, deviceID)
}

// SessionFor は device_id に対応する進行中セッションを返す。
func (b *SessionBinder) SessionFor(deviceID string) *GameSession {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sessionByDevice[deviceID]
}

// SessionForBridge は bridge にバインドされたセッションを返す。
// プレイヤーの発話をどのセッションの文脈で扱うかの解決に使う。
func (b *SessionBinder) SessionForBridge(bridgeID string) *GameSession {
	b.mu.RLock()
	deviceID, ok := b.bindingByBridge[bridgeID]
	b.mu.RUnlock()

	if !ok {
		return nil
	}
	return b.SessionFor(deviceID)
}

// Sessions は進行中の全セッションを返す (マネージャー向け Web 画面用)。
func (b *SessionBinder) Sessions() []*GameSession {
	b.mu.RLock()
	defer b.mu.RUnlock()

	list := make([]*GameSession, 0, len(b.sessionByDevice))
	for _, session := range b.sessionByDevice {
		list = append(list, session)
	}
	return list
}

// Bindings は現在のバインド表を返す (Web画面用)。
func (b *SessionBinder) Bindings() map[string]string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	result := make(map[string]string, len(b.bindingByBridge))
	for bridgeID, deviceID := range b.bindingByBridge {
		result[bridgeID] = deviceID
	}
	return result
}

// PlayingSessions は指定デバイス以外で Playing 中のセッションを返す。
// 混線のイベント駆動配信 (他チームの爆発を知らせる) の宛先解決に使う。
func (b *SessionBinder) PlayingSessions(excludeDeviceID string) []*GameSession {
	b.mu.RLock()
	defer b.mu.RUnlock()

	list := make([]*GameSession, 0, len(b.sessionByDevice))
	for deviceID, session := range b.sessionByDevice {
		if deviceID == excludeDeviceID {
			continue
		}
		session.mu.Lock()
		playing := session.State == deviceStatePlaying
		session.mu.Unlock()
		if playing {
			list = append(list, session)
		}
	}
	return list
}

// Restore は永続化から読み出したセッションを登録し直す。
// 進行状態は Core からの device_status で再同期する
// (docs/scenario_design.md §6)。
func (b *SessionBinder) Restore(sessions []*GameSession) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, session := range sessions {
		b.sessionByDevice[session.DeviceID] = session
		if session.BridgeID != "" {
			b.bindingByBridge[session.BridgeID] = session.DeviceID
		}
		log.Printf("[game] restored session %s device=%s bridge=%s",
			session.SessionID, session.DeviceID, session.BridgeID)
	}
}
