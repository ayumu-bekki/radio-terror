package main

import "time"

// ヒントレベル (docs/navigator_design.md §3.2)
const (
	HintL1 = 1 // 気づかせる: 観察対象を示す
	HintL2 = 2 // 絞り込む: 判断基準を示す
	HintL3 = 3 // ほぼ答え: 手順を示す (色・番号は言わない)
	HintL4 = 4 // 直言: 正解をそのまま言う
)

// 質問回数・誤操作によるヒントレベルの前倒し条件 (§3.2)。
const (
	// L2 へ前倒しする質問回数
	hintQuestionsForL2 = 2
	// L3 へ前倒しする質問回数
	hintQuestionsForL3 = 4
)

// StageProgress は現在のステージにおけるヒントレベル判定の材料。
// ステージが切り替わったらリセットする (§3.2)。
type StageProgress struct {
	// StartedAt はこのステージが始まった時刻
	StartedAt time.Time
	// Questions はこのステージでプレイヤーが質問した回数
	Questions int
	// WrongActions はこのステージで発生した誤操作の回数
	WrongActions int
}

// Reset はステージ切り替え時にヒントレベルを L1 へ戻す。
func (p *StageProgress) Reset(now time.Time) {
	p.StartedAt = now
	p.Questions = 0
	p.WrongActions = 0
}

// HintLevel は現在の許可ヒントレベルを算出する (§3.2)。
//
// 「そのステージでの経過時間」を基本軸に、「質問回数」「誤操作」で前倒しする。
// 閾値は固定秒ではなく、ステージ予算 (countdown ÷ ステージ数) に対する比率で
// 自動算出するため、難易度・ステージ数を変えても「直言が予算内に必ず来る」
// 整合が保たれる (docs/scenario_design.md §4.1)。
//
// hints の各比率が 0 の場合、そのレベルは解禁されない (ハードの L4 など)。
func HintLevel(progress *StageProgress, budgetMS int, hints HintRule, now time.Time) int {
	elapsedMS := int(now.Sub(progress.StartedAt) / time.Millisecond)

	level := HintL1

	// 経過時間による解禁
	if threshold := pctOf(budgetMS, hints.L2Pct); threshold > 0 && elapsedMS >= threshold {
		level = HintL2
	}
	if threshold := pctOf(budgetMS, hints.L3Pct); threshold > 0 && elapsedMS >= threshold {
		level = HintL3
	}
	if threshold := pctOf(budgetMS, hints.L4Pct); threshold > 0 && elapsedMS >= threshold {
		level = HintL4
	}

	// 質問回数・誤操作による前倒し。
	// 経過時間による判定を下回らせず、無効化されたレベル (比率0) へも到達させない。
	if progress.Questions >= hintQuestionsForL2 && level < HintL2 && hints.L2Pct > 0 {
		level = HintL2
	}
	if (progress.Questions >= hintQuestionsForL3 || progress.WrongActions > 0) &&
		level < HintL3 && hints.L3Pct > 0 {
		level = HintL3
	}

	return level
}

// pctOf は budgetMS に対する pct% のミリ秒を返す。pct が 0 以下なら 0 (無効)。
func pctOf(budgetMS, pct int) int {
	if pct <= 0 {
		return 0
	}
	return budgetMS * pct / 100
}

// HintPolicyText は現在の許可ヒントレベルに応じたプロンプトの [C] ブロックを組み立てる。
// stage はナビゲーター向けステージ知識 (briefing / answer / hint_l1..l3 / procedure)。
func HintPolicyText(level int, stage *BuiltStage) string {
	base := `# ヒントポリシー
- ヒントは段階制です。現在の「許可ヒントレベル」の範囲を超えて、正解の色や番号を
  直言してはいけません。
- プレイヤーの質問を待つだけでなく、進行イベント・沈黙・残り時間をトリガーに
  自分から声を掛けてください。
- 常に解除成功へ導く姿勢を保ってください。

`

	switch level {
	case HintL4:
		return base + "## 現在の許可ヒントレベル: L4 (直言)\n" +
			"正解をそのまま伝えてよい段階です。次の内容を、あなたの口調で明確に伝えてください。\n" +
			"「" + stage.Navigator["answer"] + "」\n"

	case HintL3:
		return base + "## 現在の許可ヒントレベル: L3 (ほぼ答え)\n" +
			"手順は示してよいが、正解の色名・番号は言わないでください。\n" +
			"指針: " + stage.Navigator["hint_l3"] + "\n"

	case HintL2:
		return base + "## 現在の許可ヒントレベル: L2 (絞り込む)\n" +
			"判断基準を示す段階です。正解そのものは言わないでください。\n" +
			"指針: " + stage.Navigator["hint_l2"] + "\n"

	default:
		return base + "## 現在の許可ヒントレベル: L1 (気づかせる)\n" +
			"観察対象を示すだけの段階です。判断基準や正解は言わないでください。\n" +
			"指針: " + stage.Navigator["hint_l1"] + "\n"
	}
}
