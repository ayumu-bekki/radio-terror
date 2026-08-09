package main

import (
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
	return result, nil
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
