package main

// シナリオテンプレートの抽選定義 (`[random]`) を解決する。
// 記法の一覧は docs/scenario_design.md §3.1。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// resolveOneVar は1つの抽選定義を解決する。
func (b *ScenarioBuilder) resolveOneVar(name string, def map[string]any, vars map[string]string, usedLines map[string]bool) (string, error) {
	// derive: 他の変数から機械的に導出する (抽選ではない)
	if deriveKind, ok := def["derive"].(string); ok {
		return b.deriveVar(deriveKind, def, vars)
	}

	pickKind, _ := def["pick"].(string)

	// exclude: 除外する色のリスト (${...} 参照を含みうる)
	excluded := make(map[string]bool)
	if raw, ok := def["exclude"].([]any); ok {
		for _, item := range raw {
			text, ok := item.(string)
			if !ok {
				continue
			}
			expanded, err := expandString(text, vars)
			if err != nil {
				return "", err
			}
			for _, color := range strings.Split(expanded, ",") {
				excluded[strings.TrimSpace(color)] = true
			}
		}
	}

	switch pickKind {
	case "line":
		// 切断線: ステージ間で重複しないよう、既に使われた色を除く
		candidates := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if !usedLines[color] && !excluded[color] {
				candidates = append(candidates, color)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no line available (all %d colors used)", len(allColors))
		}
		return candidates[b.rng.Intn(len(candidates))], nil

	case "color":
		// 任意の色 (切断線の重複制約は受けない)
		candidates := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if !excluded[color] {
				candidates = append(candidates, color)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no color available")
		}
		return candidates[b.rng.Intn(len(candidates))], nil

	case "colors":
		// 複数色をまとめて選ぶ (カンマ区切りで返す)
		countText, err := expandAny(def["count"], vars)
		if err != nil {
			return "", err
		}
		count, err := strconv.Atoi(countText)
		if err != nil {
			return "", fmt.Errorf("colors.count is not a number: %q", countText)
		}
		candidates := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if !excluded[color] {
				candidates = append(candidates, color)
			}
		}
		if len(candidates) < count {
			return "", fmt.Errorf("colors.count %d exceeds available %d", count, len(candidates))
		}
		b.rng.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		picked := candidates[:count]
		sort.Strings(picked)
		return strings.Join(picked, ","), nil

	case "color_seq":
		// **順番に意味がある色の列**を引く (201 復唱 のボタン列)。
		//
		// `colors` との違い:
		//   colors    → 互いに異なる色を選び、**ソートして**返す (表示用)
		//   color_seq → **重複を許し、引いた順のまま**返す (押す順)
		//
		// 「赤赤黄黄赤」のような繰り返しはこのステージの持ち味なので、
		// 重複を避けてはいけない。長さは難易度から引く (ADR S-11)。
		return b.resolveColorSeq(def, vars, excluded)

	case "int":
		minText, err := expandAny(def["min"], vars)
		if err != nil {
			return "", err
		}
		maxText, err := expandAny(def["max"], vars)
		if err != nil {
			return "", err
		}
		min, err1 := strconv.Atoi(minText)
		max, err2 := strconv.Atoi(maxText)
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("int.min/max are not numbers: %q %q", minText, maxText)
		}
		if max < min {
			return "", fmt.Errorf("int.max (%d) < int.min (%d)", max, min)
		}

		// exclude された数値は避ける
		candidates := make([]int, 0, max-min+1)
		for v := min; v <= max; v++ {
			if !excluded[strconv.Itoa(v)] {
				candidates = append(candidates, v)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no int available in [%d,%d]", min, max)
		}
		return strconv.Itoa(candidates[b.rng.Intn(len(candidates))]), nil

	case "choice":
		candidates, err := expandStringList(def["candidates"], vars)
		if err != nil {
			return "", err
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("choice.candidates is empty")
		}

		// as_line = true の場合は切断線として扱い、他ステージで使用済みの色を除く。
		// 5色すべてを表示に使うステージ(208)で cut を選ぶ用途を想定している。
		// (通常の choice は表示や分岐の選択に使うため、線の重複制約は受けない)
		if asLine, _ := def["as_line"].(bool); asLine {
			available := make([]string, 0, len(candidates))
			for _, c := range candidates {
				if !usedLines[c] && !excluded[c] {
					available = append(available, c)
				}
			}
			if len(available) == 0 {
				return "", fmt.Errorf("choice(as_line): no line available (all候補が使用済み)")
			}
			candidates = available
		}

		return candidates[b.rng.Intn(len(candidates))], nil

	case "noise_leds":
		// 妨害用のLED表示を残りの色へ割り当てる (203難版など)。
		// 「点灯しっぱなし / 消灯しっぱなし / 対称blink」の3種から選ぶ。
		//
		// blink は**点灯時間と消灯時間を必ず等しく**する。モールスは短点=1単位・
		// 長点=3単位の非対称なリズムなので、対称にしておけば「モールスではない」と
		// 見分けられる (見分け自体を謎解きにはしない。難度は情報量で上げる)。
		return b.resolveNoiseLeds(def, vars, excluded)

	case "romaji_word":
		// 色名のローマ字表記を選ぶ。表記がそのまま色に対応する (305)
		return b.pickWordByColor(def, vars, usedLines, excluded, romajiColor, "romaji color word")

	case "morse_word":
		// NATOフォネティックコードの語を選ぶ。頭文字が対照表で色に対応する (203)
		return b.pickWordByColor(def, vars, usedLines, excluded, morseWordColor, "morse word")

	case "codebook":
		// 301 LED照合: cut になる表示を対照表から1通り選ぶ (docs/printed_materials.md §4)。
		//
		// **cut から逆引きする**。表示を先に抽選すると cut が表側で決まってしまい、
		// 他ステージとの色の重複 (usedLines) を避けられない (ADR S-1・S-2)。
		return b.resolveCodebook(def, vars)

	case "panel":
		// 209 配電盤照合: 現在位置を抽選し "位置,危険,解除" を返す。
		// 各値は `derive = "panel_field"` で取り出す。
		return b.resolvePanel(def, vars)

	case "rotary_leds":
		// 209 配電盤照合: ロータリー位置ごとのLED表示を組み立てる。
		// **解除位置の表示は対照表に載っていない** — 回して初めて分かる。
		return b.resolveRotaryLeds(def, vars)

	case "rotary_layout":
		// 206 綱渡り: 目的位置と禁止位置の**配置ごと**抽選する。
		//
		// 禁止位置が2つになると「どこに置くか」で難しさの質が変わるため、
		// 位置を個別に引かず**配置として**決める (ADR S-10)。
		// 返り値は "target,f1[,f2]" 形式で、`nth` で各値を取り出す。
		return b.resolveRotaryLayout(def, vars)

	case "morse_letters":
		// 5色へ割り当てる**互いに異なる1文字**を選ぶ。
		// **現在の利用ステージは無い**(実装は残してある)。
		//
		// **符号の要素数がばらけるように**選ぶ。同じ要素数ばかりだと
		// 長短を数え上げる作業になり、モールスの数字 (全て5要素) と
		// 同じ見分けにくさが戻る (ADR N-41)。
		return b.resolveMorseLetters(def, vars)

	default:
		return "", fmt.Errorf("unknown pick kind: %q", pickKind)
	}
}

// deriveRankSlot は cut を rank 番目に差し込んだ並びの slot 番目を返す。
//
// 205 速さくらべが「N番目に速い色」を正解にするために使う。
// 速度そのものは [core] 側で s1〜s4 に固定値を割り当てるため、
// ここでは**並び順だけ**を決める。
//
// rank / slot はいずれも 1 始まり。並びの長さは len(others)+1 になる。
func (b *ScenarioBuilder) deriveRankSlot(def map[string]any, vars map[string]string) (string, error) {
	cut, err := expandAny(def["cut"], vars)
	if err != nil {
		return "", fmt.Errorf("rank_slot.cut: %w", err)
	}
	others, err := expandStringList(def["others"], vars)
	if err != nil {
		return "", fmt.Errorf("rank_slot.others: %w", err)
	}

	rank, err := rankSlotIndex(def["rank"], vars, "rank")
	if err != nil {
		return "", err
	}
	slot, err := rankSlotIndex(def["slot"], vars, "slot")
	if err != nil {
		return "", err
	}

	size := len(others) + 1
	if rank < 1 || rank > size {
		return "", fmt.Errorf("rank_slot.rank (%d) is out of range [1,%d]", rank, size)
	}
	if slot < 1 || slot > size {
		return "", fmt.Errorf("rank_slot.slot (%d) is out of range [1,%d]", slot, size)
	}

	// cut を rank 番目へ置き、残りを others の順で前から詰める。
	order := make([]string, 0, size)
	next := 0
	for i := 1; i <= size; i++ {
		if i == rank {
			order = append(order, cut)
			continue
		}
		order = append(order, others[next])
		next++
	}
	return order[slot-1], nil
}

// rankSlotIndex は rank_slot の rank / slot を整数として読む。
// TOML の整数でも "${rank}" のような参照でも書けるようにする。
func rankSlotIndex(value any, vars map[string]string, name string) (int, error) {
	text, err := expandAny(value, vars)
	if err != nil {
		return 0, fmt.Errorf("rank_slot.%s: %w", name, err)
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("rank_slot.%s is not a number: %q", name, text)
	}
	return n, nil
}

// deriveVar は他の変数から値を機械的に導出する。
func (b *ScenarioBuilder) deriveVar(kind string, def map[string]any, vars map[string]string) (string, error) {
	switch kind {
	case "morse_word_color":
		// 語の頭文字 → モールス対照表の色 (09. 暗号電文)
		word, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		color := morseWordColor(word)
		if color == "" {
			return "", fmt.Errorf("cannot map morse word %q to a color", word)
		}
		return color, nil

	case "romaji_color":
		// 色名のローマ字表記 → 色 (305 ローマ字電文)
		word, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		color := romajiColor(word)
		if color == "" {
			return "", fmt.Errorf("unknown romaji color name: %q", word)
		}
		return color, nil

	case "rank_slot":
		// 順位付きの並びを組み立てる (205 速さくらべ)。
		//
		// cut を rank 番目に置き、残りを others の順で前から詰めた並びの
		// slot 番目を返す。「N番目に速い色を切れ」という課題で、
		// **どの順位を正解にするかを毎回抽選する**ために使う。
		//
		//   cut=D rank=3 others=[A,B,C] のとき
		//     slot 1 → A / slot 2 → B / slot 3 → D(cut) / slot 4 → C
		return b.deriveRankSlot(def, vars)

	case "choice_value":
		// choice の抽選結果から対応する値を引く。
		//
		//   odd     = { pick = "choice", candidates = ["fast", "slow"] }
		//   odd_ms  = { derive = "choice_value", from = "${odd}", fast = "300", slow = "650" }
		//   rest_ms = { derive = "choice_value", from = "${odd}", fast = "650", slow = "300" }
		//
		// **抽選結果に応じて変わる値を別々に人手で書かない** (ADR S-1)。
		// 207 なら片方だけ直し忘れると「5色とも同じ速さ」になり、
		// 謎が成立しないまま組み立てが通ってしまう。
		return b.deriveChoiceValue(def, vars)

	case "panel_field":
		// 209 配電盤照合: 選ばれた行から1項目を取り出す。
		// `field` は position / forbidden / release / lit / blink / appearance。
		return b.derivePanelField(def, vars)

	case "codebook_field":
		// 301 LED照合: 選ばれた表示 (pick = "codebook") から1項目を取り出す。
		//
		// `field` に "lit" (点灯色) / "dark" (消灯色) / "word" (キーワード) /
		// "rotary" (ダイヤル位置) / "pattern" (4桁の2進表記) を指定する。
		return b.deriveCodebookField(def, vars)

	case "terminal_for_color":
		// 配線色 → 資料3の端子番号 (203 ブループリント)。
		//
		// `series` に "x" / "y" を指定する。ナビゲーターは**両系統を並べて**
		// 伝え、どちらを使うかはプレイヤーがシリアル銘板の下1桁 (奇数=X /
		// 偶数=Y) を見て選ぶ (ADR D-6)。サーバーは個体のシリアルを知らない。
		color, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		series, err := expandAny(def["series"], vars)
		if err != nil {
			return "", fmt.Errorf("terminal_for_color.series: %w", err)
		}
		series = strings.ToLower(strings.TrimSpace(series))
		if series != "x" && series != "y" {
			return "", fmt.Errorf("terminal_for_color.series は \"x\" か \"y\": %q", series)
		}
		terminal := b.sheet.TerminalForColor(series, color)
		if terminal == "" {
			return "", fmt.Errorf(
				"no terminal mapped to color %q (check [mission_sheet.terminal_map_%s])",
				color, series)
		}
		return terminal, nil

	case "spoken_letter":
		// 1文字を**無線で読み上げる形**にする (302)。
		//
		// 探すものを**指定する**側なので曖昧さを残せない。
		// フォネティックだけだと資料1のフォネティック列を引き直す手間が増え、
		// 文字名だけだと無線で聞き取りにくい。**両方を並べる** (ADR N-41)。
		//   "G" → 「ジー、ゴルフ」
		letter, err := expandAny(def["from"], vars)
		if err != nil {
			return "", fmt.Errorf("spoken_letter.from: %w", err)
		}
		spoken, ok := spokenLetterJA[strings.ToUpper(strings.TrimSpace(letter))]
		if !ok {
			return "", fmt.Errorf("spoken_letter: 読み方が未定義の文字 %q", letter)
		}
		return spoken, nil

	case "tail":
		// カンマ区切りの値から index 番目 (1始まり) 以降を**まとめて**返す。
		//
		// 206 綱渡り の禁止位置に使う。数が難易度で変わる (1個 or 2個) ため、
		// `nth` で1つずつ取り出すと**count=1 のときに2つ目が範囲外**になる。
		// 残り全部を返せば `positions = ["${forbidden}"]` がそのまま
		// 1要素にも2要素にも展開される (expandValue がカンマを展開する)。
		list, err := expandAny(def["from"], vars)
		if err != nil {
			return "", fmt.Errorf("tail.from: %w", err)
		}
		idxText, err := expandAny(def["index"], vars)
		if err != nil {
			return "", fmt.Errorf("tail.index: %w", err)
		}
		idx, err := strconv.Atoi(idxText)
		if err != nil {
			return "", fmt.Errorf("tail.index is not a number: %q", idxText)
		}
		items := strings.Split(list, ",")
		if idx < 1 || idx > len(items) {
			return "", fmt.Errorf("tail.index %d is out of range [1,%d]", idx, len(items))
		}
		rest := make([]string, 0, len(items)-idx+1)
		for _, item := range items[idx-1:] {
			rest = append(rest, strings.TrimSpace(item))
		}
		return strings.Join(rest, ","), nil

	case "nth":
		// カンマ区切りの値から N 番目 (1始まり) を取り出す。
		// morse_letters がまとめて選んだ文字を各色へ配るのに使う。
		list, err := expandAny(def["from"], vars)
		if err != nil {
			return "", fmt.Errorf("nth.from: %w", err)
		}
		idxText, err := expandAny(def["index"], vars)
		if err != nil {
			return "", fmt.Errorf("nth.index: %w", err)
		}
		idx, err := strconv.Atoi(idxText)
		if err != nil {
			return "", fmt.Errorf("nth.index is not a number: %q", idxText)
		}
		items := strings.Split(list, ",")
		if idx < 1 || idx > len(items) {
			return "", fmt.Errorf("nth.index %d is out of range [1,%d]", idx, len(items))
		}
		return strings.TrimSpace(items[idx-1]), nil

	default:
		return "", fmt.Errorf("unknown derive kind: %q", kind)
	}
}

// romajiColorNames は色名のローマ字表記 → 色記号。
// モールスで直接表示するため、A-Z のみで書ける表記にする (§6.1)。
var romajiColorNames = map[string]string{
	"AKA":    "A", // 赤
	"KI":     "B", // 黄
	"MIDORI": "C", // 緑
	"AO":     "D", // 青
	"SIRO":   "E", // 白
}

// romajiColor は色名のローマ字表記から色記号を引く。
func romajiColor(word string) string {
	return romajiColorNames[strings.ToUpper(word)]
}

// morseWordColor は語の頭文字からモールス対照表の色を引く。
//
// 対照表は A-Z の各文字に5色を循環割当した固定印刷
// (docs/puzzle_stage_ideas.md §6): A=A(赤), B=B(黄), C=C(緑), D=D(青), E=E(白),
// F=A(赤), G=B(黄), ... となる。
func morseWordColor(word string) string {
	if word == "" {
		return ""
	}
	head := strings.ToUpper(word)[0]
	if head < 'A' || head > 'Z' {
		return ""
	}
	return allColors[int(head-'A')%len(allColors)]
}

// spokenLetterJA は1文字を無線で読み上げる形。**文字名 + NATOフォネティック**。
//
// モールスで探す文字を指定するのに使う。片方だけだと伝わらないため
// 機械的に両方を並べる (ADR N-41)。morseLetterCodes の全文字を網羅すること。
var spokenLetterJA = map[string]string{
	"A": "エー、アルファ", "B": "ビー、ブラボー", "C": "シー、チャーリー",
	"E": "イー、エコー", "G": "ジー、ゴルフ", "H": "エイチ、ホテル",
	"I": "アイ、インディア", "K": "ケー、キロ", "M": "エム、マイク",
	"N": "エヌ、ノベンバー", "O": "オー、オスカー", "R": "アール、ロメオ",
	"S": "エス、シエラ", "T": "ティー、タンゴ", "X": "エックス、エックスレイ",
	"Z": "ゼット、ズールー",
}

// morseLetterCodes は1文字の符号。要素数で選び分けるために持つ。
//
// 要素数がばらけるよう、**1〜4要素から均等に**候補を用意してある。
// 数字 (0-9) は全て5要素で見分けにくいため使わない (ADR N-41)。
var morseLetterCodes = map[string]string{
	"E": ".", "T": "-", // 1要素
	"I": "..", "M": "--", "A": ".-", "N": "-.", // 2要素
	"S": "...", "O": "---", "G": "--.", "R": ".-.", "K": "-.-", // 3要素
	"H": "....", "X": "-..-", "Z": "--..", "B": "-...", "C": "-.-.", // 4要素
}

// resolveMorseLetters は互いに異なる1文字を count 個選び、カンマ区切りで返す。
//
// **符号の要素数がばらけるように**選ぶ: 要素数ごとにグループへ分け、
// 短いものから順に1つずつ拾う。同じ要素数ばかりだと長短を数え上げる作業になり、
// 「形で見分けられる」という 302 の狙いが消える (ADR N-41)。
func (b *ScenarioBuilder) resolveMorseLetters(def map[string]any, vars map[string]string) (string, error) {
	countText, err := expandAny(def["count"], vars)
	if err != nil {
		return "", fmt.Errorf("morse_letters.count: %w", err)
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return "", fmt.Errorf("morse_letters.count is not a number: %q", countText)
	}
	if count < 1 {
		return "", fmt.Errorf("morse_letters.count must be >= 1: %d", count)
	}

	// 要素数ごとにグループ化する (map の反復順は非決定的なのでソートして安定させる)
	byLen := map[int][]string{}
	for letter, code := range morseLetterCodes {
		byLen[len(code)] = append(byLen[len(code)], letter)
	}
	lengths := make([]int, 0, len(byLen))
	for n := range byLen {
		sort.Strings(byLen[n])
		b.rng.Shuffle(len(byLen[n]), func(i, j int) {
			byLen[n][i], byLen[n][j] = byLen[n][j], byLen[n][i]
		})
		lengths = append(lengths, n)
	}
	sort.Ints(lengths)

	if count > len(morseLetterCodes) {
		return "", fmt.Errorf("morse_letters.count %d exceeds available %d",
			count, len(morseLetterCodes))
	}

	// 要素数グループを順に回り、1つずつ拾う (ラウンドロビン)。
	// これで要素数が最大限ばらける。
	picked := make([]string, 0, count)
	for round := 0; len(picked) < count; round++ {
		progressed := false
		for _, n := range lengths {
			if round >= len(byLen[n]) {
				continue
			}
			picked = append(picked, byLen[n][round])
			progressed = true
			if len(picked) == count {
				break
			}
		}
		if !progressed {
			return "", fmt.Errorf("morse_letters: 候補が足りない (count=%d)", count)
		}
	}
	return strings.Join(picked, ","), nil
}

// noiseLedsPrefix は noise_leds の展開結果であることを示す目印。
// expandCore がこの接頭辞を見て leds テーブルへマージする。
const noiseLedsPrefix = "\x00noise\x00"

// resolveNoiseLeds は妨害用LEDの割り当てを決め、JSON化した文字列として返す。
//
// 変数は文字列しか持てないため、生成結果をJSONへ畳んで受け渡す。
// [core] 側で noise_leds = "${noise}" と書くと leds へマージされる。
func (b *ScenarioBuilder) resolveNoiseLeds(def map[string]any, vars map[string]string, excluded map[string]bool) (string, error) {
	// 点滅速度の範囲 (省略時 200-900ms)。点灯・消灯とも同じ値を使う
	minMS, err := intFromDef(def, "on_ms_min", 200, vars)
	if err != nil {
		return "", err
	}
	maxMS, err := intFromDef(def, "on_ms_max", 900, vars)
	if err != nil {
		return "", err
	}
	if maxMS < minMS {
		return "", fmt.Errorf("noise_leds: on_ms_max (%d) < on_ms_min (%d)", maxMS, minMS)
	}

	// 対象は exclude されていない全色 (通常は表示用LED以外の4色)
	targets := make([]string, 0, len(allColors))
	for _, color := range allColors {
		if !excluded[color] {
			targets = append(targets, color)
		}
	}
	if len(targets) == 0 {
		return "", fmt.Errorf("noise_leds: no color available")
	}

	assigned := make(map[string]any, len(targets))
	for _, color := range targets {
		switch b.rng.Intn(3) {
		case 0:
			assigned[color] = "on" // 点灯しっぱなし
		case 1:
			assigned[color] = "off" // 消灯しっぱなし
		default:
			// 対称blink: 点灯時間 == 消灯時間
			d := minMS + b.rng.Intn(maxMS-minMS+1)
			assigned[color] = map[string]any{
				"pattern": "blink", "on_ms": d, "off_ms": d,
			}
		}
	}

	encoded, err := json.Marshal(assigned)
	if err != nil {
		return "", fmt.Errorf("noise_leds: %w", err)
	}
	return noiseLedsPrefix + string(encoded), nil
}

// intFromDef は定義から整数値を取り出す。未指定なら fallback を返す。
func intFromDef(def map[string]any, key string, fallback int, vars map[string]string) (int, error) {
	raw, ok := def[key]
	if !ok {
		return fallback, nil
	}
	text, err := expandAny(raw, vars)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s is not a number: %q", key, text)
	}
	return value, nil
}

// pickWordByColor は「語 → 色」の対応を持つ候補から1つ選ぶ。
//
// 202 暗号電文 (頭文字→対照表の色) と 305 ローマ字電文 (色名表記→色) は
// どちらも「選んだ語がそのまま切断線の色を決める」構造なので、
// 色を導く関数だけを差し替えて共通化している。
//
// 既に切られた線 (usedLines) と exclude 指定の色に対応する語は候補から外す。
// これを忘れると切断線がステージ間で重複する。
func (b *ScenarioBuilder) pickWordByColor(
	def map[string]any, vars map[string]string,
	usedLines, excluded map[string]bool,
	colorOf func(string) string, label string,
) (string, error) {
	candidates, err := expandStringList(def["candidates"], vars)
	if err != nil {
		return "", err
	}

	available := make([]string, 0, len(candidates))
	for _, word := range candidates {
		color := colorOf(word)
		if color != "" && !usedLines[color] && !excluded[color] {
			available = append(available, word)
		}
	}
	if len(available) == 0 {
		return "", fmt.Errorf("no %s available (all mapped colors used)", label)
	}
	return available[b.rng.Intn(len(available))], nil
}

// resolveCodebook は cut になる表示を対照表から1通り選び、pattern を返す
// (301 LED照合。docs/printed_materials.md §4)。
//
// **cut から逆引きする**。表示を先に抽選して cut を導くと、
// 他ステージで使用済みの色 (usedLines) を避けられなくなる (ADR S-1・S-2)。
//
// 返り値は "0101" のような4桁の2進表記で、他の変数はこれを
// `derive = "codebook_field"` で読み直す。**表示そのものを変数にしない**のは、
// 点灯色・消灯色・キーワード・ダイヤル位置を別々に人手で書くと
// 食い違うため (ADR S-1)。
func (b *ScenarioBuilder) resolveCodebook(def map[string]any, vars map[string]string) (string, error) {
	cut, err := expandAny(def["cut"], vars)
	if err != nil {
		return "", fmt.Errorf("codebook.cut: %w", err)
	}
	cut = strings.TrimSpace(cut)

	matched := codebookEntriesForCut(cut)
	if len(matched) == 0 {
		return "", fmt.Errorf("codebook: 色 %q になる表示が対照表に無い", cut)
	}
	return matched[b.rng.Intn(len(matched))].patternText(), nil
}

// deriveCodebookField は選ばれた表示から1項目を取り出す (301 LED照合)。
func (b *ScenarioBuilder) deriveCodebookField(def map[string]any, vars map[string]string) (string, error) {
	patternText, err := expandAny(def["from"], vars)
	if err != nil {
		return "", fmt.Errorf("codebook_field.from: %w", err)
	}
	field, err := expandAny(def["field"], vars)
	if err != nil {
		return "", fmt.Errorf("codebook_field.field: %w", err)
	}

	entry, err := codebookEntryByPattern(patternText)
	if err != nil {
		return "", err
	}

	field = strings.TrimSpace(field)
	if field == "lit" {
		return entry.litColors(), nil
	}
	if field == "dark" {
		return entry.darkColors(), nil
	}
	if field == "word" {
		return entry.word, nil
	}
	if field == "rotary" {
		return strconv.Itoa(entry.rotary), nil
	}
	if field == "pattern" {
		return entry.patternText(), nil
	}
	return "", fmt.Errorf("codebook_field.field が不正: %q (lit/dark/word/rotary/pattern)", field)
}

// codebookEntryByPattern は4桁の2進表記から対照表の行を引く。
func codebookEntryByPattern(patternText string) (codebookEntry, error) {
	patternText = strings.TrimSpace(patternText)
	value, err := strconv.ParseInt(patternText, 2, 32)
	if err != nil {
		return codebookEntry{}, fmt.Errorf("codebook: pattern が2進表記ではない: %q", patternText)
	}
	for _, entry := range codebookTable {
		if entry.pattern == int(value) {
			return entry, nil
		}
	}
	return codebookEntry{}, fmt.Errorf("codebook: pattern %q が対照表に無い", patternText)
}

// deriveChoiceValue は choice の抽選結果をキーにして値を引く (207)。
//
// `from` が展開された文字列をそのままキーとして def から読む。
// 対応するキーが無ければエラーにする — 候補を増やしたのに値を
// 足し忘れた場合、**その候補が当たった回だけ**失敗するため
// (抽選次第でしか再現しない) 静かに通してはいけない。
func (b *ScenarioBuilder) deriveChoiceValue(def map[string]any, vars map[string]string) (string, error) {
	key, err := expandAny(def["from"], vars)
	if err != nil {
		return "", fmt.Errorf("choice_value.from: %w", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("choice_value.from が空")
	}

	// "derive" / "from" は指定そのものなので候補から除く
	if key == "derive" || key == "from" {
		return "", fmt.Errorf("choice_value: %q はキーに使えない", key)
	}
	raw, ok := def[key]
	if !ok {
		return "", fmt.Errorf("choice_value: 抽選結果 %q に対応する値が無い", key)
	}
	value, err := expandAny(raw, vars)
	if err != nil {
		return "", fmt.Errorf("choice_value.%s: %w", key, err)
	}
	return value, nil
}

// rotaryLayout は綱渡りのダイヤル配置 (206)。
type rotaryLayout struct {
	target    int
	forbidden []int
}

// resolveRotaryLayout は目的位置と禁止位置の配置を抽選する (206 綱渡り)。
//
// `count` に禁止位置の数を指定する (難易度テンプレートの `[load]` から引く)。
// 返り値は "target,f1[,f2]" のカンマ区切りで、ステージ定義は
// `derive = "nth"` で個別に取り出す。
//
// **位置を個別に抽選しない。** 禁止位置が2つになると、
// 隣接 / 離れている / 目的地を挟む で難しさの質がまったく変わる。
// 個別に引くと配置が偏り、**組み合わせによっては成立しない**
// (両隣が禁止だと手が滑った瞬間に即爆発する) ため、
// **成立する配置を全て列挙してから等確率で引く** (ADR S-10)。
func (b *ScenarioBuilder) resolveRotaryLayout(def map[string]any, vars map[string]string) (string, error) {
	countText, err := expandAny(def["count"], vars)
	if err != nil {
		return "", fmt.Errorf("rotary_layout.count: %w", err)
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return "", fmt.Errorf("rotary_layout.count is not a number: %q", countText)
	}

	layouts := validRotaryLayouts(count)
	if len(layouts) == 0 {
		return "", fmt.Errorf("rotary_layout: 禁止位置 %d 個で成立する配置が無い", count)
	}
	picked := layouts[b.rng.Intn(len(layouts))]

	parts := make([]string, 0, 1+len(picked.forbidden))
	parts = append(parts, strconv.Itoa(picked.target))
	for _, f := range picked.forbidden {
		parts = append(parts, strconv.Itoa(f))
	}
	return strings.Join(parts, ","), nil
}

// validRotaryLayouts は禁止位置 count 個で**成立する配置**を全て返す。
//
// ロータリーは 0-5 の直線配置 (ストッパー付き・連続回転なし)。
// 通過はセーフで、止まると違反 (docs/game_session_design.md §5)。
//
// **目的位置の両隣が禁止になる配置は除く。** 到達自体はできるが、
// 合わせたあと手が滑って1つ動かすと即爆発する。難度ではなく運になる。
func validRotaryLayouts(count int) []rotaryLayout {
	const positions = 6
	layouts := make([]rotaryLayout, 0, 64)

	if count == 1 {
		for target := 0; target < positions; target++ {
			for f := 0; f < positions; f++ {
				if f == target {
					continue
				}
				layouts = append(layouts, rotaryLayout{target: target, forbidden: []int{f}})
			}
		}
		return layouts
	}

	if count != 2 {
		return nil
	}
	for target := 0; target < positions; target++ {
		for f1 := 0; f1 < positions; f1++ {
			for f2 := f1 + 1; f2 < positions; f2++ {
				if target == f1 || target == f2 {
					continue
				}
				// 両隣が禁止 = 逃げ場が無い
				trapped := (target-1 == f1 || target-1 == f2) &&
					(target+1 == f1 || target+1 == f2)
				if trapped {
					continue
				}
				layouts = append(layouts, rotaryLayout{
					target: target, forbidden: []int{f1, f2}})
			}
		}
	}
	return layouts
}

// resolveColorSeq は順番に意味がある色の列を引く (201 復唱)。
//
// **重複を許し、引いた順のまま返す。** 押す順そのものなので、
// `colors` のように並べ替えてはいけない。
//
// **同じ色が3回以上続くのは避ける。** 無線で「赤赤赤」と読み上げると
// 何個言われたのか数えられず、記憶ではなく聞き取りの問題になる。
func (b *ScenarioBuilder) resolveColorSeq(
	def map[string]any, vars map[string]string, excluded map[string]bool,
) (string, error) {
	countText, err := expandAny(def["count"], vars)
	if err != nil {
		return "", fmt.Errorf("color_seq.count: %w", err)
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return "", fmt.Errorf("color_seq.count is not a number: %q", countText)
	}
	if count < 1 {
		return "", fmt.Errorf("color_seq.count は1以上 (現在 %d)", count)
	}

	candidates := make([]string, 0, len(allColors))
	for _, color := range allColors {
		if !excluded[color] {
			candidates = append(candidates, color)
		}
	}
	if len(candidates) < 2 {
		return "", fmt.Errorf("color_seq: 候補が %d 色しかない (2色以上必要)", len(candidates))
	}

	seq := make([]string, 0, count)
	for len(seq) < count {
		color := candidates[b.rng.Intn(len(candidates))]
		// 3連続を避ける
		n := len(seq)
		if n >= 2 && seq[n-1] == color && seq[n-2] == color {
			continue
		}
		seq = append(seq, color)
	}
	return strings.Join(seq, ","), nil
}
