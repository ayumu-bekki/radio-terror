package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// 効果音アセット (docs/operation_flow.md §6)。
// 成功・失敗のメッセージは効果音と連結して再生する。
const (
	sfxSuccessFile = "success.ogg"
	sfxFailureFile = "failure.ogg"
)

// navigatorMaxRunes は1発話の想定上限 (navigator/prompt.toml の出力ルールと同じ値)。
//
// 超えても発話は止めない。長い発話は無線を塞いで次の交信を遅らせるため、
// プロンプトが効いているかを運用中に確認できるようログへ警告を残す。
const navigatorMaxRunes = 60

// navigatorSpeakReserve は発話の生成中に無線を押さえておく見込み時間。
//
// 生成 (reply + TTS) にかかる時間も、その後の再生時間も事前には分からない。
// 生成中に混線が入ると、発話が届いた時点で無線が塞がって発話が後ろへずれる。
// 送出後は実際の再生時間で置き換わる (Speak 参照) ので、ここは
// 「生成にかかる想定時間 + 発話の平均的な長さ」程度の粗い見積もりでよい。
const navigatorSpeakReserve = 30 * time.Second

// GeminiNavigator はナビゲーターの発話を生成して無線へ送出する。
type GeminiNavigator struct {
	processor *GeminiProcessor
	ttsClient *TTSClient
	logs      *SessionLogStore
	crosstalk *CrosstalkScheduler

	// config はナビゲーター設定 (navigator/ 以下のTOML)
	config *NavigatorConfig

	// sfxDir は効果音アセットのディレクトリ
	sfxDir string
}

func NewGeminiNavigator(
	processor *GeminiProcessor,
	ttsClient *TTSClient,
	logs *SessionLogStore,
	config *NavigatorConfig,
	sfxDir string,
) *GeminiNavigator {
	return &GeminiNavigator{
		processor: processor,
		ttsClient: ttsClient,
		logs:      logs,
		config:    config,
		sfxDir:    sfxDir,
	}
}

// SetCrosstalkScheduler は発話中フラグを共有する混線スケジューラを設定する。
func (n *GeminiNavigator) SetCrosstalkScheduler(scheduler *CrosstalkScheduler) {
	n.crosstalk = scheduler
}

// Speak はトリガーに応じたナビゲーターの発話を生成し、TTS で無線へ送出する
// (docs/navigator_design.md §3.5 の発話トリガー)。
func (n *GeminiNavigator) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	// spoke は音声を送出できたか。混線の予約を解放するかの判断に使う。
	spoke := false

	// 発話中は混線を止める (§5.1: ナビゲーターの発話と重ならないようにする)。
	//
	// 生成にかかる時間は事前に分からないので、まず見込みで押さえておき、
	// 送出後に**実際の再生時間**で上書きする。生成に失敗した場合は取り消す。
	// フラグを送出完了で落とすと、bridge がこれから再生する十数秒の間に
	// 混線が割り込む (実運用で発生)。
	if n.crosstalk != nil {
		n.crosstalk.MarkBusy(session.DeviceID, navigatorSpeakReserve)
		defer func() {
			// 送出まで到達しなかった場合に予約を解放する。
			// 成功時は下で実測値に置き換わっているので、ここでは触らない。
			if !spoke {
				n.crosstalk.ClearBusy(session.DeviceID)
			}
		}()
	}

	session.mu.Lock()
	stageIndex := session.StageIndex
	remainingMS := session.RemainingMS
	budget := 0
	if session.Built != nil {
		budget = session.Built.StageBudgetMS
	}
	hints := HintRule{}
	if session.Built != nil {
		hints = session.Built.Hints
	}
	level := HintLevel(&session.progress, budget, hints, time.Now())
	session.mu.Unlock()

	history := ""
	if n.logs != nil {
		history = n.logs.Render(session.SessionID)
	}

	prompt := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:      &n.config.Prompt,
		Character:   session.Character,
		Session:     session.Built,
		StageIndex:  stageIndex,
		RemainingMS: remainingMS,
		HintLevel:   level,
		RecentEvent: event,
		History:     history,
	})

	instruction := n.config.Prompt.TriggerInstruction(trigger)
	if trigger == "" && event != "" {
		instruction = event
	}

	text, err := n.processor.GenerateNavigatorReply(ctx, prompt, instruction)
	if err != nil {
		// 生成AIの障害時は自動フォールバックを設けず、マネージャー介入で運用する
		// (docs/game_session_design.md §9)。Web画面で検知できるようログに残す。
		return fmt.Errorf("GenerateNavigatorReply: %w", err)
	}

	// 文字数を併記する。無線を塞ぐ長さになっていないか運用中に確認するため
	// (出力ルールで 60 文字以内を指示しているが、生成AIが守るとは限らない)。
	log.Printf("[navigator %s/%s] (%s L%d, %d runes) %s",
		session.DeviceID, session.Character.Name, trigger, level, countRunes(text), text)
	if n := countRunes(text); n > navigatorMaxRunes {
		log.Printf("[navigator %s] WARN reply too long: %d runes (limit %d)",
			session.DeviceID, n, navigatorMaxRunes)
	}

	// 生成AIが角括弧の演技指示を付けてくることがあるため、記録前に取り除く
	// (表情の指定方法としては廃止済み。tts_prompt.go 参照)。
	if n.logs != nil {
		n.logs.Append(session.SessionID, ConversationEntry{
			Sender:   session.Character.Name,
			Receiver: senderPlayer,
			Message:  stripTTSTags(text),
		})
	}

	// 成功・失敗は効果音を先に鳴らしてからメッセージを再生する (§6)。
	// 効果音の再生時間も無線を塞ぐので、発話の分と合算する。
	sfxDuration := time.Duration(0)
	switch trigger {
	case "defused":
		sfxDuration = n.playSFX(sender, sfxSuccessFile)
	case "exploded":
		sfxDuration = n.playSFX(sender, sfxFailureFile)
	}

	// 表情は場面説明 (ディレクターズノート) で伝える。
	// 本文に記号を混ぜると TTS の応答が不安定になる (tts_prompt.go 参照)。
	note := directorNote(trigger)

	chunks := splitAnswerForTTS(text)
	buildPrompt := func(chunk string) string {
		return buildTTSPrompt(session.Character.TTSStyle, note, chunk)
	}
	duration, err := speakTTSChunks(ctx, n.ttsClient, sender, chunks, buildPrompt,
		session.Character.TTSVoice, "[navigator "+session.DeviceID+"]")
	if err != nil {
		return err
	}

	// 実際の再生時間で押さえ直す。ここから鳴り終わるまでが「無線が塞がっている」
	// 時間で、その間は混線を流さない。効果音を先に鳴らした場合はその分も足す。
	if total := sfxDuration + duration; total > 0 && n.crosstalk != nil {
		spoke = true
		n.crosstalk.SetBusy(session.DeviceID, total)
	}
	return nil
}

// playSFX は効果音アセットを再生する。未制作の場合は黙ってスキップする。
// 戻り値は送出した効果音の再生時間 (送出しなかった場合は 0)。
func (n *GeminiNavigator) playSFX(sender *AudioSender, name string) time.Duration {
	if n.sfxDir == "" {
		return 0
	}
	path := filepath.Join(n.sfxDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[navigator] sfx not available (%s): %v", path, err)
		return 0
	}
	if !sender.Send(oneshot(data)) {
		return 0
	}
	return oggOpusDuration(data)
}
