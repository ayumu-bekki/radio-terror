package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// DeviceConn はサーバーからデバイスへメッセージを送るための送信口。
// wsSession が実装する。
type DeviceConn interface {
	SendJSON(v any) error
}

// DeviceRegistry は device_id → 接続 と、各デバイスの最新状態を管理する。
//
// 複数の core-system デバイスが1つの /ws に相乗りするため、サーバーは device_id で
// 接続を識別し、サーバー → デバイスは対象デバイスの接続へ送信する
// (docs/game_session_design.md §2)。
type DeviceRegistry struct {
	mu     sync.RWMutex
	conns  map[string]DeviceConn
	status map[string]*DeviceStatus
}

func NewDeviceRegistry() *DeviceRegistry {
	return &DeviceRegistry{
		conns:  make(map[string]DeviceConn),
		status: make(map[string]*DeviceStatus),
	}
}

// Register は device_id に対する接続を登録する。再接続時は後着で上書きする。
func (r *DeviceRegistry) Register(deviceID string, conn DeviceConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns[deviceID] = conn
	log.Printf("[device] registered: %s", deviceID)
}

// Unregister は指定接続が現行のものである場合に限り登録を解除する。
// 状態 (DeviceStatus) は再接続時の参照のために残す。
func (r *DeviceRegistry) Unregister(deviceID string, conn DeviceConn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.conns[deviceID]; !ok || current != conn {
		return
	}
	delete(r.conns, deviceID)
	log.Printf("[device] unregistered: %s", deviceID)
}

// UpdateStatus はデバイスからの device_status を保存する (§7.3 の再同期)。
func (r *DeviceRegistry) UpdateStatus(msg *deviceMessage) *DeviceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := &DeviceStatus{
		DeviceID:    msg.DeviceID,
		State:       msg.State,
		SessionID:   msg.SessionID,
		StageIndex:  msg.StageIndex,
		RemainingMS: msg.RemainingMS,
		Battery:     msg.Battery,
		LowBattery:  msg.LowBattery,
		Lines:       msg.Lines,
		UpdatedAt:   time.Now().Unix(),
	}
	// ポインタを共有せず値をコピーする (msg は呼び出し後も生きうる)
	if msg.Rotary != nil {
		rotary := *msg.Rotary
		status.Rotary = &rotary
	}
	r.status[msg.DeviceID] = status
	return status
}

// Status は device_id の最新状態を返す。
func (r *DeviceRegistry) Status(deviceID string) *DeviceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status[deviceID]
}

// AllStatus は全デバイスの最新状態を返す (マネージャー向け Web 画面用)。
func (r *DeviceRegistry) AllStatus() []*DeviceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]*DeviceStatus, 0, len(r.status))
	for _, s := range r.status {
		list = append(list, s)
	}
	return list
}

// IsConnected は device_id が現在 WS 接続中かを返す。
func (r *DeviceRegistry) IsConnected(deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.conns[deviceID]
	return ok
}

// Send は対象デバイスの接続へ JSON メッセージを送る。
func (r *DeviceRegistry) Send(deviceID string, v any) error {
	r.mu.RLock()
	conn, ok := r.conns[deviceID]
	r.mu.RUnlock()

	if !ok {
		return errDeviceNotConnected
	}
	return conn.SendJSON(v)
}

// SendSessionStart は組み立て済みセッション JSON をそのまま送る (§7.1)。
// payload は session_start メッセージ全体の JSON バイト列。
func (r *DeviceRegistry) SendSessionStart(deviceID string, payload json.RawMessage) error {
	r.mu.RLock()
	conn, ok := r.conns[deviceID]
	r.mu.RUnlock()

	if !ok {
		return errDeviceNotConnected
	}
	return conn.SendJSON(payload)
}

// SendSessionAbort は安全な中断 (Setup へ戻す) を指示する。
func (r *DeviceRegistry) SendSessionAbort(deviceID string) error {
	return r.Send(deviceID, sessionAbortCommand{Type: msgSessionAbort, DeviceID: deviceID})
}

// SendForceDetonate は強制破裂を指示する。
func (r *DeviceRegistry) SendForceDetonate(deviceID string) error {
	return r.Send(deviceID, forceDetonateCommand{Type: msgForceDetonate, DeviceID: deviceID})
}
