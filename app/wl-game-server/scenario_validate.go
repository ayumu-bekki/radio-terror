package main

import (
	"fmt"
	"strings"
)

// ValidateSession は組み立て済みセッションを session_start 送信前に検証する
// (docs/scenario_design.md §5)。
//
// Core側の検証 (docs/game_session_design.md §6.2) と同等の項目に加え、
// テンプレート起因の項目 (抽選変数の未解決・ナビゲーター知識との整合) を見る。
func ValidateSession(s *BuiltSession) error {
	if len(s.Stages) == 0 {
		return fmt.Errorf("no stages")
	}
	if len(s.Stages) > maxStagesPerSession {
		return fmt.Errorf("too many stages: %d (max %d)", len(s.Stages), maxStagesPerSession)
	}
	if s.CountdownMS <= 0 || s.CountdownMS > 999900 {
		return fmt.Errorf("countdown_ms out of range: %d", s.CountdownMS)
	}

	usedCut := make(map[string]string)

	for i, stage := range s.Stages {
		prefix := fmt.Sprintf("stage %d (%s)", i, stage.TemplateID)

		// 切断線がステージ間で重複していないこと
		if stage.Cut == "" {
			return fmt.Errorf("%s: cut is empty", prefix)
		}
		if !isValidColor(stage.Cut) {
			return fmt.Errorf("%s: invalid cut color %q", prefix, stage.Cut)
		}
		if prev, dup := usedCut[stage.Cut]; dup {
			return fmt.Errorf("%s: cut line %q already used by %s", prefix, stage.Cut, prev)
		}
		usedCut[stage.Cut] = stage.TemplateID

		// whack と push_seq が併用されていないこと (kLED表示の専有が衝突する)
		if precondition, ok := stage.Core["precondition"].(map[string]any); ok {
			_, hasWhack := precondition["whack"]
			_, hasPushSeq := precondition["push_seq"]
			if hasWhack && hasPushSeq {
				return fmt.Errorf("%s: whack and push_seq cannot be combined", prefix)
			}

			if err := validateRotaryField(prefix, precondition["rotary"]); err != nil {
				return err
			}
		}

		if forbidden, ok := stage.Core["forbidden_rotary"].(map[string]any); ok {
			positions, _ := forbidden["positions"].([]any)
			for _, position := range positions {
				if err := validateRotaryField(prefix+" forbidden_rotary", position); err != nil {
					return err
				}
			}
		}

		// 抽選変数が全て解決されていること (未解決の ${...} が残っていない)
		if err := checkUnresolved(prefix+" core", stage.Core); err != nil {
			return err
		}
		for key, text := range stage.Navigator {
			if varPattern.MatchString(text) {
				return fmt.Errorf("%s: unresolved variable in navigator.%s: %s", prefix, key, text)
			}
		}

		// ナビゲーター知識(answer)と Core向けJSON の正解が一致していること。
		// 食い違いはこのゲームで最も致命的なバグなので、組み立て時に必ず弾く。
		answer := stage.Navigator["answer"]
		if answer == "" {
			return fmt.Errorf("%s: navigator.answer is required", prefix)
		}
		if !answerMentionsCut(answer, stage.Cut) {
			return fmt.Errorf("%s: navigator.answer does not mention the cut line %q: %s",
				prefix, stage.Cut, answer)
		}
	}

	return nil
}

// isValidColor は A-E のいずれかかを判定する。
func isValidColor(color string) bool {
	for _, c := range allColors {
		if c == color {
			return true
		}
	}
	return false
}

// validateRotaryField はロータリー位置が 0-5 の範囲かを検証する。
func validateRotaryField(prefix string, value any) error {
	if value == nil {
		return nil
	}
	position, ok := value.(int)
	if !ok {
		// "rotary" (match=rotary) のような文字列指定は範囲検証の対象外
		return nil
	}
	if position < 0 || position > 5 {
		return fmt.Errorf("%s: rotary position out of range (0-5): %d", prefix, position)
	}
	return nil
}

// checkUnresolved は展開後の構造に ${...} が残っていないかを再帰的に確認する。
func checkUnresolved(prefix string, value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if varPattern.MatchString(key) {
				return fmt.Errorf("%s: unresolved variable in key: %s", prefix, key)
			}
			if err := checkUnresolved(prefix+"."+key, item); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := checkUnresolved(fmt.Sprintf("%s[%d]", prefix, i), item); err != nil {
				return err
			}
		}
	case string:
		if varPattern.MatchString(v) {
			return fmt.Errorf("%s: unresolved variable: %s", prefix, v)
		}
	}
	return nil
}

// answerMentionsCut は answer が正解の線に言及しているかを確認する。
//
// answer は「正解は${cut}色の線」のようにテンプレートで書かれ、展開後は
// 色記号(A-E)か日本語色名(赤/黄/緑/青/白)のどちらかで現れる。
func answerMentionsCut(answer, cut string) bool {
	if strings.Contains(answer, cut) {
		return true
	}
	if name, ok := colorNameJA[cut]; ok && strings.Contains(answer, name) {
		return true
	}
	return false
}
