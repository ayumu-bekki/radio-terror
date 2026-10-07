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

	// Announce は自動送信局アナウンス (docs/operation_flow.md §7.3)。
	// 混線ではなく**こちらから聞き手へ直接送る**放送なので、
	// scene を各エントリで上書きする前提にしてある。
	Announce []Voice `toml:"announce"`

	// Reask は書き起こし・発話生成の失敗時にナビゲーターが流す聞き直し
	// (game-server の navigator_reask.go)。ファイル名は <キャラクターID>_<n>。
	// 声と本文は navigator/characters/*.toml の tts_voice / reask_lines と
	// 一致させる (テストで検査する)。
	Reask []Voice `toml:"reask"`

	// Ending は解除成功・爆発の最終メッセージ (game-server の navigator_ending.go)。
	// ファイル名は <キャラクターID>_<defused|exploded>_<n>。声と本文は
	// navigator/characters/*.toml の tts_voice / defused_lines / exploded_lines と
	// 一致させる (テストで検査する)。
	Ending []Voice `toml:"ending"`
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

	// Scene は共通の [defaults] scene を上書きする。
	// 既定の scene は「傍受した混線」を前提に書かれているため、
	// 直接送信するアナウンスではそのまま使えない。
	Scene string `toml:"scene"`

	// Degrade は生成後の音声へ加えるノイズ・欠落 (degrade.go)。
	// 「ノイズで一部が途切れて聞こえにくい」聞き直しに使う。省略すれば加工しない。
	Degrade *Degrade `toml:"degrade"`
}

// Job は展開後の生成単位 (1ファイル = 1ジョブ)。
type Job struct {
	Category string // jamming / ambient / uneasy / announce / reask / ending
	Name     string // ファイル名 (拡張子なし)
	Role     string
	Model    string
	VoiceID  string
	Prompt   string // Style + 読み上げ本文 (-dry-run の表示用)
	Text     string // 本文のみ (ログ表示用。角括弧タグを含む元の台詞)
	Speech   Speech // TTS へ渡す組み立て済みの値 (3.8 TTS。style と本文を分ける)

	// Degrade は生成後に加えるノイズ・欠落。nil なら加工しない。
	Degrade *Degrade
}

const (
	catJamming  = "jamming"
	catAmbient  = "ambient"
	catUneasy   = "uneasy"
	catAnnounce = "announce"
	catReask    = "reask"
	catEnding   = "ending"
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
				Speech:   c.buildSpeech("", v.Context, text),
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
			Speech:   c.buildSpeech("", ctx, v.Text),
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
			Speech:   c.buildSpeech("", v.Context, v.Text),
			Text:     v.Text,
		})
	}

	for _, v := range c.Announce {
		if err := v.validate(catAnnounce); err != nil {
			return nil, err
		}
		jobs = append(jobs, Job{
			Category: catAnnounce,
			Name:     v.Name,
			Role:     v.Role,
			Model:    c.pickModel(v),
			VoiceID:  c.pickVoice(v),
			Speech:   c.buildSpeech(v.Scene, v.Context, v.Text),
			Text:     v.Text,
		})
	}

	for _, v := range c.Reask {
		if err := v.validate(catReask); err != nil {
			return nil, err
		}
		jobs = append(jobs, Job{
			Category: catReask,
			Name:     v.Name,
			Role:     v.Role,
			Model:    c.pickModel(v),
			VoiceID:  c.pickVoice(v),
			Speech:   c.buildSpeech(v.Scene, v.Context, v.Text),
			Text:     v.Text,
			Degrade:  v.Degrade,
		})
	}

	for _, v := range c.Ending {
		if err := v.validate(catEnding); err != nil {
			return nil, err
		}
		jobs = append(jobs, Job{
			Category: catEnding,
			Name:     v.Name,
			Role:     v.Role,
			Model:    c.pickModel(v),
			VoiceID:  c.pickVoice(v),
			Speech:   c.buildSpeech(v.Scene, v.Context, v.Text),
			Text:     v.Text,
		})
	}

	for i := range jobs {
		jobs[i].Prompt = jobs[i].Speech.String()
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
	if v.Degrade != nil {
		if err := v.Degrade.validate(); err != nil {
			return fmt.Errorf("%s %q: %w", category, v.Name, err)
		}
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

// Speech は TTS へ渡す値。**style と読み上げ本文を分ける** (Gemini 3.8 TTS)。
//
// 3.8 TTS は text を厳密な逐語録として読むので、「次のセリフを読み上げてください」や
// 状況説明を text に混ぜると**そのまま声に出る** (3.1 からの移行で実際に出た)。
// 話し方・状況の指示は `speech_metadata.style` で渡し、text は読ませる台詞だけにする。
type Speech struct {
	Style  string // speech_metadata.style (Scene / 話し方 / 台詞中の演技指示)
	Spoken string // 読み上げる本文 (<short pause> などの山かっこタグだけ含みうる)
}

// String は -dry-run の表示用。
func (s Speech) String() string {
	return "[style]\n" + s.Style + "\n\n[text]\n" + s.Spoken
}

// buildSpeech は Scene / Sample Context / 本文から style と読み上げ本文を作る。
//
// scene が空なら [defaults] の共通 scene を使う。アナウンスのように
// **傍受した混線ではない**音声では、共通 scene をそのまま使うと
// 「偶然漏れ聞こえた」という前提が邪魔になる。
//
// 本文の角括弧タグ ([relieved] など) は演技指示なので text から外し、
// 登場順に style へ移す。3.8 の山かっこタグは一時的な音声イベント専用で、
// 表情は style で伝える。[pause] だけは <short pause> として本文に残す。
func (c *Config) buildSpeech(scene, context, text string) Speech {
	if strings.TrimSpace(scene) == "" {
		scene = c.Defaults.Scene
	}
	spoken, directions := splitTags(text)

	var b strings.Builder
	b.WriteString("# Scene\n")
	b.WriteString(strings.TrimSpace(scene))
	if s := strings.TrimSpace(context); s != "" {
		b.WriteString("\n\n# Sample Context\n")
		b.WriteString(s)
	}
	if len(directions) > 0 {
		b.WriteString("\n\n# 台詞中の演技指示 (登場順。声に出して読まない)\n")
		b.WriteString(strings.Join(directions, " → "))
	}
	return Speech{Style: b.String(), Spoken: spoken}
}

// splitTags は本文から角括弧タグを外し、演技指示を登場順に返す。
// "[whispering, slowly]" はカンマで分けて別々の指示にする。
func splitTags(text string) (spoken string, directions []string) {
	spoken = tagPattern.ReplaceAllStringFunc(text, func(m string) string {
		inner := strings.TrimSpace(m[1 : len(m)-1])
		var kept []string
		for _, name := range strings.Split(inner, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if strings.EqualFold(name, "pause") {
				kept = append(kept, "<short pause>")
				continue
			}
			directions = append(directions, name)
		}
		return strings.Join(kept, " ")
	})
	spoken = strings.TrimSpace(strings.Join(strings.Fields(spoken), " "))
	return spoken, directions
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
