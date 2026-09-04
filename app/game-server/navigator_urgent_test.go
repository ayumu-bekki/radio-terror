package main

import (
	"strings"
	"testing"
)

// 残り時間の告知 (旧 time_warning) はプロンプトの独立したブロックとして渡る。
//
// **独立させるのが要点**。交信スタイルの中に混ぜると口調の指示に紛れて
// 落ちる (ADR N-51: 長い指針は末尾が落ちる)。
func TestUrgentNoticeBlockPresentOnlyWhenAnnounced(t *testing.T) {
	cfg := loadTestNavigator(t)
	character, ok := cfg.ByID("owl")
	if !ok {
		t.Fatal("フクロウが読めない")
	}

	build := func(announce bool) string {
		return BuildNavigatorPrompt(NavigatorPromptInput{
			Prompt:         &cfg.Prompt,
			Character:      character,
			StageIndex:     0,
			RemainingMS:    45000,
			HintLevel:      HintL1,
			AnnounceUrgent: announce,
		})
	}

	if got := build(false); strings.Contains(got, "残り時間の告知") {
		t.Error("告知しない発話に告知ブロックが入っている")
	}
	got := build(true)
	if !strings.Contains(got, "残り時間の告知") {
		t.Fatal("告知ブロックが入っていない")
	}

	// 実際の残り秒数を渡す。プレイヤーは装置の前でしか時間を読めないため、
	// 「あとどれくらいか」を言えないと告知の意味が無い。
	if !strings.Contains(got, "45 秒") {
		t.Errorf("残り秒数が入っていない:\n%s", got)
	}

	// **字数の目安から外すことを明示する**。含めると、この一言を足したぶん
	// 手順や危険の警告が削られる (ADR N-22)。
	if !strings.Contains(got, "数えません") {
		t.Error("字数に数えない旨が入っていない")
	}

	// **例文を置かない** (ADR N-21c)。全キャラ共通に渡るブロックなので、
	// 台詞を書くとその口調に全員が寄る (方言のキャラが標準語に戻る)。
	//
	// 検査するのは**発話の形をした引用**だけ。「キャラシートの『残り60秒』の
	// 例に従え」のような**委ね先の指示**は引用ではないので許す。
	block := got[strings.Index(got, "# 残り時間の告知"):]
	if i := strings.Index(block, "\n#"); i > 0 {
		block = block[:i]
	}
	for _, quoted := range extractQuoted(block) {
		// 「どうぞ」のような単語の引用は口調を運ばない。
		// 落とすのは文の形をしたもの (ADR N-21c / 決定110)。
		if len([]rune(quoted)) >= 8 {
			t.Errorf("告知ブロックに台詞らしい引用 %q がある — 全キャラの口調が寄る", quoted)
		}
	}
}

// extractQuoted は 「」 で囲まれた部分を抜き出す。
func extractQuoted(s string) []string {
	var out []string
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '\u300c' {
			continue
		}
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == '\u300d' {
				out = append(out, string(runes[i+1:j]))
				i = j
				break
			}
		}
	}
	return out
}

// time_warning トリガーは廃止した。独立した発話にすると、プレイヤーの手を
// 止めて無線を塞ぐだけで情報が増えない。
func TestTimeWarningTriggerRemoved(t *testing.T) {
	cfg := loadTestNavigator(t)
	if got := cfg.Prompt.Triggers["time_warning"]; got != "" {
		t.Errorf("time_warning が残っている: %q", got)
	}
}
