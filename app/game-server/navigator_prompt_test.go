package main

import (
	"strings"
	"testing"
)

// stdLoad は難易度ごとの入力量のテスト値 (ノーマル相当)。
// 未設定 (0) だと「押す回数0回」の成立しないステージになるため、
// テストでも必ず渡す (docs/scenario_design.md §4.2)。
var stdLoad = LoadRule{
	ColorMatchMin:        6,
	ColorMatchMax:        9,
	ForbiddenRotaryCount: 1,
	PushSeqLen:           5,
	SpeedRankMS:          []int{150, 300, 550, 1000},
}

// TestPromptNeverContainsCutColor は、切る線の色名が**どの場面でも**プロンプトに
// 入らないことを確かめる (決定40・決定129)。
//
// 以前は L4 (直言) でだけ answer を生のまま載せていたが、ヒントレベルごと
// 廃止した。目の前にある語はなぞられる (ADR N-1) ので、機械的に伏せる。
func TestPromptNeverContainsCutColor(t *testing.T) {
	stage := &BuiltStage{
		TemplateID: "205",
		Cut:        "E",
		Navigator: map[string]string{
			"answer":    "正解は白色の線を切ること",
			"procedure": "よく観察するよう促す",
		},
	}
	text := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:  &NavigatorPromptConfig{},
		Session: &BuiltSession{Stages: []*BuiltStage{stage}},
	})
	if strings.Contains(text, "白") {
		t.Errorf("切る線の色名 (白) がプロンプトに入っている:\n%s", text)
	}
	if !strings.Contains(text, redactedColorMark) {
		t.Errorf("伏せ字が無い — answer が載っていないか、伏せ方が変わった:\n%s", text)
	}
}

// TestMustSayReachesPrompt は `must_say` がプロンプトへ**独立したブロック**で
// 届くことを確かめる (ADR N-51)。
//
// 進め方に「必ず両方伝える」と書くだけでは、指針が長くなるほど
// **末尾の項目が落ちる** (204 色合わせ で8回中2回欠けた)。
// 散文の但し書きではなく単独の要求として置き直したので、
// **その配置が壊れていないこと**を回帰で見る。
func TestMustSayReachesPrompt(t *testing.T) {
	const mustSay = "最後に押した色を覚えておくこと"

	stage := &BuiltStage{
		TemplateID: "204",
		Navigator: map[string]string{
			"answer":    "正解は緑色の線を切ること",
			"must_say":  mustSay,
			"procedure": "観察を促す",
		},
	}
	text := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:  &NavigatorPromptConfig{},
		Session: &BuiltSession{Stages: []*BuiltStage{stage}},
	})

	if !strings.Contains(text, mustSay) {
		t.Errorf("must_say がプロンプトに無い:\n%s", text)
	}
	if !strings.Contains(text, "この課題で必ず言うこと") {
		t.Errorf("must_say の見出しが無い — 独立ブロックになっていない:\n%s", text)
	}
	// 見出しの直後に本文が来ていること (離れると埋もれる)。
	head := strings.Index(text, "この課題で必ず言うこと")
	body := strings.Index(text, mustSay)
	if head < 0 || body < head || body-head > 200 {
		t.Errorf("must_say が見出しから離れすぎ (head=%d body=%d)", head, body)
	}
}

// TestMustSayAbsentWhenUnset は `must_say` を書いていないステージで
// 見出しごと出ないことを確かめる。全ステージに出すと埋もれて意味が薄れる。
func TestMustSayAbsentWhenUnset(t *testing.T) {
	stage := &BuiltStage{
		TemplateID: "101",
		Navigator:  map[string]string{"answer": "正解は赤色の線を切ること"},
	}
	text := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:  &NavigatorPromptConfig{},
		Session: &BuiltSession{Stages: []*BuiltStage{stage}},
	})
	if strings.Contains(text, "この課題で必ず言うこと") {
		t.Errorf("must_say が無いのに見出しが出た:\n%s", text)
	}
}

// TestStagesHaveNoHintLevelFields は、ステージ定義からヒントレベル関連の
// 項目が消えていることを確かめる (決定129)。
//
// hint_l1〜hint_l3 はもうプロンプトへ渡らない。残っていると、
// 書き足しても効かない文を編集することになる。
func TestStagesHaveNoHintLevelFields(t *testing.T) {
	lib := loadTestLibrary(t)
	for _, id := range lib.StageIDs() {
		stage, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		for _, key := range []string{"hint_l1", "hint_l2", "hint_l3", "observation"} {
			if _, ok := stage.Navigator[key]; ok {
				t.Errorf("%s: navigator.%s が残っている (ヒントレベルは廃止した)", id, key)
			}
		}
		if stage.Navigator["procedure"] == "" {
			t.Errorf("%s: procedure が無い (進め方は procedure に一本化した)", id)
		}
	}
}
