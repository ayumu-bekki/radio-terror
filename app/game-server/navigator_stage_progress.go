package main

// ステージ内の進行状態 (誤った報告の数え方と、確かめ直しへの言い直し)。
//
// 判定の材料 (表示との照合) は navigator_color_check.go、ナビへの指示文は
// navigator_prompt.go の wrongReportBlock。
//
// 以前はここにヒントレベル (L1〜L4) の計算も置いていたが、廃止した (決定129)。
// 段階的に出すヒントは、各ステージの `procedure` に**条件**として書く。

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
}

// Reset はステージ切り替え時に進行状態を初期化する。
func (p *StageProgress) Reset() {
	p.WrongReportCount = 0
	p.LastWrongReport = ""
	p.LastWrongIsMismatch = false
	p.flaggedColor = ""
	p.CorrectedFrom = ""
	p.CorrectedTo = ""
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
