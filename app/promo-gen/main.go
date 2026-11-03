// promo-gen は紹介動画用のナビゲーター音声を Gemini TTS で生成する。
//
// ゲームで使う音声アセット (crosstalk-gen) とは**プログラムも設定も出力先も別**。
// ゲームの game-server/assets には何も書き込まない。出力は 24kHz mono 16bit の
// PCM (WAV) のままで、Opus への変換や無線らしい加工は行わない。
//
//	GOOGLE_APPLICATION_CREDENTIALS=../secrets/geap-sa.json go run .
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"google.golang.org/genai"
)

// Gemini TTS の出力は 24kHz / mono / 16bit PCM。
const (
	sampleRate = 24000
	channels   = 1
)

type Config struct {
	Defaults Defaults `toml:"defaults"`
	Lines    []Line   `toml:"line"`
}

type Defaults struct {
	Project           string `toml:"project"`
	Location          string `toml:"location"`
	Model             string `toml:"model"`
	MaxRetries        int    `toml:"max_retries"`
	RequestIntervalMs int    `toml:"request_interval_ms"`
	SkipExisting      bool   `toml:"skip_existing"`
}

// Line は1本のセリフ。ファイル名は name.wav。
type Line struct {
	Name    string `toml:"name"`
	Role    string `toml:"role"`
	Voice   string `toml:"voice"`
	Scene   string `toml:"scene"`
	Context string `toml:"context"`
	Text    string `toml:"text"`
}

func main() {
	var (
		configPath = flag.String("config", "promo.toml", "設定ファイル")
		outDir     = flag.String("out", "out", "出力先ディレクトリ (WAV)")
		only       = flag.String("only", "", "名前で絞る (カンマ区切り。例: owl_pitch,owl_play_1)")
		dryRun     = flag.Bool("dry-run", false, "APIを呼ばず、組み立てた style と本文を表示する")
		list       = flag.Bool("list", false, "対象一覧を表示する")
		force      = flag.Bool("force", false, "既存ファイルがあっても再生成する")
	)
	flag.Parse()

	if err := run(*configPath, *outDir, *only, *dryRun, *list, *force); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, outDir, only string, dryRun, list, force bool) error {
	var cfg Config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return fmt.Errorf("decode %s: %w", configPath, err)
	}
	if cfg.Defaults.Model == "" || cfg.Defaults.Project == "" || cfg.Defaults.Location == "" {
		return errors.New("[defaults] の model / project / location が必要です")
	}
	lines, err := selectLines(cfg.Lines, only)
	if err != nil {
		return err
	}
	fmt.Printf("設定: %s\n対象: %d / %d 件\n\n", configPath, len(lines), len(cfg.Lines))

	if list {
		for _, l := range lines {
			fmt.Printf("%-16s %-10s %s\n", l.Name+".wav", l.Voice, stripTags(l.Text))
		}
		return nil
	}
	if dryRun {
		for i, l := range lines {
			style, spoken := buildSpeech(l)
			fmt.Printf("=== [%d/%d] %s.wav (voice=%s) ===\n[style]\n%s\n\n[text]\n%s\n\n",
				i+1, len(lines), l.Name, l.Voice, style, spoken)
		}
		fmt.Println("APIは呼んでいません。")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendEnterprise,
		Project:  cfg.Defaults.Project,
		Location: cfg.Defaults.Location,
	})
	if err != nil {
		return fmt.Errorf("genai.NewClient: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	interval := time.Duration(cfg.Defaults.RequestIntervalMs) * time.Millisecond
	skip := cfg.Defaults.SkipExisting && !force
	var failed []string
	for i, l := range lines {
		path := filepath.Join(outDir, l.Name+".wav")
		if _, err := os.Stat(path); skip && err == nil {
			fmt.Printf("[%d/%d] skip   %s\n", i+1, len(lines), l.Name)
			continue
		}
		pcm, err := synthesize(ctx, client, cfg.Defaults, l, interval)
		if err != nil {
			fmt.Printf("[%d/%d] FAILED %s: %v\n", i+1, len(lines), l.Name, err)
			failed = append(failed, l.Name)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if err := os.WriteFile(path, buildWAV(pcm), 0o644); err != nil {
			return err
		}
		fmt.Printf("[%d/%d] ok     %s (%.1f秒)\n", i+1, len(lines), l.Name,
			float64(len(pcm))/sampleRate)
	}
	if len(failed) > 0 {
		return fmt.Errorf("失敗 %d件: %s (再実行で不足分だけ作り直せます)",
			len(failed), strings.Join(failed, ", "))
	}
	return nil
}

func selectLines(all []Line, only string) ([]Line, error) {
	seen := map[string]bool{}
	for _, l := range all {
		if strings.TrimSpace(l.Name) == "" || strings.TrimSpace(l.Text) == "" || l.Voice == "" {
			return nil, fmt.Errorf("name / voice / text は必須です: %+v", l)
		}
		if seen[l.Name] {
			return nil, fmt.Errorf("name が重複しています: %s", l.Name)
		}
		seen[l.Name] = true
	}
	if only == "" {
		return all, nil
	}
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	var out []Line
	for _, l := range all {
		if want[l.Name] {
			out = append(out, l)
			delete(want, l.Name)
		}
	}
	if len(want) > 0 {
		var missing []string
		for n := range want {
			missing = append(missing, n)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("-only に一致しない名前があります: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// synthesize は1本を合成する。TTS は 400 を散発的に返すので投げ直す。
func synthesize(ctx context.Context, client *genai.Client, d Defaults, l Line, interval time.Duration) ([]int16, error) {
	var lastErr error
	for attempt := 0; attempt <= d.MaxRetries; attempt++ {
		wait := interval
		if attempt > 0 {
			wait = 2 * time.Second
			if lastErr != nil && strings.Contains(lastErr.Error(), "429") {
				wait = time.Duration(1<<uint(attempt-1)) * 15 * time.Second
			}
			fmt.Printf("  retry %s (%d/%d): %v\n", l.Name, attempt, d.MaxRetries, shortErr(lastErr))
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		pcm, err := synthesizeOnce(ctx, client, d.Model, l)
		if err == nil {
			return pcm, nil
		}
		lastErr = err
		if strings.Contains(err.Error(), "PERMISSION_DENIED") ||
			strings.Contains(err.Error(), "could not find default credentials") {
			return nil, err
		}
	}
	return nil, lastErr
}

// synthesizeOnce は style (speech_metadata) と本文を分けて渡す。
// 3.8 TTS は text を逐語録として読むため、指示を text に混ぜると声に出る。
func synthesizeOnce(ctx context.Context, client *genai.Client, model string, l Line) ([]int16, error) {
	style, spoken := buildSpeech(l)
	provide := func(body map[string]any) map[string]any {
		part := map[string]any{"text": spoken}
		if style != "" {
			part["speech_metadata"] = map[string]any{"style": style}
		}
		body["contents"] = []any{map[string]any{"role": "user", "parts": []any{part}}}
		return body
	}
	resp, err := client.Models.GenerateContent(ctx, model,
		[]*genai.Content{genai.NewContentFromText(spoken, genai.RoleUser)},
		&genai.GenerateContentConfig{
			HTTPOptions:        &genai.HTTPOptions{ExtrasRequestProvider: provide},
			ResponseModalities: []string{"audio"},
			SpeechConfig: &genai.SpeechConfig{
				VoiceConfig: &genai.VoiceConfig{
					PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: l.Voice},
				},
			},
		})
	if err != nil {
		return nil, fmt.Errorf("GenerateContent: %w", err)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 ||
		resp.Candidates[0].Content.Parts[0].InlineData == nil {
		return nil, errors.New("empty TTS response")
	}
	data := stripWAVHeader(resp.Candidates[0].Content.Parts[0].InlineData.Data)
	if len(data) < 2 || len(data)%2 != 0 {
		return nil, fmt.Errorf("invalid PCM length: %d", len(data))
	}
	pcm := make([]int16, len(data)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return pcm, nil
}

var tagPattern = regexp.MustCompile(`\[[^\[\]]*\]`)

// buildSpeech は Scene / Sample Context / 台詞中の演技指示から style を作る。
// 本文の角括弧タグ ([calm] など) は演技指示なので text から外して style へ移す。
// [pause] だけは <short pause> として本文に残す (crosstalk-gen と同じ作法)。
func buildSpeech(l Line) (style, spoken string) {
	var directions []string
	spoken = tagPattern.ReplaceAllStringFunc(l.Text, func(m string) string {
		var kept []string
		for _, name := range strings.Split(strings.TrimSpace(m[1:len(m)-1]), ",") {
			name = strings.TrimSpace(name)
			if strings.EqualFold(name, "pause") {
				kept = append(kept, "<short pause>")
			} else if name != "" {
				directions = append(directions, name)
			}
		}
		return strings.Join(kept, " ")
	})
	spoken = strings.TrimSpace(strings.Join(strings.Fields(spoken), " "))

	var b strings.Builder
	b.WriteString("# Scene\n")
	b.WriteString(strings.TrimSpace(l.Scene))
	if s := strings.TrimSpace(l.Context); s != "" {
		b.WriteString("\n\n# Sample Context\n")
		b.WriteString(s)
	}
	if len(directions) > 0 {
		b.WriteString("\n\n# 台詞中の演技指示 (登場順。声に出して読まない)\n")
		b.WriteString(strings.Join(directions, " → "))
	}
	return b.String(), spoken
}

func stripTags(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(tagPattern.ReplaceAllString(s, ""), "  ", " "))
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, ", Details:"); i > 0 {
		s = s[:i]
	}
	return s
}

// stripWAVHeader は先頭に "RIFF" がある場合、"data" チャンクの中身を返す。
func stripWAVHeader(data []byte) []byte {
	if len(data) < 12 || !bytes.HasPrefix(data, []byte("RIFF")) {
		return data
	}
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if id == "data" {
			end := pos + size
			if end > len(data) {
				end = len(data)
			}
			return data[pos:end]
		}
		pos += size + size%2
	}
	return data
}

// buildWAV は 24kHz mono 16bit の WAV を組み立てる。
func buildWAV(pcm []int16) []byte {
	dataSize := len(pcm) * 2
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataSize))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataSize))
	binary.Write(&b, binary.LittleEndian, pcm)
	return b.Bytes()
}
