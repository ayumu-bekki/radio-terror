package main

// ステージ内の進行状態 (誤った報告の数え方・確かめ直しへの言い直し・
// モールスの課題でゴールを伝えたか)。
//
// 判定の材料 (表示との照合) は navigator_color_check.go、ナビへの指示文は
// navigator_prompt.go の wrongReportBlock。
//
// 以前はここにヒントレベル (L1〜L4) の計算も置いていたが、廃止した (決定129)。
// 段階的に出すヒントは、各ステージの `procedure` に**条件**として書く。

import (
	"regexp"
	"strings"
)

// StageProgress は現在のステージにおける進行状態。
// ステージが切り替わったらリセットする。
type StageProgress struct {
	// WrongReportCount は、**同じ**誤った報告が続いた回数 (決定124・137・138)。
	// 5色以外の色名・この課題で光らない色・点灯と点滅の取り違えを数え、
	// 同じ内容が2回続いたら不正解の線を切らせる。違う誤りなら1から数え直す
	// (新しい報告として確かめ直す)。表示と合う報告が来たら0に戻す
	// (報告と関係のない発話では戻さない)。ステージが変われば数え直す。
	WrongReportCount int
	// LastWrongReport は**直前の**プレイヤー発話の、表示と合わない報告
	// (「茶色」「緑」「赤が点灯」)。次の発話に無ければ空へ戻す。
	LastWrongReport string
	// LastWrongIsMismatch は LastWrongReport が5色の中の色についての食い違い
	// (光らない色・光り方の取り違え) か。1回目の聞き返し方が変わる
	// (5色以外なら「5色のどれかのはず」と言える)。
	LastWrongIsMismatch bool

	// flaggedColor は確かめ直しを頼んだ誤った報告。次の報告で正しく言い直されたら
	// CorrectedFrom/To に移して空へ戻す (決定136)。
	flaggedColor string
	// CorrectedFrom / CorrectedTo は**直前の**発話が確かめ直しへの正しい言い直し
	// だったとき、取り消された報告と新しい色。言い直しでなければ空。
	//
	// 言い直しの発話はサーバーから見れば正しい報告で、特別な指示が付かない。
	// するとナビは会話ログの自分の「赤だな」をなぞり、取り消された赤の線を
	// 切らせうる (決定136)。
	CorrectedFrom string
	CorrectedTo   string

	// LastMorseNote は**直前の**発話の解読の報告を資料1の表と照合した結果
	// (morseReportNote。決定143)。正解の単語を伏せたモールスの課題でだけ入る。
	LastMorseNote string
	// morseNoteCarried は LastMorseNote が1つ前の発話から引き継いだものか。
	morseNoteCarried bool
	// MorseMisses は、表に載っていない綴りの報告が続いた回数 (決定148)。
	// 2回目からは同じ返しを繰り返させない (「壊れたラジオ」と言われた)。
	MorseMisses int
	// MorseLessons は、読み方が分からない様子の発話が来た回数 (決定149)。
	// 初心者向けのコツをこの回数に応じて1つずつ進める。
	MorseLessons int
	// MorseMentioned は、この課題でプレイヤーかナビが「モールス」と言ったか (決定150)。
	// 言われるまでは、必ず言うことの先頭に「点滅はモールス信号」を置く。
	// プレイヤーが綴りや長短を報告したときも、モールスだと分かっているとみなす。
	MorseMentioned bool
	// PushSeqReset は、ボタン列の押し間違えで列が最初に戻ったあと、まだナビが
	// それを伝えていないか (決定156)。次のナビの返答で下ろす。
	PushSeqReset bool
	// PlayerDecoded は、この課題でプレイヤーが綴りや長短を報告したか (決定153)。
	// 報告していれば読めているので、読み方が分かるかは尋ねない。
	PlayerDecoded bool
	// ReadabilityAsked は、この課題でナビが読み方が分かるかを尋ねたか (決定153)。
	ReadabilityAsked bool
	// MorseGoalTold は、この課題でナビが「表の単語の行の色の線を切る」という
	// ゴールを伝え終えたか (決定145)。伝える前は、照合結果より表の作りとゴールを
	// 優先させる。1発話に詰めると、照合結果が先に出てゴールが削られた (7/10)。
	MorseGoalTold bool
}

// NoteNavigatorReply はナビの返答を見て、ゴールを伝えたかを記録する (決定145)。
// 「行」と「切」が両方入っていれば、表の行の色の線を切ると伝えたとみなす。
func (p *StageProgress) NoteNavigatorReply(reply string) {
	p.PushSeqReset = false
	if strings.Contains(reply, "モールス") {
		p.MorseMentioned = true
	}
	if readabilityQuestion.MatchString(reply) {
		p.ReadabilityAsked = true
	}
	if strings.Contains(reply, "行") && strings.Contains(reply, "切") {
		p.MorseGoalTold = true
	}
}

// NoteMorseReport は解読の報告を資料1の表と照合した結果を記録する (決定143)。
//
// 綴りを含まない発話 (「先頭がアールでしたが大丈夫ですか」「だからないのです」) は、
// 直前の報告についての話なので、**直前の照合結果を1回だけ引き継ぐ**。
// 引き継がないと照合結果が空になり、ナビが「アールなわけないやろ」と崩れたり、
// 前の話題を繰り返したりした。2回以上は引き継がない (別の話題に古い結果を載せない)。
func (p *StageProgress) NoteMorseReport(note string) {
	if strings.Contains(note, morseNoviceMark) {
		p.MorseLessons++
	}
	if strings.Contains(note, "載っていません") {
		p.MorseMisses++
	} else if strings.Contains(note, "載っています") {
		p.MorseMisses = 0
	}
	if note != "" {
		p.LastMorseNote, p.morseNoteCarried = note, false
		return
	}
	if p.LastMorseNote != "" && !p.morseNoteCarried {
		p.morseNoteCarried = true
		return
	}
	p.LastMorseNote, p.morseNoteCarried = "", false
}

// Reset はステージ切り替え時に進行状態を初期化する。
func (p *StageProgress) Reset() {
	p.WrongReportCount = 0
	p.LastWrongReport = ""
	p.LastWrongIsMismatch = false
	p.flaggedColor = ""
	p.CorrectedFrom = ""
	p.CorrectedTo = ""
	p.LastMorseNote = ""
	p.morseNoteCarried = false
	p.MorseGoalTold = false
	p.MorseMisses = 0
	p.MorseLessons = 0
	p.MorseMentioned = false
	p.PlayerDecoded = false
	p.PushSeqReset = false
	p.ReadabilityAsked = false
}

// readabilityQuestion は、ナビが読み方が分かるかを尋ねた言い方。
var readabilityQuestion = regexp.MustCompile(`読み方[^。]{0,12}(分か|わか|知って|大丈夫)`)

// NotePlayerMorseKnowledge は、プレイヤーの発話からモールスだと分かっているかを記録する。
// 「モールス」と言った、または綴りや長短を報告したら、分かっているとみなす (決定150)。
func (p *StageProgress) NotePlayerMorseKnowledge(text string) {
	decoded := len(reportedSpellingsMin(text, 1)) > 0 || elementReportNote(text) != ""
	if decoded {
		p.PlayerDecoded = true
	}
	if strings.Contains(text, "モールス") || decoded {
		p.MorseMentioned = true
	}
}

// NoteReport はプレイヤーの発話を表示と照合し、表示と合わない報告が続いた回数を
// 数える。states はこの課題の光り方 (stageLampStates)。nil なら5色以外の色名だけを見る。
func (p *StageProgress) NoteReport(text string, states LampStates) {
	p.CorrectedFrom, p.CorrectedTo = "", ""
	word := detectInvalidColor(text)
	mismatch := false
	correct := ""
	if word == "" {
		word, correct = checkLampReport(text, states)
		mismatch = word != ""
	}
	p.LastWrongReport = word
	p.LastWrongIsMismatch = mismatch
	if word != "" {
		// 同じ内容を続けて報告したときだけ数を進める (決定138)。
		// 「青」→「赤」のように違う誤りは新しい報告として、確かめ直しからやり直す
		if sameWrongReport(word, p.flaggedColor) {
			p.WrongReportCount++
		} else {
			p.WrongReportCount = 1
		}
		p.flaggedColor = word
		return
	}
	if correct == "" {
		return // ランプの報告ではない発話。続いた回数はそのまま
	}
	// 表示と合う報告。続いた回数を0に戻し、確かめ直しへの言い直しなら印を立てる
	p.WrongReportCount = 0
	if p.flaggedColor != "" {
		p.CorrectedFrom, p.CorrectedTo = p.flaggedColor, correct
		p.flaggedColor = ""
	}
}

// UndoReport は、応答の生成に失敗した発話で数えた誤った報告を取り消す
// (決定131)。プレイヤーは聞き返しを受けていないので、数に入れると
// 1回目の確認を飛ばして不正解の線を切らせる段階へ進んでしまう。
//
// word・count は数えた時点の値。その後に別の報告で数が進んでいたら
// (応答待ちの間に次の発話が届いた) 触らない。
func (p *StageProgress) UndoReport(word string, count int) {
	if word == "" || p.WrongReportCount != count || count <= 0 || p.LastWrongReport != word {
		return
	}
	p.WrongReportCount--
}

// UndoCorrection は、応答の生成に失敗した言い直しを未処理へ戻す (決定131 と同じ理由)。
func (p *StageProgress) UndoCorrection(from string) {
	if from != "" && p.flaggedColor == "" {
		p.flaggedColor = from
	}
}
