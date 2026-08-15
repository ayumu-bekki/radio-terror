package main

import (
	"context"
	"fmt"
	"os"
	"time"

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

	// 各API呼び出しのタイムアウト (秒)。0 なら既定値を使う。
	//
	// 実運用で TTS が 58 秒かかり、その間ずっと発話が止まる事象が出た。
	// 呼び出し側の ctx はプロセス終了まで生きるため、ここで上限を切らないと
	// 無制限に待つ。無線は「無音のまま待たされる」のが最悪なので、
	// 待ち続けるより打ち切ってその発話を捨てるほうがよい。
	TranscribeTimeoutSec int `toml:"transcribe_timeout_sec"`
	ReplyTimeoutSec      int `toml:"reply_timeout_sec"`
	TTSTimeoutSec        int `toml:"tts_timeout_sec"`

	// TTSAttempts は TTS を試す回数 (初回を含む)。0 なら既定値。
	// 打ち切った呼び出しは作り直す。詳細は defaultTTSAttempts 参照。
	TTSAttempts int `toml:"tts_attempts"`
}

// TTSAttemptCount は TTS の試行回数を返す (未設定なら既定値)。
func (c GeminiConfig) TTSAttemptCount() int {
	if c.TTSAttempts <= 0 {
		return defaultTTSAttempts
	}
	return c.TTSAttempts
}

// 各API呼び出しのタイムアウト既定値。
//
// **TTS は待たずに切って作り直す**。実測 (tts_latency_probe_test.go) では
// 中央値 4.16秒に対し、同じプロンプトで 24〜56秒かかる回が混じる。
// 待ち時間は出力の長さと**無関係** (遅い回も音声は 5秒前後) で、
// 遅い回が連続する時間帯もある。Gemini 側の事情なのでこちらでは直せない。
//
// 無線で10秒の無音は事故に見えるため、10秒で見切って作り直す。
// 遅い回を引いても次は当たり直せる (実測でも 56秒 → 29秒 → 24秒 → 4秒 と
// 呼び出しごとに変わる)。長く待つより速く終わる。
const (
	defaultTranscribeTimeout = 20 * time.Second
	defaultReplyTimeout      = 20 * time.Second
	defaultTTSTimeout        = 10 * time.Second
)

// defaultTTSAttempts は TTS を試す回数 (初回 + リトライ)。
//
// 実測では 10秒以内に返るのが約73%。3回試せば大半が拾える。
// 最悪でも 10秒×3 = 30秒で見切りをつける。
const defaultTTSAttempts = 3

// TranscribeTimeout / ReplyTimeout / TTSTimeout は設定値を time.Duration で返す。
// 未設定 (0) の場合は既定値を返す。
func (c GeminiConfig) TranscribeTimeout() time.Duration {
	return timeoutOrDefault(c.TranscribeTimeoutSec, defaultTranscribeTimeout)
}

func (c GeminiConfig) ReplyTimeout() time.Duration {
	return timeoutOrDefault(c.ReplyTimeoutSec, defaultReplyTimeout)
}

func (c GeminiConfig) TTSTimeout() time.Duration {
	return timeoutOrDefault(c.TTSTimeoutSec, defaultTTSTimeout)
}

func timeoutOrDefault(sec int, fallback time.Duration) time.Duration {
	if sec <= 0 {
		return fallback
	}
	return time.Duration(sec) * time.Second
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
