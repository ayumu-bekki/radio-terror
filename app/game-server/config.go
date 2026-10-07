package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
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
	Announce     AnnounceConfig      `toml:"announce"`
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

// AnnounceConfig は自動送信局アナウンスの設定 (docs/operation_flow.md §7.3)。
//
// 特小無線は共用チャンネルのため、他の利用者へ「これは自動送信局である」と
// 定期的に名乗る。体験中の無線には流さない。
type AnnounceConfig struct {
	// IntervalMin は送出周期 (分)。0 なら既定値 15分。
	IntervalMin int `toml:"interval_min"`

	// Disabled は true でアナウンスを止める。
	// 自宅での開発中など、鳴らす必要がない場面用。
	Disabled bool `toml:"disabled"`
}

// Interval は送出周期を time.Duration で返す (未設定なら既定値)。
func (c AnnounceConfig) Interval() time.Duration {
	if c.IntervalMin <= 0 {
		return announceIntervalDefault
	}
	return time.Duration(c.IntervalMin) * time.Minute
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

	TranscribeModel string `toml:"transcribe_model"`
	ReasoningModel  string `toml:"reasoning_model"`
	// ReasoningThinkingLevel は ReasoningModel の思考レベル
	// ("minimal" / "low" / "medium" / "high")。空ならモデルの既定。
	ReasoningThinkingLevel string `toml:"reasoning_thinking_level"`
	TTSModel               string `toml:"tts_model"`
	TranscribePromptFile   string `toml:"transcribe_prompt_file"`
	TranscribeSchemaFile   string `toml:"transcribe_schema_file"`

	// 各API呼び出しのタイムアウト (秒)。0 なら既定値を使う。
	//
	// 実運用で TTS が 58 秒かかり、その間ずっと発話が止まる事象が出た。
	// 呼び出し側の ctx はプロセス終了まで生きるため、ここで上限を切らないと
	// 無制限に待つ。無線は「無音のまま待たされる」のが最悪なので、
	// 待ち続けるより打ち切ってその発話を捨てるほうがよい。
	//
	// **タイムアウトは1回の試行あたり**。試行回数 (*_attempts) を増やすと、
	// 最悪の待ちは タイムアウト × 回数 になる。
	TranscribeTimeoutSec int `toml:"transcribe_timeout_sec"`
	ReplyTimeoutSec      int `toml:"reply_timeout_sec"`
	TTSTimeoutSec        int `toml:"tts_timeout_sec"`

	// 各API呼び出しを試す回数 (初回を含む)。0 なら既定値。
	//
	// **書き起こしと発話生成の既定は1回 (再試行しない)**。失敗したら
	// プレイヤーへ事前収録の「聞き直し」を流す (navigator_reask.go)。
	// 待って撃ち直すより、聞き直してもらうほうが無線の間が短い。
	// 429・5xx・タイムアウトだけが再試行の対象 (isRetryable)。
	// TTS も既定は1回 (defaultTTSAttempts 参照)。
	TranscribeAttempts int `toml:"transcribe_attempts"`
	ReplyAttempts      int `toml:"reply_attempts"`
	TTSAttempts        int `toml:"tts_attempts"`
}

// TranscribeAttemptCount / ReplyAttemptCount は書き起こし・発話生成の試行回数を返す
// (未設定なら既定値)。
func (c GeminiConfig) TranscribeAttemptCount() int {
	return attemptsOrDefault(c.TranscribeAttempts, defaultTranscribeAttempts)
}

func (c GeminiConfig) ReplyAttemptCount() int {
	return attemptsOrDefault(c.ReplyAttempts, defaultReplyAttempts)
}

func attemptsOrDefault(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return n
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
//
// **書き起こし・発話生成は短く切る** (1回あたり 8秒・5秒)。長く待つと無線が
// 無音のまま続く。切ったら再試行せず、事前収録の聞き直しを流す
// (navigator_reask.go)。値は実運用ログの遅延分布を見て調整する前提の出発点。
// reply は 3.5-flash-lite で約1秒、履歴の載る発話でその約2倍。書き起こしは
// 通常2秒前後だが、長い発話やコールドスタート (6〜10秒) で伸びる。
const (
	defaultTranscribeTimeout = 8 * time.Second
	defaultReplyTimeout      = 5 * time.Second
	defaultTTSTimeout        = 10 * time.Second
)

// 書き起こし・発話生成の既定の試行回数 (再試行しない)。
const (
	defaultTranscribeAttempts = 1
	defaultReplyAttempts      = 1
)

// defaultTTSAttempts は TTS を試す回数 (初回 + リトライ)。**1 = 再試行しない**。
//
// 失敗 (10秒で見切る・エラー) したら再試行せず、プレイヤーへ事前収録の聞き直しを流す
// (ADR G-7)。最悪でも 10秒で見切りをつける。以前は 3 回試していた (実測では
// 10秒以内に返るのが約73%で、3回試せば大半が拾えるが、外すと最悪30秒の無音になった)。
// 聞き直しが多すぎるなら、設定ファイルの tts_attempts を上げる。
const defaultTTSAttempts = 1

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
	if _, err := c.thinkingLevel(); err != nil {
		return err
	}
	return nil
}

// thinkingLevel は reasoning_thinking_level を SDK の値へ変換する。
func (c GeminiConfig) thinkingLevel() (genai.ThinkingLevel, error) {
	level := strings.ToLower(strings.TrimSpace(c.ReasoningThinkingLevel))
	if level == "" {
		return "", nil
	}
	for _, l := range []genai.ThinkingLevel{genai.ThinkingLevelMinimal, genai.ThinkingLevelLow,
		genai.ThinkingLevelMedium, genai.ThinkingLevelHigh} {
		if strings.EqualFold(string(l), level) {
			return l, nil
		}
	}
	return "", fmt.Errorf("[gemini] reasoning_thinking_level %q は minimal / low / medium / high のいずれかにしてください", c.ReasoningThinkingLevel)
}

// ReasoningThinkingConfig は ReasoningModel へ渡す思考設定。未指定なら nil (モデルの既定)。
func (c GeminiConfig) ReasoningThinkingConfig() *genai.ThinkingConfig {
	level, _ := c.thinkingLevel()
	if level == "" {
		return nil
	}
	return &genai.ThinkingConfig{ThinkingLevel: level}
}

// NewGenAIClient は Gemini Enterprise Agent Platform のクライアントを作る。
//
// 認証は ADC (gcloud auth application-default login、または
// GOOGLE_APPLICATION_CREDENTIALS のサービスアカウントキー)。
func NewGenAIClient(ctx context.Context, c GeminiConfig) (*genai.Client, error) {
	return newGenAIClient(ctx, c, false)
}

// priorityPayGoHeader は Priority PayGo を指定するリクエストヘッダー (ADR G-5b)。
// `service_tier` (G-5。Enterprise では 400) とは別の仕組みで、ヘッダーで指定する。
const (
	priorityPayGoHeaderName  = "X-Vertex-AI-LLM-Shared-Request-Type"
	priorityPayGoHeaderValue = "priority"
)

// NewPriorityGenAIClient は Priority PayGo を常時指定したクライアントを作る。
// TTS 以外の呼び出し (書き起こし・発話生成・カラス) 用。
// Provisioned Throughput は使っていないので、ヘッダーは priority のみ
// (`X-Vertex-AI-LLM-Request-Type: shared` は付けない)。
func NewPriorityGenAIClient(ctx context.Context, c GeminiConfig) (*genai.Client, error) {
	return newGenAIClient(ctx, c, true)
}

func newGenAIClient(ctx context.Context, c GeminiConfig, priority bool) (*genai.Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	cfg := &genai.ClientConfig{
		Backend:  genai.BackendEnterprise,
		Project:  c.Project,
		Location: c.Location,
	}
	if priority {
		cfg.HTTPOptions = genai.HTTPOptions{
			Headers: http.Header{priorityPayGoHeaderName: []string{priorityPayGoHeaderValue}},
		}
	}
	return genai.NewClient(ctx, cfg)
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
