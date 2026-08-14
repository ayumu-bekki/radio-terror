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

// TTS プロンプトに角括弧を残さないこと。
//
// **タグを含めると TTS の応答が不安定になる** (実測で8回中2回が20秒超)。
// 音声が途中で途切れる原因になるため、本文から必ず取り除く。
func TestBuildTTSPromptHasNoTags(t *testing.T) {
	for _, chunk := range []string{
		"[calm] 落ち着いて。どうぞ。",
		"よくやった。[relieved] あと一息だ。どうぞ。",
		"タグなしの発話。どうぞ。",
	} {
		p := buildTTSPrompt("低音で落ち着いた男性の声。", "落ち着いた場面。", chunk)
		if strings.Contains(p, "[") || strings.Contains(p, "]") {
			t.Errorf("プロンプトに角括弧が残っている:\n%s", p)
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

// prompt.toml が角括弧タグの使用を指示していないこと。
//
// 指示が残っていると生成AIがタグを出し、TTS が遅くなる。
func TestPromptTomlDoesNotInstructTags(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("navigator 設定が読めない: %v", err)
	}

	// 旧仕様の語がサンプルとして残っていないか
	for _, tag := range []string{"[calm]", "[warm]", "[urgent]", "[relieved]"} {
		if strings.Contains(cfg.Prompt.Output, tag) {
			t.Errorf("prompt.toml に角括弧タグの例が残っている: %s", tag)
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
