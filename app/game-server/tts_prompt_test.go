package main

import (
	"strings"
	"testing"
)

// 角括弧の演技指示は本文から取り除くこと。
//
// 表情タグは廃止したが、生成AIが付けてくることがある。
// 残したまま TTS へ渡すと読み上げ事故になり、記録にも混ざる。
func TestStripTTSTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[calm] 落ち着いて。どうぞ。", "落ち着いて。どうぞ。"},
		{"よくやった。[relieved] あと一息だ。", "よくやった。あと一息だ。"},
		{"タグなし", "タグなし"},
		{"途中[urgent]に入る", "途中に入る"},
		{"[落ち着いて] 日本語のト書きも消す", "日本語のト書きも消す"},
		{"[] 空タグ", "空タグ"},
		{"[a] [b] [c] 連続", "連続"},
	}
	for _, c := range cases {
		if got := stripTTSTags(c.in); got != c.want {
			t.Errorf("stripTTSTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TTS プロンプトに未知の角括弧タグを残さないこと。
//
// 許可リストの表情タグは**残す** (演技指示として解釈される)。
// 一覧外の語は解釈されず、そのまま読み上げられる事故になるので落とす。
// 許可タグが残ることは TestBuildTTSPromptKeepsAllowedTags が押さえる。
func TestBuildTTSPromptDropsUnknownTags(t *testing.T) {
	for _, chunk := range []string{
		"[excited] 落ち着いて。どうぞ。",
		"よくやった。[安堵して] あと一息だ。どうぞ。",
		"タグなしの発話。どうぞ。",
	} {
		p := buildTTSPrompt("低音で落ち着いた男性の声。", "落ち着いた場面。", chunk)
		if strings.Contains(p, "[") || strings.Contains(p, "]") {
			t.Errorf("プロンプトに未知タグの角括弧が残っている:\n%s", p)
		}
	}
}

// 声質指定・場面説明・本文が入ること。
func TestBuildTTSPromptContainsParts(t *testing.T) {
	const (
		style = "低音で落ち着いた男性の声。"
		note  = "任務の開始を告げる場面。"
		body  = "了解した。どうぞ。"
	)

	p := buildTTSPrompt(style, note, body)
	for _, want := range []string{style, note, body} {
		if !strings.Contains(p, want) {
			t.Errorf("プロンプトに %q が入っていない:\n%s", want, p)
		}
	}
}

// 場面説明が空なら省略すること (余計な改行を残さない)。
func TestBuildTTSPromptOmitsEmptyNote(t *testing.T) {
	p := buildTTSPrompt("style", "", "本文。")
	if strings.Contains(p, "\n\n\n") {
		t.Errorf("空の場面説明で余分な改行が入っている:\n%q", p)
	}
	if !strings.Contains(p, "本文。") {
		t.Error("本文が入っていない")
	}
}

// prompt.toml が表情タグの語彙を限定していること。
//
// 「使ってよい語の一覧」が消えると生成AIが自由な語を出し、
// サーバー側の許可リストに弾かれて表情が付かなくなる。
// 一覧の中身が allowedTTSTags と一致することは
// TestAllowedTagsMatchPrompt が押さえる。
func TestPromptTomlLimitsTagVocabulary(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("navigator 設定が読めない: %v", err)
	}

	for _, want := range []string{
		"表情タグ",
		"この一覧にない語は使わないでください",
	} {
		if !strings.Contains(cfg.Prompt.Output, want) {
			t.Errorf("prompt.toml に %q が含まれていない", want)
		}
	}
}

// トリガーに対応する場面説明があること。
//
// 表情はここでしか伝わらないため、主要なトリガーで欠けていると
// 全て同じ調子の棒読みになる。
func TestDirectorNoteCoversTriggers(t *testing.T) {
	// 演出上、表情の差が特に効くトリガー
	for _, trigger := range []string{
		"session_start", "stage_cleared", "wrong_action",
		"defused", "exploded", "hint",
	} {
		if directorNote(trigger) == "" {
			t.Errorf("トリガー %q の場面説明が無い", trigger)
		}
	}

	// 未定義のトリガーは空文字 (声質指定だけで読ませる)
	if directorNote("unknown_trigger") != "" {
		t.Error("未定義トリガーで空文字以外が返っている")
	}
}

// TestSanitizeTTSTags は許可リストにないタグだけが落とされることを確かめる。
//
// 表情タグは TTS へ**残したまま**渡す (演技指示として解釈させる) が、
// 一覧外の語は解釈されず「リリーブド」のように読み上げられてしまう。
func TestSanitizeTTSTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"許可タグは残す", "[relieved] よくやった。", "[relieved] よくやった。"},
		{"複数の許可タグ", "[urgent] 急げ。[tense] 止まるな。", "[urgent] 急げ。[tense] 止まるな。"},
		{"未知タグは落とす", "[excited] よくやった。", "よくやった。"},
		{"許可と未知の混在", "[calm] 待て。[excited] 行け。", "[calm] 待て。行け。"},
		{"大文字は正規化", "[CALM] 待て。", "[calm] 待て。"},
		{"前後の空白を許容", "[ calm ] 待て。", "[calm] 待て。"},
		{"タグなしはそのまま", "よくやった。", "よくやった。"},
		{"日本語のト書きは落とす", "[安堵して] よくやった。", "よくやった。"},
		{"空タグは落とす", "[] よくやった。", "よくやった。"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeTTSTags(c.in); got != c.want {
				t.Errorf("sanitizeTTSTags(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestBuildTTSPromptKeepsAllowedTags は TTS プロンプトに許可タグが
// 残ること・未知タグが落ちることを確かめる。
//
// ここで誤って全タグを除去すると、表情指定が効かなくなる
// (docs/navigator_design.md §5 決定17)。
func TestBuildTTSPromptKeepsAllowedTags(t *testing.T) {
	prompt := buildTTSPrompt("声質", "場面", "[relieved] よくやった。")
	if !strings.Contains(prompt, "[relieved]") {
		t.Errorf("許可タグが除去されている: %q", prompt)
	}

	prompt = buildTTSPrompt("声質", "場面", "[excited] よくやった。")
	if strings.Contains(prompt, "[excited]") {
		t.Errorf("未知タグが残っている: %q", prompt)
	}
}

// TestAllowedTagsMatchPrompt は許可タグの一覧が
// navigator/prompt.toml の指示と一致していることを確かめる。
//
// 片方だけ増やすと、生成AIが使うのにサーバーが落とす (またはその逆) という
// 分かりにくい状態になる。
func TestAllowedTagsMatchPrompt(t *testing.T) {
	cfg := loadTestNavigator(t)

	for tag := range allowedTTSTags {
		want := "`[" + tag + "]`"
		if !strings.Contains(cfg.Prompt.Output, want) {
			t.Errorf("prompt.toml に %s の説明がない (allowedTTSTags と揃えること)", want)
		}
	}
}
