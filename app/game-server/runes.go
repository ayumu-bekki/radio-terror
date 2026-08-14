package main

// countRunes は文字列の文字数 (rune 数) を返す。
//
// 発話長の判定やログに使う。日本語では len() のバイト数が
// 文字数と一致しないため、rune で数える。
func countRunes(s string) int {
	return len([]rune(s))
}
