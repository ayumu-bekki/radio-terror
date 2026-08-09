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
	Type          string   `json:"type"`
	Callsign      string   `json:"callsign,omitempty"`
	PeerCallsigns []string `json:"peer_callsigns,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// wsSession は1つの WebSocket 接続のセッション状態を保持する。
//
// /ws は1ストリームにトランシーバー用とデバイス (core_system) 用の両方が相乗りする。
// 接続種別は最初のメッセージで判定する (login → トランシーバー、
// device_status → core_system デバイス。docs/game_session_design.md §7)。
type wsSession struct {
	conn      *websocket.Conn
	connMu    sync.Mutex // WriteMessage を直列化する (複数 goroutine から送るため)
	callsigns *CallsignManager
	registry  *SessionRegistry
	processor *GeminiProcessor
	ttsClient *TTSClient

	sharedLog *ConversationLog // 全接続で共有する会話ログ
	scenario  Scenario         // NPC に割り当てるペルソナのリスト（共有）

	callsign      string   // 自分CS
	peerCallsigns []string // 相手CS 0..n（将来複数前提）

	// personaByCallsign は払い出した相手NPC CS → 割り当てペルソナ。
	personaByCallsign map[string]Persona

	// --- core_system デバイス接続として使われる場合 ---
	devices  *DeviceRegistry  // デバイス接続・状態の登録先
	game     *GameCoordinator // デバイスイベントを演出へ繋ぐ
	deviceID string           // この接続が名乗った device_id (空ならデバイスではない)
}

func newWSSession(
	conn *websocket.Conn,
	callsigns *CallsignManager,
	registry *SessionRegistry,
	processor *GeminiProcessor,
	ttsClient *TTSClient,
	sharedLog *ConversationLog,
	scenario Scenario,
	devices *DeviceRegistry,
	game *GameCoordinator,
) *wsSession {
	return &wsSession{
		conn:              conn,
		callsigns:         callsigns,
		registry:          registry,
		processor:         processor,
		ttsClient:         ttsClient,
		sharedLog:         sharedLog,
		scenario:          scenario,
		personaByCallsign: make(map[string]Persona),
		devices:           devices,
		game:              game,
	}
}

func (s *wsSession) run(ctx context.Context) {
	defer func() {
		// 自分CS + 全相手CS をプールへ返却し、レジストリから解除する
		for _, cs := range append([]string{s.callsign}, s.peerCallsigns...) {
			if cs == "" {
				continue
			}
			s.callsigns.Release(cs)
			s.registry.Unregister(cs)
			log.Printf("[WS] callsign released: %s", cs)
		}
		// デバイス接続だった場合は device_id の登録を解除する。
		// 状態はサーバー側に残し、再接続時の device_status で再同期する (§7.3)。
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
		case "login":
			s.handleLogin()
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

func (s *wsSession) handleLogin() {
	if s.callsign != "" {
		// すでにログイン済み: 現在のコールサインをそのまま返す
		s.sendJSON(wsResponse{Type: "login", Callsign: s.callsign, PeerCallsigns: s.peerCallsigns})
		return
	}

	// 自分CS を発行
	cs, ok := s.callsigns.Issue()
	if !ok {
		s.sendError("no callsign available")
		return
	}
	s.callsign = cs

	// 相手CS を1つ発行（同一プール。将来複数発行はここで繰り返す）
	peerCS, ok := s.callsigns.Issue()
	if !ok {
		s.callsigns.Release(cs)
		s.callsign = ""
		s.sendError("no peer callsign available")
		return
	}
	s.peerCallsigns = []string{peerCS}

	// 払い出し順にペルソナを割り当てる（今回は相手NPC1体なので idx=0 固定）。
	for idx, peer := range s.peerCallsigns {
		s.personaByCallsign[peer] = s.scenario.PersonaFor(idx)
	}

	// レジストリに自分CS + 全相手CS を登録
	s.registry.Register(s.callsign, s)
	for _, peer := range s.peerCallsigns {
		s.registry.Register(peer, s)
	}

	log.Printf("[WS] login: callsign=%s peers=%v", s.callsign, s.peerCallsigns)
	s.sendJSON(wsResponse{Type: "login", Callsign: s.callsign, PeerCallsigns: s.peerCallsigns})
}

// HandleMessage は TranscriptionItem を受け取り、共有会話ログ全体を文脈に
// item.Receiver（応答する NPC）のペルソナで応答を生成し、TTS 送信する。
// 会話ログへの発話追記は Dispatcher 側に集約しているため、ここでは行わない。
// genai.Chat を使わずワンショットの GenerateContent を呼ぶ（ステートレス・スレッドセーフ）。
func (s *wsSession) HandleMessage(ctx context.Context, sender *AudioSender, item TranscriptionItem) error {
	persona := s.personaByCallsign[item.Receiver]

	answer, err := s.processor.GenerateReply(ctx, persona, item.Receiver, s.sharedLog.Render())
	if err != nil {
		return fmt.Errorf("GenerateReply: %w", err)
	}
	log.Printf("[WS chat] sender=%s receiver=%s answer: %s", item.Sender, item.Receiver, answer)

	// NPC の応答を共有会話ログへ追記する（他NPC・他プレイヤーからも見える）。
	s.sharedLog.Append(ConversationEntry{Sender: item.Receiver, Receiver: item.Sender, Message: answer})

	// 全チャンクを並列に TTS 生成して全体の生成時間を短縮しつつ、できた順に同一 stream_id +
	// START/CONTINUE/END を付けて 1 チャンクずつ送出する（分割送信）。radio-bridge は同一
	// stream_id を 1 区間で連続再生するため、先頭チャンクが鳴るまでの体感レイテンシが小さい。
	chunks := splitAnswerForTTS(answer)
	buildPrompt := func(text string) string { return fmt.Sprintf(ttsPromptTemplateS4CQ, text) }
	return streamTTSChunks(ctx, s.ttsClient, sender, chunks, buildPrompt, "[WS chat]")
}

// handleDeviceMessage は core_system デバイスからのメッセージを処理する (§7.2)。
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

	// 接続種別の確定。以降この接続は device_id 宛の送信先になる。
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
	s.sendJSON(wsResponse{Type: "error", Error: msg})
}

func (s *wsSession) sendJSON(v any) {
	if err := s.SendJSON(v); err != nil {
		log.Printf("[WS] write error: %v", err)
	}
}
