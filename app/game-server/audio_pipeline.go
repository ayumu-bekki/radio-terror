package main

import (
	"context"
	"log"
)

// AudioPipeline は bridge から受信した音声を transcribe → reason → dispatch する。
//
// 旧 BridgeClient.handleAudio の流れを維持しつつ、「どの bridge から来た音声か」を
// 文脈に加える (docs/bridge_connection_design.md §4)。
type AudioPipeline struct {
	processor *GeminiProcessor
	registry  *BridgeRegistry

	// commands はマネージャーの音声コマンド (開始申告・強制リセット) を処理する。
	// nil の場合はコマンド判定を行わない。
	commands *ManagerCommandHandler

	// game / navigator / logs はバインド済み bridge の発話をナビゲーターへ繋ぐ。
	game      *GameCoordinator
	navigator NavigatorSpeaker
	logs      *SessionLogStore

	// testResponder はセッション未バインド時の疎通確認応答 (カラス)。
	testResponder *TestResponder
}

// SetTestResponder は疎通確認応答の相手を設定する。
func (p *AudioPipeline) SetTestResponder(responder *TestResponder) {
	p.testResponder = responder
}

// SetGameCoordinator はプレイヤー発話をナビゲーターへ繋ぐ経路を設定する。
func (p *AudioPipeline) SetGameCoordinator(game *GameCoordinator, navigator NavigatorSpeaker, logs *SessionLogStore) {
	p.game = game
	p.navigator = navigator
	p.logs = logs
}

func NewAudioPipeline(processor *GeminiProcessor, registry *BridgeRegistry) *AudioPipeline {
	return &AudioPipeline{
		processor: processor,
		registry:  registry,
	}
}

// SetManagerCommandHandler はマネージャー音声コマンドの処理先を設定する。
func (p *AudioPipeline) SetManagerCommandHandler(h *ManagerCommandHandler) {
	p.commands = h
}

// HandleAudio は BridgeServer から呼ばれ、1つの音声チャンクを処理する。
func (p *AudioPipeline) HandleAudio(ctx context.Context, bridgeID string, data []byte) {
	result, err := p.processor.Transcribe(ctx, data)
	if err != nil {
		log.Printf("[audio %s] transcribe error: %v", bridgeID, err)
		return
	}
	for _, item := range result.Items {
		log.Printf("[audio %s] transcribed: %q", bridgeID, item.Message)
	}

	sender := NewAudioSender(p.registry, bridgeID)

	// マネージャーの音声コマンド (開始申告・秘密ワード付きリセット) を先に判定する。
	// コマンドとして処理された発話はナビゲーターの会話へは流さない
	// (docs/operation_flow.md §7)。
	if p.commands != nil {
		handled, err := p.commands.Handle(ctx, sender, result)
		if err != nil {
			log.Printf("[audio %s] manager command error: %v", bridgeID, err)
		}
		if handled {
			return
		}
	}

	// この bridge がゲームセッションにバインドされていれば、発話は
	// ナビゲーターとの交信として扱う (docs/navigator_design.md §3.5)。
	if p.game != nil {
		if session := p.game.SessionForBridge(bridgeID); session != nil {
			p.handlePlayerMessage(ctx, sender, session, result)
			return
		}
	}

	// 未バインドの bridge からの発話。マネージャーの開始申告 (§5) は上で
	// 処理済みなので、ここへ来るのは開始前の発話。
	//
	// 無反応だと「無線が壊れているのか、まだ始まっていないだけか」が
	// 区別できないため、疎通確認用の相手 (カラス) が応答する。
	if p.testResponder != nil && isTestResponderTarget(result) {
		if err := p.testResponder.Respond(ctx, sender, result); err != nil {
			log.Printf("[audio %s] test responder error: %v", bridgeID, err)
		}
		return
	}

	log.Printf("[audio %s] no session bound: ignoring %d item(s)", bridgeID, len(result.Items))
}

// handlePlayerMessage はバインド済みセッションでのプレイヤー発話に応答する。
func (p *AudioPipeline) handlePlayerMessage(ctx context.Context, sender *AudioSender, session *GameSession, result *TranscriptionResult) {
	spoke := false

	for _, item := range result.Items {
		if item.Message == "" {
			continue
		}

		// 会話ログへ記録する (ナビゲーターの文脈になる)
		if p.logs != nil {
			p.logs.Append(session.SessionID, ConversationEntry{
				Sender:   senderPlayer,
				Receiver: session.Character.Name,
				Message:  item.Message,
			})
		}

		// 質問回数はヒントレベルの前倒し条件になる (docs/navigator_design.md §3.2)
		p.game.NoteQuestion(session.DeviceID)
		spoke = true
	}

	if !spoke || p.navigator == nil {
		return
	}

	if err := p.navigator.Speak(ctx, sender, session, "player_message", ""); err != nil {
		log.Printf("[audio %s] navigator error: %v", sender.BridgeID(), err)
	}
}
