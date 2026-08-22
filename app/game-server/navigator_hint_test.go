package main

import (
	"strings"
	"testing"
	"time"
)

// stdHints は イージー・ノーマルの標準比率 (docs/scenario_design.md §4.1)。
var stdHints = HintRule{L2Pct: 25, L3Pct: 50, L4Pct: 75}

// hardHints は ハード。L4 を無効化している (l4_pct = 0)。
var hardHints = HintRule{L2Pct: 25, L3Pct: 50, L4Pct: 0}

// progressAfter は「ステージ開始から elapsed 経過した」進捗を組み立てる。
func progressAfter(elapsed time.Duration, questions, wrongActions int) (*StageProgress, time.Time) {
	now := time.Now()
	return &StageProgress{
		StartedAt:    now.Add(-elapsed),
		Questions:    questions,
		WrongActions: wrongActions,
	}, now
}

// TestHintLevelByElapsedTime は経過時間による解禁を確かめる (§3.2)。
func TestHintLevelByElapsedTime(t *testing.T) {
	const budget = 140000 // ノーマルのステージ予算 140秒

	cases := []struct {
		elapsed time.Duration
		want    int
	}{
		{0, HintL1},
		{34 * time.Second, HintL1},  // 25%(35秒)未満
		{35 * time.Second, HintL2},  // 25%
		{69 * time.Second, HintL2},  // 50%(70秒)未満
		{70 * time.Second, HintL3},  // 50%
		{104 * time.Second, HintL3}, // 75%(105秒)未満
		{105 * time.Second, HintL4}, // 75%
		{200 * time.Second, HintL4},
	}

	for _, c := range cases {
		progress, now := progressAfter(c.elapsed, 0, 0)
		if got := HintLevel(progress, budget, stdHints, now); got != c.want {
			t.Errorf("elapsed=%v: HintLevel = L%d, want L%d", c.elapsed, got, c.want)
		}
	}
}

// TestHintLevelFrontLoading は質問回数・誤操作による前倒しを確かめる (§3.2)。
func TestHintLevelFrontLoading(t *testing.T) {
	const budget = 140000

	// 質問2回で L2 へ前倒し
	progress, now := progressAfter(0, hintQuestionsForL2, 0)
	if got := HintLevel(progress, budget, stdHints, now); got != HintL2 {
		t.Errorf("questions=2: HintLevel = L%d, want L2", got)
	}

	// 質問4回で L3 へ前倒し
	progress, now = progressAfter(0, hintQuestionsForL3, 0)
	if got := HintLevel(progress, budget, stdHints, now); got != HintL3 {
		t.Errorf("questions=4: HintLevel = L%d, want L3", got)
	}

	// 誤操作1回で L3 へ前倒し
	progress, now = progressAfter(0, 0, 1)
	if got := HintLevel(progress, budget, stdHints, now); got != HintL3 {
		t.Errorf("wrongActions=1: HintLevel = L%d, want L3", got)
	}
}

// TestHintLevelFrontLoadingDoesNotLower は前倒しが経過時間による判定を
// 下回らせないことを確かめる (§3.2)。
func TestHintLevelFrontLoadingDoesNotLower(t *testing.T) {
	const budget = 140000

	// 既に時間で L3。質問2回(L2相当)で L2 へ落ちてはいけない
	progress, now := progressAfter(70*time.Second, hintQuestionsForL2, 0)
	if got := HintLevel(progress, budget, stdHints, now); got != HintL3 {
		t.Errorf("elapsed=50%%, questions=2: HintLevel = L%d, want L3", got)
	}

	// 既に時間で L4。誤操作で L3 へ落ちてはいけない
	progress, now = progressAfter(105*time.Second, 0, 3)
	if got := HintLevel(progress, budget, stdHints, now); got != HintL4 {
		t.Errorf("elapsed=75%%, wrongActions=3: HintLevel = L%d, want L4", got)
	}
}

// TestHintLevelHardNeverReachesL4 はハードで L4 (直言) に到達しないことを確かめる。
// 比率0は無効化を意味し、前倒しも無効レベルを飛び越えない (§3.2)。
func TestHintLevelHardNeverReachesL4(t *testing.T) {
	const budget = 120000 // ハードのステージ予算 120秒

	// 予算を大きく超過しても L3 止まり
	for _, elapsed := range []time.Duration{
		0, 30 * time.Second, 60 * time.Second, 90 * time.Second,
		120 * time.Second, 300 * time.Second,
	} {
		progress, now := progressAfter(elapsed, 0, 0)
		if got := HintLevel(progress, budget, hardHints, now); got == HintL4 {
			t.Errorf("hard elapsed=%v: reached L4 (直言); L4 must be disabled", elapsed)
		}
	}

	// 質問・誤操作を重ねても L4 には到達しない
	progress, now := progressAfter(300*time.Second, 10, 5)
	if got := HintLevel(progress, budget, hardHints, now); got != HintL3 {
		t.Errorf("hard with many questions/wrongs: HintLevel = L%d, want L3", got)
	}
}

// TestHintLevelDisabledLevelNotJumped は無効化されたレベルへ前倒しが
// 飛び越えないことを確かめる (§3.2)。
func TestHintLevelDisabledLevelNotJumped(t *testing.T) {
	const budget = 140000

	// L3 を無効化した設定では、誤操作しても L3 へ上がらない
	noL3 := HintRule{L2Pct: 25, L3Pct: 0, L4Pct: 75}
	progress, now := progressAfter(0, 0, 1)
	if got := HintLevel(progress, budget, noL3, now); got == HintL3 {
		t.Errorf("L3 disabled but front-loading reached L3")
	}

	// L2 を無効化した設定では、質問2回でも L2 へ上がらない
	noL2 := HintRule{L2Pct: 0, L3Pct: 50, L4Pct: 75}
	progress, now = progressAfter(0, hintQuestionsForL2, 0)
	if got := HintLevel(progress, budget, noL2, now); got == HintL2 {
		t.Errorf("L2 disabled but front-loading reached L2")
	}
}

// TestStageProgressReset はステージ切り替えで L1 へ戻ることを確かめる (§3.2)。
func TestStageProgressReset(t *testing.T) {
	const budget = 140000

	progress, _ := progressAfter(120*time.Second, 5, 2)

	now := time.Now()
	progress.Reset(now)

	if got := HintLevel(progress, budget, stdHints, now); got != HintL1 {
		t.Errorf("after Reset: HintLevel = L%d, want L1", got)
	}
	if progress.Questions != 0 || progress.WrongActions != 0 {
		t.Errorf("after Reset: questions=%d wrongActions=%d, want 0 0",
			progress.Questions, progress.WrongActions)
	}
}

// TestL4RespectsStageLevelRestriction は L4 (直言) のプロンプトが
// **ステージ側の「これは言うな」という但し書きを優先する**よう
// 指示していることを確かめる (docs/navigator_design.md §5 決定19・決定20)。
//
// 205 ブループリントは色名を言うとシートを読む工程が消えてしまうため、
// L4 でも端子番号しか言えない。L4 のブロックは「正解をそのまま伝えてよい」と
// 書くため、但し書きの優先を明示しないと **そのまま読み上げて色が漏れる**。
func TestL4RespectsStageLevelRestriction(t *testing.T) {
	stage := &BuiltStage{
		TemplateID: "205",
		Navigator: map[string]string{
			"answer": "正解は端子T5の線 = 白色。プレイヤーに伝えてよいのは端子番号だけ。",
		},
	}

	text := HintPolicyText(HintL4, stage)

	// 正解文そのものは渡っていること (照合に必要)
	if !strings.Contains(text, stage.Navigator["answer"]) {
		t.Fatalf("L4 に answer が含まれていない:\n%s", text)
	}
	// 但し書きが優先される旨が書かれていること
	if !strings.Contains(text, "但し書きが優先") {
		t.Errorf("L4 が「但し書きの優先」を指示していない — "+
			"ステージ側の『言うな』が無視されて漏れる:\n%s", text)
	}
}

// TestHintPolicyBelowL4NeverEmbedsAnswer は L1-L3 のプロンプトブロックに
// **正解文そのものが埋め込まれない**ことを確かめる。
//
// 正解は [D] セッション状態側で「⚠ 口に出すな」と併記して渡す設計
// (navigator_prompt.go)。ヒントポリシー側にも正解文が入ると、禁止と併記されない
// 2つ目の正解がプロンプト内に生まれ、決定19 で塞いだ漏洩経路が復活する。
func TestHintPolicyBelowL4NeverEmbedsAnswer(t *testing.T) {
	const answer = "正解は白色の線を切ること"

	stage := &BuiltStage{
		TemplateID: "208",
		Navigator: map[string]string{
			"answer":  answer,
			"hint_l1": "よく観察するよう促す",
			"hint_l2": "判断基準を示す",
			"hint_l3": "手順を示す",
		},
	}

	for _, level := range []int{HintL1, HintL2, HintL3} {
		text := HintPolicyText(level, stage)
		if strings.Contains(text, answer) {
			t.Errorf("L%d のヒントポリシーに正解文が埋め込まれている "+
				"(禁止の併記なしに正解が2箇所へ入る):\n%s", level, text)
		}
	}
}

// TestHintLevelObservedFrontLoading は観察の報告による前倒しを確かめる
// (§3.2 / ADR N-35)。
//
// 実運用で 105 早い者勝ち を「緑がゆっくり、青が早く点滅」と完璧に報告したのに、
// 経過22秒では L1 のままで「2つの光り方の違いをよく見比べてください」と
// 空振りが返った。報告できた時点で判断基準を示せるようにする。
func TestHintLevelObservedFrontLoading(t *testing.T) {
	const budget = 140000

	// 報告済みなら、経過0でも L2 (判断基準を示す) へ前倒しする
	progress, now := progressAfter(0, 0, 0)
	progress.Observed = true
	if got := HintLevel(progress, budget, stdHints, now); got != HintL2 {
		t.Errorf("observed: HintLevel = L%d, want L2", got)
	}

	// 未報告なら L1 のまま (前倒しの条件は報告そのもの)
	progress, now = progressAfter(0, 0, 0)
	if got := HintLevel(progress, budget, stdHints, now); got != HintL1 {
		t.Errorf("not observed: HintLevel = L%d, want L1", got)
	}

	// 既に時間で L3 のとき、報告済みでも L2 へ落としてはいけない
	progress, now = progressAfter(70*time.Second, 0, 0)
	progress.Observed = true
	if got := HintLevel(progress, budget, stdHints, now); got != HintL3 {
		t.Errorf("observed at L3: HintLevel = L%d, want L3", got)
	}

	// L2 が無効 (比率0) なら、報告済みでも解禁しない
	progress, now = progressAfter(0, 0, 0)
	progress.Observed = true
	noL2 := HintRule{L2Pct: 0, L3Pct: 50, L4Pct: 75}
	if got := HintLevel(progress, budget, noL2, now); got != HintL1 {
		t.Errorf("observed with l2_pct=0: HintLevel = L%d, want L1", got)
	}
}

// TestStageProgressResetClearsObserved はステージ切り替えで観察の報告が
// リセットされることを確かめる。次のステージは別の観察を要求するため、
// 持ち越すと最初から L2 で始まってしまう。
func TestStageProgressResetClearsObserved(t *testing.T) {
	p := &StageProgress{Observed: true, Questions: 3, WrongActions: 1}
	p.Reset(time.Now())
	if p.Observed {
		t.Error("Reset 後も Observed が立っている")
	}
}
