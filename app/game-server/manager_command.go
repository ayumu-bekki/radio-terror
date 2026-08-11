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

	// secretWord は強制リセット(キルスイッチ)の秘密ワード。
	// プレイヤーがマネージャーを騙ってリセットするのを防ぐため運営内でのみ共有する。
	secretWord string
}

func NewManagerCommandHandler(game *GameCoordinator, secretWord string) *ManagerCommandHandler {
	return &ManagerCommandHandler{game: game, secretWord: secretWord}
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
			log.Printf("[manager] reset: bridge=%s device=%s", sender.BridgeID(), cmd.DeviceID)
			return true, h.game.AbortSession(ctx, sender, cmd.DeviceID)

		case managerCommandDetonate:
			log.Printf("[manager] force detonate: bridge=%s device=%s", sender.BridgeID(), cmd.DeviceID)
			return true, h.game.ForceDetonate(ctx, cmd.DeviceID)
		}
	}
	return false, nil
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
	if h.secretWord != "" && strings.Contains(normalized, normalizeForMatch(h.secretWord)) {
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
