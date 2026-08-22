package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
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
//
// 304 は現在無効化中 (.toml.disabled)。noise_leds の実装自体は残っているため、
// 復活させたときに壊れていないよう検証も残し、未ロード時はスキップする。
func TestNoiseLedsAreSymmetric(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("304")
	if err != nil {
		t.Skip("304 は無効化中 (.toml.disabled): noise_leds の検証をスキップ")
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
// **ナビゲーター知識の「速い順」の並びが実際のLED速度と一致する**ことと、
// **正解が必ず最速である**ことを確かめる。
//
// 並びが実機とずれるとナビゲーターが誤った順位を伝え、プレイヤーが
// 理不尽に失敗する。この整合はテンプレートの書き方だけで保っているため、
// 変数の並び順を崩す編集を検出できるようにしておく。
//
// 正解が最速から外れると answer が「○番目に速い色」を指す形になり、
// ナビゲーターが色名を言えなくなる (213 光の長さを無効化したのと同じ問題。
// docs/puzzle_stage_ideas.md §5)。ここが回帰の要。
func TestSpeedRankingConsistency(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("208")
	if err != nil {
		t.Fatalf("Stage(208): %v", err)
	}

	// answer の「速い順は X(最速) → Y → Z → W(最遅)」から点滅4色の並びを取る。
	// ナビゲーター知識は無線で読み上げるため**日本語色名**で展開される
	// (Core向けJSON は A-E のまま)。
	orderRe := regexp.MustCompile(
		`速い順は (.)\(最速\) → (.) → (.) → (.)\(最遅\)`)
	// 点灯しっぱなしの基準色 (数に入れない色)
	steadyRe := regexp.MustCompile(`点灯しっぱなしなのは(.)色`)

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
		// 日本語色名を色コードへ戻して、leds (A-E キー) と突き合わせる
		order := make([]string, 0, 4)
		for _, name := range m[1:5] {
			code, ok := colorCodeFromJA(name)
			if !ok {
				t.Fatalf("seed=%d: 未知の色名 %q (answer=%s)", seed, name, built.Navigator["answer"])
			}
			order = append(order, code)
		}

		leds, ok := built.Core["leds"].(map[string]any)
		if !ok || len(leds) != len(allColors) {
			t.Fatalf("seed=%d: leds が5色分ない", seed)
		}

		// 基準色は点灯しっぱなし ("on") であること。
		// 点滅させてしまうと「数に入れない色」が見分けられなくなる。
		sm := steadyRe.FindStringSubmatch(built.Navigator["answer"])
		if sm == nil {
			t.Fatalf("seed=%d: answer から基準色を読み取れない: %s", seed, built.Navigator["answer"])
		}
		steady, ok := colorCodeFromJA(sm[1])
		if !ok {
			t.Fatalf("seed=%d: 未知の基準色名 %q", seed, sm[1])
		}
		if leds[steady] != "on" {
			t.Errorf("seed=%d: 基準色 %s が点灯しっぱなしでない: %v", seed, steady, leds[steady])
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

		// **正解は必ず最速**であること (中間順位に置くと色名で救済できなくなる)
		if built.Cut != order[0] {
			t.Fatalf("seed=%d: cut %q が最速 %q でない — ナビゲーターが色名を言えなくなる",
				seed, built.Cut, order[0])
		}
		// 基準色を正解にしない (点灯 = 正解 とプレイヤーが誤学習する)
		if built.Cut == steady {
			t.Errorf("seed=%d: 基準色 %q が正解になった", seed, built.Cut)
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
// 208 は5色すべてを表示に使う (基準1色 + 点滅4色) ため、正解だけは
// 未使用の線に限る必要がある。cut を `pick = "line"` で**先に**引き、
// 残りの表示色をそこから除外して埋めることで成立させている。
//
// かつては表示色を先に決めて `choice` の `as_line` で cut を選んでいたが、
// 候補が両端2色しかないため、その2色が両方とも使用済みだと組み立てが
// 失敗した (セッション後半で実際に再現)。cut を先に引く形はこの失敗が起きない。
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

// colorCodeFromJA は日本語色名を色コード (A-E) へ戻す。
// ナビゲーター知識は日本語で展開されるため、Core向けJSON と突き合わせる際に使う。
func colorCodeFromJA(name string) (string, bool) {
	for code, ja := range colorNameJA {
		if ja == name {
			return code, true
		}
	}
	return "", false
}

// TestTutorialStageHasNoForbiddenRotary は 101 に禁止位置が無いことを確かめる。
//
// 101 は「無線で報告し、指示を受けて操作する」交信の型を覚えるステージ。
// 罠を仕込むと最初の一手で失敗して萎縮させるため、禁止位置は置かない
// (禁止位置の緊張感は 209 綱渡りが担う)。
func TestTutorialStageHasNoForbiddenRotary(t *testing.T) {
	lib := loadTestLibrary(t)

	checked := 0
	for seed := int64(0); seed < 50; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))

		session, err := builder.Build("s-test", difficultyEasy)
		if err != nil {
			t.Fatalf("seed=%d: Build: %v", seed, err)
		}

		for _, stage := range session.Stages {
			if stage.TemplateID != "101" {
				continue
			}
			checked++
			if _, ok := stage.Core["forbidden_rotary"]; ok {
				t.Fatalf("seed=%d: 101 に forbidden_rotary が設定されている", seed)
			}
		}
	}

	if checked == 0 {
		t.Fatal("101 が1度も選出されなかった (easy 先頭固定のはず)")
	}
}

// TestTutorialProcedureUsesDrawnPath は 101 の procedure が
// 抽選された経路 (via1/final_rotary) で書かれていることを確かめる。
//
// かつて procedure に中間経路を固定値で書いていたところ、その値が
// 危険位置の抽選と衝突し「そこへ回せ」と「そこは危険」を同時に言う
// 矛盾が起きた。禁止位置は廃止したが、**手順の値を固定値で書かない**
// という原則は残す (毎回同じ手順になるのを避ける意味もある)。
func TestTutorialProcedureUsesDrawnPath(t *testing.T) {
	lib := loadTestLibrary(t)
	digits := regexp.MustCompile(`[0-9]`)

	// seed ごとに procedure が変わること (固定値なら常に同じになる)
	seen := map[string]bool{}
	checked := 0

	for seed := int64(0); seed < 100; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))

		session, err := builder.Build("s-test", difficultyEasy)
		if err != nil {
			t.Fatalf("seed=%d: Build: %v", seed, err)
		}

		for _, stage := range session.Stages {
			if stage.TemplateID != "101" {
				continue
			}
			checked++

			procedure := stage.Navigator["procedure"]
			if procedure == "" {
				t.Fatalf("seed=%d: procedure が空", seed)
			}
			// 未解決の変数が残っていないこと
			if strings.Contains(procedure, "${") {
				t.Fatalf("seed=%d: procedure に未解決の変数が残っている: %q", seed, procedure)
			}
			seen[strings.Join(digits.FindAllString(procedure, -1), ",")] = true
		}
	}

	if checked == 0 {
		t.Fatal("101 が1度も選出されなかった (easy 先頭固定のはず)")
	}
	if len(seen) < 2 {
		t.Errorf("procedure の数値が %d 種類しかない (固定値で書かれている疑い)", len(seen))
	}
}

// TestDistinctLedRolesNotCollapsed は「役割の違うLEDに別々の色を割り当てる」
// ステージで、**抽選次第で同色になって表示が潰れない**ことを確かめる。
//
// leds はテーブルなので同じ色を2回指定すると1エントリに畳まれ、後勝ちで
// 上書きされる (点灯が点滅で潰れる)。103 ホールド&カットで hold と cut が
// 同色になると「点滅=押すボタン / 点灯=切る線」の対応が画面に現れず、
// 104 コール&レスポンスで lit と cut が同色になると
// 「点灯を報告 → 点滅を切る」という対話そのものが成立しない。
// どちらも**抽選の2割前後で発生**していた (実プレイで発覚)。
func TestDistinctLedRolesNotCollapsed(t *testing.T) {
	lib := loadTestLibrary(t)

	// ステージID → 常に出るべき leds のエントリ数
	want := map[string]int{
		"103": 2, // 点滅(押すボタン) + 点灯(切る線)
		"104": 2, // 点灯(報告させる色) + 点滅(切る線)
		"306": 3, // 点滅2(押さえる2色) + 点灯1(切る線)
		"307": 2, // 点滅(押さえるボタン) + 点灯(切る線)
	}

	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%s): %v", id, err)
		}
		for seed := int64(0); seed < 300; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{})
			if err != nil {
				t.Fatalf("%s seed=%d: buildStage: %v", id, seed, err)
			}
			leds, ok := built.Core["leds"].(map[string]any)
			if !ok {
				t.Fatalf("%s seed=%d: leds is not a table", id, seed)
			}
			if len(leds) != want[id] {
				t.Fatalf("%s seed=%d: leds が %d エントリ (want %d) — "+
					"役割の違う色が同色に抽選され、表示が潰れている",
					id, seed, len(leds), want[id])
			}
		}
	}
}

// TestUnobservableInfoRevealedAtL1 は「**装置を見ても分からない情報**」を持つ
// ステージが、L1 の時点でそれを伝えるようになっていることを確かめる。
//
// ボタンを押す順番 (102/201/305) や危険なダイヤル位置 (209) は装置に一切現れず、
// ナビゲーターしか知らない。これを伏せるとプレイヤーは観察でも推理でも
// たどり着けず、無線が「順番を教えて」の往復で浪費されるだけになる
// (102 の実プレイで発生。開始から約1分間、色が一切伝わらなかった)。
// 209 に至っては、知らずに踏むと即爆発するので罰としても理不尽。
//
// 伏せてよいのは**推理できる情報**だけ。ヒント段階で調整するのは
// 「一度に何色まで言うか」であって、言うか言わないかではない。
func TestUnobservableInfoRevealedAtL1(t *testing.T) {
	lib := loadTestLibrary(t)

	// ステージID → hint_l1 に必ず現れるべき抽選変数
	want := map[string][]string{
		"102": {"p1"},                         // 1色目は必ず伝える
		"201": {"p1", "p2", "p3", "p4", "p5"}, // 列は最初から読み上げる
		"305": {"p1", "p2", "p3", "p4", "p5"}, // 1周目は読み上げる (2周目は伏せてよい)
		"209": {"forbidden"},                  // 危険位置は先に警告する
	}

	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%s): %v", id, err)
		}
		// テンプレート段階で ${変数} が hint_l1 に書かれているかを見る
		// (展開後の値は他の語と紛れるため、定義そのものを検証する)
		hintL1 := stageTmpl.Navigator["hint_l1"]
		for _, v := range want[id] {
			ref := "${" + v + "}"
			if !strings.Contains(hintL1, ref) {
				t.Errorf("%s: hint_l1 が %s を伝えていない — "+
					"装置を見ても分からない情報を伏せるとプレイヤーが手詰まりになる:\n  %s",
					id, ref, hintL1)
			}
		}

		// 展開しても未解決の変数が残らないこと
		for seed := int64(0); seed < 20; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{})
			if err != nil {
				t.Fatalf("%s seed=%d: buildStage: %v", id, seed, err)
			}
			if varPattern.MatchString(built.Navigator["hint_l1"]) {
				t.Fatalf("%s seed=%d: hint_l1 に未解決の変数: %s",
					id, seed, built.Navigator["hint_l1"])
			}
		}
	}
}

// TestStagesAskForLampReportFirst は、全ステージの hint_l1 が
// 「まずランプの状態を報告させる」形で始まっていることを確かめる。
//
// 実プレイのログで、ナビゲーターが装置を見ないうちから手順
// (ボタンの順番・ダイヤルの位置・点滅の速さ) を話し始めていた。
// 原因は hint_l1 の多くが「〜に気づかせる」「〜を確認させる」と
// **観察の結果だけ**を書いており、生成AIがそれを「自分で言う」と
// 解釈していたこと。「報告させる」と**動作**で書く必要がある
// (docs/navigator_design.md 決定32)。
//
// 装置を見ても分からない情報を先出しするステージ (102/201/209/305) も、
// ランプの確認自体は省かない。
func TestStagesAskForLampReportFirst(t *testing.T) {
	lib := loadTestLibrary(t)

	// 「報告させる」ことを求める語。いずれかが hint_l1 にあればよい。
	askForms := []string{"報告させ", "尋ね", "確認を兼ねる"}

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		hintL1 := stageTmpl.Navigator["hint_l1"]
		if hintL1 == "" {
			t.Errorf("%s: hint_l1 が空", id)
			continue
		}

		found := false
		for _, form := range askForms {
			if strings.Contains(hintL1, form) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: hint_l1 がプレイヤーに報告させる形になっていない — "+
				"装置を見る前に手順を話し始める原因になる。%v のいずれかを含めること:\n  %s",
				id, askForms, hintL1)
		}
	}
}

// TestSheetSectionResolves は 202 の区画見出しが両分岐で解決することを確かめる。
//
// シートには「区画A」(記号) /「区画B」(数字) を印刷してあり、ナビゲーターは
// 「区画Aを見ろ」と一言で指示する (docs/printed_materials.md §3.2.1)。
// 配線色の記号 (A-E) とは別物なので、必ず「区画」を伴う。
func TestSheetSectionResolves(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("202")
	if err != nil {
		t.Fatalf("Stage(202): %v", err)
	}

	seen := map[string]bool{}
	for seed := int64(0); seed < 60; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{})
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}
		procedure := built.Navigator["procedure"]
		if varPattern.MatchString(procedure) {
			t.Fatalf("seed=%d: procedure に未解決の変数: %s", seed, procedure)
		}
		for _, label := range []string{"区画A", "区画B"} {
			if strings.Contains(procedure, label) {
				seen[label] = true
			}
		}
	}

	for _, label := range []string{"区画A", "区画B"} {
		if !seen[label] {
			t.Errorf("%s が一度も出ていない (60シード) — 分岐が偏っている可能性", label)
		}
	}
}

// TestStagesDefineObservation は、全ステージが observation を定義していることを
// 確かめる (docs/navigator_design.md §3.2 / ADR N-35)。
//
// observation はプレイヤーが報告すべき観察の定義で、これが無いステージは
// ヒントレベルの前倒しが働かない。実運用で 105 早い者勝ち を完璧に報告したのに
// 経過22秒では L1 のままとなり、「よく見比べてください」と空振りが返った。
//
// 報告そのものが答えの決め手になる観察系ステージ (105/204/207/212 など) で
// 特に効くが、**全ステージが第一声でランプの報告を求める**設計
// (TestStagesAskForLampReportFirst) なので、定義も全ステージに要る。
func TestStagesDefineObservation(t *testing.T) {
	lib := loadTestLibrary(t)

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		if stageTmpl.Navigator["observation"] == "" {
			t.Errorf("%s: observation が未定義 — このステージだけ"+
				"報告してもヒントレベルが前倒しされない", id)
		}
	}
}

// TestStagesObservationIsAQuestion は observation が「報告できたか」を問う形で
// 書かれていることを確かめる。
//
// observation はプロンプトで「この観察を報告できているかを判定せよ」と
// 使うため、**判定できる条件**として書く必要がある (ADR N-33)。
// 装置の状態を書き写しただけ (「2つ点滅している」) だと、生成AIは
// 自分の知識と照合してしまい、プレイヤーが何も言っていなくても true を返す。
func TestStagesObservationIsAQuestion(t *testing.T) {
	lib := loadTestLibrary(t)

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		observation := stageTmpl.Navigator["observation"]
		if observation == "" {
			continue // 未定義は TestStagesDefineObservation が報告する
		}
		if !strings.Contains(observation, "報告できたか") {
			t.Errorf("%s: observation が判定条件の形になっていない — "+
				"「〜を報告できたか」と書くこと:\n  %s", id, observation)
		}
	}
}
