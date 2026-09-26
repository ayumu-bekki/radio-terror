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

// TestPromptHidesSecretWord は、モールスの課題 (202・302) で正解の単語が
// プロンプトのどこにも入らないことを確かめる (決定142)。
// 知っていると、1文字違いの読み違いを正解へ読み替えて教えた (実運用)。
func TestPromptHidesSecretWord(t *testing.T) {
	lib := loadTestLibrary(t)
	for _, id := range []string{"202", "302"} {
		for seed := int64(0); seed < 20; seed++ {
			built, err := simBuildStage(lib, testMissionSheet(), id, seed)
			if err != nil {
				t.Fatal(id, err)
			}
			word := built.Stages[0].Navigator["secret_word"]
			if word == "" {
				t.Fatalf("%s: secret_word が無い", id)
			}
			// 資料1の表には26語すべてが載る (決定143)。伏せるのは「どれが正解か」なので、
			// 表を除いた部分 (正解欄・進め方) に単語が出ないことを見る
			stage := *built.Stages[0]
			stage.Navigator = map[string]string{}
			for k, v := range built.Stages[0].Navigator {
				if k != "morse_sheet" {
					stage.Navigator[k] = v
				}
			}
			text := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt:  &NavigatorPromptConfig{},
				Session: &BuiltSession{Stages: []*BuiltStage{&stage}},
			})
			// 1文字 (202) は文章に紛れるので、単独の文字として出ていないかで見る
			if (len(word) > 1 && strings.Contains(text, word)) ||
				(len(word) == 1 && redactSecretWord(text, word) != text) {
				t.Errorf("%s seed=%d: 正解の単語 %q がプロンプトに入っている", id, seed, word)
			}
			if !strings.Contains(text, redactedWordMark) {
				t.Errorf("%s seed=%d: 伏せ字が無い", id, seed)
			}
		}
	}
}

// TestMorseSheetBlockHasNoColors は、ナビへ渡す資料1の表に色が入らず、
// 出題候補の単語がすべて載っていることを確かめる (決定143)。
// 色まで渡すと、ナビが候補に挙げた単語から切る線の色が決まってしまう。
func TestMorseSheetBlockHasNoColors(t *testing.T) {
	block := morseSheetBlock("資料1")
	for _, name := range colorNameJA {
		if strings.Contains(block, name) {
			t.Errorf("資料1の表に色名 %q が入っている", name)
		}
	}
	lib := loadTestLibrary(t)
	for _, id := range []string{"202", "302"} {
		for seed := int64(0); seed < 30; seed++ {
			built, err := simBuildStage(lib, testMissionSheet(), id, seed)
			if err != nil {
				t.Fatal(id, err)
			}
			word := built.Stages[0].Navigator["secret_word"]
			inBlock := strings.Contains(block, " "+word+"\n")
			if len(word) == 1 {
				inBlock = strings.Contains(block, "- "+word+" ")
			}
			if !inBlock {
				t.Errorf("%s: 出題語 %q が資料1の表に無い", id, word)
			}
		}
	}
	// 表の頭文字は A-Z の順で26行
	for i, r := range morseSheetRows {
		if r.letter != string(rune('A'+i)) || !strings.HasPrefix(r.word, r.letter) {
			t.Errorf("行 %d: %v", i, r)
		}
	}
}
