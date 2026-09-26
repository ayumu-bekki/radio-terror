package main

// 資料1 (モールス対照表) のうち、ナビゲーターへ渡してよい列 (決定143)。
//
// モールスの課題 (202・302) では正解の単語をナビから伏せている (決定142)。
// 伏せたままだとナビは表の中身も知らず、「MIKE は表に載ってない」と事実と
// 違うことを言い、表に無い綴り (MANGO) で手詰まりになったプレイヤーを
// 助けられなかった (実運用)。紙に印刷されている**文字・モールス信号・単語**を
// 渡し、綴りの近い単語を挙げて導けるようにする。
//
// **色の列は渡さない。** ナビが候補に挙げた単語から切る線の色が決まってしまう。
// 紙の表との一致は docs/printed_materials.md §2.2 (刷ったら突き合わせる)。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// morseSheetRows は資料1の行 (A-Z)。docs/printed_materials.md §2.2 と同じ内容。
var morseSheetRows = []struct {
	letter string
	morse  string
	word   string
}{
	{"A", "・－", "ALFA"}, {"B", "－・・・", "BRAVO"}, {"C", "－・－・", "CHARLIE"},
	{"D", "－・・", "DELTA"}, {"E", "・", "ECHO"}, {"F", "・・－・", "FOXTROT"},
	{"G", "－－・", "GOLF"}, {"H", "・・・・", "HOTEL"}, {"I", "・・", "INDIA"},
	{"J", "・－－－", "JULIETT"}, {"K", "－・－", "KILO"}, {"L", "・－・・", "LIMA"},
	{"M", "－－", "MIKE"}, {"N", "－・", "NOVEMBER"}, {"O", "－－－", "OSCAR"},
	{"P", "・－－・", "PAPA"}, {"Q", "－－・－", "QUEBEC"}, {"R", "・－・", "ROMEO"},
	{"S", "・・・", "SIERRA"}, {"T", "－", "TANGO"}, {"U", "・・－", "UNIFORM"},
	{"V", "・・・－", "VICTOR"}, {"W", "・－－", "WHISKEY"}, {"X", "－・・－", "XRAY"},
	{"Y", "－・－－", "YANKEE"}, {"Z", "－－・・", "ZULU"},
}

// morseSheetBlock は資料1の表 (色の列を除く) と読み方をプロンプト用に組み立てる。
func morseSheetBlock(sheetName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %sの表 (色の列は伏せてある)\n", sheetName)
	b.WriteString("プレイヤーの手元の表と同じ内容です。**どの単語が表に載っているか**、" +
		"**各文字の長短**はこれで確かめられます。色の列はあなたには見えません。\n" +
		"呼び方は**「モールス信号」に揃えます** (紙の表の列見出しも同じ。プレイヤーが" +
		"「モールス符号」と言っても同じもの。決定151)。\n")
	for _, r := range morseSheetRows {
		fmt.Fprintf(&b, "- %s %s %s\n", r.letter, r.morse, r.word)
	}
	b.WriteString(morseReadingGuide)
	return b.String()
}

// morseReadingGuide はモールスの読み方の手順。プレイヤーへ**丁寧に教えてよい**内容。
//
// 「光り方の区切りを確かめて」では伝わらず (実運用: HOTEL を EEEETEETEE と読み、
// 同じ返しを繰り返した)、秒数 (約0.3秒・約0.9秒) を言っても点滅を見ながら測れない
// (ユーザー指摘)。**見え方のたとえと、数え方の工夫**で教える (決定148)。
// 光と消灯の長さの比は実機 (core-system main/led_pattern.cc の MakeMorse) に合わせる:
// 短い光1・長い光3・同じ文字の中の消灯1・文字の区切り3・単語の終わり7。
const morseReadingGuide = `
### モールスの読み方 (プレイヤーに教えてよい。見え方と数え方で丁寧に)
**秒数は言わない** (点滅を見ながら測れない)。見え方のたとえと、数え方の工夫で教える。
1回の発話では**1〜2ステップずつ**。報告を聞いて、**つまずいている所から**教える。
1. **同じ単語を何度も繰り返している**。何周見てもよい。1周目は区切りだけ、2周目で長短を
   書き取る、のように分けると読みやすい
2. **単語の始まりを見つける**: しばらく消えたままになり「終わったかな」と思ったあと、
   また光り始めたところが単語の頭
3. **光の長さは2種類**: 「パッ」と一瞬の短い光と、「パーッ」とはっきり長い光
   (長い光は短い光の3倍くらい)。声に出して「トン」「ツー」と言いながら見ると取り違えにくい
4. **消え方の見分け**: 同じ文字の中では、光と光が**ほとんど続けて**見える。
   文字が変わるところでは**一呼吸おく**。一呼吸おいたら紙に「/」を書いて区切る。
   ここを見落とすと、2文字を1文字に (または1文字を2文字に) 数えてしまう。いちばん多い読み違い
5. **長い光が続くとき** (O は ツー・ツー・ツー) は、あいだで一瞬だけ消える。
   光るたびに**指を折って回数を数える**。まとめて1回と数えるのも多い読み違い
6. 「/」で区切った1文字ぶんの長短を、上の表の符号と照らして文字にする
- 1文字ずつ「長・短・短」のように報告してもらえれば、サーバーが表の符号で文字に直して渡す
`

// ---- 解読の報告を資料1の表と照合する (決定143) ----
//
// 生成AIは綴りの照合 (表にあるか・どれが1文字違いか) が苦手で、表を渡しても
// 「M は表にない」と言ったり、MANGO に TANGO を挙げられなかったりした。
// サーバーが**印刷された表だけ**を使って機械的に照合し、結果をナビへ渡す。
// **正解の単語は使わない** — 表にある近い単語を候補として挙げるだけ。

// morseLetterNames は文字の読み (カタカナ)。**長い読みを先に並べる**。
var morseLetterNames = []struct{ kana, letter string }{
	{"ダブリュー", "W"}, {"エックス", "X"}, {"エイチ", "H"}, {"エッチ", "H"},
	{"ジェイ", "J"}, {"ジェー", "J"}, {"ディー", "D"}, {"ティー", "T"},
	{"キュー", "Q"}, {"アール", "R"}, {"ゼット", "Z"}, {"ヴィー", "V"},
	{"エー", "A"}, {"エイ", "A"}, {"ビー", "B"}, {"シー", "C"}, {"デー", "D"},
	{"イー", "E"}, {"エフ", "F"}, {"ジー", "G"}, {"アイ", "I"}, {"ケー", "K"},
	{"ケイ", "K"}, {"エル", "L"}, {"エム", "M"}, {"エヌ", "N"}, {"オー", "O"},
	{"ピー", "P"}, {"エス", "S"}, {"テー", "T"}, {"ユー", "U"}, {"ブイ", "V"},
	{"ワイ", "Y"},
}

// morseWordNames はフォネティックコードの読み (カタカナ) と綴り。
var morseWordNames = map[string]string{
	"アルファ": "ALFA", "ブラボー": "BRAVO", "チャーリー": "CHARLIE", "デルタ": "DELTA",
	"エコー": "ECHO", "フォックストロット": "FOXTROT", "ゴルフ": "GOLF", "ホテル": "HOTEL",
	"インディア": "INDIA", "ジュリエット": "JULIETT", "キロ": "KILO", "リマ": "LIMA",
	"マイク": "MIKE", "ノベンバー": "NOVEMBER", "ノーベンバー": "NOVEMBER", "オスカー": "OSCAR",
	"パパ": "PAPA", "ケベック": "QUEBEC", "ロメオ": "ROMEO", "シエラ": "SIERRA",
	"タンゴ": "TANGO", "ユニフォーム": "UNIFORM", "ビクター": "VICTOR", "ヴィクター": "VICTOR",
	"ウィスキー": "WHISKEY", "エックスレイ": "XRAY", "ヤンキー": "YANKEE", "ズールー": "ZULU",
}

var (
	asciiWordPattern   = regexp.MustCompile(`[A-Za-z]{2,}`)
	asciiLetterPattern = regexp.MustCompile(`[A-Za-z]+`)
	katakanaRun        = regexp.MustCompile(`[ァ-ヶー]+`)
)

// reportedSpellings は発話から、解読の報告らしき綴りを取り出す (大文字)。
// 英字の並び・文字の読みの並び (アールエルエフエー)・単語の読み (タンゴ)。
func reportedSpellings(text string) []string {
	return reportedSpellingsMin(text, 2)
}

// reportedSpellingsMin は reportedSpellings の最短の長さを指定できる版。
// 202 (英字1文字。決定152) では1文字の報告も拾う。
func reportedSpellingsMin(text string, minLen int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if len(s) >= minLen && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	pattern := asciiWordPattern
	if minLen < 2 {
		pattern = asciiLetterPattern
	}
	for _, w := range pattern.FindAllString(text, -1) {
		add(strings.ToUpper(w))
	}
	for _, run := range katakanaRun.FindAllString(text, -1) {
		if word, ok := morseWordNames[run]; ok {
			add(word)
			continue
		}
		if letters, ok := parseLetterNames(run); ok {
			add(letters)
		}
	}
	return out
}

// parseLetterNames は文字の読みだけでできたカタカナ列を英字へ直す。
// 読みで埋め尽くせなければ false (普通の語とみなす)。
func parseLetterNames(run string) (string, bool) {
	var b strings.Builder
	for run != "" {
		matched := false
		for _, n := range morseLetterNames {
			if strings.HasPrefix(run, n.kana) {
				b.WriteString(n.letter)
				run = strings.TrimPrefix(run, n.kana)
				matched = true
				break
			}
		}
		if !matched {
			return "", false
		}
	}
	return b.String(), b.Len() >= 1
}

// unsureForms は、報告に確信が無いことを示す言い方。
var unsureForms = []string{"でしょうか", "ですか", "かな", "かも", "たぶん", "多分", "思う", "?", "？"}

// soundsUnsure は、報告に迷いの言葉が含まれるかを返す (決定144)。
func soundsUnsure(text string) bool {
	return hasAnyForm(text, unsureForms)
}

// morseOf は文字のモールス信号 (資料1の表)。
func morseOf(letter byte) string {
	for _, r := range morseSheetRows {
		if r.letter[0] == letter {
			return r.morse
		}
	}
	return "?"
}

// spellingDistance は2つの綴りの編集距離 (文字単位。符号の・や－は複数バイトなので
// バイト単位で測ると距離が水増しされる)。
func spellingDistance(sa, sb string) int {
	a, b := []rune(sa), []rune(sb)
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// morseReportNote は解読の報告を表と照合した結果を、ナビへの指示として返す。
// 綴りらしきものが無ければ空。
func morseReportNote(text string) string {
	var lines []string
	for _, sp := range reportedSpellings(text) {
		inTable := false
		for _, r := range morseSheetRows {
			if r.word == sp {
				inTable = true
			}
		}
		if inTable {
			var code []string
			for i := 0; i < len(sp); i++ {
				code = append(code, morseOf(sp[i]))
			}
			// 表にあっても正解とは限らない。推測で挙げた単語 (「MIKEでしょうか?」) に
			// 「その行の色を確認して切って」と進めると、読んだ長短と合わないまま切って
			// 爆発する (実測: 「一致しました」「切るといい」)。符号と照らしてからにする
			// ただし確認を重ねると、正しく解読した人まで「見比べて」を繰り返されて
			// ゴールに進めない (実運用: INDIA に確認を3往復)。表にある単語は
			// その行の色の線を切るまで伝え、当て推量のときだけ確認を1回挟む (決定144)
			// 当て推量かどうかはナビに見分けさせると外れる (「MIKEでしょうか?」に
			// 5回中3回「切れ」)。迷いの言葉の有無でサーバーが決める
			next := "**この単語と同じ行にある色の線を切る**と、次にやることまで伝えます。"
			if soundsUnsure(text) {
				next = "プレイヤーは**確信が無い様子**です (当て推量の可能性)。先に長短がこの符号と" +
					"合うかを1回確かめてもらい、**合っていればこの単語と同じ行にある色の線を切る**、" +
					"と続けて伝えます。確かめる前に切らせません。"
			}
			lines = append(lines, fmt.Sprintf("- 「%s」は表に**載っています** (綴りの符号: %s)。%s"+
				"正解かどうかは分からないので、一致した・合っている、とは言いません。",
				sp, strings.Join(code, " / "), next))
			continue
		}
		type cand struct {
			word string
			dist int
		}
		var cands []cand
		for _, r := range morseSheetRows {
			if d := spellingDistance(sp, r.word); d <= 2 {
				cands = append(cands, cand{r.word, d})
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
		if len(cands) > 2 {
			cands = cands[:2]
		}
		line := fmt.Sprintf("- 「%s」は表に**載っていません**。", sp)
		// 区切り違い: 長短をつなげると同じ並びになる表の単語 (ZC と MIKE は
		// どちらも －－・・－・－・)。文字の区切り (少し長い消灯) の見落としで起きる。
		// 綴りの近さより強い手がかりなので先に渡す (決定146)
		stream := morseStream(sp)
		var regrouped []string
		for _, r := range morseSheetRows {
			if r.word != sp && morseStream(r.word) == stream {
				regrouped = append(regrouped, r.word+" ("+spellMorse(r.word)+")")
			}
		}
		if len(regrouped) > 0 {
			// 一般論 (「区切りを見落とすな」) だけでは何をすればいいか分からない
			// (実運用: ETLFET)。どこをつなげる/分けるかを具体的に渡す (決定147)
			var fixes []string
			for _, r := range morseSheetRows {
				if r.word != sp && morseStream(r.word) == stream {
					fixes = append(fixes, r.word+": "+regroupNote(sp, r.word))
				}
			}
			line += fmt.Sprintf("**長短をつなげると %s で、区切り方を変えると表の %s と同じ並びになります**"+
				" (報告の区切り: %s)。文字の区切りを見落としている可能性が高い。\n"+
				"  区切りの違い: %s\n"+
				"  **プレイヤーには、候補の単語と、どこをつなげる (分ける) とそうなるかを具体的に"+
				"伝えてください** (何文字目と何文字目のあいだの消灯が短いか、長いか)。"+
				"一般論 (区切りを見落とすな・見直して) だけで終わらせない。",
				stream, strings.Join(regrouped, "、"), spellMorse(sp), strings.Join(fixes, " / "))
		}
		if len(regrouped) == 0 && len(cands) > 0 {
			var parts []string
			for _, c := range cands {
				parts = append(parts, c.word+diffNote(sp, c.word))
			}
			line += "綴りの近い表の単語: " + strings.Join(parts, "、") + "。"
		}
		// 綴りでも区切りでも近い単語が無いときは、長短の並びで近い単語を探す。
		// HOTEL (・・・・－－－－・・－・・) を EEEETEETEE (・・・・－・・－・・) と読んだ
		// 例では、長い光の連続を1回に数えていた。回数の違いを具体的に渡す (決定148)
		if len(regrouped) == 0 && len(cands) == 0 {
			if near := nearByElements(sp); near != "" {
				line += near
			}
		}
		// 近い単語が無い・表の単語 (どれも4文字以上) より短い・区切り違いがある、
		// のどれかなら、読み方を基礎から教える。「長短を見直して」を繰り返すだけでは、
		// 何を見直すのか分からず、プレイヤーを怒らせた (実運用: ZC)
		// 区切り違いの候補があるときは具体的な手がかりの方が効く。一般的な読み方の
		// 手順は、手がかりが無いときだけ渡す (両方渡すと一般論が前に出た。決定147)
		if len(regrouped) == 0 && (len(cands) == 0 || len(sp) < 4) {
			line += morseBasicsGuide
		}
		lines = append(lines, line)
	}
	if note := elementReportNote(text); note != "" {
		lines = append(lines, note)
	}
	// 読み方が分からない様子なら、初心者向けの順でコツを渡す (決定149)
	if noviceReaderPattern.MatchString(text) {
		lines = append(lines, morseNoviceMark)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// elementGroupPattern は長短の並びで報告された1文字ぶんのまとまり (「長短短」「長・短・短」)。
// **前後が区切り (読点・空白・文頭文末など) のものだけ**を拾う。「長く光ったり短く光ったり」の
// 「長」「短」を T・E と取り違えた (決定149)。
var elementGroupPattern = regexp.MustCompile(`(?:^|[はが:：])([長短](?:・?[長短])*)(?:です|でした|で|と)?$`)

// elementSeparators で発話を区切ってから、区切りごとに elementGroupPattern を当てる
// (正規表現1本で拾うと、あいだの読点を前のまとまりが使ってしまい次を拾えない)。
var elementSeparators = regexp.MustCompile(`[、,，。\s/「」!?！？]+`)

// elementReportNote は、長短の並びでの報告を表の符号で文字に直す (決定146)。
// 生成AIにモールスを変換させると誤る (符号の書き間違いが出た) ため、サーバーが直す。
func elementReportNote(text string) string {
	var groups []string
	for _, token := range elementSeparators.Split(text, -1) {
		if m := elementGroupPattern.FindStringSubmatch(token); m != nil {
			groups = append(groups, m[1])
		}
	}
	if len(groups) == 0 {
		return ""
	}
	var parts []string
	var letters strings.Builder
	for _, g := range groups {
		code := strings.NewReplacer("・", "", "長", "－", "短", "・").Replace(g)
		letter := ""
		for _, r := range morseSheetRows {
			if r.morse == code {
				letter = r.letter
			}
		}
		if letter == "" {
			if len([]rune(code)) > 4 {
				parts = append(parts, g+"→(5個以上。2文字以上がつながっている)")
			} else {
				parts = append(parts, g+"→(表に無い並び)")
			}
			letters.WriteString("?")
			continue
		}
		parts = append(parts, g+"→"+letter)
		letters.WriteString(letter)
	}
	note := "- 報告された長短を表の符号で文字にすると: " + strings.Join(parts, "、") + "。"
	joined := letters.String()
	for _, r := range morseSheetRows {
		if r.word == joined {
			note += fmt.Sprintf("並べると「%s」で、表に載っています。", joined)
		}
	}
	return note + "この結果をプレイヤーに伝えて、次の文字の報告を促すか、単語になったら表で探させます。"
}

// diffNote は同じ長さの綴りで、違う文字とその符号を示す (「1文字目 M(－－)→T(－)」)。
func diffNote(from, to string) string {
	if len(from) != len(to) {
		return ""
	}
	var diffs []string
	for i := 0; i < len(from); i++ {
		if from[i] != to[i] {
			diffs = append(diffs, fmt.Sprintf("%d文字目 %c(%s)→%c(%s)",
				i+1, from[i], morseOf(from[i]), to[i], morseOf(to[i])))
		}
	}
	if len(diffs) == 0 {
		return ""
	}
	return " (" + strings.Join(diffs, "・") + ")"
}

// morseStream は綴りの符号を区切りなしでつなげる (「MIKE」→「－－・・－・－・」)。
func morseStream(word string) string {
	var b strings.Builder
	for i := 0; i < len(word); i++ {
		b.WriteString(morseOf(word[i]))
	}
	return b.String()
}

// spellMorse は綴りの符号を1文字ずつ区切って並べる (「MIKE」→「－－ / ・・ / －・－ / ・」)。
func spellMorse(word string) string {
	var code []string
	for i := 0; i < len(word); i++ {
		code = append(code, morseOf(word[i]))
	}
	return strings.Join(code, " / ")
}

// morseBasicsGuide は、読み方を基礎から教えるときの指示 (決定146・決定148)。
// 手順と秒数は「モールスの読み方」(morseReadingGuide) にある。
const morseBasicsGuide = `
  **「モールスの読み方」の手順で、つまずいている所から丁寧に教えてください**
  (「区切りを確かめて」「長短を見直して」とだけ言わない。**見え方のたとえと数え方の工夫**
  (一呼吸おく・指を折って数える・紙に「/」を書く、など) で何を見ればいいかを言う。秒数は言わない。
  同じ否定を繰り返さない。プレイヤーを責めない)。表の単語はどれも4文字以上。`

// nearByElements は、長短の並びが近い表の単語と、長い光・短い光の回数の違いを示す。
func nearByElements(sp string) string {
	stream := morseStream(sp)
	type cand struct {
		word string
		dist int
	}
	var cands []cand
	for _, r := range morseSheetRows {
		if d := spellingDistance(stream, morseStream(r.word)); d <= 3 {
			cands = append(cands, cand{r.word, d})
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
	if len(cands) > 2 {
		cands = cands[:2]
	}
	count := func(st, mark string) int { return strings.Count(st, mark) }
	var parts []string
	for _, c := range cands {
		ws := morseStream(c.word)
		parts = append(parts, fmt.Sprintf("%s (%s。長い光%d回・短い光%d回、報告は長い光%d回・短い光%d回)",
			c.word, spellMorse(c.word), count(ws, "－"), count(ws, "・"), count(stream, "－"), count(stream, "・")))
	}
	note := fmt.Sprintf("長短をつなげた並び (%s) が近い表の単語: %s。", stream, strings.Join(parts, "、"))
	// E (・) と T (－) だけの綴りは、長短の1つ1つを別の文字にしている
	if strings.Trim(sp, "ET") == "" && len(sp) >= 4 {
		note += "**報告が E と T だけでできている**ので、光1回ずつを別々の文字にしている " +
			"(文字の区切りを全部の消灯で切っている) 可能性が高い。"
	}
	return note + "**違いを具体的に伝えてください** (長い光が続くところの回数、一瞬の消灯と少し長い消灯の違い)。"
}

// regroupNote は、報告の綴りと区切り違いの単語で、区切りが違う箇所だけを示す
// (「1〜2文字目 E(・)+T(－) → A(・－)」)。2つは長短をつなげると同じ並び。
func regroupNote(reported, word string) string {
	cut := func(w string) []int {
		var ends []int
		n := 0
		for i := 0; i < len(w); i++ {
			n += len([]rune(morseOf(w[i])))
			ends = append(ends, n)
		}
		return ends
	}
	re, we := cut(reported), cut(word)
	var parts []string
	ri, wi := 0, 0
	rStart, wStart := 0, 0
	for ri < len(re) && wi < len(we) {
		if re[ri] == we[wi] {
			if ri-rStart != 0 || wi-wStart != 0 {
				var from, to []string
				for k := rStart; k <= ri; k++ {
					from = append(from, fmt.Sprintf("%c(%s)", reported[k], morseOf(reported[k])))
				}
				for k := wStart; k <= wi; k++ {
					to = append(to, fmt.Sprintf("%c(%s)", word[k], morseOf(word[k])))
				}
				pos := fmt.Sprintf("%d文字目", rStart+1)
				if ri > rStart {
					pos = fmt.Sprintf("%d〜%d文字目", rStart+1, ri+1)
				}
				parts = append(parts, fmt.Sprintf("報告の%s %s → %s",
					pos, strings.Join(from, "+"), strings.Join(to, "+")))
			}
			ri, wi = ri+1, wi+1
			rStart, wStart = ri, wi
			continue
		}
		if re[ri] < we[wi] {
			ri++
		} else {
			wi++
		}
	}
	return strings.Join(parts, "、")
}

// morseTips は読み違いが続くときに1つずつ渡すコツ (決定148)。
// どのコツを伝えるかをナビに選ばせると、無難な一般論 (「区切りを見直して」) に寄り、
// 同じ返しも繰り返した。サーバーが読み違いの種類と回数から1つ選ぶ。
var morseTips = []string{
	"声に出して「トン」「ツー」と言いながら見る。「パッ」と一瞬ならトン、「パーッ」とはっきり長ければツー",
	"光と光のあいだで**一呼吸おいたら**、そこで1文字が終わり。そのたびに紙に「/」を書いて区切る。" +
		"ほとんど続けて光るあいだは同じ文字",
	"長い光が続くところは、光るたびに**指を折って**何回光ったか数える (あいだで一瞬だけ消える)",
	"しばらく消えたままになり「終わったかな」と思ったあとが単語の頭。そこから1周ぶんを書き取り、" +
		"2周目で見直す (同じ単語を何度も繰り返している)",
	"最初の1文字だけ、長短を「長・短・短」のように教えてもらい、一緒に読む",
}

// morseTipFor は、照合結果と表に無い報告が続いた回数から、今回伝えるコツを選ぶ。
// E と T だけの綴り (光1回ずつを別の文字にしている) は区切りのコツから、
// 長短の並びが近い単語があるとき (長い光の回数違い) は数え方のコツから始める。
func morseTipFor(note string, misses int) string {
	base := 0
	if strings.Contains(note, "E と T だけ") || strings.Contains(note, "区切りの違い") {
		base = 1
	} else if strings.Contains(note, "長短をつなげた並び") {
		base = 2
	}
	step := misses - 1
	if step < 0 {
		step = 0
	}
	return morseTips[(base+step)%len(morseTips)]
}

// noviceReaderPattern は、モールスの読み方が分からない様子を示す言い方。
var noviceReaderPattern = regexp.MustCompile(`分から|分かりませ|わから|わかりませ|知らな|知りませ|読めな|読めませ|自信がな|初めて|はじめて`)

// morseNoviceMark は照合結果に添える印。プロンプトはこれを見て初心者向けのコツを渡す。
const morseNoviceMark = "- プレイヤーは**モールスの読み方が分からない様子**です。" +
	"「モールスの読み方」を最初のステップから、下のコツを1つずつ教えます。"

// morseNoviceOrder は、読み方が分からない人に教える順 (morseTips の添字)。
// 単語の頭の見つけ方 → トン・ツー → 一呼吸で区切る → 指を折って数える → 1文字だけ一緒に読む。
var morseNoviceOrder = []int{3, 0, 1, 2, 4}

// morseNoviceTip は、読み方が分からない人に lessons 回目に教えるコツ (1始まり)。
func morseNoviceTip(lessons int) string {
	if lessons < 1 {
		lessons = 1
	}
	return morseTips[morseNoviceOrder[(lessons-1)%len(morseNoviceOrder)]]
}

// ---- 202: 英字1文字の報告を照合する (決定152) ----

// morseLetterReportNote は、正解が英字1文字の課題 (202) で、報告を表と照合する。
// 1文字の報告はその文字の行へ導き、2文字以上なら区切りの取り違えとして、
// 同じ長短の並びになる1文字 (ET → A) を探して渡す。正解の文字は使わない。
func morseLetterReportNote(text string) string {
	var lines []string
	for _, sp := range reportedSpellingsMin(text, 1) {
		// フォネティックコードの呼び名は頭文字の1文字として受け取る
		if isMorseSheetWord(sp) {
			sp = sp[:1]
		}
		if len(sp) == 1 {
			next := "**この文字の行に書かれている色の線を切る**と、次にやることまで伝えます。"
			if soundsUnsure(text) {
				next = "プレイヤーは**確信が無い様子**です。先に長短がこの符号と合うかを1回確かめてもらい、" +
					"**合っていればこの文字の行に書かれている色の線を切る**、と続けて伝えます。"
			}
			lines = append(lines, fmt.Sprintf("- 文字「%s」(符号: %s) は表にあります。%s"+
				"正解かどうかは分からないので、一致した・合っている、とは言いません。",
				sp, morseOf(sp[0]), next))
			continue
		}
		stream := morseStream(sp)
		line := fmt.Sprintf("- 「%s」と2文字以上に読めています。**表示は1文字**なので、"+
			"文字の区切りと、1文字の中の一瞬の消灯を取り違えている可能性が高い"+
			" (報告の長短: %s)。", sp, spellMorse(sp))
		found := ""
		for _, r := range morseSheetRows {
			if r.morse == stream {
				found = r.letter
			}
		}
		if found != "" {
			line += fmt.Sprintf("**つなげると %s で、文字「%s」と同じ並びです** (区切りの違い: %s)。"+
				"つなげると1文字になることを具体的に伝えてください。",
				stream, found, regroupNote(sp, found))
		} else {
			line += "つなげた長短 (" + stream + ") に合う1文字はありません。" +
				"**しばらく消えたあとから、1回ぶんの長短だけ**を報告してもらいます。"
		}
		lines = append(lines, line)
	}
	if note := elementLetterNote(text); note != "" {
		lines = append(lines, note)
	}
	if noviceReaderPattern.MatchString(text) {
		lines = append(lines, morseNoviceMark)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// elementLetterNote は、1文字の課題で長短の並びの報告を1文字に直す。
// 分けて報告された長短 (「長短、長」) も、表示は1文字なのでつなげて読む。
func elementLetterNote(text string) string {
	var groups []string
	for _, token := range elementSeparators.Split(text, -1) {
		if m := elementGroupPattern.FindStringSubmatch(token); m != nil {
			groups = append(groups, m[1])
		}
	}
	if len(groups) == 0 {
		return ""
	}
	code := strings.NewReplacer("・", "", "長", "－", "短", "・").Replace(strings.Join(groups, ""))
	letter := ""
	for _, r := range morseSheetRows {
		if r.morse == code {
			letter = r.letter
		}
	}
	reported := strings.Join(groups, "、")
	if letter == "" {
		return fmt.Sprintf("- 報告された長短 (%s) をつなげた %s に合う1文字は表にありません。"+
			"しばらく消えたあとから、1回ぶんの長短だけを報告してもらいます。", reported, code)
	}
	note := fmt.Sprintf("- 報告された長短 (%s) は、表の符号で文字「%s」(%s) です。", reported, letter, code)
	if len(groups) > 1 {
		note += "表示は1文字なので、分けて報告された長短はつなげて1文字として読みます。"
	}
	return note + "**この文字の行に書かれている色の線を切る**と伝えます。"
}

// isMorseSheetWord は、表のフォネティックコードの単語かを返す。
func isMorseSheetWord(sp string) bool {
	for _, r := range morseSheetRows {
		if r.word == sp {
			return true
		}
	}
	return false
}

// stageSecretWord は課題の伏せる語 (モールスの正解)。無ければ空。
func stageSecretWord(stage *BuiltStage) string {
	if stage == nil {
		return ""
	}
	return stage.Navigator["secret_word"]
}

// morseNoteForStage は、課題に合った照合結果を返す。正解の語を伏せていない課題では空。
// 202 は英字1文字 (morseLetterReportNote)、302 はフォネティックコードの単語 (morseReportNote)。
func morseNoteForStage(stage *BuiltStage, text string) string {
	word := stageSecretWord(stage)
	if word == "" {
		return ""
	}
	if len(word) == 1 {
		return morseLetterReportNote(text)
	}
	return morseReportNote(text)
}
