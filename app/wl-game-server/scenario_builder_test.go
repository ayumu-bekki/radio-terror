package main

import (
	"encoding/json"
	"math/rand"
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
