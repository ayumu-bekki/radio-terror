package main

import (
	"fmt"
	"regexp"
	"strings"
)

// 表情の指定方法について。
//
// 以前は本文へ `[calm]` のような角括弧タグを混ぜ、TTS に演技指示として
// 解釈させていた。しかし**タグを含めると TTS の応答が不安定になる**ことが
// 実測で分かった (通常2.6秒のところ、8回中2回が20秒超。最大21.7秒)。
// 音声が途中で途切れる原因になっていたため廃止した
// (docs/navigator_design.md §2.7)。
//
// 現在は「ディレクターズノート」— 場面の説明文 — をプロンプトの先頭で
// 添えて表情を伝える。本文には記号を混ぜないので遅延が起きない。

// ttsTagPattern は本文中の角括弧タグにマッチする。
//
// 表情タグは廃止したが、**生成AIが過去の学習や指示の揺れで付けてくる**
// ことがあるため、TTS へ渡す前と記録前に取り除く。残ると読み上げ事故になる。
var ttsTagPattern = regexp.MustCompile(`\[[^\[\]]*\]\s*`)

// stripTTSTags は角括弧の演技指示を取り除いた本文を返す。
//
// 日本語には語間の空白がないため、タグと直後の空白をまとめて除去する
// (「よくやった。[relieved] あと一息だ。」→「よくやった。あと一息だ。」)。
func stripTTSTags(s string) string {
	return strings.TrimSpace(ttsTagPattern.ReplaceAllString(s, ""))
}

// directorNotes はトリガーごとの場面説明 (ディレクターズノート)。
//
// TTS へ「どういう場面の発話か」を伝えて表情を作らせる。本文に記号を
// 混ぜる方式と違い、**プロンプトの前置きなので遅延が起きない**。
//
// キーは navigator/prompt.toml の [triggers] と同じトリガー名。
var directorNotes = map[string]string{
	"session_start":  "任務の開始を告げる場面。落ち着いて、頼りになる調子で。",
	"stage_cleared":  "プレイヤーが課題を突破した直後。安堵と称賛を短く。",
	"whack_done":     "細かい作業をやり切った直後。ねぎらう調子で。",
	"wrong_action":   "誤操作が起きた直後。慌てさせず、立て直しを促す調子で。",
	"defused":        "解除に成功した瞬間。喜びと安堵をはっきり出して。",
	"exploded":       "解除に失敗した直後。落胆を抑え、相手を気遣う調子で。",
	"hint":           "行き詰まった相手へ助け舟を出す場面。急かさず、導くように。",
	"time_running":   "残り時間が少ない場面。緊迫感を出し、短く畳みかけるように。",
	"player_message": "プレイヤーの報告に応じる場面。事務的すぎず、自然な受け答えで。",
}

// directorNote はトリガーに対応する場面説明を返す。
// 未定義のトリガーでは空文字を返し、声質指定だけで読ませる。
func directorNote(trigger string) string {
	return directorNotes[trigger]
}

// buildTTSPrompt は声質指定と本文から TTS プロンプトを組み立てる。
//
// note は場面説明 (ディレクターズノート)。空なら省略する。
func buildTTSPrompt(style, note, chunk string) string {
	// 生成AIがタグを付けてきた場合に備えて取り除く (読み上げ事故を防ぐ)
	chunk = stripTTSTags(chunk)

	var b strings.Builder
	b.WriteString(style)
	if note != "" {
		b.WriteString("\n")
		b.WriteString(note)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "次のセリフを読み上げてください:\n%s", chunk)
	return b.String()
}
