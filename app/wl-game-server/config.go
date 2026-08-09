package main

import (
	"github.com/BurntSushi/toml"
)

type Config struct {
	RadioBridge  RadioBridgeConfig   `toml:"radio_bridge"`
	Gemini       GeminiConfig        `toml:"gemini"`
	Log          LogConfig           `toml:"log"`
	WebSocket    WebSocketConfig     `toml:"websocket"`
	Scenario     ScenarioConfig      `toml:"scenario"`
	Navigator    NavigatorConfigPath `toml:"navigator"`
	Valkey       ValkeyConfig        `toml:"valkey"`
	Assets       AssetsConfig        `toml:"assets"`
	Manager      ManagerConfig       `toml:"manager"`
	MissionSheet MissionSheet        `toml:"mission_sheet"`
}

// ScenarioConfig はシナリオテンプレートの配置。
type ScenarioConfig struct {
	Dir string `toml:"dir"`
}

// NavigatorConfigPath はナビゲーター設定 (キャラクター・プロンプト) の配置。
type NavigatorConfigPath struct {
	Dir string `toml:"dir"`
}

// ValkeyConfig はセッション状態・会話ログ・スコアの永続化先
// (docs/game_session_design.md §9)。addr が空の場合はメモリのみで動作する。
type ValkeyConfig struct {
	Addr string `toml:"addr"`
}

// AssetsConfig は混線音声・効果音アセットの配置。
type AssetsConfig struct {
	Dir string `toml:"dir"`
}

// ManagerConfig はマネージャーの音声コマンド設定 (docs/operation_flow.md §7)。
type ManagerConfig struct {
	// SecretWord は強制リセット(キルスイッチ)の秘密ワード。運営内でのみ共有する。
	SecretWord string `toml:"secret_word"`
}

type WebSocketConfig struct {
	ListenAddr string `toml:"listen_addr"`
}

// RadioBridgeConfig は radio-bridge からのダイヤルインを受ける gRPC サーバー設定。
//
// 接続方向は反転済み (docs/bridge_connection_design.md §2 決定1) のため、
// サーバーは個々の bridge のアドレスを持たず listen アドレスのみを設定する。
// bridge の増設でサーバー側 config を変更する必要はない。
type RadioBridgeConfig struct {
	ListenAddr string `toml:"listen_addr"`
}

type GeminiConfig struct {
	APIKey               string `toml:"api_key"`
	TranscribeModel      string `toml:"transcribe_model"`
	ReasoningModel       string `toml:"reasoning_model"`
	TTSModel             string `toml:"tts_model"`
	TranscribePromptFile string `toml:"transcribe_prompt_file"`
	TranscribeSchemaFile string `toml:"transcribe_schema_file"`
}

type LogConfig struct {
	Level string `toml:"level"`
}

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
