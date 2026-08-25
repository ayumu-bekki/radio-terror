package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// stdHints は イージー・ノーマルの標準比率 (docs/scenario_design.md §4.1)。
var stdHints = HintRule{L2Pct: 25, L3Pct: 50, L4Pct: 75}

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

// TestL4KeepCutSecretNeverEmbedsAnswer は `keep_cut_secret` のステージで
// **L4 でも正解文がプロンプトへ埋め込まれない**ことを確かめる (ADR N-38)。
//
// 以前は「正解文に『これは言うな』と但し書きがあれば優先」という**散文の指示**で
// 守らせていた。しかし L4 では正解文が生の色名込みで載り、ヒントポリシーが
// 「そのまま伝えてください」と指示するため**但し書きが負ける**
// (目の前にある語はなぞられる — N-1)。フラグで機械的に分岐させる。
func TestL4KeepCutSecretNeverEmbedsAnswer(t *testing.T) {
	answer := "正解は端子T5の線 = 白色。プレイヤーに伝えてよいのは端子番号だけ。"
	stage := &BuiltStage{
		TemplateID:    "203",
		KeepCutSecret: true,
		Navigator: map[string]string{
			"answer":  answer,
			"hint_l3": "対応表の読み方を説明してよい",
		},
	}

	text := HintPolicyText(HintL4, stage)

	// 正解文そのものを載せない (載せると読み上げられる)
	if strings.Contains(text, answer) {
		t.Errorf("keep_cut_secret なのに L4 へ answer が埋め込まれた:\n%s", text)
	}
	// 「そのまま伝えてよい」と指示しない
	if strings.Contains(text, "正解をそのまま伝えてよい段階です") {
		t.Errorf("keep_cut_secret なのに「そのまま伝えてよい」と指示している:\n%s", text)
	}
	// 色名を伏せる旨が明示されていること
	if !strings.Contains(text, "切る線の色名") {
		t.Errorf("L4 が色名を伏せる指示になっていない:\n%s", text)
	}
	// 手順は L3 の指針で導く
	if !strings.Contains(text, stage.Navigator["hint_l3"]) {
		t.Errorf("L4 に hint_l3 の指針が含まれていない:\n%s", text)
	}
}

// TestL4WithoutKeepCutSecretStillDirects は、フラグを立てていない
// 通常のステージでは L4 が従来どおり正解を直言することを確かめる。
//
// 塞ぎすぎると「全滅よりも成功体験を優先する」という L4 の趣旨 (N-23) が
// 失われるため、対象を限定していることをテストで固定する。
func TestL4WithoutKeepCutSecretStillDirects(t *testing.T) {
	answer := "正解は赤色の線。"
	stage := &BuiltStage{
		TemplateID: "105",
		Navigator:  map[string]string{"answer": answer},
	}

	text := HintPolicyText(HintL4, stage)
	if !strings.Contains(text, answer) {
		t.Errorf("通常ステージの L4 に answer が含まれていない:\n%s", text)
	}
}

// TestKeepCutSecretStagesAreFlagged は、色名を言うと課題が消えるステージに
// `keep_cut_secret` が立っていることを確かめる (ADR N-38)。
//
// **散文の但し書きは L4 で負ける。** かつては L4 を名指しした但し書きで
// 個別に守っていたが、同じ性質のステージで守られていなかった。フラグで揃える。
func TestKeepCutSecretStagesAreFlagged(t *testing.T) {
	lib := loadTestLibrary(t)

	// 色名を言うと**課題そのものが消える**ステージ。
	//   203: 回路図シートを読む工程 / 301: 資料の読み解き / 202: モールス解読
	//
	// **205 速さくらべ は立てない** (ADR N-50)。正解が紙資料に無く、
	// 点滅の速さは装置を見て数えるしかないため、色を伏せると
	// **誰も正誤を確定できず誤答のまま切る** (実測8回中5回で通した)。
	want := map[string]bool{"203": true, "301": true, "202": true}

	for id := range lib.stages {
		stageTmpl, err := lib.Stage(id)
		if err != nil {
			t.Fatalf("Stage(%q): %v", id, err)
		}
		if got := stageTmpl.KeepCutSecret; got != want[id] {
			t.Errorf("%s: keep_cut_secret = %v, want %v", id, got, want[id])
		}
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
		TemplateID: "205",
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
// 実運用で 104 早い者勝ち を「緑がゆっくり、青が早く点滅」と完璧に報告したのに、
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

// TestStageHintOverrideDisablesL4 は 204 色合わせが L4 (直言) へ到達しないことを
// 確かめる (ADR N-36)。
//
// 206 は**正解がプレイヤーの記憶の中にしかない**。押し切ると全消灯するため
// 装置に手がかりが残らず、色名の直言は課題そのものを消す。
// 実運用で L4 到達後に正解色を直言し、プレイヤーの正しい記憶を上書きして
// 誤切断 → 即爆発を招いた (2026-08-23)。
func TestStageHintOverrideDisablesL4(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))

	stageTmpl, err := lib.Stage("204")
	if err != nil {
		t.Fatalf("Stage(206): %v", err)
	}
	built, err := builder.buildStage(stageTmpl, map[string]bool{}, stdHints, stdLoad)
	if err != nil {
		t.Fatalf("buildStage(206): %v", err)
	}

	if built.Hints.L4Pct != 0 {
		t.Errorf("206: l4_pct = %d, want 0 — 正解の直言が課題を消す",
			built.Hints.L4Pct)
	}
	// L2・L3 は難易度テンプレートの値を引き継ぐ (上書きしたのは L4 だけ)
	if built.Hints.L2Pct != stdHints.L2Pct || built.Hints.L3Pct != stdHints.L3Pct {
		t.Errorf("206: L2/L3 が難易度の値を引き継いでいない: %+v", built.Hints)
	}

	// 予算を使い切っても L4 へ到達しない
	progress, now := progressAfter(10*time.Minute, 99, 99)
	progress.Observed = true
	if got := HintLevel(progress, 140000, built.Hints, now); got >= HintL4 {
		t.Errorf("206: 時間を使い切っても L%d へ到達した、want L3 以下", got)
	}
}

// TestStageHintOverrideKeepsDifficultyDefault は [hints] を書いていない
// ステージが難易度テンプレートの値をそのまま使うことを確かめる。
func TestStageHintOverrideKeepsDifficultyDefault(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))

	// 104 早い者勝ち は [hints] を持たない
	stageTmpl, err := lib.Stage("104")
	if err != nil {
		t.Fatalf("Stage(104): %v", err)
	}
	built, err := builder.buildStage(stageTmpl, map[string]bool{}, hardHints, stdLoad)
	if err != nil {
		t.Fatalf("buildStage(105): %v", err)
	}
	if built.Hints != hardHints {
		t.Errorf("105: Hints = %+v, want %+v (難易度の値をそのまま使う)",
			built.Hints, hardHints)
	}
}

// TestStageHintOverrideZeroIsMeaningful は「0 は無効化」であって
// 「未指定」ではないことを確かめる。
//
// HintRule の 0 はハードの l4_pct = 0 のように**意味を持つ値**なので、
// 上書きの有無はポインタで区別している。ここが int に戻ると、
// l4_pct = 0 と書いても「未指定」と読まれて直言が復活する。
func TestStageHintOverrideZeroIsMeaningful(t *testing.T) {
	zero := 0
	over := StageHintOverride{L4Pct: &zero}
	got := over.Apply(stdHints)

	if got.L4Pct != 0 {
		t.Errorf("l4_pct = 0 の上書きが効いていない: %+v", got)
	}
	if got.L2Pct != stdHints.L2Pct || got.L3Pct != stdHints.L3Pct {
		t.Errorf("未指定の項目まで書き換わった: %+v", got)
	}

	// 何も指定しなければ base のまま
	if empty := (StageHintOverride{}).Apply(stdHints); empty != stdHints {
		t.Errorf("空の上書きで値が変わった: %+v", empty)
	}
}

// TestMustSayReachesPrompt は `must_say` がプロンプトへ**独立したブロック**で
// 届くことを確かめる (ADR N-51)。
//
// `hint_l1` に「必ず両方伝える」と書くだけでは、指針が長くなるほど
// **末尾の項目が落ちる** (204 色合わせ で8回中2回欠けた)。
// 散文の但し書きではなく単独の要求として置き直したので、
// **その配置が壊れていないこと**を回帰で見る。
func TestMustSayReachesPrompt(t *testing.T) {
	const mustSay = "最後に押した色を覚えておくこと"

	stage := &BuiltStage{
		TemplateID: "204",
		Navigator: map[string]string{
			"answer":   "正解は緑色の線を切ること",
			"must_say": mustSay,
			"hint_l1":  "観察を促す",
		},
	}
	text := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:    &NavigatorPromptConfig{},
		Session:   &BuiltSession{Stages: []*BuiltStage{stage}},
		HintLevel: HintL1,
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
		Prompt:    &NavigatorPromptConfig{},
		Session:   &BuiltSession{Stages: []*BuiltStage{stage}},
		HintLevel: HintL1,
	})
	if strings.Contains(text, "この課題で必ず言うこと") {
		t.Errorf("must_say が無いのに見出しが出た:\n%s", text)
	}
}
