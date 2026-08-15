package main

import (
	"fmt"
	"strings"
)

// 交信スタイル (docs/navigator_design.md §2.6)。
// サーバーが残り時間から判定し、プロンプトの [C] ブロックに含める。
const (
	commStyleNormal = "通常"
	commStyleUrgent = "緊迫"
)

// NavigatorPromptInput はプロンプト組み立てに必要な動的情報。
type NavigatorPromptInput struct {
	// Prompt は共通のプロンプト定義 (navigator/prompt.toml)
	Prompt    *NavigatorPromptConfig
	Character NavigatorCharacter
	Session   *BuiltSession

	// StageIndex は現在のステージ番号 (0始まり)
	StageIndex int
	// RemainingMS は Core から報告された残り時間
	RemainingMS int
	// HintLevel は現在の許可ヒントレベル
	HintLevel int
	// RecentEvent は直近のゲームイベントの説明 (stage_cleared 等)。空でもよい
	RecentEvent string
	// History は直近の無線のやり取り
	History string
}

// BuildNavigatorPrompt は思考モデルへ渡すプロンプトを組み立てる (§3.3)。
//
//	[A. 共通役割定義] [B. キャラシート] [C. ヒントポリシー]
//	[D. セッション状態] [E. 会話ログ] [F. 出力ルール]
func BuildNavigatorPrompt(in NavigatorPromptInput) string {
	var b strings.Builder

	// [A] 共通役割定義 (設定ファイルから)
	b.WriteString(strings.TrimSpace(in.Prompt.Role))
	b.WriteString("\n\n")

	// [B] キャラシート (セッション中固定)
	b.WriteString("# あなたのキャラクター\n")
	b.WriteString(in.Character.Sheet)
	b.WriteString("\n\n")

	// [C] ヒントポリシー + 交信スタイル (動的)
	stage := in.currentStage()
	if stage != nil {
		b.WriteString(HintPolicyText(in.HintLevel, stage))
		b.WriteString("\n")
	}

	style := commStyleNormal
	if in.RemainingMS > 0 && in.RemainingMS <= in.Prompt.UrgentThresholdMS {
		style = commStyleUrgent
	}
	b.WriteString(fmt.Sprintf("# 交信スタイル: %s\n", style))
	if style == commStyleUrgent {
		b.WriteString("緊迫時の崩し方: ")
		b.WriteString(in.Character.UrgentStyle)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// [D] セッション状態 (動的)
	b.WriteString("# 現在の状況\n")
	if in.Session != nil {
		total := len(in.Session.Stages)
		b.WriteString(fmt.Sprintf("- 難易度: %s\n", in.Session.Difficulty))
		// 全ステージ完了後は StageIndex が末尾を越える。「3 / 2 番目の課題」と
		// 書くと生成AIが存在しない課題があると解釈するため、完了と明示する。
		if in.StageIndex >= total {
			b.WriteString(fmt.Sprintf("- 進行: 全%d課題を完了\n", total))
		} else {
			b.WriteString(fmt.Sprintf("- 進行: %d / %d 番目の課題\n",
				in.StageIndex+1, total))
		}
	}
	b.WriteString(fmt.Sprintf("- 残り時間: 約%d秒\n", in.RemainingMS/1000))

	if stage != nil {
		b.WriteString("\n## 今の課題\n")
		if briefing := stage.Navigator["briefing"]; briefing != "" {
			b.WriteString("- 内容: " + briefing + "\n")
		}
		// ナビゲーターは正解を知っている状態で話す (§3.1)。
		//
		// ただし**正解を渡すこと自体が漏洩の原因**になる。実運用で L3 のときに
		// 「次は緑色の線を切ってください」と色名を直言した事例が出た
		// (answer の文をほぼそのままなぞっていた)。禁止指示はヒントポリシー側に
		// あるが、正解文と離れた位置にあると引きずられる。
		// **正解と同じ行に、今それを言ってよいかを併記する**。
		if answer := stage.Navigator["answer"]; answer != "" {
			b.WriteString("- 正解(あなただけが知っている): " + answer + "\n")
			if in.HintLevel < HintL4 {
				fmt.Fprintf(&b, "  ⚠ **この正解をそのまま口に出してはいけません。**"+
					"色名・番号を直言できるのは L4 のみです。現在は L%d なので、"+
					"上の「進め方」と下の「ヒントポリシー」に従って導いてください。\n",
					in.HintLevel)
			}
		}
		if procedure := stage.Navigator["procedure"]; procedure != "" {
			b.WriteString("- 進め方: " + procedure + "\n")
		}
	} else {
		// ステージ知識が無い状態 (全ステージ完了後など)。
		//
		// 何も書かないと、生成AIは「次の課題へ進む」といった指示だけを頼りに
		// **存在しない課題を捏造する** (実運用で、解除済みの装置に対して
		// 「もう一本、赤の線を切ってください」と指示した)。
		// 操作を促してはいけないことを明示する。
		b.WriteString("\n## 今の課題\n")
		b.WriteString("- **今は指示する課題がありません。**装置の操作 (線を切る・" +
			"ボタンを押す・ダイヤルを回す) を促してはいけません。" +
			"色や番号を挙げて何かを切らせる発言は禁止です。" +
			"直近の出来事への短い受け答えだけにとどめてください。\n")
	}

	if in.RecentEvent != "" {
		b.WriteString("\n## 直近の出来事\n")
		b.WriteString(in.RecentEvent + "\n")
	}
	b.WriteString("\n")

	// [E] 会話ログ (動的)
	if in.History != "" {
		b.WriteString("# 直近の交信\n")
		b.WriteString(in.History)
		b.WriteString("\n\n")
	}

	// [F] 出力ルール (設定ファイルから)
	b.WriteString(strings.TrimSpace(in.Prompt.Output))

	return b.String()
}

// currentStage は現在のステージ知識を返す。範囲外なら nil。
func (in NavigatorPromptInput) currentStage() *BuiltStage {
	if in.Session == nil {
		return nil
	}
	if in.StageIndex < 0 || in.StageIndex >= len(in.Session.Stages) {
		return nil
	}
	return in.Session.Stages[in.StageIndex]
}
