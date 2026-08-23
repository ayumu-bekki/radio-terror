package main

import (
	"fmt"
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
	stageTmpl, err := lib.Stage("202")
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
