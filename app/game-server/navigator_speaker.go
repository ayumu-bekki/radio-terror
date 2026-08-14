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
	// 発話中は混線を止める (§5.1: ナビゲーターの発話と重ならないようにする)
	if n.crosstalk != nil {
		n.crosstalk.SetSpeaking(session.DeviceID, true)
		defer n.crosstalk.SetSpeaking(session.DeviceID, false)
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

	log.Printf("[navigator %s/%s] (%s L%d) %s",
		session.DeviceID, session.Character.Name, trigger, level, text)

	// 生成AIが角括弧の演技指示を付けてくることがあるため、記録前に取り除く
	// (表情の指定方法としては廃止済み。tts_prompt.go 参照)。
	if n.logs != nil {
		n.logs.Append(session.SessionID, ConversationEntry{
			Sender:   session.Character.Name,
			Receiver: senderPlayer,
			Message:  stripTTSTags(text),
		})
	}

	// 成功・失敗は効果音を先に鳴らしてからメッセージを再生する (§6)
	switch trigger {
	case "defused":
		n.playSFX(sender, sfxSuccessFile)
	case "exploded":
		n.playSFX(sender, sfxFailureFile)
	}

	// 表情は場面説明 (ディレクターズノート) で伝える。
	// 本文に記号を混ぜると TTS の応答が不安定になる (tts_prompt.go 参照)。
	note := directorNote(trigger)

	chunks := splitAnswerForTTS(text)
	buildPrompt := func(chunk string) string {
		return buildTTSPrompt(session.Character.TTSStyle, note, chunk)
	}
	return streamTTSChunks(ctx, n.ttsClient, sender, chunks, buildPrompt,
		session.Character.TTSVoice, "[navigator "+session.DeviceID+"]")
}

// playSFX は効果音アセットを再生する。未制作の場合は黙ってスキップする。
func (n *GeminiNavigator) playSFX(sender *AudioSender, name string) {
	if n.sfxDir == "" {
		return
	}
	path := filepath.Join(n.sfxDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[navigator] sfx not available (%s): %v", path, err)
		return
	}
	sender.Send(oneshot(data))
}
