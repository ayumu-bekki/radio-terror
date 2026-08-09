package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
)

// TestResponderCallsign は疎通確認用の応答者のコールサイン。
//
// ナビゲーターの4キャラクター(鳥)とは別の存在として「カラス」を割り当てる。
// ゲーム外の相手だと分かる名前にしておき、本番の交信と混同しないようにする。
const TestResponderCallsign = "カラス"

// testResponderPrompt は疎通確認応答のシステムプロンプト。
//
// ナビゲーターと違い、ゲームの進行・正解・ヒントには一切関与しない。
// 「無線がつながっているか」を確かめるためだけの相手。
const testResponderPrompt = `あなたは特定小電力トランシーバーの無線交信の相手役です。
コールサインは「カラス」。

# 状況
これは**機材の疎通確認**です。ゲームはまだ始まっていません。
相手(多くは運営スタッフ)は、無線が正しくつながるか試しています。

# ルール
- 短い応答を心がけてください。発話は1〜2文。
- 相手の発話内容に自然に反応してください。聞き取れた内容が伝わるよう、
  要点を一言添えると相手が疎通を確認しやすくなります。
- 聞き取れない・意味が取れない場合は「感度が悪いようだ。もう一度どうぞ」と返してください。
- ゲームの内容(装置の解除手順・正解・ヒント)は**一切知りません**。
  尋ねられたら「こちらは通信の確認だけだ。ゲームの話は担当に聞いてくれ」と返してください。
- 落ち着いた、少しそっけない口調。「〜だ」「〜してくれ」。

# 出力ルール
- 発話するテキストだけを出力してください。ト書きや説明は不要です。
- 最初に「こちらカラス。」と名乗ってください。
- 発話の最後は「どうぞ」で締めてください。`

// testResponderTTSStyle は疎通確認応答のTTS指定。
// ナビゲーターの誰とも違う声にして、聞き分けられるようにする。
const testResponderTTSStyle = "落ち着いた低めの中性的な声。淡々と、事務的に読み上げる。"

// TestResponder はセッション未バインドの bridge へ応答する疎通確認用の相手。
//
// マネージャーの開始申告 (docs/bridge_connection_design.md §5) が行われる前は
// どのセッションにも紐づいていないため、そのままでは無線が無反応になる。
// 会場設営時に「無線・PTT・文字起こし・TTS が通っているか」を確かめられるよう、
// この応答者が返事をする。
type TestResponder struct {
	processor *GeminiProcessor
	ttsClient *TTSClient

	// logs は bridge ごとの交信ログ。文脈を保って会話できるようにする。
	// 複数の bridge から並行に呼ばれるため mutex で保護する。
	mu   sync.Mutex
	logs map[string]*ConversationLog
}

func NewTestResponder(processor *GeminiProcessor, ttsClient *TTSClient) *TestResponder {
	return &TestResponder{
		processor: processor,
		ttsClient: ttsClient,
		logs:      make(map[string]*ConversationLog),
	}
}

// testResponderLogWindow は文脈として渡す直近の交信件数。
// 疎通確認なので短くてよい。
const testResponderLogWindow = 10

// Respond は未バインドの bridge からの発話へ応答する。
func (r *TestResponder) Respond(ctx context.Context, sender *AudioSender, result *TranscriptionResult) error {
	bridgeID := sender.BridgeID()

	// 聞き取れた発話を集める
	var spoken []string
	for _, item := range result.Items {
		if item.Message != "" {
			spoken = append(spoken, item.Message)
		}
	}
	if len(spoken) == 0 {
		// 無音・雑音のみ。応答しない (docs/navigator_design.md §3.6 と同じ方針)
		log.Printf("[test-responder %s] no speech detected: skipping", bridgeID)
		return nil
	}

	history := r.logFor(bridgeID)
	for _, message := range spoken {
		history.Append(ConversationEntry{
			Sender: "相手", Receiver: TestResponderCallsign, Message: message,
		})
	}

	prompt := fmt.Sprintf("%s\n\n# 直近の交信\n%s", testResponderPrompt, history.Render())
	instruction := "直前の相手の発話に応答してください。"

	text, err := r.processor.GenerateNavigatorReply(ctx, prompt, instruction)
	if err != nil {
		return fmt.Errorf("GenerateNavigatorReply: %w", err)
	}

	log.Printf("[test-responder %s] %s", bridgeID, text)
	history.Append(ConversationEntry{
		Sender: TestResponderCallsign, Receiver: "相手", Message: text,
	})

	chunks := splitAnswerForTTS(text)
	buildPrompt := func(chunk string) string {
		return fmt.Sprintf("%s\n\n次のセリフを読み上げてください:\n%s", testResponderTTSStyle, chunk)
	}
	return streamTTSChunks(ctx, r.ttsClient, sender, chunks, buildPrompt,
		"[test-responder "+bridgeID+"]")
}

// logFor は bridge ごとの交信ログを返す (無ければ作る)。
func (r *TestResponder) logFor(bridgeID string) *ConversationLog {
	r.mu.Lock()
	defer r.mu.Unlock()

	if history, ok := r.logs[bridgeID]; ok {
		return history
	}
	history := NewConversationLog(testResponderLogWindow)
	r.logs[bridgeID] = history
	return history
}

// Reset は bridge の交信ログを破棄する。
// セッションが始まったら疎通確認の文脈は不要になるため。
func (r *TestResponder) Reset(bridgeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.logs, bridgeID)
}

// isTestResponderTarget は疎通確認応答を返すべき発話かを判定する。
//
// マネージャーの開始申告は ManagerCommandHandler が先に処理するため、
// ここへ来るのは「セッション開始前の通常の発話」。
// ただし空白のみの発話は除く。
func isTestResponderTarget(result *TranscriptionResult) bool {
	for _, item := range result.Items {
		if strings.TrimSpace(item.Message) != "" {
			return true
		}
	}
	return false
}
