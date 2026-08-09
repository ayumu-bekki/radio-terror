package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config は crosstalk.toml 全体。
//
// 接続先は Gemini Enterprise Agent Platform (旧 Vertex AI) のみ。
// 認証は ADC を使うため、設定ファイルに秘密情報は一切含まれない。
type Config struct {
	Defaults    Defaults          `toml:"defaults"`
	Colors      map[string]string `toml:"colors"`
	AmbientBase string            `toml:"ambient_base"`

	Jamming []Voice `toml:"jamming"`
	Ambient []Voice `toml:"ambient"`
	Uneasy  []Voice `toml:"uneasy"`
}

type Defaults struct {
	// Project / Location は Gemini Enterprise Agent Platform の接続先。
	// 環境変数 GOOGLE_CLOUD_PROJECT / GOOGLE_CLOUD_LOCATION でも指定できるが、
	// ここに書いた値が優先される。
	Project  string `toml:"project"`
	Location string `toml:"location"`

	Model       string `toml:"model"`
	Voice       string `toml:"voice"`
	Scene       string `toml:"scene"`
	OpusBitrate int    `toml:"opus_bitrate"`
	KeepWAV     bool   `toml:"keep_wav"`
	Concurrency int    `toml:"concurrency"`
	MaxRetries  int    `toml:"max_retries"`
	SkipExists  bool   `toml:"skip_existing"`

	// RequestIntervalMs は API リクエストの最小間隔。
	// Gemini API のレート制限 (RPM) に当たらないよう間隔を空ける。
	RequestIntervalMs int `toml:"request_interval_ms"`
	// MaxRequests は1回の実行で投げるリクエスト数の上限 (0 で無制限)。
	// 事故でレート枠を食い潰さないための安全弁。
	MaxRequests int `toml:"max_requests"`
}

// Voice は1つの発話定義。model / voice は省略時に defaults が使われる。
type Voice struct {
	Name    string `toml:"name"`
	Role    string `toml:"role"`
	Context string `toml:"context"`
	Text    string `toml:"text"`
	Model   string `toml:"model"`
	Voice   string `toml:"voice"`
}

// Job は展開後の生成単位 (1ファイル = 1ジョブ)。
type Job struct {
	Category string // jamming / ambient / uneasy
	Name     string // ファイル名 (拡張子なし)
	Role     string
	Model    string
	VoiceID  string
	Prompt   string // Scene + Context + 本文を組み立てたもの
	Text     string // 本文のみ (ログ表示用)
}

const (
	catJamming = "jamming"
	catAmbient = "ambient"
	catUneasy  = "uneasy"
)

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if cfg.Defaults.Model == "" {
		return nil, fmt.Errorf("defaults.model is required")
	}
	if strings.TrimSpace(cfg.Defaults.Scene) == "" {
		return nil, fmt.Errorf("defaults.scene is required")
	}
	if cfg.Defaults.OpusBitrate <= 0 {
		cfg.Defaults.OpusBitrate = 16000
	}
	if cfg.Defaults.Concurrency <= 0 {
		cfg.Defaults.Concurrency = 1
	}
	if cfg.Defaults.MaxRetries < 0 {
		cfg.Defaults.MaxRetries = 0
	}
	if cfg.Defaults.RequestIntervalMs < 0 {
		cfg.Defaults.RequestIntervalMs = 0
	}
	if cfg.Defaults.MaxRequests < 0 {
		cfg.Defaults.MaxRequests = 0
	}
	// 未指定なら環境変数にフォールバックする (SDK が読む)
	if cfg.Defaults.Project == "" {
		cfg.Defaults.Project = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if cfg.Defaults.Location == "" {
		cfg.Defaults.Location = os.Getenv("GOOGLE_CLOUD_LOCATION")
	}
	if cfg.Defaults.Project == "" || cfg.Defaults.Location == "" {
		return nil, fmt.Errorf("[defaults] project と location が必要です\n" +
			"  crosstalk.toml に書くか、環境変数 GOOGLE_CLOUD_PROJECT /\n" +
			"  GOOGLE_CLOUD_LOCATION で指定してください")
	}
	return &cfg, nil
}

// BuildJobs は設定を展開して生成ジョブ一覧を作る。
// jamming は ${color} を色数ぶん展開し {name}_{記号} にする。
func (c *Config) BuildJobs() ([]Job, error) {
	var jobs []Job

	// 色記号を安定順で回す (実行ごとに順序が変わらないように)
	colorKeys := make([]string, 0, len(c.Colors))
	for k := range c.Colors {
		colorKeys = append(colorKeys, k)
	}
	sort.Strings(colorKeys)

	for _, v := range c.Jamming {
		if err := v.validate(catJamming); err != nil {
			return nil, err
		}
		if !strings.Contains(v.Text, "${color}") {
			return nil, fmt.Errorf("jamming %q: text must contain ${color}", v.Name)
		}
		if len(colorKeys) == 0 {
			return nil, fmt.Errorf("jamming %q: [colors] is empty", v.Name)
		}
		for _, code := range colorKeys {
			text := strings.ReplaceAll(v.Text, "${color}", c.Colors[code])
			jobs = append(jobs, Job{
				Category: catJamming,
				Name:     fmt.Sprintf("%s_%s", v.Name, code),
				Role:     v.Role,
				Model:    c.pickModel(v),
				VoiceID:  c.pickVoice(v),
				Prompt:   c.buildPrompt(v.Context, text),
				Text:     text,
			})
		}
	}

	for _, v := range c.Ambient {
		if err := v.validate(catAmbient); err != nil {
			return nil, err
		}
		// ambient は共通ベースに個別の一行を足す (話者を毎回変えるため)
		ctx := joinContext(c.AmbientBase, v.Context)
		jobs = append(jobs, Job{
			Category: catAmbient,
			Name:     v.Name,
			Role:     v.Role,
			Model:    c.pickModel(v),
			VoiceID:  c.pickVoice(v),
			Prompt:   c.buildPrompt(ctx, v.Text),
			Text:     v.Text,
		})
	}

	for _, v := range c.Uneasy {
		if err := v.validate(catUneasy); err != nil {
			return nil, err
		}
		jobs = append(jobs, Job{
			Category: catUneasy,
			Name:     v.Name,
			Role:     v.Role,
			Model:    c.pickModel(v),
			VoiceID:  c.pickVoice(v),
			Prompt:   c.buildPrompt(v.Context, v.Text),
			Text:     v.Text,
		})
	}

	if len(jobs) == 0 {
		return nil, fmt.Errorf("no voices defined")
	}
	if err := checkDuplicateNames(jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (v Voice) validate(category string) error {
	if strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("%s: name is required", category)
	}
	if strings.TrimSpace(v.Text) == "" {
		return fmt.Errorf("%s %q: text is required", category, v.Name)
	}
	// サーバーは {name}_{色}.ogg を最後の "_" で分割するため、
	// jamming の name に "_" があるとファイル名の解釈がずれる。
	if category == catJamming && strings.Contains(v.Name, "_") {
		return fmt.Errorf("jamming %q: name must not contain '_' (色サフィックスと衝突する)", v.Name)
	}
	return nil
}

// checkDuplicateNames は同一カテゴリ内でのファイル名衝突を検出する。
// 衝突すると後勝ちで上書きされ、片方が無言で失われる。
func checkDuplicateNames(jobs []Job) error {
	seen := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		key := j.Category + "/" + j.Name
		if seen[key] {
			return fmt.Errorf("duplicate name: %s", key)
		}
		seen[key] = true
	}
	return nil
}

func (c *Config) pickModel(v Voice) string {
	if v.Model != "" {
		return v.Model
	}
	return c.Defaults.Model
}

func (c *Config) pickVoice(v Voice) string {
	if v.Voice != "" {
		return v.Voice
	}
	return c.Defaults.Voice
}

// buildPrompt は Scene / Sample Context / 本文を組み立てる。
//
// 読み上げ本文にはタグを入れず、口調指定は context に一本化している
// (docs/crosstalk_audio_generation.md 決定記録 #3)。
func (c *Config) buildPrompt(context, text string) string {
	var b strings.Builder
	b.WriteString("# Scene\n")
	b.WriteString(strings.TrimSpace(c.Defaults.Scene))
	if s := strings.TrimSpace(context); s != "" {
		b.WriteString("\n\n# Sample Context\n")
		b.WriteString(s)
	}
	b.WriteString("\n\n# 読み上げるセリフ\n")
	// 角括弧は演技指示であって読み上げ対象ではない、と明示する。
	// これを書かないと TTS がタグを言葉として読んでしまうことがある。
	//
	// この一文に "[...]" というリテラルを使うと、TTS API が
	// 400 (INVALID_ARGUMENT) を返す (実測で再現。"[abc]" や「...」なら通る)。
	// 角括弧を日本語で言い表して回避する。
	b.WriteString("次のセリフだけを読み上げてください。前後に説明や補足を加えないこと。\n")
	b.WriteString("角括弧の中は演技の指示です。声に出して読まず、その通りの話し方に反映してください。\n\n")
	b.WriteString(strings.TrimSpace(text))
	return b.String()
}

// tagPattern は本文中の非言語タグ (例: [whispering]) にマッチする。
var tagPattern = regexp.MustCompile(`\[[^\[\]]*\]`)

// stripTags は非言語タグを取り除いた読み上げ本文を返す (ログ表示・検証用)。
//
// 日本語には語間の空白がないため、タグは空白に置き換えず除去する。
// タグの直後に置いた区切りの空白だけを畳む。
func stripTags(s string) string {
	out := tagPattern.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.ReplaceAll(out, "  ", " "))
}

func joinContext(base, extra string) string {
	base, extra = strings.TrimSpace(base), strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n" + extra
	}
}
