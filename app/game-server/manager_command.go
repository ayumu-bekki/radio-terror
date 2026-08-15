package main

import (
	"context"
	"log"
	"regexp"
	"strings"
	"unicode"
)

// deviceIDPattern は音声申告から CoreID を検出する。
// device_id は数字4桁のため「4桁の数字」を候補として拾う
// (docs/bridge_connection_design.md §5)。
var deviceIDPattern = regexp.MustCompile(`\d{4}`)

// ManagerCommand はマネージャーの音声コマンドの解析結果 (docs/operation_flow.md §7)。
type ManagerCommand struct {
	// Kind は "start" (開始申告) / "reset" (強制リセット) /
	// "detonate" (強制破裂) / "" (コマンドではない)
	Kind string
	// DeviceID は対象 Core の4桁ID
	DeviceID string
	// Difficulty は開始申告で指定された難易度
	Difficulty string
}

// マネージャーコマンドの種別
const (
	managerCommandStart    = "start"
	managerCommandReset    = "reset"
	managerCommandDetonate = "detonate"
)

// difficultyKeywords は音声申告の難易度語彙 (§5)。
// 文字起こしのゆれを吸収するため複数の表記を受ける。
var difficultyKeywords = map[string]string{
	"イージー":   difficultyEasy,
	"いーじー":   difficultyEasy,
	"easy":   difficultyEasy,
	"簡単":     difficultyEasy,
	"ノーマル":   difficultyNormal,
	"のーまる":   difficultyNormal,
	"normal": difficultyNormal,
	"普通":     difficultyNormal,
	"ハード":    difficultyHard,
	"はーど":    difficultyHard,
	"hard":   difficultyHard,
	"難しい":    difficultyHard,
}

// ManagerCommandHandler はマネージャーの音声コマンドを解析して実行する。
type ManagerCommandHandler struct {
	game *GameCoordinator

	// secretWords は強制リセット(キルスイッチ)の秘密ワードの表記ゆれ一覧。
	// プレイヤーがマネージャーを騙ってリセットするのを防ぐため運営内でのみ共有する。
	//
	// **複数の表記を持つ**のは、音声認識が同じ音を別の表記で返すため。
	// 「でんぱ」と設定しても STT は「電波」と書き起こすことがあり、
	// かな正規化では漢字をかなへ戻せないので一致しない
	// (実運用でキルスイッチが効かなかった原因。§7)。
	// 設定はカンマ区切りで複数書ける ("でんぱ,電波")。
	secretWords []string
}

func NewManagerCommandHandler(game *GameCoordinator, secretWord string) *ManagerCommandHandler {
	return &ManagerCommandHandler{game: game, secretWords: parseSecretWords(secretWord)}
}

// parseSecretWords は設定値をカンマ区切りで分解し、正規化した候補一覧にする。
// 空要素は捨てる。全て空なら nil (= 秘密ワード未設定) を返す。
func parseSecretWords(raw string) []string {
	words := make([]string, 0, 2)
	for _, part := range strings.Split(raw, ",") {
		normalized := normalizeForMatch(strings.TrimSpace(part))
		if normalized != "" {
			words = append(words, normalized)
		}
	}
	if len(words) == 0 {
		return nil
	}
	return words
}

// matchesSecretWord は文字列に秘密ワードのいずれかが含まれるかを返す。
// 秘密ワード未設定の場合は常に false (秘密ワード必須のコマンドを通さない)。
func (h *ManagerCommandHandler) matchesSecretWord(normalized string) bool {
	for _, word := range h.secretWords {
		if strings.Contains(normalized, word) {
			return true
		}
	}
	return false
}

// Handle は文字起こし結果からマネージャーコマンドを判定し、該当すれば実行する。
// コマンドとして処理した場合 true を返す (ナビゲーターの会話へは流さない)。
func (h *ManagerCommandHandler) Handle(ctx context.Context, sender *AudioSender, result *TranscriptionResult) (bool, error) {
	if h.game == nil {
		return false, nil
	}

	for _, item := range result.Items {
		cmd := h.Parse(item.Message)
		switch cmd.Kind {
		case managerCommandStart:
			log.Printf("[manager] start: bridge=%s device=%s difficulty=%s",
				sender.BridgeID(), cmd.DeviceID, cmd.Difficulty)
			return true, h.game.StartSession(ctx, sender, cmd.DeviceID, cmd.Difficulty)

		case managerCommandReset:
			// 音声認識は数字を取り違える (3701 → 3710 の実例あり)。
			// その無線が担当しているデバイスを優先して対象にする。
			target := h.game.ResolveResetTarget(sender.BridgeID(), cmd.DeviceID)
			log.Printf("[manager] reset: bridge=%s device=%s (spoken=%s)",
				sender.BridgeID(), target, cmd.DeviceID)
			return true, h.game.AbortSession(ctx, sender, target)

		case managerCommandDetonate:
			log.Printf("[manager] force detonate: bridge=%s device=%s", sender.BridgeID(), cmd.DeviceID)
			return true, h.game.ForceDetonate(ctx, cmd.DeviceID)

		default:
			// コマンドとして成立しなかった理由をログに残す。
			//
			// 成立しないと発話はそのままナビゲーター/カラスへ流れるため、
			// **なぜ通らなかったのか**がログから読めないと切り分けられない
			// (秘密ワードの表記ゆれでキルスイッチが効かず、原因の特定に
			// 時間を要した)。マネージャーの定型文らしき発話に限って出す。
			if reason := h.rejectReason(item.Message); reason != "" {
				log.Printf("[manager] not a command (%s): %q", reason, item.Message)
			}
		}
	}
	return false, nil
}

// rejectReason は「マネージャーの操作らしいが成立しなかった」理由を返す。
// 操作と無関係な発話では空文字を返し、ログを汚さない。
func (h *ManagerCommandHandler) rejectReason(message string) string {
	normalized := normalizeForMatch(message)

	isOperation := containsAny(message, normalized,
		[]string{"リセット", "強制爆破", "強制破裂"},
		[]string{"りせつと", "りせっと", "reset", "ばくは", "はれつ"})
	if !isOperation {
		return ""
	}

	if deviceIDPattern.FindString(normalized) == "" {
		return "CoreID(4桁の数字)が聞き取れていない"
	}
	if len(h.secretWords) == 0 {
		return "秘密ワードが未設定"
	}
	if !h.matchesSecretWord(normalized) {
		return "秘密ワードが一致しない (表記ゆれの可能性: config の secret_word にカンマ区切りで追加する)"
	}
	return "条件不足"
}

// Parse は発話からマネージャーコマンドを抽出する。
func (h *ManagerCommandHandler) Parse(message string) ManagerCommand {
	if message == "" {
		return ManagerCommand{}
	}

	normalized := normalizeForMatch(message)

	// 4桁の数字を CoreID 候補として検出する
	deviceID := deviceIDPattern.FindString(normalized)
	if deviceID == "" {
		return ManagerCommand{}
	}

	// 秘密ワードを要するコマンド (強制破裂・強制リセット)。
	// どちらも運営内でのみ共有する秘密ワードが必須なので、
	// プレイヤーが定型文を真似ても成立しない。
	//
	// 一致判定はかな正規化のゆるい一致とする (認識ゆれでキルスイッチが
	// 効かない事態を防ぐ。ワード自体が運営外秘のため緩くても安全)。
	if h.matchesSecretWord(normalized) {
		// 強制破裂: **風船が実際に割れる**ため、リセットより先に判定する
		if containsAny(message, normalized, []string{"強制爆破", "強制破裂"},
			[]string{"きようせいばくは", "きようせいはれつ", "ばくは"}) {
			return ManagerCommand{Kind: managerCommandDetonate, DeviceID: deviceID}
		}

		if containsAny(message, normalized, []string{"リセット"},
			[]string{"りせつと", "りせっと", "reset"}) {
			return ManagerCommand{Kind: managerCommandReset, DeviceID: deviceID}
		}
	}

	// 開始申告: CoreID + 難易度 + 開始の意図
	difficulty := findDifficulty(message, normalized)
	if difficulty == "" {
		return ManagerCommand{}
	}
	if !strings.Contains(normalized, "かいし") && !strings.Contains(message, "開始") &&
		!strings.Contains(normalized, "はじめ") && !strings.Contains(message, "始め") {
		return ManagerCommand{}
	}

	return ManagerCommand{
		Kind:       managerCommandStart,
		DeviceID:   deviceID,
		Difficulty: difficulty,
	}
}

// containsAny は原文・正規化文字列のいずれかにキーワードが含まれるかを返す。
// originals は原文に対して、normalizeds は正規化後の文字列に対して照合する。
func containsAny(original, normalized string, originals, normalizeds []string) bool {
	for _, keyword := range originals {
		if strings.Contains(original, keyword) {
			return true
		}
	}
	for _, keyword := range normalizeds {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return false
}

// findDifficulty は発話から難易度を検出する。
func findDifficulty(original, normalized string) string {
	lower := strings.ToLower(original)
	for keyword, difficulty := range difficultyKeywords {
		if strings.Contains(original, keyword) || strings.Contains(lower, strings.ToLower(keyword)) {
			return difficulty
		}
		if strings.Contains(normalized, normalizeForMatch(keyword)) {
			return difficulty
		}
	}
	return ""
}

// normalizeForMatch はかな正規化のゆるい一致用に文字列を整える。
//
// カタカナ→ひらがな、長音・記号・空白の除去、英字の小文字化を行う。
// 音声認識のゆれ (「でんぱ」「デンパ」「電波」など表記違い) を吸収する。
func normalizeForMatch(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'ァ' && r <= 'ヶ':
			// カタカナ → ひらがな
			b.WriteRune(r - 0x60)
		case r == 'ー' || r == '・' || r == '、' || r == '。':
			// 長音・区切り記号は無視する
		case unicode.IsSpace(r):
			// 空白は無視する
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
