package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func loadTestNavigator(t *testing.T) *NavigatorConfig {
	t.Helper()
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	return cfg
}

// TestNavigatorConfigLoads は設定一式が読めることを確かめる。
func TestNavigatorConfigLoads(t *testing.T) {
	cfg := loadTestNavigator(t)

	// docs/navigator_design.md §2 の4キャラクター
	if len(cfg.Characters) != 4 {
		t.Fatalf("キャラクター数 = %d, want 4 (%v)", len(cfg.Characters), cfg.Names())
	}

	wantIDs := map[string]string{
		"owl": "フクロウ", "lark": "ヒバリ", "heron": "アオサギ", "nightjar": "ヨタカ",
	}
	for id, name := range wantIDs {
		c, ok := cfg.ByID(id)
		if !ok {
			t.Errorf("キャラクター %q が読み込まれていない", id)
			continue
		}
		if c.Name != name {
			t.Errorf("%s の名前 = %q, want %q", id, c.Name, name)
		}
		// 必須項目が埋まっていること
		if strings.TrimSpace(c.Sheet) == "" {
			t.Errorf("%s: sheet が空", id)
		}
		if c.UrgentStyle == "" {
			t.Errorf("%s: urgent_style が空", id)
		}
		if c.TTSVoice == "" || c.TTSStyle == "" {
			t.Errorf("%s: TTS設定が空", id)
		}
	}

	// プロンプト定義
	if !strings.Contains(cfg.Prompt.Role, "ナビゲーター") {
		t.Error("role に役割定義が入っていない")
	}
	if !strings.Contains(cfg.Prompt.Output, "出力ルール") {
		t.Error("output に出力ルールが入っていない")
	}
	if cfg.Prompt.UrgentThresholdMS != 60000 {
		t.Errorf("urgent_threshold_ms = %d, want 60000", cfg.Prompt.UrgentThresholdMS)
	}
}

// TestNavigatorPromptContentPreserved は外部ファイル化の前後で
// プロンプトの内容が変わっていないことを確かめる。
// 口調・ヒント制約の文言が欠けると挙動が変わるため、要点を照合する。
func TestNavigatorPromptContentPreserved(t *testing.T) {
	cfg := loadTestNavigator(t)

	// [A] 共通役割定義に含まれるべき要点 (docs/navigator_design.md §3.4)
	for _, want := range []string{
		"解除手順(正解)をすべて知っています",
		"許可ヒントレベル",
		"復唱して確認",
		"もう一度どうぞ",
		"見捨てず",
		"混線のせいにして受け流して",
	} {
		if !strings.Contains(cfg.Prompt.Role, want) {
			t.Errorf("role に %q が含まれていない", want)
		}
	}

	// [F] 出力ルールに含まれるべき要点。
	// 文数・文字数の上限は無線を塞がないための制約なので、指示が
	// 消えていないことを押さえる (docs/navigator_design.md)。
	for _, want := range []string{
		"2文以内", "60文字以内", "コールサイン", "どうぞ", "口調", "システム用語",
	} {
		if !strings.Contains(cfg.Prompt.Output, want) {
			t.Errorf("output に %q が含まれていない", want)
		}
	}
}

// TestNavigatorTriggerInstructions は発話トリガーの指示が
// 実装が使う全トリガー分そろっていることを確かめる (§3.5)。
func TestNavigatorTriggerInstructions(t *testing.T) {
	cfg := loadTestNavigator(t)

	// GameCoordinator / AudioPipeline が実際に渡すトリガー名
	for _, trigger := range []string{
		"session_start", "stage_cleared", "whack_completed", "push_progress",
		"wrong_action", "exploded", "defused", "player_message",
		"silence", "time_warning",
	} {
		got := cfg.Prompt.TriggerInstruction(trigger)
		if got == "" {
			t.Errorf("トリガー %q の指示が空", trigger)
		}
		if got == cfg.Prompt.Triggers["fallback"] {
			t.Errorf("トリガー %q が定義されておらず fallback になっている", trigger)
		}
	}

	// 未定義のトリガーは fallback になること
	if got := cfg.Prompt.TriggerInstruction("no_such_trigger"); got != cfg.Prompt.Triggers["fallback"] {
		t.Errorf("未定義トリガーが fallback にならない: %q", got)
	}
}

// TestNavigatorCharacterAssignment は割り当てが候補内から選ばれ、
// IDから復元できることを確かめる。
func TestNavigatorCharacterAssignment(t *testing.T) {
	cfg := loadTestNavigator(t)

	picked := map[string]bool{}
	for seed := int64(0); seed < 200; seed++ {
		c := cfg.Pick(rand.New(rand.NewSource(seed)))
		if _, ok := cfg.ByID(c.ID); !ok {
			t.Fatalf("候補外のキャラクターが選ばれた: %q", c.ID)
		}
		picked[c.ID] = true
	}
	// 十分な試行で全員が選ばれること (偏りがないことの確認)
	if len(picked) != len(cfg.Characters) {
		t.Errorf("選ばれたのは %d 種類のみ (全 %d 種類)", len(picked), len(cfg.Characters))
	}
}

// TestBuildPromptUsesConfig は組み立てたプロンプトに
// 設定ファイルの内容が反映されることを確かめる。
func TestBuildPromptUsesConfig(t *testing.T) {
	cfg := loadTestNavigator(t)
	lib := loadTestLibrary(t)

	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-test", difficultyNormal)
	if err != nil {
		t.Fatal(err)
	}

	character, _ := cfg.ByID("owl")
	prompt := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:      &cfg.Prompt,
		Character:   character,
		Session:     built,
		StageIndex:  0,
		RemainingMS: 120000,
		HintLevel:   HintL1,
	})

	// [A][B][F] が入っていること
	for _, want := range []string{
		"危険物解体ゲーム",        // role
		"フクロウ",            // キャラシート
		"出力ルール",           // output
		"許可ヒントレベル",        // ヒントポリシー
		"正解(あなただけが知っている)", // セッション状態
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("プロンプトに %q が含まれていない", want)
		}
	}

	// 通常時は緊迫スタイルにならないこと
	if strings.Contains(prompt, "交信スタイル: 緊迫") {
		t.Error("残り120秒で緊迫スタイルになっている")
	}

	// 残り時間が閾値を切ると緊迫スタイルになること
	urgent := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:      &cfg.Prompt,
		Character:   character,
		Session:     built,
		StageIndex:  0,
		RemainingMS: 30000,
		HintLevel:   HintL1,
	})
	if !strings.Contains(urgent, "交信スタイル: 緊迫") {
		t.Error("残り30秒で緊迫スタイルになっていない")
	}
	if !strings.Contains(urgent, character.UrgentStyle) {
		t.Error("緊迫時の崩し方が含まれていない")
	}
}

// TestNavigatorMaxRunesMatchesPrompt は発話長の上限が
// プロンプトの指示とサーバー側の警告閾値で一致していることを確かめる。
//
// 片方だけ変えると、プロンプトは守られているのに WARN が出続ける
// (またはその逆) という分かりにくい状態になる。
func TestNavigatorMaxRunesMatchesPrompt(t *testing.T) {
	cfg := loadTestNavigator(t)

	want := fmt.Sprintf("%d文字以内", navigatorMaxRunes)
	if !strings.Contains(cfg.Prompt.Output, want) {
		t.Errorf("output に %q が含まれていない (navigatorMaxRunes=%d と揃えること)",
			want, navigatorMaxRunes)
	}
}
