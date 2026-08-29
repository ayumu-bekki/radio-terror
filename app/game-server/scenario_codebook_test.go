package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// letterRanges は資料2 デコード表の「頭文字 → ダイヤル位置」区間
// (docs/printed_materials.md §3.3)。**紙資料に印刷する区間そのもの**。
// ここを変えたら資料2 も刷り直す。
var letterRanges = []struct {
	from, to rune
	rotary   int
}{
	{'A', 'E', 0},
	{'F', 'I', 1},
	{'J', 'M', 2},
	{'N', 'Q', 3},
	{'R', 'U', 4},
	{'V', 'Z', 5},
}

// letterColors は資料2 デコード表の「末尾文字 → 基準色」対応 (docs/printed_materials.md §3.3)。
// A から5色を循環で割り当てる (資料1 のモールス対照表と同じ割り当て方)。
var letterColors = []string{"A", "D", "B", "C", "E"} // 赤・青・黄・緑・白

// rotaryForLetter は資料2 デコード表の区間から頭文字のダイヤル位置を引く。
func rotaryForLetter(letter rune) int {
	for _, r := range letterRanges {
		if letter >= r.from && letter <= r.to {
			return r.rotary
		}
	}
	return -1
}

// baseColorForLetter は資料2 デコード表の循環割当から末尾文字の基準色を引く。
func baseColorForLetter(letter rune) string {
	return letterColors[int(letter-'A')%len(letterColors)]
}

// TestCodebookMatchesPrintedRules は Go の対照表が**紙資料の手順どおり**に
// 解けることを確かめる。
//
// 対照表 (scenario_codebook.go) は解いた結果だけを持つため、
// 途中の変換規則を書き換えても Go 側は静かに通ってしまう。
// **プレイヤーは紙どおりに解く**ので、ここがずれると理不尽に失敗する。
// 資料2 のデコード表・資料3 の手順をテスト側で再現し、突き合わせる。
func TestCodebookMatchesPrintedRules(t *testing.T) {
	for _, entry := range codebookTable {
		word := entry.word

		// 資料2 デコード表 前半: 頭文字 → ダイヤル位置
		wantRotary := rotaryForLetter(rune(word[0]))
		if wantRotary != entry.rotary {
			t.Errorf("%s: 頭文字 %c のダイヤルは資料3 では %d だが表は %d",
				word, word[0], wantRotary, entry.rotary)
		}

		// 資料2 デコード表 後半: 末尾文字 → 基準色
		last := rune(word[len(word)-1])
		baseColor := baseColorForLetter(last)

		// 資料3: 基準色とランプの状態で最終的な切る線が決まる。
		//   基準色が 白(E) / 黄(B) → **緑のランプ**を見る。消えていれば青(D)
		//   それ以外               → **白のランプ**を見る。消えていれば赤(A)
		lit := map[string]bool{}
		for _, color := range strings.Split(entry.litColors(), ",") {
			lit[color] = true
		}
		var wantCut string
		if baseColor == "E" || baseColor == "B" {
			wantCut = baseColor
			if !lit["C"] { // 緑が消灯
				wantCut = "D" // 青
			}
		} else {
			wantCut = baseColor
			if !lit["E"] { // 白が消灯
				wantCut = "A" // 赤
			}
		}
		if wantCut != entry.cut {
			t.Errorf("%s (%s): 紙どおりに解くと %s だが表は %s (基準色 %s)",
				word, entry.patternText(), wantCut, entry.cut, baseColor)
		}
	}
}

// TestCodebookLitAndDarkAreComplementary は点灯色と消灯色が
// 5色をちょうど1回ずつ覆うことを確かめる。
//
// プレイヤーは「点いているランプ」と「消えているランプ」の両方を報告するため、
// 片方に漏れがあるとナビゲーターの知識と実機の表示が食い違う。
func TestCodebookLitAndDarkAreComplementary(t *testing.T) {
	for _, entry := range codebookTable {
		seen := map[string]int{}
		for _, color := range strings.Split(entry.litColors(), ",") {
			if color != "" {
				seen[color]++
			}
		}
		for _, color := range strings.Split(entry.darkColors(), ",") {
			if color != "" {
				seen[color]++
			}
		}
		for _, color := range allColors {
			if seen[color] != 1 {
				t.Errorf("%s: 色 %s が点灯/消灯あわせて %d 回 (1回であるべき)",
					entry.word, color, seen[color])
			}
		}
		// 赤(A)は常時消灯のダミーなので、必ず消灯側にいる
		if strings.Contains(entry.litColors(), "A") {
			t.Errorf("%s: 赤(A)が点灯側にいる (常時消灯のダミーのはず)", entry.word)
		}
	}
}

// TestNavigatorTextHasNoHardcodedDocumentNumber は**ナビゲーターが読み上げる文面**に
// 資料番号が直書きされていないことを確かめる (ADR D-2)。
//
// 呼称は `[mission_sheet.documents]` から引く決まりで、文面へ「資料2」と
// 書くと**番号を振り直したときに食い違う**。202 の資料統合 (2026-08-23) で
// 資料4 → 資料3 の繰り上げが起きたとき、コメントに残った古い番号が見つかった。
// コメントは読み上げられないので対象外だが、**文面は毎回検査する**。
func TestNavigatorTextHasNoHardcodedDocumentNumber(t *testing.T) {
	lib := loadTestLibrary(t)

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		for key, text := range stageTmpl.Navigator {
			for n := 1; n <= 9; n++ {
				if strings.Contains(text, fmt.Sprintf("資料%d", n)) {
					t.Errorf("%s の navigator.%s に資料番号が直書きされている (資料%d) — "+
						"${sheet_*} で参照すること (ADR D-2)", id, key, n)
				}
			}
		}
	}
}

// TestStage202DoesNotReferenceCircuit は 202 が**回路図(資料3)を参照しない**ことを
// 確かめる (ADR D-9)。
//
// 一度 202 の分岐表を回路図の余白へ載せてしまった。資料3 は 205 が
// 「端子番号だけを頼りに色を引く」ための紙で、そこへ 202 用の**色の分岐表**を
// 同居させると 205 のプレイヤーの視界に色対応が入る。
// **余白があるという理由で別ステージの表を相乗りさせない**。
func TestStage202DoesNotReferenceCircuit(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("301")
	if err != nil {
		t.Fatalf("Stage(202): %v", err)
	}
	for key, text := range stageTmpl.Navigator {
		if strings.Contains(text, "${sheet_circuit}") {
			t.Errorf("202 の navigator.%s が回路図を参照している — "+
				"202 は資料2 だけで完結する (ADR D-9): %s", key, text)
		}
	}
}

// TestColorMatchCountNeverReachesNavigator は 206 の**押す回数がナビゲーター知識に
// 入らない**ことを確かめる (ADR N-42)。
//
// ナビゲーターは装置を見ていないため**今何回目かを追えない**。回数を知って
// いると「あと3回だ」と実況したくなるが、実際には何回押されたか分からず
// **必ず嘘になる**。
//
// **「言うな」と但し書きを書く方式は採らない。** 目の前にある語はなぞられ
// (ADR N-1)、文言を引用して禁じるとそれ自体が手本になる (ADR N-32)。
// 202・205 の色名と同じく、**最初からプロンプトへ載せない**のが確実。
//
// 回数は難易度で変わるため (docs/scenario_design.md §4.2)、
// 3難易度すべてで検査する。
func TestColorMatchCountNeverReachesNavigator(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("204")
	if err != nil {
		t.Fatalf("Stage(206): %v", err)
	}

	// テンプレートの時点で navigator が ${count} を参照していないこと。
	// 展開後の数字を探すより確実で、抽選値に依存しない。
	for key, text := range stageTmpl.Navigator {
		if strings.Contains(text, "${count}") {
			t.Errorf("206 の navigator.%s が押す回数を参照している — "+
				"ナビゲーターは回数を知ってはいけない (ADR N-42): %s", key, text)
		}
	}

	for _, difficulty := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		diff, err := lib.Difficulty(difficulty)
		if err != nil {
			t.Fatalf("Difficulty(%s): %v", difficulty, err)
		}
		for seed := int64(0); seed < 40; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{}, diff.Hints, diff.Load)
			if err != nil {
				t.Fatalf("%s seed=%d: buildStage: %v", difficulty, seed, err)
			}

			precondition, ok := built.Core["precondition"].(map[string]any)
			if !ok {
				t.Fatalf("%s seed=%d: precondition が無い", difficulty, seed)
			}
			colorMatch, ok := precondition["color_match"].(map[string]any)
			if !ok {
				t.Fatalf("%s seed=%d: color_match が無い", difficulty, seed)
			}
			count := fmt.Sprintf("%v", colorMatch["count"])

			// 回数は難易度の範囲に収まっていること
			n, err := strconv.Atoi(count)
			if err != nil {
				t.Fatalf("%s seed=%d: count が数値でない: %q", difficulty, seed, count)
			}
			if n < diff.Load.ColorMatchMin || n > diff.Load.ColorMatchMax {
				t.Errorf("%s seed=%d: count %d が [load] の範囲 [%d,%d] の外",
					difficulty, seed, n, diff.Load.ColorMatchMin, diff.Load.ColorMatchMax)
			}
		}
	}
}

// TestColorMatchCountVariesByDifficulty は押す回数が**難易度で変わる**ことを
// 確かめる (docs/scenario_design.md §4.2)。
//
// ステージ定義へ数値を直書きすると難易度を跨いで同じ負荷になり、
// イージーとハードで同じ回数を押させることになる。
func TestColorMatchCountVariesByDifficulty(t *testing.T) {
	lib := loadTestLibrary(t)

	ranges := make(map[string][2]int, 3)
	for _, difficulty := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		diff, err := lib.Difficulty(difficulty)
		if err != nil {
			t.Fatalf("Difficulty(%s): %v", difficulty, err)
		}
		if err := diff.Load.Validate(difficulty); err != nil {
			t.Fatalf("%s: %v", difficulty, err)
		}
		ranges[difficulty] = [2]int{diff.Load.ColorMatchMin, diff.Load.ColorMatchMax}
	}

	// **難易度が上がるほど重くする**。同じ範囲なら分ける意味が無い。
	easy, normal, hard := ranges[difficultyEasy], ranges[difficultyNormal], ranges[difficultyHard]
	if !(easy[1] <= normal[1] && normal[1] <= hard[1]) {
		t.Errorf("押す回数の上限が難易度順になっていない: easy=%v normal=%v hard=%v",
			easy, normal, hard)
	}
	if easy == hard {
		t.Errorf("イージーとハードで押す回数が同じ (%v) — 難易度で変える意味が無い", easy)
	}
}

// TestDifficultyPoolsHaveEnoughStages は各難易度タグの在庫が
// **構成に必要な数を満たす**ことを確かめる。
//
// ステージを無効化したり `difficulty` タグを付け替えたりすると、
// **その難易度を引いたセッションだけ**が組み立てに失敗する。
// 抽選次第でしか再現しないため、タグごとの件数を明示的に数える。
//
// **難易度は `difficulty` タグで決まり、IDの番号帯ではない** (ADR S-8)。
// 2026-08-24 の見直しで 202・302 が hard、301 が normal になっている。
func TestDifficultyPoolsHaveEnoughStages(t *testing.T) {
	lib := loadTestLibrary(t)

	pool := map[string][]string{}
	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		pool[stageTmpl.Difficulty] = append(pool[stageTmpl.Difficulty], id)
	}

	// 各難易度テンプレートの compose が要求する数を満たしているか
	for _, difficulty := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		diff, err := lib.Difficulty(difficulty)
		if err != nil {
			t.Fatalf("Difficulty(%s): %v", difficulty, err)
		}
		for tag, need := range diff.Compose.Random {
			got := len(pool[tag])
			if got < need {
				t.Errorf("難易度 %s: タグ %q が %d 件必要だが %d 件しかない — "+
					"この難易度のセッションが組み立てに失敗する",
					difficulty, tag, need, got)
			}
			// 抽選の余地が無いと毎回同じ並びになる
			if got == need {
				t.Logf("難易度 %s: タグ %q は %d 件ちょうど — "+
					"抽選の余地が無く毎回同じ顔ぶれになる", difficulty, tag, got)
			}
		}
	}

	for _, tag := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		sort.Strings(pool[tag])
		t.Logf("%-6s (%d件): %v", tag, len(pool[tag]), pool[tag])
	}
}

// TestStageIDsFollowNumberingScheme は**ID の採番規則**を守っているか確かめる
// (ADR S-9)。
//
//	easy    101-105   normal  201-209   hard  301-305
//
// **有効なステージは隙間なく詰める。** 欠番が残ると「なぜ204だけ無いのか」を
// 毎回調べ直すことになる。無効化を90番台へ寄せれば、
// **有効な番号帯を見るだけで現役の全ステージが分かる**。
func TestStageIDsFollowNumberingScheme(t *testing.T) {
	lib := loadTestLibrary(t)

	band := map[string]byte{
		difficultyEasy:   '1',
		difficultyNormal: '2',
		difficultyHard:   '3',
	}

	byBand := map[byte][]int{}
	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		if len(id) != 3 {
			t.Errorf("%s: ステージIDは3桁であること", id)
			continue
		}

		// 百の位が難易度帯と一致すること
		want, ok := band[stageTmpl.Difficulty]
		if !ok {
			t.Errorf("%s: 未知の difficulty %q", id, stageTmpl.Difficulty)
			continue
		}
		if id[0] != want {
			t.Errorf("%s: difficulty=%q なら %c00番台のはず (ADR S-9)",
				id, stageTmpl.Difficulty, want)
		}

		n, err := strconv.Atoi(id)
		if err != nil {
			t.Errorf("%s: 数値として読めない", id)
			continue
		}
		// 有効なステージが90番台にいてはいけない (90番台は無効化の退避先)
		if n%100 >= 90 {
			t.Errorf("%s: 90番台は無効化したステージの退避先 (ADR S-9)。"+
				"有効なステージは帯の先頭から詰める", id)
		}
		byBand[id[0]] = append(byBand[id[0]], n)
	}

	// 各帯が 01 から隙間なく続いていること
	for b, nums := range byBand {
		sort.Ints(nums)
		base := int(b-'0') * 100
		for i, n := range nums {
			if want := base + i + 1; n != want {
				t.Errorf("%d00番台に欠番がある: %d の位置に %d — "+
					"有効なステージは隙間なく詰める (ADR S-9)", int(b-'0'), want, n)
				break
			}
		}
	}
}

// TestForbiddenRotaryCountFollowsDifficulty は 206 綱渡り の禁止位置の数が
// **難易度で変わる**ことを確かめる (ADR S-10)。
//
// ハードは2つ。**ハード難易度は「普通×2」を引く**ため、normal タグの
// このステージもハードのセッションに出る。そのときに2つになる。
func TestForbiddenRotaryCountFollowsDifficulty(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("206")
	if err != nil {
		t.Fatalf("Stage(206): %v", err)
	}

	for _, difficulty := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		diff, err := lib.Difficulty(difficulty)
		if err != nil {
			t.Fatalf("Difficulty(%s): %v", difficulty, err)
		}
		want := diff.Load.ForbiddenRotaryCount

		for seed := int64(0); seed < 120; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{}, diff.Hints, diff.Load)
			if err != nil {
				t.Fatalf("%s seed=%d: buildStage: %v", difficulty, seed, err)
			}

			forbidden, ok := built.Core["forbidden_rotary"].(map[string]any)
			if !ok {
				t.Fatalf("%s seed=%d: forbidden_rotary が無い", difficulty, seed)
			}
			positions, ok := forbidden["positions"].([]any)
			if !ok {
				t.Fatalf("%s seed=%d: positions が配列でない", difficulty, seed)
			}
			if len(positions) != want {
				t.Fatalf("%s seed=%d: 禁止位置が %d 個 (期待 %d)",
					difficulty, seed, len(positions), want)
			}

			target := built.Core["precondition"].(map[string]any)["rotary"]
			targetN, err := strconv.Atoi(fmt.Sprintf("%v", target))
			if err != nil {
				t.Fatalf("%s seed=%d: target が数値でない: %v", difficulty, seed, target)
			}

			seen := map[int]bool{}
			for _, p := range positions {
				n, err := strconv.Atoi(fmt.Sprintf("%v", p))
				if err != nil {
					t.Fatalf("%s seed=%d: 禁止位置が数値でない: %v", difficulty, seed, p)
				}
				if n < 0 || n > 5 {
					t.Errorf("%s seed=%d: 禁止位置 %d が範囲外 (0-5)", difficulty, seed, n)
				}
				if n == targetN {
					t.Errorf("%s seed=%d: 目的位置 %d が禁止位置になっている", difficulty, seed, targetN)
				}
				seen[n] = true
			}

			// **目的位置の両隣が禁止だと逃げ場が無い。** 合わせたあと
			// 手が滑って1つ動かすと即爆発する。難度ではなく運になる。
			if seen[targetN-1] && seen[targetN+1] {
				t.Errorf("%s seed=%d: 目的位置 %d の両隣が禁止 — 逃げ場が無い",
					difficulty, seed, targetN)
			}
		}
	}
}

// TestForbiddenRotaryCoversAllLayouts は禁止位置が2つのとき
// **3つの配置の型がすべて出る**ことを確かめる (ADR S-10)。
//
//	隣接        連続して通過する。一気に回せば1回で抜ける
//	離れている  間の安全地帯で一度止まれる。2段構えになる
//	目的地を挟む 行き過ぎると危険。止める精度が要る
//
// 位置を個別に抽選する形へ戻すと配置が偏り、この検査が落ちる。
func TestForbiddenRotaryCoversAllLayouts(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("206")
	if err != nil {
		t.Fatalf("Stage(206): %v", err)
	}
	diff, err := lib.Difficulty(difficultyHard)
	if err != nil {
		t.Fatalf("Difficulty(hard): %v", err)
	}
	if diff.Load.ForbiddenRotaryCount != 2 {
		t.Skipf("ハードの禁止位置が2つでないため飛ばす (現在 %d)", diff.Load.ForbiddenRotaryCount)
	}

	kinds := map[string]int{}
	for seed := int64(0); seed < 300; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{}, diff.Hints, diff.Load)
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}
		positions := built.Core["forbidden_rotary"].(map[string]any)["positions"].([]any)
		target, _ := strconv.Atoi(fmt.Sprintf("%v", built.Core["precondition"].(map[string]any)["rotary"]))
		f1, _ := strconv.Atoi(fmt.Sprintf("%v", positions[0]))
		f2, _ := strconv.Atoi(fmt.Sprintf("%v", positions[1]))
		if f1 > f2 {
			f1, f2 = f2, f1
		}

		switch {
		case f2 == f1+1:
			kinds["adjacent"]++
		case f1 < target && target < f2:
			kinds["straddle"]++
		default:
			kinds["split"]++
		}
	}

	for _, kind := range []string{"adjacent", "straddle", "split"} {
		if kinds[kind] == 0 {
			t.Errorf("配置の型 %q が一度も出ていない (300シード) — "+
				"位置を個別に抽選する形へ戻すと偏る (ADR S-10): %v", kind, kinds)
		}
	}
	t.Logf("配置の型: %v", kinds)
}

// TestPushSeqLenFollowsDifficulty は 201 復唱 のボタン列の長さが
// **難易度で変わる**ことを確かめる (ADR S-11)。
//
// ハードは8個。**ハード難易度は「普通×2」を引く**ため、normal タグの
// このステージもハードのセッションに出て、そこで8個になる。
//
// あわせて列の中身も検査する:
//   - **正解色が列に混ざらない** — 第一声で正解色を音として出してしまう
//   - **同じ色が3連続しない** — 無線で何個言われたか数えられなくなる
func TestPushSeqLenFollowsDifficulty(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("201")
	if err != nil {
		t.Fatalf("Stage(201): %v", err)
	}

	for _, difficulty := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		diff, err := lib.Difficulty(difficulty)
		if err != nil {
			t.Fatalf("Difficulty(%s): %v", difficulty, err)
		}
		want := diff.Load.PushSeqLen

		for seed := int64(0); seed < 150; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{}, diff.Hints, diff.Load)
			if err != nil {
				t.Fatalf("%s seed=%d: buildStage: %v", difficulty, seed, err)
			}

			precondition := built.Core["precondition"].(map[string]any)
			pushSeq, ok := precondition["push_seq"].(map[string]any)
			if !ok {
				t.Fatalf("%s seed=%d: push_seq が無い", difficulty, seed)
			}
			entries, ok := pushSeq["entries"].([]any)
			if !ok {
				t.Fatalf("%s seed=%d: entries が配列でない", difficulty, seed)
			}
			if len(entries) != want {
				t.Fatalf("%s seed=%d: ボタン列が %d 個 (期待 %d)",
					difficulty, seed, len(entries), want)
			}
			// colors は entries へ畳み込まれて消えていること
			if _, leftover := pushSeq["colors"]; leftover {
				t.Errorf("%s seed=%d: push_seq.colors が残っている", difficulty, seed)
			}

			run, prev := 1, ""
			for i, raw := range entries {
				entry, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("%s seed=%d: entries[%d] がテーブルでない", difficulty, seed, i)
				}
				color, ok := entry["push"].(string)
				if !ok {
					t.Fatalf("%s seed=%d: entries[%d].push が文字列でない", difficulty, seed, i)
				}
				if color == built.Cut {
					t.Fatalf("%s seed=%d: 列に正解色 %s が混ざっている — "+
						"第一声で正解色を音として出してしまう", difficulty, seed, built.Cut)
				}
				if color == prev {
					run++
					if run >= 3 {
						t.Errorf("%s seed=%d: %s が3連続 — "+
							"無線で何個言われたか数えられない", difficulty, seed, color)
					}
				} else {
					run = 1
				}
				prev = color
			}
		}
	}
}

// TestPushSeqLenIsHarderOnHard はハードのボタン列がノーマルより
// **長いこと**を確かめる。同じ長さなら難易度で変える意味が無い。
func TestPushSeqLenIsHarderOnHard(t *testing.T) {
	lib := loadTestLibrary(t)

	normal, err := lib.Difficulty(difficultyNormal)
	if err != nil {
		t.Fatalf("Difficulty(normal): %v", err)
	}
	hard, err := lib.Difficulty(difficultyHard)
	if err != nil {
		t.Fatalf("Difficulty(hard): %v", err)
	}
	if hard.Load.PushSeqLen <= normal.Load.PushSeqLen {
		t.Errorf("ハードのボタン列が %d 個でノーマル (%d 個) より長くない",
			hard.Load.PushSeqLen, normal.Load.PushSeqLen)
	}
}

// TestRevealCutOnCompleteReachesCore は 103 コール&レスポンス の
// `reveal_cut_on_complete` が**Core向けJSONへ届く**ことを確かめる (ADR C-13)。
//
// この演出はファーム側の挙動 (押し切ったら切る線だけ点灯)。
// フラグが落ちると**押し終わっても表示が変わらず**、
// ナビゲーターの「表示が変わったはずだ」が嘘になる。
//
// あわせて**押下中は2色見えている**ことを検査する。最初から
// 「報告用の点灯」と「切る線の点滅」が両方見えているのが要点で、
// 片方だけだと「報告した色は答えではない」という気づきが発生しない。
func TestRevealCutOnCompleteReachesCore(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("103")
	if err != nil {
		t.Fatalf("Stage(103): %v", err)
	}

	for seed := int64(0); seed < 120; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{}, stdHints, stdLoad)
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}

		pushSeq := built.Core["precondition"].(map[string]any)["push_seq"].(map[string]any)
		if pushSeq["reveal_cut_on_complete"] != true {
			t.Fatalf("seed=%d: reveal_cut_on_complete が Core向けJSON に無い — "+
				"押し終わっても表示が変わらず、ナビの案内が嘘になる", seed)
		}

		// 押下中は「報告用の点灯」と「切る線の点滅」の2色
		leds := built.Core["leds"].(map[string]any)
		if len(leds) != 2 {
			t.Fatalf("seed=%d: leds が %d 個 (点灯1 + 点滅1 のはず)", seed, len(leds))
		}
		if leds[built.Cut] != "blink" {
			t.Errorf("seed=%d: 切る線 %s が点滅していない: %v", seed, built.Cut, leds[built.Cut])
		}

		// 報告用の点灯色は切る線と別であること
		for color, spec := range leds {
			if spec == "on" && color == built.Cut {
				t.Errorf("seed=%d: 報告用の点灯色が切る線と同じ — "+
					"「報告した色は答えではない」が成立しない", seed)
			}
		}
	}
}

// TestEasySpeedGapStaysReadable は 104 早い者勝ち の点滅差が
// **見分けられる範囲**に収まっていることを確かめる。
//
// 2026-08-24 に 150/700ms (4.67倍) から **150/300ms (2.0倍)** へ締めた。
// 4.67倍は「片方がほぼ点きっぱなし」に見えて簡単すぎた。
//
// **これ以上詰めない。** 1.5倍を切ると両方とも 2.5Hz 以上の「速い点滅」帯に入り、
// どちらも速く見えて差が読めなくなる。205 速さくらべ (ノーマル・隣接2.0倍) より
// 難しくなり、イージーとノーマルの難易度が逆転する。
//
// **緩めすぎもしない。** 3倍を超えると片方が点灯に近く見え、
// 見比べる工程が消えて「明るいほうを切る」だけになる。
func TestEasySpeedGapStaysReadable(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("104")
	if err != nil {
		t.Fatalf("Stage(104): %v", err)
	}

	const minRatio, maxRatio = 1.5, 3.0

	for seed := int64(0); seed < 60; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{}, stdHints, stdLoad)
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}

		leds := built.Core["leds"].(map[string]any)
		if len(leds) != 2 {
			t.Fatalf("seed=%d: leds が %d 個 (2色のはず)", seed, len(leds))
		}

		cutMS := blinkOnMS(t, leds, built.Cut)
		var otherMS int
		for color := range leds {
			if color != built.Cut {
				otherMS = blinkOnMS(t, leds, color)
			}
		}

		// 切る線が**速いほう**であること (このステージの定義)
		if cutMS >= otherMS {
			t.Fatalf("seed=%d: 切る線 (%dms) が遅いほうになっている (相手 %dms)",
				seed, cutMS, otherMS)
		}

		ratio := float64(otherMS) / float64(cutMS)
		if ratio < minRatio {
			t.Errorf("seed=%d: 点滅差が %.2f倍 — 詰めすぎで両方とも速く見える "+
				"(%dms vs %dms)", seed, ratio, cutMS, otherMS)
		}
		if ratio > maxRatio {
			t.Errorf("seed=%d: 点滅差が %.2f倍 — 緩すぎて見比べる工程が消える "+
				"(%dms vs %dms)", seed, ratio, cutMS, otherMS)
		}
	}
}

// blinkOnMS は指定色の blink の on_ms を取り出す。
func blinkOnMS(t *testing.T, leds map[string]any, color string) int {
	t.Helper()
	spec, ok := leds[color].(map[string]any)
	if !ok {
		t.Fatalf("色 %s の LED 指定がオブジェクトでない: %v", color, leds[color])
	}
	ms, err := strconv.Atoi(fmt.Sprintf("%v", spec["on_ms"]))
	if err != nil {
		t.Fatalf("色 %s の on_ms が数値でない: %v", color, spec["on_ms"])
	}
	return ms
}

// TestSpeedRanksFollowDifficulty は 205 速さくらべ の点滅速度が
// **難易度で変わる**ことを確かめる (ADR S-12)。
//
// ハードは隣接比を詰めて見分けにくくする。**ハード難易度は「普通×2」を引く**
// ため、normal タグのこのステージもハードのセッションに出て、そこで詰まる。
//
// あわせて次を検査する:
//   - 速度が**速い順に並んでいる** (s1 が最速)
//   - 4色すべてが**違う速度** (同速だと並べ替えが成立しない)
//   - ハードの隣接比がノーマルより**詰まっている**
func TestSpeedRanksFollowDifficulty(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("205")
	if err != nil {
		t.Fatalf("Stage(205): %v", err)
	}

	worstRatio := map[string]float64{}

	for _, difficulty := range []string{difficultyNormal, difficultyHard} {
		diff, err := lib.Difficulty(difficulty)
		if err != nil {
			t.Fatalf("Difficulty(%s): %v", difficulty, err)
		}

		for seed := int64(0); seed < 80; seed++ {
			builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
			built, err := builder.buildStage(stageTmpl, map[string]bool{}, diff.Hints, diff.Load)
			if err != nil {
				t.Fatalf("%s seed=%d: buildStage: %v", difficulty, seed, err)
			}

			leds := built.Core["leds"].(map[string]any)
			// 点滅している4色の速度を集める (点灯しっぱなしの1色は除く)
			blinks := make([]int, 0, 4)
			for color, spec := range leds {
				if _, isObj := spec.(map[string]any); !isObj {
					continue // "on" の基準色
				}
				blinks = append(blinks, blinkOnMS(t, leds, color))
			}
			if len(blinks) != 4 {
				t.Fatalf("%s seed=%d: 点滅が %d 色 (4色のはず)", difficulty, seed, len(blinks))
			}
			sort.Ints(blinks)

			// 難易度テンプレートの値とそのまま一致すること
			for i, ms := range blinks {
				if ms != diff.Load.SpeedRankMS[i] {
					t.Fatalf("%s seed=%d: 速度が %v (期待 %v)",
						difficulty, seed, blinks, diff.Load.SpeedRankMS)
				}
			}

			// 4色すべて違う速度であること
			for i := 1; i < len(blinks); i++ {
				if blinks[i] == blinks[i-1] {
					t.Fatalf("%s seed=%d: 同じ速度が2色ある (%v) — 並べ替えが成立しない",
						difficulty, seed, blinks)
				}
			}
		}

		// 最小の隣接比 (= 一番見分けにくいペア)
		worst := math.Inf(1)
		for i := 1; i < len(diff.Load.SpeedRankMS); i++ {
			r := float64(diff.Load.SpeedRankMS[i]) / float64(diff.Load.SpeedRankMS[i-1])
			worst = math.Min(worst, r)
		}
		worstRatio[difficulty] = worst
		t.Logf("%-6s %v  最小隣接比 %.2f倍", difficulty, diff.Load.SpeedRankMS, worst)
	}

	// **ハードのほうが詰まっている**こと。同じなら難易度で変える意味が無い。
	if worstRatio[difficultyHard] >= worstRatio[difficultyNormal] {
		t.Errorf("ハードの隣接比 %.2f がノーマル %.2f より詰まっていない",
			worstRatio[difficultyHard], worstRatio[difficultyNormal])
	}
}

// TestPanelTableIsValid は 209 配電盤照合 の対照表が成立するかを確かめる。
// 起動時に main.go が呼ぶ検査と同じもの。
func TestPanelTableIsValid(t *testing.T) {
	if err := validatePanelTable(); err != nil {
		t.Fatalf("validatePanelTable: %v", err)
	}
}

// TestPanelStageRotaryLeds は 209 配電盤照合 の**位置ごとの見え方**が
// 設計どおりかを確かめる (ADR C-14)。
//
//	現在位置 … 対照表どおり (点灯 + 点滅)
//	解除位置 … **cut だけが点滅**
//	危険位置 … **赤だけ点灯** (1色のみ)
//	その他   … ダミー。**1色だけにしない** (危険位置の合図と紛れる)
//
// **解除位置の表示は対照表に載っていない**ことが要点 — 載せると
// 回す前に切る線が分かってしまい、回す工程が消える。
// TestPanelStageRotaryLeds は 209 の表示テーブルと対照表の整合を確かめる
// (決定91)。
//
// **現在位置は抽選しない。** ロータリーは前のプレイから物理的に位置が残るため、
// サーバーは実位置を知らない。6行すべてを渡し、Core が開始時点の位置で
// 行を確定する。したがってサーバーが出すのは:
//
//	rotary_leds … 位置ごとの点灯色 (対照表そのもの)
//	panel_rows  … 位置ごとの危険位置・解除位置 (6行ぶん)
//
// **precondition.rotary / forbidden_rotary は出さない** — 開始位置が
// 分からないと決まらないため、Core が実行時に設定する。
func TestPanelStageRotaryLeds(t *testing.T) {
	lib := loadTestLibrary(t)
	stageTmpl, err := lib.Stage("209")
	if err != nil {
		t.Fatalf("Stage(209): %v", err)
	}

	for seed := int64(0); seed < 120; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.buildStage(stageTmpl, map[string]bool{}, stdHints, stdLoad)
		if err != nil {
			t.Fatalf("seed=%d: buildStage: %v", seed, err)
		}

		// **サーバーは位置を決めない**
		if _, exists := built.Core["precondition"]; exists {
			t.Fatalf("seed=%d: precondition が出ている — 開始位置は Core が決める", seed)
		}
		if _, exists := built.Core["forbidden_rotary"]; exists {
			t.Fatalf("seed=%d: forbidden_rotary が出ている — 開始位置は Core が決める", seed)
		}

		table, ok := built.Core["rotary_leds"].(map[string]any)
		if !ok {
			t.Fatalf("seed=%d: rotary_leds が無い", seed)
		}
		if len(table) != rotaryPositionNum {
			t.Fatalf("seed=%d: rotary_leds が %d 通り (%d のはず)",
				seed, len(table), rotaryPositionNum)
		}

		cut, _ := built.Core["cut"].(string)
		if colorNameJA[cut] == "" {
			t.Fatalf("seed=%d: cut が不正: %q", seed, cut)
		}

		// 表示は**対照表そのもの**。点滅は一切含まない
		// (点滅は「切る線」の合図に予約してある)。
		for key, raw := range table {
			pos, err := strconv.Atoi(key)
			if err != nil || pos < 0 || rotaryPositionNum <= pos {
				t.Fatalf("seed=%d: 位置キーが不正: %q", seed, key)
			}
			leds, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("seed=%d: 位置 %d の表示がテーブルでない", seed, pos)
			}
			row, err := panelRowByPosition(pos)
			if err != nil {
				t.Fatalf("seed=%d: %v", seed, err)
			}
			if len(leds) != len(row.lit) {
				t.Errorf("seed=%d 位置%d: 点灯 %d 色 (対照表は %d 色)",
					seed, pos, len(leds), len(row.lit))
			}
			for _, want := range row.lit {
				if leds[want] != "on" {
					t.Errorf("seed=%d 位置%d: %s が点灯していない", seed, pos, want)
				}
			}
			for color, spec := range leds {
				if obj, isObj := spec.(map[string]any); isObj && obj["pattern"] == "blink" {
					t.Errorf("seed=%d 位置%d: %s が点滅している — "+
						"見え方に点滅を混ぜると cut と衝突する", seed, pos, color)
				}
			}
		}

		// panel_rows が6行そろい、対照表と一致すること
		spec, ok := built.Core["panel_rows"].(map[string]any)
		if !ok {
			t.Fatalf("seed=%d: panel_rows が無い", seed)
		}
		rows, ok := spec["rows"].(map[string]any)
		if !ok {
			t.Fatalf("seed=%d: panel_rows.rows が無い", seed)
		}
		if len(rows) != rotaryPositionNum {
			t.Fatalf("seed=%d: panel_rows が %d 行 (%d 行のはず)",
				seed, len(rows), rotaryPositionNum)
		}
		for _, row := range panelTable {
			got, ok := rows[strconv.Itoa(row.position)].(map[string]any)
			if !ok {
				t.Fatalf("seed=%d: 位置%d の行が無い", seed, row.position)
			}
			if fmt.Sprintf("%v", got["forbidden"]) != strconv.Itoa(row.forbidden) {
				t.Errorf("seed=%d 位置%d: 危険位置 %v (対照表は %d)",
					seed, row.position, got["forbidden"], row.forbidden)
			}
			if fmt.Sprintf("%v", got["release"]) != strconv.Itoa(row.release) {
				t.Errorf("seed=%d 位置%d: 解除位置 %v (対照表は %d)",
					seed, row.position, got["release"], row.release)
			}
		}
	}
}

// TestPanelCutWorksFromEveryPosition は **cut が5色すべて使える**ことを
// 確かめる (決定91)。
//
// 見え方に点滅を混ぜていた頃は、cut と点滅色が衝突する行を避けて
// 抽選していた。点灯のみにしたので、どの色を切ることになっても
// **どの位置から始めても答えが見えない**。
func TestPanelCutWorksFromEveryPosition(t *testing.T) {
	for _, cut := range allColors {
		for _, row := range panelTable {
			for _, lit := range row.lit {
				if lit != cut {
					continue
				}
				// 点灯するのは構わない — 点滅こそが「切れ」の合図。
				// ここでは「点滅していないこと」だけを確かめる。
				t.Logf("cut=%s は位置%d で点灯する (点滅ではないので可)", cut, row.position)
			}
		}
	}
	// 対照表に点滅色の概念が残っていないこと
	for _, row := range panelTable {
		if len(row.lit) < panelLitMin || panelLitMax < len(row.lit) {
			t.Errorf("位置%d: 点灯 %d 色 — %d〜%d 色にする",
				row.position, len(row.lit), panelLitMin, panelLitMax)
		}
	}
}

// TestDocExtensionTableMatchesStages は `puzzle_stage_ideas.md` §4 の
// **拡張表が実装と一致している**ことを確かめる。
//
// この表は「どの拡張をどのステージが使うか」の索引で、**IDだけを書くと
// 番号を振り直したときに静かに古くなる**。実際 2026-08-25 の点検で
// 7行中6行が旧IDのまま取り残されていた
// (`forbidden_rotary` が 101 を指しているなど、実在しない対応)。
//
// ステージ定義を直接読んで突き合わせるので、**表を直し忘れると落ちる**。
func TestDocExtensionTableMatchesStages(t *testing.T) {
	const docPath = "../../docs/puzzle_stage_ideas.md"
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Skipf("%s を読めない: %v", docPath, err)
	}
	doc := string(raw)

	start := strings.Index(doc, "## 4. 事前条件")
	end := strings.Index(doc, "## 5. ")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("%s: §4 の範囲を特定できない", docPath)
	}
	section := doc[start:end]

	// 表に載せている拡張キーワード
	keys := []string{
		"push_seq", "forbidden_rotary", "timer_digit", "last_matches_cut",
		"rotary_leds", "leds_all_off", "morse_word", "romaji_word", "noise_leds",
		"terminal_for_color", "codebook", "rank_slot", "color_seq",
		"rotary_layout", "choice_value",
	}

	// ステージ定義を読み込む
	files, err := filepath.Glob("scenarios/stages/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("ステージ定義が見つからない: %v", err)
	}
	bodies := make(map[string]string, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		bodies[filepath.Base(f)[:3]] = string(b)
	}

	idPattern := regexp.MustCompile(`(?:^|[^0-9])([123]\d\d)(?:[^0-9]|$)`)

	for _, key := range keys {
		// 実際に使っているステージ
		actual := make([]string, 0, 4)
		for id, body := range bodies {
			if strings.Contains(body, key) {
				actual = append(actual, id)
			}
		}
		sort.Strings(actual)

		// 表の該当行を探す
		var cell string
		found := false
		for _, line := range strings.Split(section, "\n") {
			cols := strings.Split(line, "|")
			if len(cols) < 3 || !strings.Contains(cols[1], "`"+key+"`") {
				continue
			}
			cell = cols[len(cols)-2]
			found = true
			break
		}
		if !found {
			t.Errorf("§4 の表に %q の行が無い — 実際は %v が使っている", key, actual)
			continue
		}

		if len(actual) == 0 {
			if !strings.Contains(cell, "利用なし") {
				t.Errorf("%s: 利用ステージが無いのに表が %q と書いている", key, strings.TrimSpace(cell))
			}
			continue
		}

		listed := make([]string, 0, 4)
		for _, m := range idPattern.FindAllStringSubmatch(cell, -1) {
			listed = append(listed, m[1])
		}
		sort.Strings(listed)
		listed = dedupeStrings(listed)

		if !equalStrings(listed, actual) {
			t.Errorf("%s: 表は %v と書いているが、実際に使っているのは %v",
				key, listed, actual)
		}
	}
}

func dedupeStrings(in []string) []string {
	out := in[:0:0]
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPanelWorksFromAnyStartPosition は **どの開始位置でも謎が成立する**ことを
// 確かめる (決定91)。
//
// 実運用の事故の再現: ロータリーが4にあるのに、サーバーは「現在位置は2」として
// 組み立てていた。プレイヤーは正しく資料を読んでも別の行を引き、
// 危険位置を踏んで爆発した。
//
// **サーバーは開始位置を知らない**ので、6行すべてが単独で成立していなければ
// ならない — どこから始まっても、解除位置へ到達でき、危険位置が経路の内側にあり、
// 開始位置の表示で cut が点滅しないこと。
func TestPanelWorksFromAnyStartPosition(t *testing.T) {
	lib := loadTestLibrary(t)
	tmpl, _ := lib.Stage("209")

	for seed := int64(0); seed < 30; seed++ {
		b := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := b.buildStage(tmpl, map[string]bool{}, stdHints, stdLoad)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(built.Core)
		var core map[string]any
		json.Unmarshal(raw, &core)

		spec := core["panel_rows"].(map[string]any)
		rows := spec["rows"].(map[string]any)
		leds := core["rotary_leds"].(map[string]any)
		cut := core["cut"].(string)

		// **どの開始位置でも成立すること**
		for start := 0; start < rotaryPositionNum; start++ {
			row, ok := rows[fmt.Sprint(start)].(map[string]any)
			if !ok {
				t.Fatalf("seed=%d start=%d: 行が無い", seed, start)
			}
			rel := int(row["release"].(float64))
			forb := int(row["forbidden"].(float64))

			if rel == start || forb == start || rel == forb {
				t.Errorf("seed=%d start=%d: 解除%d 危険%d が不正", seed, start, rel, forb)
			}
			lo, hi := start, rel
			if hi < lo {
				lo, hi = hi, lo
			}
			if forb <= lo || hi <= forb {
				t.Errorf("seed=%d start=%d: 危険%d が経路(%d〜%d)の外", seed, start, forb, lo, hi)
			}
			// 開始位置の表示が対照表どおりで、cut が点滅していないこと
			disp := leds[fmt.Sprint(start)].(map[string]any)
			if v, ok := disp[cut].(map[string]any); ok && v["pattern"] == "blink" {
				t.Errorf("seed=%d start=%d: cut(%s) が開始位置で点滅している", seed, start, cut)
			}
			if len(disp) < panelLitMin {
				t.Errorf("seed=%d start=%d: 点灯 %d 色 — 危険位置の合図と紛れる", seed, start, len(disp))
			}
		}
	}
}

// TestPanelCutColorDistribution は 209 の cut が **5色すべて出る**ことを
// 確かめる (決定91)。
//
// 旧設計では見え方に点滅を混ぜていたため、cut と点滅色が衝突する行を
// 避けて抽選していた。旧表は点滅色に5色すべてを使っており、
// 位置を選べなくすると **cut の候補がゼロ**になった。
//
// 見え方を点灯のみにして点滅を「切る線」の合図に予約したので、
// どの色でも衝突しない。**偏りが出たらこの前提が崩れている。**
func TestPanelCutColorDistribution(t *testing.T) {
	lib := loadTestLibrary(t)
	tmpl, _ := lib.Stage("209")
	seen := map[string]int{}
	for seed := int64(0); seed < 200; seed++ {
		b := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := b.buildStage(tmpl, map[string]bool{}, stdHints, stdLoad)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(built.Core)
		var core map[string]any
		json.Unmarshal(raw, &core)
		seen[core["cut"].(string)]++
	}
	t.Logf("cut の分布: %v", seen)
	for _, c := range allColors {
		if seen[c] == 0 {
			t.Errorf("cut に %s (%s) が一度も出ていない", c, colorNameJA[c])
		}
	}
}
