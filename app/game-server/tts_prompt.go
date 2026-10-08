package main

import (
	"regexp"
	"strings"
)

// 表情の指定方法について。
//
// **ディレクターズノート(読み方の指定)と角括弧タグを併用する**。
//
//	<声質指定>
//	<読み方の指定>          ← ディレクターズノート
//
//	次のセリフを読み上げてください:
//	[relieved] よくやった。  ← 本文中の表情タグ
//
// 経緯: タグは一度廃止された。「タグを含めると TTS の応答が不安定になる」
// という実測があったため (通常2.6秒が8回中2回20秒超、最大21.7秒)。
// しかし後の調査で、その不安定さの正体は**一括受信**だったと判明した —
// 一括受信は入力内容と無関係に27%が10秒超になる
// (docs/navigator_design.md §5 決定15)。
// ストリーミング受信へ移行後にタグを測り直したところ、各10回で
// **10秒超はゼロ・中央値2.87〜3.32秒**とタグなしと差がなく、
// 読み上げ事故 (「リリーブド」と発音してしまう) も起きなかった。
// 表現としてはノート単独よりタグ併用の方が良いと確認できたので復活させた。

// ttsTagPattern は本文中の角括弧タグにマッチする。
//
// TTS へは**タグを残したまま**渡す (演技指示として解釈させる)。
// 除去するのは会話ログへ記録するときだけ — マネージャー画面に
// `[relieved]` が並ぶと読みにくいため。
var ttsTagPattern = regexp.MustCompile(`\[[^\[\]]*\]\s*`)

// stripTTSTags は角括弧の演技指示を取り除いた本文を返す。
//
// 日本語には語間の空白がないため、タグと直後の空白をまとめて除去する
// (「よくやった。[relieved] あと一息だ。」→「よくやった。あと一息だ。」)。
//
// **TTS へ渡す経路では使わない**。表示・記録用。
func stripTTSTags(s string) string {
	return strings.TrimSpace(ttsTagPattern.ReplaceAllString(s, ""))
}

// allowedTTSTags は TTS へ渡してよい表情タグ。
// navigator/prompt.toml の「表情タグ」一覧と一致させる
// (整合はテストで担保している)。
var allowedTTSTags = map[string]bool{
	"calm":     true, // 落ち着いて
	"urgent":   true, // 切迫して
	"relieved": true, // 安堵して
	"warm":     true, // 温かく
	"stern":    true, // 厳しく
	"tense":    true, // 緊張して
}

// sanitizeTTSTags は許可リストにない角括弧タグだけを取り除く。
//
// プロンプトで語彙を限定しているが、**生成AIが一覧外の語を使うことがある**。
// 未知のタグをそのまま渡すと演技として解釈されず、そのまま読み上げられて
// 「リリーブド」のような事故になる。既知のタグだけ残して他は落とす。
func sanitizeTTSTags(s string) string {
	out := ttsTagPattern.ReplaceAllStringFunc(s, func(match string) string {
		open := strings.Index(match, "[")
		close := strings.Index(match, "]")
		if open < 0 || close < open {
			return ""
		}
		name := strings.ToLower(strings.TrimSpace(match[open+1 : close]))
		if allowedTTSTags[name] {
			// 正規化した形で残す (大文字・余分な空白を揃える)
			return "[" + name + "] "
		}
		return ""
	})
	return strings.TrimSpace(out)
}

// directorNotes はトリガーごとの**読み方の指定**。
//
// TTS へ「どういう調子で読むか」を伝えて表情を作らせる。本文に記号を
// 混ぜる方式と違い、**プロンプトの前置きなので遅延が起きない**。
//
// **場面や登場人物を説明してはいけない。** TTS は音声専用エンジンではなく
// 生成モデルなので、「プレイヤーの報告に応じる場面」のように会話の状況を
// 書くと、**その場面全体を演じようとして相手の発話まで自分で作り、
// 本文の前に読み上げる**ことがある (実運用で発生: ログに無い
// 「プレイヤーの応答」らしき音声が先頭に混入した)。
//
// 書いてよいのは**声の出し方だけ** — 速度・強弱・感情の乗せ方。
// 「〜の場面」「プレイヤーが〜した直後」といった状況説明は書かない。
//
// キーは navigator/prompt.toml の [triggers] と同じトリガー名。
var directorNotes = map[string]string{
	"session_start":    "落ち着いた、頼りになる調子で。",
	"stage_cleared":    "安堵と称賛をにじませて、短く。",
	"color_match_done": "ねぎらう調子で、穏やかに。",
	"wrong_action":     "慌てさせず、立て直しを促す落ち着いた調子で。",
	"hint":             "急かさず、導くような調子で。",
	"time_running":     "緊迫感を出し、短く畳みかけるように。",
	"player_message":   "事務的すぎない、自然な受け答えの調子で。",
}

// directorNote はトリガーに対応する読み方の指定を返す。
// 未定義のトリガーでは空文字を返し、声質指定だけで読ませる。
func directorNote(trigger string) string {
	return directorNotes[trigger]
}

// TTSRequest は TTS へ渡す値。**style と読み上げ本文を分ける** (Gemini 3.8 TTS)。
//
// 3.8 TTS は text を厳密な逐語録として読むので、「次のセリフを読み上げてください」や
// 声質・場面の指示を text に混ぜると**そのまま声に出る**。話し方は
// `speech_metadata.style` で渡し、text は読ませる台詞だけにする
// (docs/gemini_enterprise_setup.md 決定22)。
type TTSRequest struct {
	Style string // speech_metadata.style (声質・読み方・台詞中の演技指示)
	Text  string // 読み上げる本文 (角括弧タグは含まない)
}

// buildTTSPrompt は声質指定と本文から TTS への入力を組み立てる。
//
// note は読み方の指定 (ディレクターズノート)。空なら省略する。
// 本文中の角括弧タグは**許可リストのものだけ**を演技指示として拾い、本文からは外して
// style の「台詞中の演技指示」へ登場順に移す (3.8 の山かっこタグは一時的な音声イベント
// 専用で、表情は style で伝える)。一覧外のタグは読み上げ事故になるので落とす。
func buildTTSPrompt(style, note, chunk string) TTSRequest {
	chunk = sanitizeTTSTags(chunk)

	var directions []string
	text := ttsTagPattern.ReplaceAllStringFunc(chunk, func(m string) string {
		directions = append(directions, strings.TrimSpace(m[1:strings.Index(m, "]")]))
		return ""
	})

	var b strings.Builder
	b.WriteString(style)
	if note != "" {
		b.WriteString("\n")
		b.WriteString(note)
	}
	if len(directions) > 0 {
		b.WriteString("\n台詞中の演技指示 (登場順。声に出して読まない): ")
		b.WriteString(strings.Join(directions, " → "))
	}
	return TTSRequest{Style: b.String(), Text: strings.TrimSpace(text)}
}
