package main

// 抽選済みの変数から**機械的に導出する**定義 (`derive`) を解決する。
//
// 抽選 (`pick`) との違いは乱数を使わないこと — 既に引かれた値から
// 一意に決まる。記法の一覧は docs/scenario_design.md §3.1。
//
// **同じ抽選結果から Core向けJSON とナビゲーター知識の両方を作る**ため、
// ここが実機の状態とナビゲーターの知識を一致させる要になる (ADR S-1)。

import (
	"fmt"
	"strconv"
	"strings"
)

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
		return b.deriveTerminalForColor(def, vars)

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
		return deriveTail(def, vars)

	case "nth":
		return deriveNth(def, vars)

	default:
		return "", fmt.Errorf("unknown derive kind: %q", kind)
	}
}

// deriveTerminalForColor は配線色から資料3の端子番号を引く (203 ブループリント)。
//
// `series` に "x" / "y" を指定する。ナビゲーターは**両系統を並べて**伝え、
// どちらを使うかはプレイヤーがシリアル銘板の下1桁 (奇数=X / 偶数=Y) を見て
// 選ぶ (ADR D-6)。サーバーは個体のシリアルを知らない。
func (b *ScenarioBuilder) deriveTerminalForColor(
	def map[string]any, vars map[string]string,
) (string, error) {
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
}

// listAndIndex は `from` (カンマ区切り) と `index` (1始まり) を解決する。
// tail・nth が同じ形をしているため共通化してある。
func listAndIndex(def map[string]any, vars map[string]string, kind string) ([]string, int, error) {
	list, err := expandAny(def["from"], vars)
	if err != nil {
		return nil, 0, fmt.Errorf("%s.from: %w", kind, err)
	}
	idxText, err := expandAny(def["index"], vars)
	if err != nil {
		return nil, 0, fmt.Errorf("%s.index: %w", kind, err)
	}
	idx, err := strconv.Atoi(idxText)
	if err != nil {
		return nil, 0, fmt.Errorf("%s.index is not a number: %q", kind, idxText)
	}
	items := strings.Split(list, ",")
	if idx < 1 || idx > len(items) {
		return nil, 0, fmt.Errorf("%s.index %d is out of range [1,%d]", kind, idx, len(items))
	}
	return items, idx, nil
}

// deriveTail はカンマ区切りの値から index 番目 (1始まり) 以降を**まとめて**返す。
//
// 206 綱渡り の禁止位置に使う。数が難易度で変わる (1個 or 2個) ため、
// `nth` で1つずつ取り出すと**count=1 のときに2つ目が範囲外**になる。
// 残り全部を返せば `positions = ["${forbidden}"]` がそのまま
// 1要素にも2要素にも展開される (expandValue がカンマを展開する)。
func deriveTail(def map[string]any, vars map[string]string) (string, error) {
	items, idx, err := listAndIndex(def, vars, "tail")
	if err != nil {
		return "", err
	}
	rest := make([]string, 0, len(items)-idx+1)
	for _, item := range items[idx-1:] {
		rest = append(rest, strings.TrimSpace(item))
	}
	return strings.Join(rest, ","), nil
}

// deriveNth はカンマ区切りの値から N 番目 (1始まり) を取り出す。
// morse_letters がまとめて選んだ文字を各色へ配るのに使う。
func deriveNth(def map[string]any, vars map[string]string) (string, error) {
	items, idx, err := listAndIndex(def, vars, "nth")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(items[idx-1]), nil
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
