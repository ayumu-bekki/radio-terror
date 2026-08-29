package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// expandString は文字列中の ${name} を解決済み変数で置換する。
// 未解決の参照があった場合は errUnresolvedRef を返す。
func expandString(text string, vars map[string]string) (string, error) {
	var missing string

	result := varPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]
		value, ok := vars[name]
		if !ok {
			if missing == "" {
				missing = name
			}
			return match
		}
		return value
	})

	if missing != "" {
		return "", &errUnresolvedRef{name: missing}
	}
	return result, nil
}

// expandAny は TOML から読んだ任意の値を文字列として展開する。
// 数値はそのまま文字列化し、文字列は ${...} を解決する。
func expandAny(value any, vars map[string]string) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", fmt.Errorf("value is missing")
	case string:
		return expandString(v, vars)
	case int64:
		return strconv.FormatInt(v, 10), nil
	case int:
		return strconv.Itoa(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		return "", fmt.Errorf("unsupported value type %T", value)
	}
}

// expandStringList は文字列配列の各要素を展開する。
func expandStringList(value any, vars map[string]string) ([]string, error) {
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected an array, got %T", value)
	}

	list := make([]string, 0, len(raw))
	for _, item := range raw {
		text, err := expandAny(item, vars)
		if err != nil {
			return nil, err
		}
		list = append(list, text)
	}
	return list, nil
}

// expandCore は [core] テーブルを再帰的に展開し、Core向けセッションJSONの
// ステージ要素を生成する (docs/scenario_design.md §3)。
//
// キー側にも ${...} を書けるため (leds = { "${cut}" = "on" })、キーと値の両方を展開する。
// キーがカンマ区切りの複数色に展開された場合 (${rest} や ${lit}) は、
// 同じ値を各色へ展開する。
func (b *ScenarioBuilder) expandCore(core map[string]any, vars map[string]string) (map[string]any, error) {
	expanded, err := expandValue(core, vars)
	if err != nil {
		return nil, err
	}
	result, ok := expanded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("core must be a table")
	}

	// morse の word は文字列でなければならない (数値化されると実機が拒否する)
	normalizeLedWords(result)

	// noise_leds = "${noise}" は leds テーブルへマージする専用キー。
	// 妨害用LEDの割り当て(点灯/消灯/対称blink)をまとめて展開する。
	if err := mergeNoiseLeds(result); err != nil {
		return nil, err
	}

	// push_seq_colors = "${seq}" は entries を組み立てる専用キー。
	// 列の長さが難易度で変わるステージ (201 復唱) で使う。
	if err := expandPushSeqColors(result); err != nil {
		return nil, err
	}

	// rotary_leds = "${panel_leds}" は位置ごとのLED表示を展開する専用キー。
	// ロータリーを回すたびに表示が変わるステージ (209 配電盤照合) で使う。
	if err := expandRotaryLeds(result); err != nil {
		return nil, err
	}
	if err := expandPanelRows(result); err != nil {
		return nil, err
	}
	return result, nil
}

// expandPanelRows は core の panel_rows を位置ごとのテーブルへ展開する
// (209 配電盤照合。決定91)。
//
//	panel_rows = { rows = "${panel_rows}", blink_ms = 500 }
//	  ↓
//	panel_rows = { blink_ms: 500, rows: { "0": {forbidden:1, release:3}, ... } }
//
// **Core はステージ開始時点の実位置でこの表から行を引く**。サーバーは
// 開始位置を知らないので、6行すべてを渡して選択を Core に委ねる。
func expandPanelRows(core map[string]any) error {
	raw, ok := core["panel_rows"]
	if !ok {
		return nil
	}
	spec, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("panel_rows must be a table")
	}
	text, ok := spec["rows"].(string)
	if !ok {
		return fmt.Errorf("panel_rows.rows must reference a panel variable")
	}

	rows := map[string]any{}
	for _, entry := range strings.Split(text, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.Split(entry, ":")
		if len(fields) != 3 {
			return fmt.Errorf("panel_rows: 行の形式が不正: %q (位置:危険:解除)", entry)
		}
		nums := make([]int, 3)
		for i, f := range fields {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				return fmt.Errorf("panel_rows: %q が数値でない", f)
			}
			nums[i] = n
		}
		rows[strconv.Itoa(nums[0])] = map[string]any{
			"forbidden": nums[1],
			"release":   nums[2],
		}
	}
	if len(rows) != rotaryPositionNum {
		return fmt.Errorf("panel_rows: %d 行しかない (%d 行必要)", len(rows), rotaryPositionNum)
	}

	out := map[string]any{"rows": rows}
	if blink, ok := spec["blink_ms"]; ok {
		out["blink_ms"] = blink
	}
	core["panel_rows"] = out
	return nil
}

// expandRotaryLeds は core の rotary_leds を位置ごとのテーブルへ展開する。
//
// `pick = "rotary_leds"` が JSON へ畳んだ結果を受け取り、
// Core向けJSONの `rotary_leds` キーへ戻す (209 配電盤照合)。
func expandRotaryLeds(core map[string]any) error {
	raw, ok := core["rotary_leds"]
	if !ok {
		return nil
	}
	text, ok := raw.(string)
	if !ok || !strings.HasPrefix(text, rotaryLedsPrefix) {
		return fmt.Errorf("rotary_leds must reference a rotary_leds variable")
	}

	var table map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, rotaryLedsPrefix)), &table); err != nil {
		return fmt.Errorf("rotary_leds: %w", err)
	}
	core["rotary_leds"] = table
	return nil
}

// expandPushSeqColors は precondition.push_seq.colors を entries へ展開する。
//
// **列の長さが難易度で変わる**ステージ (201 復唱) のための記法。
// entries を TOML へ直接書くと個数が固定されてしまい、
// `p1`〜`p5` のように変数を並べる形では 8個へ伸ばせない。
//
//	push_seq = { colors = "${seq}", on_wrong_press = {...} }
//
// `colors` はカンマ区切りの色コード列 (pick = "colors" などが返す)。
// 各色が `{ push = "<色>" }` の1エントリになる。
func expandPushSeqColors(core map[string]any) error {
	precondition, ok := core["precondition"].(map[string]any)
	if !ok {
		return nil
	}
	pushSeq, ok := precondition["push_seq"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := pushSeq["colors"]
	if !ok {
		return nil
	}
	delete(pushSeq, "colors")

	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return fmt.Errorf("push_seq.colors must be a non-empty color list")
	}
	if _, exists := pushSeq["entries"]; exists {
		return fmt.Errorf("push_seq: colors and entries cannot be combined")
	}

	entries := make([]any, 0, 8)
	for _, color := range strings.Split(text, ",") {
		color = strings.TrimSpace(color)
		if color == "" {
			return fmt.Errorf("push_seq.colors に空の要素がある: %q", text)
		}
		entries = append(entries, map[string]any{"push": color})
	}
	pushSeq["entries"] = entries
	return nil
}

// mergeNoiseLeds は core の noise_leds を leds へ畳み込んで削除する。
//
// 既に leds に指定がある色(モールス表示など)は**上書きしない**。
// 表示用LEDが妨害で潰れると謎が成立しないため。
func mergeNoiseLeds(core map[string]any) error {
	raw, ok := core["noise_leds"]
	if !ok {
		return nil
	}
	delete(core, "noise_leds")

	text, ok := raw.(string)
	if !ok || !strings.HasPrefix(text, noiseLedsPrefix) {
		return fmt.Errorf("noise_leds must reference a noise_leds variable")
	}

	var assigned map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, noiseLedsPrefix)), &assigned); err != nil {
		return fmt.Errorf("noise_leds: %w", err)
	}

	leds, _ := core["leds"].(map[string]any)
	if leds == nil {
		leds = make(map[string]any, len(assigned))
	}
	for color, spec := range assigned {
		if _, exists := leds[color]; exists {
			continue // 表示用LEDを妨害で上書きしない
		}
		leds[color] = spec
	}
	core["leds"] = leds
	return nil
}

// expandValue は任意のTOML値を再帰的に展開する。
func expandValue(value any, vars map[string]string) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			expandedKey, err := expandString(key, vars)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", key, err)
			}
			expandedItem, err := expandValue(item, vars)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", key, err)
			}

			// キーがカンマ区切りに展開された場合は、各色へ同じ値を割り当てる
			// (leds = { "${rest}" = {...} } のような一括指定)
			for _, part := range strings.Split(expandedKey, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				result[part] = expandedItem
			}
		}
		return result, nil

	case []any:
		result := make([]any, 0, len(v))
		for _, item := range v {
			expandedItem, err := expandValue(item, vars)
			if err != nil {
				return nil, err
			}

			// 配列要素が複数値に展開された場合は展開して並べる
			// (forbidden_rotary.positions = ["${forbidden}"] など)
			if text, ok := expandedItem.(string); ok && strings.Contains(text, ",") {
				for _, part := range strings.Split(text, ",") {
					result = append(result, coerceScalar(strings.TrimSpace(part)))
				}
				continue
			}
			result = append(result, expandedItem)
		}
		return result, nil

	case string:
		text, err := expandString(v, vars)
		if err != nil {
			return nil, err
		}
		return coerceScalar(text), nil

	default:
		return value, nil
	}
}

// coerceScalar は展開結果の文字列を、数値として解釈できる場合は数値へ戻す。
//
// TOML では precondition.rotary = "${rotary}" のように数値フィールドも文字列で
// 書くしかないため、展開後にJSONの数値へ戻す。色 (A-E) や "on"/"blink" などは
// 文字列のまま残る。
func coerceScalar(text string) any {
	if text == "" {
		return text
	}
	if n, err := strconv.Atoi(text); err == nil {
		return n
	}
	return text
}
