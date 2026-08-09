package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// NavigatorCharacter はナビゲーターのキャラクター設定
// (docs/navigator_design.md §2)。
//
// セッション開始(バインド確立)時に候補から1人をランダムに割り当て、
// セッション中は固定する。コードネームは無線コールサイン風に鳥で統一。
//
// 定義は navigator/characters/*.toml に置く(1キャラ1ファイル)。
// ファイルを追加すれば候補が増え、口調の調整は再ビルドなしで反映できる。
type NavigatorCharacter struct {
	// ID は内部識別子 (Valkey への保存・Web画面表示に使う)
	ID string `toml:"id" json:"id"`
	// Name は無線で名乗るコードネーム
	Name string `toml:"name" json:"name"`

	// Sheet はプロンプトの [B. キャラシート] ブロックに入る設定文
	Sheet string `toml:"sheet" json:"sheet"`

	// UrgentStyle は緊迫時 (残り時間僅少) の崩し方 (§2.6)
	UrgentStyle string `toml:"urgent_style" json:"urgent_style"`

	// TTSVoice / TTSStyle は音声合成のボイスとスタイル指定 (§4)
	TTSVoice string `toml:"tts_voice" json:"tts_voice"`
	TTSStyle string `toml:"tts_style" json:"tts_style"`
}

// NavigatorPromptConfig は全キャラクター共通のプロンプト定義
// (navigator/prompt.toml)。
type NavigatorPromptConfig struct {
	// Role は [A. 共通役割定義]
	Role string `toml:"role"`
	// Output は [F. 出力ルール]
	Output string `toml:"output"`

	// Triggers は発話トリガーごとの指示 (§3.5)。
	// キーはトリガー名。"fallback" は未定義トリガーの既定文。
	Triggers map[string]string `toml:"triggers"`

	// UrgentThresholdMS は交信スタイルが「緊迫」へ切り替わる残り時間 (§2.6)
	UrgentThresholdMS int `toml:"urgent_threshold_ms"`
}

// TriggerInstruction はトリガーに対応する発話指示を返す。
// 未定義のトリガーは fallback を使う。
func (c *NavigatorPromptConfig) TriggerInstruction(trigger string) string {
	if text, ok := c.Triggers[trigger]; ok && text != "" {
		return text
	}
	return c.Triggers["fallback"]
}

// NavigatorConfig はナビゲーター設定一式 (キャラクター + プロンプト)。
type NavigatorConfig struct {
	Prompt     NavigatorPromptConfig
	Characters []NavigatorCharacter
}

// urgentThresholdDefaultMS は prompt.toml に指定が無い場合の既定値 (§2.6 仮: 60秒)。
const urgentThresholdDefaultMS = 60000

// LoadNavigatorConfig は navigator/ 以下の設定を読み込む。
//
//	navigator/
//	├── prompt.toml       … 共通役割定義・出力ルール・発話トリガー指示
//	└── characters/*.toml … キャラクター定義 (1キャラ1ファイル)
func LoadNavigatorConfig(root string) (*NavigatorConfig, error) {
	cfg := &NavigatorConfig{}

	promptPath := filepath.Join(root, "prompt.toml")
	if _, err := toml.DecodeFile(promptPath, &cfg.Prompt); err != nil {
		return nil, fmt.Errorf("decode %q: %w", promptPath, err)
	}
	if strings.TrimSpace(cfg.Prompt.Role) == "" {
		return nil, fmt.Errorf("%s: role is required", promptPath)
	}
	if strings.TrimSpace(cfg.Prompt.Output) == "" {
		return nil, fmt.Errorf("%s: output is required", promptPath)
	}
	if cfg.Prompt.Triggers == nil {
		cfg.Prompt.Triggers = map[string]string{}
	}
	if cfg.Prompt.Triggers["fallback"] == "" {
		cfg.Prompt.Triggers["fallback"] = "状況に応じて適切に発話してください。"
	}
	if cfg.Prompt.UrgentThresholdMS <= 0 {
		cfg.Prompt.UrgentThresholdMS = urgentThresholdDefaultMS
	}

	charDir := filepath.Join(root, "characters")
	entries, err := os.ReadDir(charDir)
	if err != nil {
		return nil, fmt.Errorf("read character dir %q: %w", charDir, err)
	}

	seen := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		path := filepath.Join(charDir, entry.Name())

		var character NavigatorCharacter
		if _, err := toml.DecodeFile(path, &character); err != nil {
			return nil, fmt.Errorf("decode %q: %w", path, err)
		}
		if character.ID == "" || character.Name == "" {
			return nil, fmt.Errorf("%s: id and name are required", path)
		}
		if strings.TrimSpace(character.Sheet) == "" {
			return nil, fmt.Errorf("%s: sheet is required", path)
		}
		if prev, dup := seen[character.ID]; dup {
			return nil, fmt.Errorf("%s: duplicated character id %q (also in %s)",
				path, character.ID, prev)
		}
		seen[character.ID] = path

		cfg.Characters = append(cfg.Characters, character)
	}

	if len(cfg.Characters) == 0 {
		return nil, fmt.Errorf("no character defined in %s", charDir)
	}

	// 割り当ての再現性のためID順に並べる
	sort.Slice(cfg.Characters, func(i, j int) bool {
		return cfg.Characters[i].ID < cfg.Characters[j].ID
	})
	return cfg, nil
}

// Pick は候補から1人をランダムに選ぶ。
// セッション開始時に1回だけ呼び、セッション中は固定する (§2)。
func (c *NavigatorConfig) Pick(rng *rand.Rand) NavigatorCharacter {
	return c.Characters[rng.Intn(len(c.Characters))]
}

// ByID は保存済みの割当をIDから復元する (Valkey からの復元用)。
func (c *NavigatorConfig) ByID(id string) (NavigatorCharacter, bool) {
	for _, character := range c.Characters {
		if character.ID == id {
			return character, true
		}
	}
	return NavigatorCharacter{}, false
}

// Names は読み込んだキャラクター名の一覧を返す (起動ログ用)。
func (c *NavigatorConfig) Names() []string {
	names := make([]string, 0, len(c.Characters))
	for _, character := range c.Characters {
		names = append(names, character.Name)
	}
	return names
}
