package main

import (
	"fmt"
	"regexp"
	"strings"
)

// 表情タグ (角括弧で囲んだ英語の指示) の扱い。
//
// ナビゲーターの応答生成 (navigator/prompt.toml の output) が
// `[calm]` のようなタグを本文に混ぜてくる。TTS はこれを演技指示として解釈し、
// 読み上げない。ただし TTS API には実測で分かった制約が2つあるため、
// 送信前にここで正規化する。
//
//  1. プロンプトに "[...]" というリテラル (角括弧＋三点) を含めると
//     400 INVALID_ARGUMENT が返る。指示文では角括弧を日本語で言い表す。
//  2. 短い文にタグを3つ以上入れると空応答になることがある。
//     1発話あたり maxTTSTags 個までに切り詰める。
const maxTTSTags = 2

// allowedTTSTags は表情タグとして許可する語。
// prompt.toml の「表情の指定」で提示している語と一致させる。
//
// 生成AIが指示を外れた語や日本語を入れてくることがあるため、
// 許可リストで弾いて TTS に渡さない (読み上げ事故を防ぐ)。
var allowedTTSTags = map[string]bool{
	"calm":        true,
	"warm":        true,
	"firmly":      true,
	"urgent":      true,
	"relieved":    true,
	"serious":     true,
	"gently":      true,
	"encouraging": true,
}

// ttsTagPattern は本文中の角括弧タグにマッチする。
var ttsTagPattern = regexp.MustCompile(`\[([^\[\]]*)\]`)

// sanitizeTTSTags は本文中の表情タグを検査し、TTS に渡せる形へ整える。
//
//   - 許可リストに無いタグは削除する (日本語のト書きが読み上げられるのを防ぐ)
//   - maxTTSTags を超えた分は削除する (空応答対策)
//
// 削除しても本文は残るため、発話内容そのものは失われない。
func sanitizeTTSTags(s string) string {
	kept := 0
	out := ttsTagPattern.ReplaceAllStringFunc(s, func(m string) string {
		body := strings.ToLower(strings.TrimSpace(ttsTagPattern.FindStringSubmatch(m)[1]))
		if !allowedTTSTags[body] || kept >= maxTTSTags {
			return ""
		}
		kept++
		// 表記ゆれを正規化して渡す
		return "[" + body + "]"
	})
	// タグを削った跡の余分な空白を畳む
	out = strings.ReplaceAll(out, "  ", " ")
	return strings.TrimSpace(out)
}

// stripTTSTags は表情タグを取り除いた本文を返す。
// 会話ログ・マネージャー画面など、人が読む表示に使う。
//
// 日本語には語間の空白がないため、タグと直後の空白をまとめて除去する
// (「よくやった。[relieved] あと一息だ。」→「よくやった。あと一息だ。」)。
var ttsTagWithSpacePattern = regexp.MustCompile(`\[[^\[\]]*\]\s*`)

func stripTTSTags(s string) string {
	return strings.TrimSpace(ttsTagWithSpacePattern.ReplaceAllString(s, ""))
}

// buildTTSPrompt は声質指定と本文から TTS プロンプトを組み立てる。
//
// 角括弧を日本語で言い表しているのは意図的。"[...]" というリテラルを
// 含めると TTS API が 400 を返す (crosstalk-gen で再現・特定済み)。
func buildTTSPrompt(style, chunk string) string {
	chunk = sanitizeTTSTags(chunk)

	var b strings.Builder
	b.WriteString(style)
	b.WriteString("\n\n")
	if ttsTagPattern.MatchString(chunk) {
		// タグが残っている場合だけ、読み上げ対象外だと伝える
		b.WriteString("角括弧の中は演技の指示です。声に出して読まず、その通りの話し方に反映してください。\n\n")
	}
	fmt.Fprintf(&b, "次のセリフを読み上げてください:\n%s", chunk)
	return b.String()
}
