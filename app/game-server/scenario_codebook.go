package main

// 202 LED照合 で使う暗号チェーンの対照表 (docs/printed_materials.md §4)。
//
// **紙資料に印刷する内容そのもの**。表を書き換えたら資料2・資料3も刷り直す。
// 資料と食い違うとプレイヤーが理不尽に失敗するため、値はここ1箇所にだけ置く
// (配線テーブルを hardware_config.h に一本化してあるのと同じ方針。ADR C-4)。

import (
	"fmt"
	"strconv"
	"strings"
)

// codebookEntry は4bitのランプ表示1通りぶん。
//
// pattern は kLED B(黄)・C(緑)・D(青)・E(白) の点灯状態を
// **左から詰めた4bit** で表す (bit3=B黄 / bit2=C緑 / bit1=D青 / bit0=E白)。
// kLED A(赤) は**常時消灯のダミー**で、パターンには含めない。
type codebookEntry struct {
	pattern int
	// word は資料2 の変換表が返すキーワード
	word string
	// rotary は word の頭文字をデコード表で引いたダイヤル位置 (0-5)
	rotary int
	// cut はデコード表の基準色と資料3 の分岐まで解いた最終的な切断色
	cut string
}

// codebookTable は資料2・資料3 を解いた結果の一覧。
//
// **全消灯 (0b0000) は載せない**。ランプが1つも点かない状態は故障と
// 見分けがつかず、プレイヤーが「装置が壊れた」と報告して止まる。
//
// 各色に最低2通りの表示が対応するよう語を選んである。cut は先に抽選され、
// そこから表示を逆引きするため (ADR S-1)、対応が1通りしか無い色があると
// 表示が毎回同じになって暗記されうる。
var codebookTable = []codebookEntry{
	{pattern: 0b0001, word: "ZONE", rotary: 5, cut: "D"},   // Z→5 / E→白 / 白は緑消灯→青
	{pattern: 0b0010, word: "BOMB", rotary: 0, cut: "A"},   // B→0 / B→青 / 青は白消灯→赤
	{pattern: 0b0011, word: "LEAD", rotary: 2, cut: "C"},   // L→2 / D→緑 / 緑は白点灯→緑
	{pattern: 0b0100, word: "PROBE", rotary: 3, cut: "E"},  // P→3 / E→白 / 白は緑点灯→白
	{pattern: 0b0101, word: "BOARD", rotary: 0, cut: "C"},  // B→0 / D→緑 / 緑は白点灯→緑
	{pattern: 0b0110, word: "XENON", rotary: 5, cut: "A"},  // X→5 / N→緑 / 緑は白消灯→赤
	{pattern: 0b0111, word: "WAVE", rotary: 5, cut: "E"},   // W→5 / E→白 / 白は緑点灯→白
	{pattern: 0b1000, word: "ZERO", rotary: 5, cut: "D"},   // Z→5 / O→白 / 白は緑消灯→青
	{pattern: 0b1001, word: "MODULE", rotary: 2, cut: "D"}, // M→2 / E→白 / 白は緑消灯→青
	{pattern: 0b1010, word: "QUIET", rotary: 3, cut: "D"},  // Q→3 / T→白 / 白は緑消灯→青
	{pattern: 0b1011, word: "INDEX", rotary: 1, cut: "C"},  // I→1 / X→緑 / 緑は白点灯→緑
	{pattern: 0b1100, word: "HANDLE", rotary: 1, cut: "E"}, // H→1 / E→白 / 白は緑点灯→白
	{pattern: 0b1101, word: "LEVER", rotary: 2, cut: "B"},  // L→2 / R→黄 / 黄は緑点灯→黄
	{pattern: 0b1110, word: "SENSOR", rotary: 4, cut: "B"}, // S→4 / R→黄 / 黄は緑点灯→黄
	{pattern: 0b1111, word: "INPUT", rotary: 1, cut: "E"},  // I→1 / T→白 / 白は緑点灯→白
}

// codebookBitColors は pattern の各bitに対応する kLED の色記号。
// **左(上位bit)から順に** B(黄) C(緑) D(青) E(白)。
// kLED A(赤) はダミーのため含まない。
var codebookBitColors = []string{"B", "C", "D", "E"}

// codebookEntriesForCut は指定した切断色になる表示を全て返す。
//
// cut を先に抽選し、そこから表示を逆引きする (ADR S-1)。
func codebookEntriesForCut(cut string) []codebookEntry {
	matched := make([]codebookEntry, 0, 4)
	for _, entry := range codebookTable {
		if entry.cut == cut {
			matched = append(matched, entry)
		}
	}
	return matched
}

// litColors は pattern で点灯する kLED の色記号をカンマ区切りで返す。
// テンプレートの leds = { "${lit}" = "on" } へそのまま渡せる形にする。
func (e codebookEntry) litColors() string {
	lit := make([]string, 0, len(codebookBitColors))
	for i, color := range codebookBitColors {
		shift := len(codebookBitColors) - 1 - i
		if (e.pattern>>shift)&1 == 1 {
			lit = append(lit, color)
		}
	}
	return strings.Join(lit, ",")
}

// darkColors は pattern で消灯している kLED の色記号をカンマ区切りで返す。
//
// **A(赤)を必ず含める**。赤は常時消灯のダミーで、パターンには現れないが
// 「消えているランプ」としてはプレイヤーの目に映るため。
func (e codebookEntry) darkColors() string {
	dark := []string{"A"}
	for i, color := range codebookBitColors {
		shift := len(codebookBitColors) - 1 - i
		if (e.pattern>>shift)&1 == 0 {
			dark = append(dark, color)
		}
	}
	return strings.Join(dark, ",")
}

// patternText は pattern を "0101" のような4桁の2進表記にする。
// ナビゲーター知識に載せて、資料2 のどの行かを指せるようにする。
func (e codebookEntry) patternText() string {
	text := strconv.FormatInt(int64(e.pattern), 2)
	return strings.Repeat("0", len(codebookBitColors)-len(text)) + text
}

// validateCodebookTable は対照表が謎として成立するかを起動時に確かめる。
//
// **抽選次第でしか再現しない不具合**になるため、組み立て時ではなく起動時に落とす
// (terminal_map の5色必須と同じ性質)。
func validateCodebookTable() error {
	seenPattern := make(map[int]bool, len(codebookTable))
	seenWord := make(map[string]bool, len(codebookTable))
	for _, entry := range codebookTable {
		if entry.pattern <= 0 || entry.pattern > 0b1111 {
			return fmt.Errorf("codebook: pattern %04b が範囲外 (全消灯は故障と紛れるため使えない)", entry.pattern)
		}
		if seenPattern[entry.pattern] {
			return fmt.Errorf("codebook: pattern %04b が重複している", entry.pattern)
		}
		seenPattern[entry.pattern] = true

		if seenWord[entry.word] {
			return fmt.Errorf("codebook: キーワード %q が重複している", entry.word)
		}
		seenWord[entry.word] = true

		if entry.rotary < 0 || entry.rotary > 5 {
			return fmt.Errorf("codebook: %s のダイヤル位置 %d が範囲外 (0-5)", entry.word, entry.rotary)
		}
	}

	// 5色すべてに最低2通りの表示が要る。1通りしか無いと、その色が正解の回は
	// 毎回同じ表示になり暗記できてしまう。
	for _, color := range allColors {
		if n := len(codebookEntriesForCut(color)); n < 2 {
			return fmt.Errorf("codebook: 色 %q に対応する表示が %d 通りしか無い (2通り以上必要)", color, n)
		}
	}
	return nil
}
