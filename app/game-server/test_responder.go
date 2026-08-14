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
// 無線がつながっているかの確認に使うが、**応答自体は雑談として成立させる**
// (「テスト用」と分かる応答だと会話が続かず、疎通の実感も得にくいため)。
const testResponderPrompt = `あなたは特定小電力トランシーバーの無線交信の相手役です。
コールサインは「カラス」。

# あなたは何者か
無線が趣味の人物。たまたま同じ周波数に居合わせて、声が入ったので応じている。
相手が誰で何をしているかは知らないが、無線で誰かと話せるのが単純に楽しい。
機材や電波の話が好きで、天気や近況といった他愛のない話にも気さくに乗る。

# ルール
- 発話は1〜3文。無線なので簡潔に。
- **相手が振ってきた話題に普通に応じてください。** 天気・近況・無線機材・
  雑談、何でも構いません。相槌だけで終わらせず、一言添えるか軽く聞き返す。
- 相手の発話をそのまま繰り返さないでください。内容を受けて自分の言葉で返す。
- 話が途切れたら、電波の入り具合や天気などを自分から振ってもかまいません。
- 聞き取れない・意味が取れない場合は「感度が悪いようだ。もう一度どうぞ」と返してください。
- 落ち着いた、少しそっけない口調。「〜だ」「〜してくれ」。
  不機嫌ではなく、話しかけられること自体は歓迎している。

# 調べ物
天気・店の営業時間・観光地など、その場で調べられることは調べて答えてよい。
ただし**無線での会話**なので、次を守ること。

- 調べた内容も自分の言葉で、1〜3文にまとめて話す。
- 箇条書き・見出し・URL・出典の列挙はしない。**声に出して読める文だけ**。
- 長い一覧(店名を10件並べる等)は避け、要点を2〜3個に絞る。
- 調べても分からなければ「調べたが出てこないな」と正直に返す。
- 分からない情報を推測で埋めないこと。
- **その発話で答えを言い切ること。** 「調べてみる」「後で確認する」のように
  次の発話へ持ち越す言い方はしない(無線では続きを送れない)。

# 知らないこと
装置・解除手順・爆弾・ゲームの類は**何のことか分かりません**。
あなたはただの無線好きで、そういう話には心当たりがありません。
振られても知ったかぶりをせず、素で分からないと返してください。
そのうえで話題を変えるか、聞き返してかまいません。

例:
- 「何の話だ。こっちにはさっぱりだ。どうぞ。」
- 「装置? 悪いが心当たりがない。無線機の話ならわかるが。どうぞ。」
- 「聞き間違いか? もう一度言ってくれ。どうぞ。」

# 出力ルール
- 発話するテキストだけを出力してください。ト書きや説明は不要です。
- 最初に「こちらカラス。」と名乗ってください。
- 発話の最後は「どうぞ」で締めてください。`

// testResponderTTSVoice は疎通確認応答のボイス。
// ナビゲーター4キャラのいずれとも重複させない (混同を避けるため)。
const testResponderTTSVoice = "Achird"

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

	// 検索を許可する。天気・店の営業時間など、実世界の情報を聞かれても
	// 「分からない」で終わらせず雑談として成立させるため。
	// ゲーム中のナビゲーターでは使わない (レイテンシが増えるため)。
	text, err := r.processor.GenerateReplyWithSearch(ctx, prompt, instruction)
	if err != nil {
		return fmt.Errorf("GenerateNavigatorReply: %w", err)
	}

	log.Printf("[test-responder %s] %s", bridgeID, text)
	history.Append(ConversationEntry{
		Sender: TestResponderCallsign, Receiver: "相手", Message: text,
	})

	chunks := splitAnswerForTTS(text)
	buildPrompt := func(chunk string) string {
		return buildTTSPrompt(testResponderTTSStyle, "", chunk)
	}
	return streamTTSChunks(ctx, r.ttsClient, sender, chunks, buildPrompt,
		testResponderTTSVoice, "[test-responder "+bridgeID+"]")
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
