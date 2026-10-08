package main

// 装置の表示と合わない報告を検出する (ADR N-9b)。前半は5色以外の色名
// (決定124)、後半は5色の中の食い違い (決定135・決定137)。
//
// 装置のランプは赤・黄・緑・青・白の5色しかない。プレイヤーがそれ以外の
// 色名を報告したら、1回目は復唱して確かめさせ、同じ誤りを繰り返したら
// 不正解の線を切らせる (誤った報告をすると正しく解体できない、というゲーム性。
// 数え方は navigator_stage_progress.go)。
//
// **判定はサーバーが機械的に行う。** 生成AIに「成り立たない報告か」を
// 判定させると揺れる (シミュレーションで、誤った報告を通したり、正しい
// 報告を疑ったりした)。回数も会話ログから数えさせると取り違える。

import (
	"encoding/json"
	"sort"
	"strings"
)

// invalidColorGroups は5色に無い色名。**各組の先頭が代表の書き方**で、
// 同じ組の語は同じ報告とみなす (「茶色」と「茶」。決定138)。
// 組の中は**長い語を先に並べる** (「茶色」を「茶」より先に照合し、
// 復唱させる語を正確にする)。
//
// **LEDの見え方として言われうる語は入れない** (オレンジ・橙・水色)。
// 実機では黄色のLEDがオレンジ・橙に、青が水色に見えることがあり、
// 正しく見て報告したプレイヤーを爆発させてしまう。これらは正式な色へ
// 読み替える (lampColorWords。決定124 追記)。
var invalidColorGroups = [][]string{
	{"茶色", "ちゃいろ", "茶"},
	{"紫色", "むらさき", "紫"},
	{"桃色", "ピンク"},
	{"黒色", "くろいろ", "黒"},
	{"灰色", "はいいろ", "グレー"},
	{"金色", "きんいろ"},
	{"銀色", "ぎんいろ"},
	{"黄緑", "きみどり"},
	{"紺色", "紺"},
}

// detectInvalidColor は発話に含まれる5色以外の色名を返す。無ければ空文字。
func detectInvalidColor(text string) string {
	for _, group := range invalidColorGroups {
		for _, word := range group {
			if strings.Contains(text, word) {
				return word
			}
		}
	}
	return ""
}

// sameWrongReport は、2つの誤った報告が同じ内容かを返す (決定138)。
// 5色以外の色名は同じ組の語を同じとみなす。
func sameWrongReport(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return canonicalInvalidColor(a) == canonicalInvalidColor(b)
}

// canonicalInvalidColor は5色以外の色名を代表の書き方へ揃える。それ以外はそのまま。
func canonicalInvalidColor(word string) string {
	for _, group := range invalidColorGroups {
		for _, w := range group {
			if w == word {
				return group[0]
			}
		}
	}
	return word
}

// 装置の表示と合わないランプの報告を検出する (決定135・決定137)。
//
// 5色の中の色でも、その課題で**一度も光らない色**を報告したり、**点灯と点滅を
// 取り違えて**報告したりしたら誤った報告として、5色以外の色名と同じく数える
// (1回目は確かめ直させ、2回続いたら不正解の線)。ナビゲーターは切る線の色を
// 知らないので自分では気づけない。サーバーは Core へ送った表示を知っているので、
// 機械的に照合できる。
//
// **「交互に」のような光り方の関係は見ない。** 207 息が合わない は5色すべてが
// 点滅し、位相ずれを正しく見たプレイヤーほど「交互に」と言う (決定124)。
// 見るのは色ごとの「点灯か点滅か」だけ。
//
// 誤検知はありうるが、ナビゲーターが聞こえた内容を復唱するので、プレイヤーは
// 言い間違い・聞き違いに気づいて言い直せる (ユーザー判断)。

const (
	lampOn    = "on"
	lampBlink = "blink"
)

// LampStates は、課題の間に各色 (線ID) がとりうる光り方の集合。
// 載っていない色は、その課題で一度も光らない。
type LampStates map[string]map[string]bool

// lampColorWords は報告の中の5色の言い方と、その色の線ID。
// LEDの見え方として言われうる語 (オレンジ・橙・水色) は正式な色へ読み替える
// (決定124 追記)。**長い語を先に並べる**。
var lampColorWords = []struct {
	word string
	line string
}{
	{"オレンジ", "B"}, {"だいだい", "B"}, {"橙", "B"}, {"水色", "D"},
	{"赤", "A"}, {"あか", "A"},
	{"黄", "B"}, {"きいろ", "B"},
	{"緑", "C"}, {"みどり", "C"},
	{"青", "D"}, {"あお", "D"},
	{"白", "E"}, {"しろ", "E"},
}

// blinkPhrases は点滅を言う言い方。文節に分ける前に「点滅」へ揃える
// (「点いたり消えたり」の「消え」で文節ごと外されないように)。
var blinkPhrases = []string{
	"点いたり消えたり", "ついたり消えたり", "点灯と消灯", "チカチカ", "ちかちか",
	"長短", "モールス",
}

// steadyPhrases は点きっぱなしを言う言い方。「光っている」「ついている」は
// 点滅しているランプにも使うので入れない (光り方は見ずに色だけ照合する)。
var steadyPhrases = []string{
	"点きっぱなし", "つきっぱなし", "点いたまま", "ついたまま", "点灯したまま",
	"ずっと点い", "ずっとつい", "常に点い", "点灯",
}

// notLampReportForms を含む文節はランプの報告として扱わない。
//
// 線・ボタンの色は光っているランプの色と一致しない (202 は解読した語から
// 線の色を引く、103 は押すボタンを色で言う)。資料を読んだ話も同じ
// (301 の「基準色は白です」は表で引いた色)。
// 消えている・ついていないという言及や言い直し (「緑じゃなくて白」) の前半も外す。
var notLampReportForms = []string{
	"線", "コード", "ケーブル", "切", "ボタン", "押", "資料", "表", "端子",
	"基準", "引", "読", "キーワード", "単語", "文字",
	"消え", "消灯", "ついてな", "点いてな", "光ってな", "つかな", "点かな",
	"じゃな", "ではな", "でなく", "違",
}

// reportSegmentSeparators で発話を文節に分ける。
var reportSegmentSeparators = strings.NewReplacer(
	"。", "\n", "、", "\n", ",", "\n", "，", "\n", "!", "\n", "！", "\n",
	"?", "\n", "？", "\n", " ", "\n", "　", "\n", "けど", "\n", "が、", "\n",
	// 言い直し (「緑じゃなくて白」) は否定の直後で区切り、前半だけを外す
	"じゃなくて", "じゃなくて\n", "ではなくて", "ではなくて\n",
	"じゃなく", "じゃなく\n", "ではなく", "ではなく\n",
)

// lampWords は、その文節がランプの報告だと分かる言葉。
var lampWords = []string{"点灯", "点滅", "光", "ランプ", "ついて", "点いて", "つきっぱなし", "点きっぱなし"}

// lampFiller は、色だけを言った文節 (「緑です」) から取り除く言葉。
var lampFiller = strings.NewReplacer("です", "", "でした", "", "だった", "", "だ", "", "色", "",
	"どうぞ", "", "が", "", "と", "", "の", "", "は", "", "も", "", "かな", "", "かも", "",
	"っぽい", "", "みたい", "", "ぽい", "")

// soundsLikeLampReport は、文節がランプの報告かを返す (決定156)。
// ランプの言葉がある文節か、色だけを言った文節 (「緑です」) に限る。
// 「青を押しました」の打ち間違い (「青を添いsました」) をランプの報告と取り違え、
// ボタンを押している最中に確かめ直しを頼んだ。
func soundsLikeLampReport(segment string) bool {
	if hasAnyForm(segment, lampWords) {
		return true
	}
	rest := segment
	for _, c := range lampColorWords {
		rest = strings.ReplaceAll(rest, c.word, "")
	}
	rest = strings.TrimSpace(lampFiller.Replace(rest))
	return rest == ""
}

// lampClaim はランプの報告1つ。state は言い方から読み取れた光り方で、
// 読み取れなければ空 (色だけを照合する)。
type lampClaim struct {
	line  string
	state string
}

// lampReportClaims は、発話のうちランプの報告として言った色と光り方を順に返す。
func lampReportClaims(text string) []lampClaim {
	// 5色以外の色名 (黄緑など) は別に数えるので、ここでは取り除いておく
	for _, group := range invalidColorGroups {
		for _, word := range group {
			text = strings.ReplaceAll(text, word, "")
		}
	}
	for _, phrase := range blinkPhrases {
		text = strings.ReplaceAll(text, phrase, "点滅")
	}
	var claims []lampClaim
	for _, segment := range strings.Split(reportSegmentSeparators.Replace(text), "\n") {
		if segment == "" || hasAnyForm(segment, notLampReportForms) || !soundsLikeLampReport(segment) {
			continue
		}
		hits := colorHitsInOrder(segment)
		for i, h := range hits {
			state := ""
			if len(hits) == 1 {
				// 色が1つの文節は、文節全体の言い方から光り方を読む
				state = claimedState(segment)
			} else {
				// 色が2つ以上ある文節は、**その色に付いた言い方だけ**を読む
				// (「青が点滅して白が光っています」の「点滅」は青のもの。
				// 文節全体で読むと白も点滅と取り違え、正しい報告を誤りと判定した)。
				// 色の直後の「が」「は」で始まる区間だけを、その色の光り方とみなす。
				// 「点滅している青と点灯している白」のように光り方が色の前に来る
				// 言い方は読み取れないので、色だけを照合する。
				end := len(segment)
				if i+1 < len(hits) {
					end = hits[i+1].pos
				}
				span := segment[h.pos+len(h.word) : end]
				if strings.HasPrefix(span, "が") || strings.HasPrefix(span, "は") {
					state = claimedState(span)
				}
			}
			claims = append(claims, lampClaim{line: h.line, state: state})
		}
	}
	return claims
}

// claimedState は言い方から読み取れた光り方を返す。読み取れなければ空。
func claimedState(s string) string {
	blink := strings.Contains(s, "点滅")
	steady := hasAnyForm(s, steadyPhrases)
	if blink && !steady {
		return lampBlink
	}
	if steady && !blink {
		return lampOn
	}
	return ""
}

// colorHit は文節の中の色の言い方1つと、その位置。
type colorHit struct {
	word string
	line string
	pos  int
}

// colorHitsInOrder は文節に出てくる色を、言った順に全て返す
// (同じ色を2回言えば2つ。区間を次の色の手前で切るため)。
func colorHitsInOrder(segment string) []colorHit {
	var hits []colorHit
	for _, c := range lampColorWords {
		for from := 0; from < len(segment); {
			i := strings.Index(segment[from:], c.word)
			if i < 0 {
				break
			}
			hits = append(hits, colorHit{word: c.word, line: c.line, pos: from + i})
			from += i + len(c.word)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	return hits
}

// checkLampReport は報告を表示と照合する。
//   - wrong: 表示と合わない最初の報告 (「緑」「赤が点灯」)。無ければ空
//   - correct: 表示と合っている最初の色。誤りが無く合っている報告があるときだけ
//
// states が nil なら判定しない (表示が押すたびに変わる課題)。
func checkLampReport(text string, states LampStates) (wrong, correct string) {
	if states == nil {
		return "", ""
	}
	for _, c := range lampReportClaims(text) {
		st := states[c.line]
		if st == nil {
			return colorNameJA[c.line], ""
		}
		if c.state != "" && !st[c.state] {
			return colorNameJA[c.line] + "が" + lampStateJA(c.state), ""
		}
		if correct == "" {
			correct = colorNameJA[c.line]
		}
	}
	return "", correct
}

func lampStateJA(state string) string {
	if state == lampBlink {
		return "点滅"
	}
	return "点灯"
}

func hasAnyForm(s string, forms []string) bool {
	for _, form := range forms {
		if strings.Contains(s, form) {
			return true
		}
	}
	return false
}

// stageLampStates は、この課題の間に各色がとりうる光り方を返す。
// 判定できない課題 (204 色合わせ: 押すたびに表示が変わる) では nil。
//
// Core の中身は組み立て方で型が揺れる (map[string]string / map[string]any)
// ため、JSON を経由して読む。Core へ実際に送る形そのものを見ることにもなる。
// 表示の変わり方はファーム (game_task.cc) に合わせる。
func stageLampStates(stage *BuiltStage) LampStates {
	if stage == nil || stage.Core == nil {
		return nil
	}
	raw, err := json.Marshal(stage.Core)
	if err != nil {
		return nil
	}
	var core struct {
		Leds         map[string]json.RawMessage            `json:"leds"`
		RotaryLeds   map[string]map[string]json.RawMessage `json:"rotary_leds"`
		Precondition struct {
			ColorMatch json.RawMessage `json:"color_match"`
			PushSeq    struct {
				RevealCutOnComplete bool `json:"reveal_cut_on_complete"`
			} `json:"push_seq"`
		} `json:"precondition"`
	}
	if err := json.Unmarshal(raw, &core); err != nil {
		return nil
	}
	if len(core.Precondition.ColorMatch) > 0 {
		return nil
	}
	states := LampStates{}
	add := func(line, state string) {
		if state == "" {
			return
		}
		if states[line] == nil {
			states[line] = map[string]bool{}
		}
		states[line][state] = true
	}
	for line, spec := range core.Leds {
		add(line, ledSpecState(spec))
	}
	// 209 配電盤: ダイヤル位置ごとの表示すべて。解除位置は切る線だけが点滅し、
	// 危険位置は赤だけが点灯する (game_task.cc の配電盤の組み立て)。
	if len(core.RotaryLeds) > 0 {
		for _, leds := range core.RotaryLeds {
			for line, spec := range leds {
				add(line, ledSpecState(spec))
			}
		}
		add(stage.Cut, lampBlink)
		add("A", lampOn)
	}
	// 103・201: 押し切ると切る線の色だけが点灯する (C-13)
	if core.Precondition.PushSeq.RevealCutOnComplete {
		add(stage.Cut, lampOn)
	}
	return states
}

// ledSpecState は Core の LED 指定1つを光り方へ直す。消灯は空。
func ledSpecState(spec json.RawMessage) string {
	var s string
	if json.Unmarshal(spec, &s) == nil {
		switch s {
		case "on":
			return lampOn
		case "blink":
			return lampBlink
		}
		return ""
	}
	var obj struct {
		Pattern string `json:"pattern"`
	}
	if json.Unmarshal(spec, &obj) == nil {
		switch obj.Pattern {
		case "on":
			return lampOn
		case "blink", "morse":
			return lampBlink
		}
	}
	return ""
}
