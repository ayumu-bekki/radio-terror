package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocket の死活監視 (docs/game_session_design.md §7.3)。
//
// **電池切れ (Deep Sleep) や電源断は TCP を正常終了しない**ため、
// ping/pong が無いと ReadMessage が OS の TCP タイムアウト (十数分) まで
// ブロックし、切断に気付けない。マネージャー画面に「生きている」ように
// 見え続けるので、アプリ層で検知する。
const (
	// wsPongWait はこの時間 pong が来なければ切断とみなす。
	// 短くしすぎると会場のWiFiが一時的に不安定なだけで切ってしまい、
	// Ready 中なら Setup へ戻る (プレイ中はゲームが続くため影響は無い)。
	wsPongWait = 60 * time.Second

	// wsPingPeriod は ping の送信間隔。pong を待つ猶予を残すため
	// wsPongWait より十分短くする。
	wsPingPeriod = 25 * time.Second

	// wsWriteWait は1回の書き込みの制限時間。
	wsWriteWait = 10 * time.Second
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
	// 読み取りを抜けたら ping の送信も止める
	done := make(chan struct{})

	defer func() {
		close(done)

		// device_id の登録を解除する。状態はサーバー側に残し、
		// 再接続時の device_status で再同期する (§7.3)。
		if s.deviceID != "" && s.devices != nil {
			s.devices.Unregister(s.deviceID, s)
		}
		s.conn.Close()
	}()

	// 死活監視を開始する。pong が途絶えたら ReadMessage が期限切れで返り、
	// この関数を抜けて Unregister される。
	s.startKeepalive(ctx, done)

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

// startKeepalive は ping/pong による死活監視を開始する。
//
// 読み取りに期限を設け、pong を受けるたび延長する。相手が黙って消えた場合、
// 最長 wsPongWait で ReadMessage がエラーを返して接続が片付く。
func (s *wsSession) startKeepalive(ctx context.Context, done <-chan struct{}) {
	_ = s.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	s.conn.SetPongHandler(func(string) error {
		return s.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.writePing(); err != nil {
					// 送信できない = 既に切れている。読み取り側が後始末する
					return
				}
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// writePing は ping フレームを送る。SendJSON と書き込みを直列化する。
func (s *wsSession) writePing() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if err := s.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.PingMessage, nil)
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

	// **書き込みのたびに期限を設定し直す**。SetWriteDeadline は接続単位の
	// 永続設定なので、ping で設定した期限がそのまま残る。更新しないと
	// 前回の ping から wsWriteWait を過ぎた時点で、接続が生きていても
	// 即座に i/o timeout になる (session_abort が届かない不具合を起こした)。
	if err := s.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
		return fmt.Errorf("SetWriteDeadline: %w", err)
	}
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *wsSession) sendError(msg string) {
	if err := s.SendJSON(wsResponse{Type: "error", Error: msg}); err != nil {
		log.Printf("[WS] write error: %v", err)
	}
}
