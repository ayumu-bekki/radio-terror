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

// navigatorMaxRunes は1発話の**目安**の上限 (navigator/prompt.toml の出力ルールと同じ値)。
//
// **緩い条件として運用する。** 超えても発話は止めないし、切り詰めもしない —
// 途中で切ると指示が欠けて誤解を生むほうが害が大きい。長い発話は無線を塞いで
// 次の交信を遅らせるため、プロンプトが効いているかを運用中に確認できるよう
// ログへ警告を残すだけにする。
//
// 実測では口調によって 30〜40 字と幅があり (フクロウ29.5 / ヒバリ38.2)、
// 敬語・高テンションのキャラは要素が増えると超える。
// **これは想定内**で、超過ゼロを目指して指示を締めると
// 安心させる一言のような「後から足したもの」が削られる (決定24・31・34・37)。
//
// **名乗り (コールサイン) はこの数えに含めない** (countBodyRunes)。
// 名乗りは緊迫時を除いて毎回入れる方針 (ADR N-21) なので、含めると
// 名乗ったぶんだけ本文が削られる。
const navigatorMaxRunes = 60

// urgentNoticeAllowanceRunes は残り時間の告知を添える発話にだけ足す猶予。
//
// 告知はセッション中1回だけ本来の受け答えへ**添える**もので、
// 目安の文字数には数えないと決めてある (navigator/prompt.toml)。
// ただし文言が自由なため、名乗り (countBodyRunes) のように位置で
// 切り出して引き算できない。**引けない以上、その1回だけ枠を広げる**。
//
// 20字は「時間がねえ、あと1分だ」程度の一言を見込んだ幅。
const urgentNoticeAllowanceRunes = 20

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

// generateReply はプロンプト組み立て・生成AI呼び出し・
// ログ追記までを行う (Speak と SpeakText の共有部分)。
//
// TTS生成・SFX連結・無線送出はここには含まない。**Speak(TTS込み)と
// SpeakText(テキストのみ)が完全に同じ発話内容を得られる**ことが要点 —
// ここが分岐すると「コンソールで見た応答」が本番の応答と一致する保証が
// なくなる (ADR M-7)。
func (n *GeminiNavigator) generateReply(ctx context.Context, session *GameSession, trigger, event string) (text string, announceUrgent bool, remainingMSOut int, err error) {
	session.mu.Lock()
	stageIndex := session.StageIndex
	remainingMS := session.RemainingMS

	// 残り時間が僅少になったことを、この発話で初めて伝えるか
	// (セッション中に1回だけ。旧 time_warning トリガーの置き換え)。
	//
	// **ここでは印を立てない。** 生成に失敗した発話でも立ててしまうと、
	// 一度も伝えないまま「伝えた」ことになる。送出できてから立てる (呼び出し側)。
	//
	// **終幕の発話では告知しない** — 爆発・解除はもう時間の話をする場面ではない。
	announceUrgent = !session.urgentNoticed &&
		remainingMS > 0 && remainingMS <= n.config.Prompt.UrgentThresholdMS &&
		trigger != "exploded" && trigger != "defused"
	// 誤った報告は**プレイヤー発話への応答でだけ**扱う (ADR N-9b)。
	// 無応答の声掛けなど他のトリガーで反応すると、古い報告を蒸し返す。
	wrongReport, wrongCount, wrongMismatch := "", 0, false
	correctedFrom, correctedTo := "", ""
	morseNote := ""
	morseGoalTold := false
	morseMisses := 0
	morseLessons := 0
	morseMentioned := false
	justAdvanced := false
	if trigger == "player_message" {
		wrongReport = session.progress.LastWrongReport
		wrongCount = session.progress.WrongReportCount
		wrongMismatch = session.progress.LastWrongIsMismatch
		correctedFrom, correctedTo = session.progress.CorrectedFrom, session.progress.CorrectedTo
		morseNote = session.progress.LastMorseNote
		morseGoalTold = session.progress.MorseGoalTold
		morseMisses = session.progress.MorseMisses
		morseLessons = session.progress.MorseLessons
		morseMentioned = session.progress.MorseMentioned
		// 突破後の最初の報告への返答で1回だけ使う (決定127)
		justAdvanced = session.firstReportAfterStage
		session.firstReportAfterStage = false
	}
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
		RecentEvent: event,
		History:     history,

		AnnounceUrgent: announceUrgent,

		WrongReport:         wrongReport,
		WrongReportCount:    wrongCount,
		WrongReportMismatch: wrongMismatch,
		CorrectedFrom:       correctedFrom,
		CorrectedTo:         correctedTo,
		MorseReportNote:     morseNote,
		MorseGoalTold:       morseGoalTold,
		MorseMisses:         morseMisses,
		MorseLessons:        morseLessons,
		MorseMentioned:      morseMentioned,
		JustAdvanced:        justAdvanced,
	})

	instruction := n.config.Prompt.TriggerInstruction(trigger)
	if trigger == "" && event != "" {
		instruction = event
	}

	reply, err := n.processor.GenerateNavigatorReply(ctx, prompt, instruction)
	if err != nil {
		// プレイヤーへ何も返せなかったので、この発話で使った印を戻す (決定131)。
		// 戻さないと、誤った報告が確かめ直しを経ずに不正解の線を切らせる
		// 段階へ進み、突破直後の印も失われる。
		if trigger == "player_message" {
			session.mu.Lock()
			session.progress.UndoReport(wrongReport, wrongCount)
			session.progress.UndoCorrection(correctedFrom)
			if justAdvanced {
				session.firstReportAfterStage = true
			}
			session.mu.Unlock()
		}
		// 生成AIの障害時は自動フォールバックを設けず、マネージャー介入で運用する
		// (docs/game_session_design.md §9)。Web画面で検知できるようログに残す。
		return "", false, remainingMS, fmt.Errorf("GenerateNavigatorReply: %w", err)
	}
	text = reply.Reply
	session.mu.Lock()
	session.progress.NoteNavigatorReply(text)
	session.mu.Unlock()

	// 文字数を併記する。無線を塞ぐ長さになっていないか運用中に確認するため
	// (出力ルールで 60 文字以内を指示しているが、生成AIが守るとは限らない)。
	//
	// **名乗りは数えない** (countBodyRunes)。名乗りは毎回入れる方針
	// (ADR N-21) なので、数えに含めると常時それだけ本文が圧迫され、
	// 警告が「名乗ったから長い」で埋まって本当の超過が見えなくなる。
	// ログには全長も併記して、実際に無線を塞ぐ長さは追えるようにする。
	bodyRunes := countBodyRunes(text, session.Character.Name)
	log.Printf("[navigator %s/%s] (%s, %d runes / %d total) %s",
		session.DeviceID, session.Character.Name, trigger,
		bodyRunes, countRunes(text), text)

	// **残り時間の告知を添えた発話は目安を広げる。**
	//
	// 告知の文言は自由なので、名乗りのように位置で切り出して引き算できない。
	// 「数えない」と言いながら数えていると、告知を入れたぶんだけ警告が出て、
	// 次に指示を締める材料にされる — 削られるのは手順や警告のほうになる
	// (ADR N-22)。**引けない以上、その1回だけ枠を広げる**方が実態に合う。
	limit := navigatorMaxRunes
	if announceUrgent {
		limit += urgentNoticeAllowanceRunes
	}
	if bodyRunes > limit {
		log.Printf("[navigator %s] WARN reply too long: %d runes (limit %d, 名乗りを除く)",
			session.DeviceID, bodyRunes, limit)
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

	return text, announceUrgent, remainingMS, nil
}

// SpeakText はテキスト入力に対する応答をテキストのみで生成する
// (コンソールモード専用。TTS/無線送出を行わない)。
//
// generateReply を直接呼ぶだけで、Speak と全く同じプロンプト組み立て・
// ログ追記を経る。
func (n *GeminiNavigator) SpeakText(ctx context.Context, session *GameSession, trigger, event string) (string, error) {
	text, announceUrgent, _, err := n.generateReply(ctx, session, trigger, event)
	if err != nil {
		return "", err
	}
	if announceUrgent {
		session.mu.Lock()
		session.urgentNoticed = true
		session.mu.Unlock()
	}
	return stripTTSTags(text), nil
}

// Speak はトリガーに応じたナビゲーターの発話を生成し、TTS で無線へ送出する
// (docs/navigator_design.md §3.5 の発話トリガー)。
func (n *GeminiNavigator) Speak(ctx context.Context, sender *AudioSender, session *GameSession, trigger, event string) error {
	session.mu.Lock()
	consoleMode := session.ConsoleMode
	session.mu.Unlock()

	// **コンソールモードはここで完結させる。** TTS生成・SFX連結・無線送出・
	// 混線のbusy予約は無線演出そのものなので、テキストのみのデバッグでは
	// 一切不要 (ADR M-7)。発話内容自体は generateReply を通して本番と
	// 完全に共有するので、ここで応答テキストを捨てても検証結果は変わらない。
	if consoleMode {
		_, _, _, err := n.generateReply(ctx, session, trigger, event)
		return err
	}

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

	text, announceUrgent, remainingMS, err := n.generateReply(ctx, session, trigger, event)
	if err != nil {
		return err
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

	// 表情は読み方の指定 (ディレクターズノート) と本文中の表情タグの
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

	// **送出できてから印を立てる。** 生成・送出に失敗した発話で立てると、
	// 一度も伝えないまま「伝えた」ことになり、残り時間の告知が消える。
	if announceUrgent {
		session.mu.Lock()
		session.urgentNoticed = true
		session.mu.Unlock()
		log.Printf("[navigator %s] urgent notice delivered (remaining %ds)",
			session.DeviceID, remainingMS/1000)
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
