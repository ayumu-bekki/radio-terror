package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// testMissionSheet は紙資料の物理定数のテスト値 (docs/puzzle_stage_ideas.md §7)。
func testMissionSheet() MissionSheet {
	return MissionSheet{
		SymbolCount: 28,
		NumberSum:   47,
		TerminalMap: map[string]string{
			"T1": "A", "T2": "B", "T3": "C", "T4": "D", "T5": "E",
		},
	}
}

func loadTestLibrary(t *testing.T) *ScenarioLibrary {
	t.Helper()
	lib, err := LoadScenarioLibrary("scenarios")
	if err != nil {
		t.Fatalf("LoadScenarioLibrary: %v", err)
	}
	return lib
}

// TestBuildAllDifficulties は3難易度すべてを多数回組み立て、
// 抽選のどの分岐でも検証を通ることを確かめる。
func TestBuildAllDifficulties(t *testing.T) {
	lib := loadTestLibrary(t)

	expectedStages := map[string]int{
		difficultyEasy:   2,
		difficultyNormal: 3,
		difficultyHard:   4,
	}

	for _, difficulty := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		for seed := int64(0); seed < 200; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))

			session, err := builder.Build("s-test", difficulty)
			if err != nil {
				t.Fatalf("%s seed=%d: Build: %v", difficulty, seed, err)
			}

			if got, want := len(session.Stages), expectedStages[difficulty]; got != want {
				t.Fatalf("%s seed=%d: stage count = %d, want %d", difficulty, seed, got, want)
			}

			// 切断線がステージ間で重複していないこと
			seen := make(map[string]bool)
			for _, stage := range session.Stages {
				if seen[stage.Cut] {
					t.Fatalf("%s seed=%d: duplicated cut %q", difficulty, seed, stage.Cut)
				}
				seen[stage.Cut] = true
			}

			// session_start がJSONとして組み立てられること
			payload := session.SessionStartPayload("3701")
			if _, err := json.Marshal(payload); err != nil {
				t.Fatalf("%s seed=%d: marshal payload: %v", difficulty, seed, err)
			}
		}
	}
}

// TestEasyStartsWithTutorial はイージーの先頭が 01 固定であることを確かめる。
func TestEasyStartsWithTutorial(t *testing.T) {
	lib := loadTestLibrary(t)

	for seed := int64(0); seed < 50; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		session, err := builder.Build("s-test", difficultyEasy)
		if err != nil {
			t.Fatalf("seed=%d: Build: %v", seed, err)
		}
		if session.Stages[0].TemplateID != "101" {
			t.Fatalf("seed=%d: easy head = %q, want 101", seed, session.Stages[0].TemplateID)
		}
	}
}

// TestEasyOnlyStageExcluded は 01 がノーマル・ハードで選出されないことを確かめる。
func TestEasyOnlyStageExcluded(t *testing.T) {
	lib := loadTestLibrary(t)

	for _, difficulty := range []string{difficultyNormal, difficultyHard} {
		for seed := int64(0); seed < 100; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			session, err := builder.Build("s-test", difficulty)
			if err != nil {
				t.Fatalf("%s seed=%d: Build: %v", difficulty, seed, err)
			}
			for _, stage := range session.Stages {
				if stage.TemplateID == "101" {
					t.Fatalf("%s seed=%d: easy_only stage 101 selected", difficulty, seed)
				}
			}
		}
	}
}

// TestNavigatorKnowledgeMatchesCore は全ステージ定義について、
// ナビゲーター知識の正解と Core向けJSON の cut が一致することを確かめる。
// この食い違いはゲームで最も致命的なバグなので、テンプレート単位で網羅確認する。
func TestNavigatorKnowledgeMatchesCore(t *testing.T) {
	lib := loadTestLibrary(t)

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}

		for seed := int64(0); seed < 60; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))

			built, err := builder.buildStage(stageTmpl, map[string]bool{})
			if err != nil {
				t.Fatalf("stage %s seed=%d: buildStage: %v", id, seed, err)
			}

			if !answerMentionsCut(built.Navigator["answer"], built.Cut) {
				t.Fatalf("stage %s seed=%d: answer %q does not mention cut %q",
					id, seed, built.Navigator["answer"], built.Cut)
			}

			// 未解決の ${...} が残っていないこと
			if err := checkUnresolved("core", built.Core); err != nil {
				t.Fatalf("stage %s seed=%d: %v", id, seed, err)
			}
			for key, text := range built.Navigator {
				if varPattern.MatchString(text) {
					t.Fatalf("stage %s seed=%d: unresolved in navigator.%s: %s", id, seed, key, text)
				}
			}
		}
	}
}

// TestMorseWordColorMapping はモールス対照表の循環割当を確かめる
// (A=A, B=B, C=C, D=D, E=E, F=A, G=B, ...)。
func TestMorseWordColorMapping(t *testing.T) {
	cases := map[string]string{
		"ALFA":    "A",
		"BRAVO":   "B",
		"CHARLIE": "C",
		"DELTA":   "D",
		"ECHO":    "E",
		"FOXTROT": "A",
		"GOLF":    "B",
		"JULIETT": "E",
	}
	for word, want := range cases {
		if got := morseWordColor(word); got != want {
			t.Errorf("morseWordColor(%q) = %q, want %q", word, got, want)
		}
	}
}

// TestLedKeyExpansion は ${rest} のような複数色キーが各色へ展開されることを確かめる
// (207 仲間はずれの leds 指定)。
func TestLedKeyExpansion(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("207")
	if err != nil {
		t.Fatalf("Stage(207): %v", err)
	}

	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.buildStage(stageTmpl, map[string]bool{})
	if err != nil {
		t.Fatalf("buildStage: %v", err)
	}

	leds, ok := built.Core["leds"].(map[string]any)
	if !ok {
		t.Fatalf("leds is not a table: %T", built.Core["leds"])
	}
	// 5色すべてに点滅指定が入っていること
	if len(leds) != len(allColors) {
		t.Fatalf("leds has %d entries, want %d: %v", len(leds), len(allColors), leds)
	}
	for _, color := range allColors {
		if _, ok := leds[color]; !ok {
			t.Errorf("leds missing color %q", color)
		}
	}
}

// TestCutLineNotReusedAcrossStages はハードで切断線が重複せず、
// かつ**必ず1色以上余る**ことを確かめる。
//
// 全色を使い切ると切断線の抽選に余地が無くなり、色の制約を持つステージ
// (203 暗号電文) が組み立て不能になるため、余りを保証する
// (docs/puzzle_stage_ideas.md §5)。
func TestCutLineNotReusedAcrossStages(t *testing.T) {
	lib := loadTestLibrary(t)

	for seed := int64(0); seed < 200; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		session, err := builder.Build("s-test", difficultyHard)
		if err != nil {
			t.Fatalf("seed=%d: Build: %v", seed, err)
		}

		var cuts []string
		for _, stage := range session.Stages {
			cuts = append(cuts, stage.Cut)
		}

		// 重複が無いこと
		joined := strings.Join(cuts, "")
		for _, color := range allColors {
			if n := strings.Count(joined, color); n > 1 {
				t.Fatalf("seed=%d: color %q used %d times (cuts=%v)", seed, color, n, cuts)
			}
		}

		// 1色以上余っていること
		if len(cuts) >= len(allColors) {
			t.Fatalf("seed=%d: all %d colors consumed (cuts=%v); at least one must remain",
				seed, len(cuts), cuts)
		}
	}
}

// TestMorseStageWorksWithAnyRemainingColor は 203 暗号電文が、
// どの色が1つだけ残っている状況でも組み立てられることを確かめる。
//
// 候補語が5色すべてをカバーしているため、割り当て順を特別扱いしなくても
// 詰まないことを保証する。
func TestMorseStageWorksWithAnyRemainingColor(t *testing.T) {
	lib := loadTestLibrary(t)

	stageTmpl, err := lib.Stage("203")
	if err != nil {
		t.Fatalf("Stage(203): %v", err)
	}

	for _, remaining := range allColors {
		// remaining 以外の全色を使用済みにする
		usedLines := make(map[string]bool)
		for _, color := range allColors {
			if color != remaining {
				usedLines[color] = true
			}
		}

		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
		built, err := builder.buildStage(stageTmpl, usedLines)
		if err != nil {
			t.Fatalf("remaining=%s: buildStage: %v", remaining, err)
		}
		if built.Cut != remaining {
			t.Errorf("remaining=%s: cut = %s, want %s", remaining, built.Cut, remaining)
		}
	}
}

// TestNoiseLedsAreSymmetric は 304 暗号電文・混信の妨害LEDが
// **対称blink(点灯時間=消灯時間)** になることを確かめる。
//
// モールスは短点=1単位・長点=3単位の非対称なリズムなので、妨害を対称に
// しておけば「これはモールスではない」と見分けられる。非対称になると
// 妨害がモールスに見えてしまい、意図した難度から外れる。
func TestNoiseLedsAreSymmetric(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("304")
	if err != nil {
		t.Fatalf("Stage(304): %v", err)
	}

	kinds := map[string]bool{}

	for seed := int64(0); seed < 100; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{})
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}

		leds, ok := built.Core["leds"].(map[string]any)
		if !ok {
			t.Fatalf("seed=%d: leds is not a table", seed)
		}
		// 5色すべてに指定があること (モールス1色 + 妨害4色)
		if len(leds) != len(allColors) {
			t.Fatalf("seed=%d: leds has %d entries, want %d", seed, len(leds), len(allColors))
		}

		morseCount := 0
		for color, spec := range leds {
			switch v := spec.(type) {
			case string:
				if v != "on" && v != "off" {
					t.Errorf("seed=%d %s: unexpected string spec %q", seed, color, v)
				}
				kinds[v] = true

			case map[string]any:
				switch v["pattern"] {
				case "morse":
					morseCount++
					kinds["morse"] = true
				case "blink":
					// 妨害の点滅は必ず対称であること
					on, off := v["on_ms"], v["off_ms"]
					if on != off {
						t.Errorf("seed=%d %s: 非対称blink on_ms=%v off_ms=%v (対称であるべき)",
							seed, color, on, off)
					}
					kinds["blink"] = true
				default:
					t.Errorf("seed=%d %s: unexpected pattern %v", seed, color, v["pattern"])
				}

			default:
				t.Errorf("seed=%d %s: unexpected spec type %T", seed, color, spec)
			}
		}

		// モールスは必ず1色だけ (妨害が表示用LEDを上書きしていないこと)
		if morseCount != 1 {
			t.Fatalf("seed=%d: モールス表示が %d 色 (1色であるべき)", seed, morseCount)
		}
	}

	// 十分な試行で3種類の妨害がすべて出ること
	for _, kind := range []string{"on", "off", "blink"} {
		if !kinds[kind] {
			t.Errorf("妨害の種類 %q が一度も生成されなかった", kind)
		}
	}
}

// TestSpeedRankingConsistency は 208 速さくらべで、
// **ナビゲーター知識の「速い順」の並びが実際のLED速度と一致する**ことを確かめる。
//
// 並びが実機とずれるとナビゲーターが誤った順位を伝え、プレイヤーが
// 理不尽に失敗する。この整合はテンプレートの書き方だけで保っているため、
// 変数の並び順を崩す編集を検出できるようにしておく。
func TestSpeedRankingConsistency(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("208")
	if err != nil {
		t.Fatalf("Stage(208): %v", err)
	}

	// answer の「速い順に X(最速) → Y → Z → W → V(最遅)」から並びを取る
	orderRe := regexp.MustCompile(`速い順に ([A-E])\(最速\) → ([A-E]) → ([A-E]) → ([A-E]) → ([A-E])\(最遅\)`)

	for seed := int64(0); seed < 100; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{})
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}

		m := orderRe.FindStringSubmatch(built.Navigator["answer"])
		if m == nil {
			t.Fatalf("seed=%d: answer から速い順を読み取れない: %s", seed, built.Navigator["answer"])
		}
		order := m[1:6]

		leds, ok := built.Core["leds"].(map[string]any)
		if !ok || len(leds) != len(allColors) {
			t.Fatalf("seed=%d: leds が5色分ない", seed)
		}

		// answer の並びどおりに速度が単調増加していること (速い→遅い)
		prev := -1
		for rank, color := range order {
			spec, ok := leds[color].(map[string]any)
			if !ok {
				t.Fatalf("seed=%d: %s のLED指定がない", seed, color)
			}
			onMS, err := toInt(spec["on_ms"])
			if err != nil {
				t.Fatalf("seed=%d: %s の on_ms: %v", seed, color, err)
			}
			// 対称blinkであること
			if spec["off_ms"] != spec["on_ms"] {
				t.Errorf("seed=%d %s: 非対称blink on=%v off=%v", seed, color, spec["on_ms"], spec["off_ms"])
			}
			if onMS <= prev {
				t.Fatalf("seed=%d: %d番目の%s (%dms) が前順位 (%dms) より速くない — answerの並びと実速度が不一致",
					seed, rank+1, color, onMS, prev)
			}
			prev = onMS
		}

		// cut がこの5色のいずれかであること
		found := false
		for _, color := range order {
			if color == built.Cut {
				found = true
			}
		}
		if !found {
			t.Errorf("seed=%d: cut %q が並びに含まれない", seed, built.Cut)
		}
	}
}

// toInt は TOML/JSON 由来の数値 (int / int64 / float64) を int にそろえる。
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("数値でない: %T", v)
	}
}

// TestAsLineExcludesUsedLines は 208 速さくらべで、
// **既に切られた線が正解に選ばれない**ことを確かめる。
//
// 5色すべてを点滅させるステージでは `pick = "color"` の重複制約が効かないため、
// `choice` の `as_line` で明示的に除外している。表示(5色)は狭まらず、
// 正解だけが未使用の線に限られる。
func TestAsLineExcludesUsedLines(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("208")
	if err != nil {
		t.Fatalf("Stage(208): %v", err)
	}

	// A・B・D を使用済みにする
	used := map[string]bool{"A": true, "B": true, "D": true}

	for seed := int64(0); seed < 50; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, used)
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}

		if used[built.Cut] {
			t.Fatalf("seed=%d: 既に切られた色 %q が正解になった", seed, built.Cut)
		}

		// 表示は5色すべて出ていること (正解が狭まっても見た目は変わらない)
		leds, ok := built.Core["leds"].(map[string]any)
		if !ok || len(leds) != len(allColors) {
			t.Fatalf("seed=%d: LEDが5色分ない (%d色)", seed, len(leds))
		}
	}

	// 残り1色でも組み立てられること
	only := map[string]bool{"A": true, "B": true, "C": true, "D": true}
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.buildStage(stageTmpl, only)
	if err != nil {
		t.Fatalf("残り1色: buildStage: %v", err)
	}
	if built.Cut != "E" {
		t.Errorf("残り1色なのに cut=%q (E であるべき)", built.Cut)
	}
}

// TestMorseWordIsAlwaysString は morse の word が必ず文字列になることを確かめる。
//
// 抽選変数の展開では数字だけの文字列が数値へ変換される (rotary などの数値
// フィールドのため)。しかし Core 側は morse の word に `cJSON_IsString` を
// 要求するため (§6.1)、数字を表示するステージ (211) で数値のまま渡すと
// 実機が session_rejected を返す。ビルドもサーバー側検証も通ってしまい、
// 実機で初めて発覚する類の不具合なのでテストで固定する。
func TestMorseWordIsAlwaysString(t *testing.T) {
	lib := loadTestLibrary(t)

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		for seed := int64(0); seed < 30; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{})
			if err != nil {
				t.Fatalf("stage %s seed=%d: %v", id, seed, err)
			}

			leds, ok := built.Core["leds"].(map[string]any)
			if !ok {
				continue
			}
			for color, spec := range leds {
				table, ok := spec.(map[string]any)
				if !ok || table["pattern"] != "morse" {
					continue
				}
				if _, isString := table["word"].(string); !isString {
					t.Errorf("stage %s seed=%d %s: morse.word が文字列でない: %T (%v)",
						id, seed, color, table["word"], table["word"])
				}
			}
		}
	}
}

// TestPickWordByColorRespectsExclude は「語→色」の抽選 (203/308) が
// exclude 指定と使用済みの線の**両方**を尊重することを確かめる。
//
// 統合前は morse_word だけ exclude を見ておらず、片方だけ効かない状態だった。
func TestPickWordByColorRespectsExclude(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))

	cases := []struct {
		name       string
		candidates []any
		colorOf    func(string) string
	}{
		{"morse_word", []any{"ALFA", "BRAVO", "MIKE"}, morseWordColor}, // A, B, C
		{"romaji_word", []any{"AKA", "KI", "MIDORI"}, romajiColor},     // A, B, C
	}

	for _, c := range cases {
		def := map[string]any{"candidates": c.candidates}

		// exclude で A を外す → A に対応する語が選ばれないこと
		for i := 0; i < 30; i++ {
			got, err := builder.pickWordByColor(def, map[string]string{},
				map[string]bool{}, map[string]bool{"A": true}, c.colorOf, c.name)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if c.colorOf(got) == "A" {
				t.Errorf("%s: exclude された色 A の語 %q が選ばれた", c.name, got)
			}
		}

		// usedLines で B を外す → B に対応する語が選ばれないこと
		for i := 0; i < 30; i++ {
			got, err := builder.pickWordByColor(def, map[string]string{},
				map[string]bool{"B": true}, map[string]bool{}, c.colorOf, c.name)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if c.colorOf(got) == "B" {
				t.Errorf("%s: 使用済みの色 B の語 %q が選ばれた", c.name, got)
			}
		}

		// 全色を塞ぐとエラーになること
		if _, err := builder.pickWordByColor(def, map[string]string{},
			map[string]bool{"A": true, "B": true, "C": true},
			map[string]bool{}, c.colorOf, c.name); err == nil {
			t.Errorf("%s: 候補が尽きてもエラーにならない", c.name)
		}
	}
}
