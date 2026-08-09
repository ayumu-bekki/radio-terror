package main

import (
	"context"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"google.golang.org/genai"
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

// GeminiConfig は生成AI (Gemini Enterprise Agent Platform) の設定。
//
// 認証は ADC (Application Default Credentials) を使うため、
// この設定に秘密情報は含まれない。鍵の場所は環境変数
// GOOGLE_APPLICATION_CREDENTIALS で渡す (docs/gemini_enterprise_setup.md)。
type GeminiConfig struct {
	// Project / Location は接続先。
	// 環境変数 GOOGLE_CLOUD_PROJECT / GOOGLE_CLOUD_LOCATION でも指定できるが、
	// ここに書いた値が優先される。
	// Location は TTS モデルの提供リージョンに合わせる (例: "us-central1")。
	Project  string `toml:"project"`
	Location string `toml:"location"`

	TranscribeModel      string `toml:"transcribe_model"`
	ReasoningModel       string `toml:"reasoning_model"`
	TTSModel             string `toml:"tts_model"`
	TranscribePromptFile string `toml:"transcribe_prompt_file"`
	TranscribeSchemaFile string `toml:"transcribe_schema_file"`
}

// Validate は接続先が揃っているかを確認する。
// 起動時に落とすことで、実行中の初回API呼び出しまで気づかない事態を避ける。
func (c GeminiConfig) Validate() error {
	if c.Project == "" {
		return fmt.Errorf("[gemini] project が未設定です " +
			"(環境変数 GOOGLE_CLOUD_PROJECT でも指定できます)")
	}
	if c.Location == "" {
		return fmt.Errorf("[gemini] location が未設定です " +
			"(環境変数 GOOGLE_CLOUD_LOCATION でも指定できます)")
	}
	return nil
}

// NewGenAIClient は Gemini Enterprise Agent Platform のクライアントを作る。
//
// 認証は ADC (gcloud auth application-default login、または
// GOOGLE_APPLICATION_CREDENTIALS のサービスアカウントキー)。
func NewGenAIClient(ctx context.Context, c GeminiConfig) (*genai.Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendEnterprise,
		Project:  c.Project,
		Location: c.Location,
	})
}

type LogConfig struct {
	Level string `toml:"level"`
}

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	// 未指定なら環境変数にフォールバックする (SDK が読むものと同じ変数)
	if cfg.Gemini.Project == "" {
		cfg.Gemini.Project = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if cfg.Gemini.Location == "" {
		cfg.Gemini.Location = os.Getenv("GOOGLE_CLOUD_LOCATION")
	}
	return &cfg, nil
}
