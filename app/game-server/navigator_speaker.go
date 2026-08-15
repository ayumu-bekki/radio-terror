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

	// 成功・失敗は効果音を**メッセージと1つの音声に連結して**送る (§6)。
	//
	// 効果音を別パケットで先に送ると、効果音が鳴り終わってから TTS の生成を
	// 待つ数秒の無音が無線に乗る。連結すれば「効果音 → メッセージ」が
	// 途切れずに流れ、生成にかかる時間がそのまま演出の「間」になる。
	var sfxPCM []int16
	switch trigger {
	case "defused":
		sfxPCM = n.loadSFX(sfxSuccessFile)
	case "exploded":
		sfxPCM = n.loadSFX(sfxFailureFile)
	}

	// 表情は場面説明 (ディレクターズノート) と本文中の表情タグの
	// 両方で伝える (tts_prompt.go 参照)。
	note := directorNote(trigger)

	buildPrompt := func(body string) string {
		return buildTTSPrompt(session.Character.TTSStyle, note, body)
	}
	// duration は効果音を連結した後の全長 (speakTTS が連結してから測る)。
	duration, err := speakTTS(ctx, n.ttsClient, sender, text, buildPrompt,
		session.Character.TTSVoice, "[navigator "+session.DeviceID+"]", sfxPCM)
	if err != nil {
		return err
	}

	// 実際の再生時間で押さえ直す。ここから鳴り終わるまでが「無線が塞がっている」
	// 時間で、その間は混線を流さない。
	if duration > 0 && n.crosstalk != nil {
		spoke = true
		n.crosstalk.SetBusy(session.DeviceID, duration)
	}
	return nil
}

// loadSFX は効果音アセットを読み込み、連結できる PCM へデコードする。
// 未制作・デコード不能の場合は nil を返し、発話だけを送る
// (効果音が無くてもゲームは続行できるため、ここで失敗させない)。
func (n *GeminiNavigator) loadSFX(name string) []int16 {
	if n.sfxDir == "" {
		return nil
	}
	path := filepath.Join(n.sfxDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[navigator] sfx not available (%s): %v", path, err)
		return nil
	}
	pcm, err := decodeOggOpusToPCM(data)
	if err != nil {
		// レート違い等で連結できない。アセットを 24kHz mono で作り直す必要がある。
		log.Printf("[navigator] WARN sfx decode failed (%s): %v", path, err)
		return nil
	}
	return pcm
}
