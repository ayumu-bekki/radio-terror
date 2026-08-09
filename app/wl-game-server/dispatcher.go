package main

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Handler は1件の発話を処理する。
// 接続反転により送出先 bridge は接続ごとに変わるため、sender で宛先を受け取る
// (docs/bridge_connection_design.md §4)。
type Handler interface {
	Handle(ctx context.Context, sender *AudioSender, item TranscriptionItem, response string, audioData []byte) error
}

type Dispatcher struct {
	handlers  map[string]Handler
	mu        sync.RWMutex
	fallback  Handler
	registry  *SessionRegistry // nil 許容: 未注入時は従来の静的ハンドラのみ
	sharedLog *ConversationLog // 全接続で共有する会話ログ
}

func NewDispatcher(registry *SessionRegistry, sharedLog *ConversationLog) *Dispatcher {
	return &Dispatcher{
		handlers:  make(map[string]Handler),
		fallback:  &SystemHandler{},
		registry:  registry,
		sharedLog: sharedLog,
	}
}

func (d *Dispatcher) Register(callsign string, handler Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[callsign] = handler
}

// Dispatch は TranscriptionResult の各 item を receiver に対応するハンドラへ渡す。
// まず SessionRegistry で動的払い出しCS宛てを優先解決し、次に静的ハンドラ、最後に fallback。
func (d *Dispatcher) Dispatch(ctx context.Context, sender *AudioSender, result *TranscriptionResult, response string, audioData []byte) error {
	for _, item := range result.Items {
		// 全発話を共有会話ログへ記録する（プレイヤー発話・NPC宛・別NPC宛すべて一様に）。
		// 無線=単一周波数のブロードキャストなので、誰宛のメッセージも全NPCが「聞いている」。
		if d.sharedLog != nil {
			d.sharedLog.Append(ConversationEntry{Sender: item.Sender, Receiver: item.Receiver, Message: item.Message})
		}

		// 動的払い出しCS宛て: WSセッションのチャットへ流す
		if d.registry != nil {
			if sess, ok := d.registry.Lookup(item.Receiver); ok {
				if err := sess.HandleMessage(ctx, sender, item); err != nil {
					return err
				}
				continue
			}
		}

		// 静的ハンドラ (S4CE/S4CA/S4CQ 等) or fallback
		d.mu.RLock()
		h, ok := d.handlers[item.Receiver]
		d.mu.RUnlock()

		if !ok {
			h = d.fallback
		}
		if err := h.Handle(ctx, sender, item, response, audioData); err != nil {
			return err
		}
	}
	return nil
}

type SystemHandler struct{}

func (h *SystemHandler) Handle(ctx context.Context, sender *AudioSender, item TranscriptionItem, response string, audioData []byte) error {
	log.Printf("[SYSTEM] sender=%s receiver=%s message=%q response=%q",
		item.Sender, item.Receiver, item.Message, response)
	return nil
}

// S4CAHandler はS4CA宛メッセージをGemini TTSで音声化してradio-bridgeへ送り返す。
type S4CAHandler struct {
	ttsClient *TTSClient
}

func NewS4CAHandler(ttsClient *TTSClient) *S4CAHandler {
	return &S4CAHandler{ttsClient: ttsClient}
}

func (h *S4CAHandler) Handle(ctx context.Context, sender *AudioSender, item TranscriptionItem, response string, audioData []byte) error {
	log.Printf("[S4CA] sender=%s message=%q — generating TTS", item.Sender, item.Message)
	oggData, err := h.ttsClient.GenerateOggOpus(ctx, item.Sender, item.Message)
	if err != nil {
		log.Printf("[S4CA] TTS error: %v", err)
		return nil // エラーでも処理継続
	}
	log.Printf("[S4CA] TTS generated %d bytes, sending", len(oggData))
	sender.Send(oneshot(oggData))
	return nil
}

// S4CQHandler はS4CQ宛メッセージをGoogle検索付きGeminiで回答し、TTSで音声化して返す。
type S4CQHandler struct {
	ttsClient *TTSClient
	processor *GeminiProcessor
}

func NewS4CQHandler(ttsClient *TTSClient, processor *GeminiProcessor) *S4CQHandler {
	return &S4CQHandler{ttsClient: ttsClient, processor: processor}
}

func (h *S4CQHandler) Handle(ctx context.Context, sender *AudioSender, item TranscriptionItem, response string, audioData []byte) error {
	log.Printf("[S4CQ] sender=%s message=%q — asking Gemini", item.Sender, item.Message)

	answer, err := h.processor.Ask(ctx, item.Sender, item.Receiver, item.Message)
	if err != nil {
		log.Printf("[S4CQ] Ask error: %v", err)
		return nil
	}
	log.Printf("[S4CQ] answer: %s", answer)

	// 回答を冒頭呼び出し + 本文(句点)のチャンクに分割し、全チャンクを並列に TTS 生成する。
	// 各チャンクの生成を同時に走らせて全体の生成時間を短縮しつつ、できた順に同一 stream_id +
	// START/CONTINUE/END を付けて 1 チャンクずつ送出する (分割送信)。radio-bridge は同一
	// stream_id を 1 区間で連続再生するため、先頭チャンクが鳴るまでの体感レイテンシが小さい。
	chunks := splitAnswerForTTS(answer)
	buildPrompt := func(text string) string { return fmt.Sprintf(ttsPromptTemplateS4CQ, text) }
	return streamTTSChunks(ctx, h.ttsClient, sender, chunks, buildPrompt, "[S4CQ]")
}

// EchoHandler は受信した音声データをそのまま radio-bridge へ送り返す。
type EchoHandler struct{}

func NewEchoHandler() *EchoHandler {
	return &EchoHandler{}
}

func (h *EchoHandler) Handle(ctx context.Context, sender *AudioSender, item TranscriptionItem, response string, audioData []byte) error {
	log.Printf("[ECHO] sender=%s receiver=%s message=%q — echoing %d bytes",
		item.Sender, item.Receiver, item.Message, len(audioData))
	sender.Send(oneshot(audioData))
	return nil
}
