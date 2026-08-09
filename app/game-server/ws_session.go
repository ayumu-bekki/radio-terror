package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// クライアントから送られるコマンドの共通構造
type wsCommand struct {
	Type string `json:"type"`
}

// サーバーからクライアントへのレスポンス
type wsResponse struct {
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

// wsSession は1つの WebSocket 接続のセッション状態を保持する。
//
// 接続してくるのは core-system デバイス。最初の device_status で device_id を
// 名乗り、以降その接続が対象デバイスへの送信路になる
// (docs/game_session_design.md §7)。
type wsSession struct {
	conn   *websocket.Conn
	connMu sync.Mutex // WriteMessage を直列化する (複数 goroutine から送るため)

	devices *DeviceRegistry  // デバイス接続・状態の登録先
	game    *GameCoordinator // デバイスイベントを演出へ繋ぐ

	deviceID string // この接続が名乗った device_id (空なら未確定)
}

func newWSSession(conn *websocket.Conn, devices *DeviceRegistry, game *GameCoordinator) *wsSession {
	return &wsSession{
		conn:    conn,
		devices: devices,
		game:    game,
	}
}

func (s *wsSession) run(ctx context.Context) {
	defer func() {
		// device_id の登録を解除する。状態はサーバー側に残し、
		// 再接続時の device_status で再同期する (§7.3)。
		if s.deviceID != "" && s.devices != nil {
			s.devices.Unregister(s.deviceID, s)
		}
		s.conn.Close()
	}()

	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[WS] read error: %v", err)
			}
			return
		}

		var cmd wsCommand
		if err := json.Unmarshal(msg, &cmd); err != nil {
			s.sendError("invalid JSON")
			continue
		}

		switch cmd.Type {
		case msgDeviceStatus, msgSessionAccepted, msgSessionRejected, msgStageCleared,
			msgWhackCompleted, msgPushProgress, msgWrongAction, msgExploded, msgDefused:
			s.handleDeviceMessage(ctx, msg)
		default:
			s.sendError("unknown command type: " + cmd.Type)
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// handleDeviceMessage は core-system デバイスからのメッセージを処理する (§7.2)。
// 最初の device_status でこの接続をデバイスとして登録する。
func (s *wsSession) handleDeviceMessage(ctx context.Context, raw []byte) {
	var msg deviceMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.sendError("invalid device message")
		return
	}
	if msg.DeviceID == "" {
		s.sendError("device_id is required")
		return
	}

	// 接続の主が確定する。以降この接続は device_id 宛の送信先になる。
	if s.deviceID == "" {
		s.deviceID = msg.DeviceID
		if s.devices != nil {
			s.devices.Register(msg.DeviceID, s)
		}
	}

	if s.devices != nil {
		s.devices.UpdateStatus(&msg)
	}
	if s.game != nil {
		s.game.HandleDeviceMessage(ctx, &msg)
	}
}

// SendJSON は DeviceConn の実装。サーバー → デバイスの送信に使う。
func (s *wsSession) SendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json.Marshal: %w", err)
	}

	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *wsSession) sendError(msg string) {
	if err := s.SendJSON(wsResponse{Type: "error", Error: msg}); err != nil {
		log.Printf("[WS] write error: %v", err)
	}
}
