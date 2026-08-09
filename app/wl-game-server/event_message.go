package main

// デバイスからの進行イベント (docs/game_session_design.md §7.2) を
// 交信ログ用の日本語へ整形する。

import "fmt"

// describeWrongAction は誤操作の内容をログ用の日本語にする (§7.2 の detail)。
func describeWrongAction(msg *deviceMessage) string {
	line := ""
	if msg.Line != "" {
		if name, ok := colorNameJA[msg.Line]; ok {
			line = fmt.Sprintf("(%s)", name)
		} else {
			line = fmt.Sprintf("(%s)", msg.Line)
		}
	}

	switch msg.Detail {
	case "wrong_line":
		return "誤切断" + line
	case "precondition_unmet":
		return "手順未達のまま切断" + line
	case "forbidden_rotary":
		return "禁止位置でダイヤルを停止"
	case "push_seq":
		return "ボタン列の入力ミス"
	default:
		return "誤操作" + line
	}
}

// describePenalty はペナルティをログ用の文字列にする。
func describePenalty(penaltyMS int) string {
	if penaltyMS <= 0 {
		return ""
	}
	return fmt.Sprintf(" −%d秒", penaltyMS/1000)
}

// describeExplodeReason は爆発理由をログ用の日本語にする (§7.2 の reason)。
func describeExplodeReason(msg *deviceMessage) string {
	switch msg.Reason {
	case "timeout":
		return "時間切れ"
	case "forced":
		return "マネージャーによる強制破裂"
	case "wrong_cut":
		return describeWrongAction(msg)
	default:
		return msg.Reason
	}
}
