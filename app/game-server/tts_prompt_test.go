package main

import (
	"strings"
	"testing"
)

func TestSanitizeTTSTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"許可タグはそのまま",
			"[calm] 落ち着いて。どうぞ。", "[calm] 落ち着いて。どうぞ。"},
		{"大文字・空白を正規化",
			"[ CALM ] はい。", "[calm] はい。"},
		{"許可外の語は削除",
			"[angry] 待て。どうぞ。", "待て。どうぞ。"},
		{"日本語のト書きは削除",
			"[落ち着いて] 待て。", "待て。"},
		{"3つ目以降は削除",
			"[calm] あ [warm] い [urgent] う [firmly] え",
			"[calm] あ [warm] い う え"},
		{"タグなしはそのまま",
			"了解した。どうぞ。", "了解した。どうぞ。"},
		{"空タグは削除",
			"[] 了解。", "了解。"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeTTSTags(c.in); got != c.want {
				t.Errorf("sanitizeTTSTags(%q)\n = %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

// タグ数の上限を必ず守ること。
// 短文にタグを3つ以上入れると TTS が空応答を返すことがある。
func TestSanitizeTTSTagsRespectsLimit(t *testing.T) {
	in := "[calm] あ [warm] い [urgent] う [firmly] え [gently] お"
	got := sanitizeTTSTags(in)
	if n := strings.Count(got, "["); n > maxTTSTags {
		t.Errorf("タグが %d 個残っている (上限 %d): %q", n, maxTTSTags, got)
	}
}

// プロンプトに "[...]" リテラルを含めないこと。
// これを含めると TTS API が 400 INVALID_ARGUMENT を返す。
func TestBuildTTSPromptAvoidsEllipsisLiteral(t *testing.T) {
	for _, chunk := range []string{
		"[calm] 落ち着いて。どうぞ。",
		"タグなしの発話。どうぞ。",
	} {
		p := buildTTSPrompt("低音で落ち着いた男性の声。", chunk)
		if strings.Contains(p, "[...]") {
			t.Errorf("プロンプトに [...] が含まれている:\n%s", p)
		}
	}
}

// タグがある時だけ演技指示の説明を添えること。
func TestBuildTTSPromptExplainsTagsOnlyWhenPresent(t *testing.T) {
	const note = "角括弧の中は演技の指示です"

	withTag := buildTTSPrompt("style", "[calm] はい。どうぞ。")
	if !strings.Contains(withTag, note) {
		t.Error("タグがあるのに説明が無い")
	}
	if !strings.Contains(withTag, "[calm]") {
		t.Error("タグが本文から消えている")
	}

	noTag := buildTTSPrompt("style", "はい。どうぞ。")
	if strings.Contains(noTag, note) {
		t.Error("タグが無いのに説明が付いている")
	}

	// 許可外タグは除去されるので、説明も付かない
	dropped := buildTTSPrompt("style", "[angry] はい。どうぞ。")
	if strings.Contains(dropped, note) {
		t.Error("タグが除去されたのに説明が付いている")
	}
}

// 声質指定と本文が両方入ること。
func TestBuildTTSPromptContainsStyleAndText(t *testing.T) {
	p := buildTTSPrompt("低音で落ち着いた男性の声。", "了解した。どうぞ。")
	if !strings.Contains(p, "低音で落ち着いた男性の声。") {
		t.Error("声質指定が入っていない")
	}
	if !strings.Contains(p, "了解した。どうぞ。") {
		t.Error("本文が入っていない")
	}
}

func TestStripTTSTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[calm] 落ち着いて。どうぞ。", "落ち着いて。どうぞ。"},
		{"よくやった。[relieved] あと一息だ。", "よくやった。あと一息だ。"},
		{"タグなし", "タグなし"},
		{"途中[urgent]に入る", "途中に入る"},
	}
	for _, c := range cases {
		if got := stripTTSTags(c.in); got != c.want {
			t.Errorf("stripTTSTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// prompt.toml が提示する語と、コードの許可リストが一致していること。
// ずれると生成AIが出したタグが無言で捨てられる。
func TestPromptTomlTagsMatchAllowList(t *testing.T) {
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("navigator 設定が読めない: %v", err)
	}
	for tag := range allowedTTSTags {
		if !strings.Contains(cfg.Prompt.Output, tag) {
			t.Errorf("許可タグ %q が prompt.toml の output に載っていない", tag)
		}
	}
}
