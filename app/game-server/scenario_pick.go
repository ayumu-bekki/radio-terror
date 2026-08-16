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
		// 色名のローマ字表記を選ぶ。表記がそのまま色に対応する (308)
		return b.pickWordByColor(def, vars, usedLines, excluded, romajiColor, "romaji color word")

	case "morse_word":
		// NATOフォネティックコードの語を選ぶ。頭文字が対照表で色に対応する (203)
		return b.pickWordByColor(def, vars, usedLines, excluded, morseWordColor, "morse word")

	default:
		return "", fmt.Errorf("unknown pick kind: %q", pickKind)
	}
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
		// 色名のローマ字表記 → 色 (308 ローマ字電文)
		word, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		color := romajiColor(word)
		if color == "" {
			return "", fmt.Errorf("unknown romaji color name: %q", word)
		}
		return color, nil

	case "terminal_for_color":
		// 配線色 → 回路図シートの端子番号 (E. ブループリント)
		color, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		terminal := b.sheet.TerminalForColor(color)
		if terminal == "" {
			return "", fmt.Errorf("no terminal mapped to color %q (check [mission_sheet].terminal_map)", color)
		}
		return terminal, nil

	case "sheet_threshold":
		// 07. 運命の二択: シート実測値から閾値を決める。
		// 実測値と大きく離した閾値にして、数え間違いで誤答しないようにする
		// (docs/puzzle_stage_ideas.md §2.2)。
		branch, err := expandAny(def["branch"], vars)
		if err != nil {
			return "", err
		}
		actual, err := b.sheetValue(branch)
		if err != nil {
			return "", err
		}
		// 実測値から2離した値を閾値にする (28個なら30)
		return strconv.Itoa(actual + 2), nil

	case "sheet_direction":
		// 閾値との比較方向。実測値は必ず閾値未満になるよう組み立てるため "lt" 固定。
		// (テンプレートが閾値を実測値+2に置くため、シート集計は常に未満側に落ちる)
		branch, err := expandAny(def["branch"], vars)
		if err != nil {
			return "", err
		}
		if _, err := b.sheetValue(branch); err != nil {
			return "", err
		}
		return "lt", nil

	case "sheet_section":
		// 202 運命の二択: 分岐に対応するシートの区画見出し。
		//
		// シートには「区画A」(記号) / 「区画B」(数字) を大きく印刷してあり、
		// ナビゲーターは「区画Aを見ろ」と**一言で**指示できる
		// (docs/printed_materials.md §3.2.1)。「記号のほうを数えてください」と
		// 説明的に言うと発話が伸び、どこを見るのかも曖昧になる。
		branch, err := expandAny(def["branch"], vars)
		if err != nil {
			return "", err
		}
		return sheetSectionLabel(branch)

	default:
		return "", fmt.Errorf("unknown derive kind: %q", kind)
	}
}

// sheetSectionLabel は分岐種別に対応するシートの区画見出しを返す。
//
// **配線色の記号 (A-E) とは別物**。色記号は紙に印刷しないため紙面上で
// 衝突しないが、混同を避けるため必ず「区画」を付けて呼ぶ
// (docs/printed_materials.md §1.1・§3.2.1)。
func sheetSectionLabel(branch string) (string, error) {
	if branch == "symbol" {
		return "区画A", nil
	}
	if branch == "number" {
		return "区画B", nil
	}
	return "", fmt.Errorf("unknown sheet branch: %q", branch)
}

// sheetValue は分岐種別に対応するミッションシートの実測値を返す。
func (b *ScenarioBuilder) sheetValue(branch string) (int, error) {
	switch branch {
	case "symbol":
		if b.sheet.SymbolCount <= 0 {
			return 0, fmt.Errorf("[mission_sheet].symbol_count is not configured")
		}
		return b.sheet.SymbolCount, nil
	case "number":
		if b.sheet.NumberSum <= 0 {
			return 0, fmt.Errorf("[mission_sheet].number_sum is not configured")
		}
		return b.sheet.NumberSum, nil
	default:
		return 0, fmt.Errorf("unknown sheet branch: %q", branch)
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
// 203 暗号電文 (頭文字→対照表の色) と 308 ローマ字電文 (色名表記→色) は
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
