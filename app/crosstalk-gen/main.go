// crosstalk-gen は混線音声アセットを Gemini TTS で一括生成する。
//
// 接続先は Gemini Enterprise Agent Platform (旧 Vertex AI)。認証は ADC。
//
//	GOOGLE_APPLICATION_CREDENTIALS=../secrets/sa.json go run .
//
// 設定を変えて試す用途を想定しているため、-dry-run でプロンプトの確認、
// -only で対象の絞り込みができる。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	var (
		configPath = flag.String("config", "crosstalk.toml", "設定ファイルのパス")
		outDir     = flag.String("out", "../game-server/assets/crosstalk", "出力先ディレクトリ")
		only       = flag.String("only", "", "生成対象を名前で絞る (カンマ区切り。例: aserase_A,kuromaku)")
		category   = flag.String("category", "", "カテゴリで絞る (jamming/ambient/uneasy/announce)")
		dryRun     = flag.Bool("dry-run", false, "APIを呼ばず、生成されるプロンプトとファイル名だけ表示する")
		force      = flag.Bool("force", false, "既存ファイルがあっても再生成する (skip_existing を無視)")
		list       = flag.Bool("list", false, "生成対象の一覧だけ表示する")
	)
	flag.Parse()

	if err := run(*configPath, *outDir, *only, *category, *dryRun, *force, *list); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, outDir, only, category string, dryRun, force, list bool) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}

	jobs, err := cfg.BuildJobs()
	if err != nil {
		return err
	}
	total := len(jobs)

	if jobs, err = filterJobs(jobs, only, category); err != nil {
		return err
	}
	fmt.Printf("設定: %s\n対象: %d / %d 件\n\n", configPath, len(jobs), total)

	if list {
		printList(jobs)
		return nil
	}
	if dryRun {
		printDryRun(jobs)
		return nil
	}

	// Ctrl-C で進行中のリクエストも打ち切る
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gen, err := NewGenerator(ctx, cfg)
	if err != nil {
		return err
	}

	skipExisting := cfg.Defaults.SkipExists && !force

	// レート制限に当たりやすいので、投げる前に見込みを表示する
	fmt.Printf("接続先: Gemini Enterprise Agent Platform (project=%s location=%s)\n",
		cfg.Defaults.Project, cfg.Defaults.Location)
	fmt.Printf("レート制御: 間隔 %dms / 並列 %d / リトライ上限 %d回",
		cfg.Defaults.RequestIntervalMs, cfg.Defaults.Concurrency, cfg.Defaults.MaxRetries)
	if cfg.Defaults.MaxRequests > 0 {
		fmt.Printf(" / リクエスト上限 %d回", cfg.Defaults.MaxRequests)
	}
	fmt.Printf("\n最短所要: 約%s\n\n",
		(time.Duration(len(jobs)*cfg.Defaults.RequestIntervalMs) * time.Millisecond).Round(time.Second))

	err = generateAll(ctx, cfg, gen, jobs, outDir, skipExisting)
	fmt.Printf("APIリクエスト消費: %d回\n", gen.Used())
	return err
}

// filterJobs は -only / -category で対象を絞る。
func filterJobs(jobs []Job, only, category string) ([]Job, error) {
	if category != "" {
		switch category {
		case catJamming, catAmbient, catUneasy, catAnnounce:
		default:
			return nil, fmt.Errorf("unknown category %q (jamming/ambient/uneasy/announce)", category)
		}
		var out []Job
		for _, j := range jobs {
			if j.Category == category {
				out = append(out, j)
			}
		}
		jobs = out
	}

	if only != "" {
		want := make(map[string]bool)
		for _, n := range strings.Split(only, ",") {
			if n = strings.TrimSpace(n); n != "" {
				want[n] = true
			}
		}
		var out []Job
		for _, j := range jobs {
			if want[j.Name] {
				out = append(out, j)
				delete(want, j.Name)
			}
		}
		// 指定名のタイポに気づけるようにする (黙って0件にしない)
		if len(want) > 0 {
			var missing []string
			for n := range want {
				missing = append(missing, n)
			}
			sort.Strings(missing)
			return nil, fmt.Errorf("-only に一致しない名前があります: %s", strings.Join(missing, ", "))
		}
		jobs = out
	}

	if len(jobs) == 0 {
		return nil, fmt.Errorf("生成対象が0件です")
	}
	return jobs, nil
}

func printList(jobs []Job) {
	fmt.Printf("%-10s %-22s %-14s %s\n", "CATEGORY", "FILE", "VOICE", "TEXT (タグ除去)")
	fmt.Println(strings.Repeat("-", 100))
	for _, j := range jobs {
		fmt.Printf("%-10s %-22s %-14s %s\n",
			j.Category, j.Name+".ogg", j.VoiceID, truncate(stripTags(j.Text), 46))
	}
}

// truncate は表示用に文字列を切り詰める (日本語を含むため rune 単位で数える)。
func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func printDryRun(jobs []Job) {
	for i, j := range jobs {
		fmt.Printf("=== [%d/%d] %s/%s.ogg (voice=%s model=%s) ===\n",
			i+1, len(jobs), j.Category, j.Name, j.VoiceID, j.Model)
		fmt.Println(j.Prompt)
		fmt.Println()
	}
	fmt.Printf("%d 件。APIは呼んでいません。\n", len(jobs))
}

type result struct {
	job     Job
	skipped bool
	err     error
	dur     time.Duration
	bytes   int
}

func generateAll(ctx context.Context, cfg *Config, gen *Generator, jobs []Job, outDir string, skipExisting bool) error {
	for _, cat := range []string{catJamming, catAmbient, catUneasy, catAnnounce} {
		if err := os.MkdirAll(filepath.Join(outDir, cat), 0o755); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
	}

	sem := make(chan struct{}, cfg.Defaults.Concurrency)
	results := make([]result, len(jobs))
	var wg sync.WaitGroup

	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j Job) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = result{job: j, err: ctx.Err()}
				return
			}
			results[i] = generateOne(ctx, cfg, gen, j, outDir, skipExisting)
		}(i, j)
	}
	wg.Wait()

	return report(results, outDir)
}

func generateOne(ctx context.Context, cfg *Config, gen *Generator, j Job, outDir string, skipExisting bool) result {
	oggPath := filepath.Join(outDir, j.Category, j.Name+".ogg")

	if skipExisting {
		if _, err := os.Stat(oggPath); err == nil {
			fmt.Printf("[skip]  %s/%s.ogg (既存)\n", j.Category, j.Name)
			return result{job: j, skipped: true}
		}
	}

	start := time.Now()
	pcm, err := gen.Synthesize(ctx, j, cfg.Defaults.MaxRetries)
	if err != nil {
		fmt.Printf("[FAIL]  %s/%s.ogg: %v\n", j.Category, j.Name, err)
		return result{job: j, err: err}
	}

	ogg, err := encodePCMToOggOpus(pcm, cfg.Defaults.OpusBitrate)
	if err != nil {
		fmt.Printf("[FAIL]  %s/%s.ogg: encode: %v\n", j.Category, j.Name, err)
		return result{job: j, err: err}
	}
	if err := os.WriteFile(oggPath, ogg, 0o644); err != nil {
		return result{job: j, err: err}
	}

	if cfg.Defaults.KeepWAV {
		wavPath := filepath.Join(outDir, j.Category, j.Name+".wav")
		if err := os.WriteFile(wavPath, buildWAV(pcm), 0o644); err != nil {
			return result{job: j, err: err}
		}
	}

	dur := time.Duration(float64(len(pcm)) / sampleRate * float64(time.Second))
	fmt.Printf("[ok]    %s/%s.ogg  %.1fs  %.1fKB  (%s)\n",
		j.Category, j.Name, dur.Seconds(), float64(len(ogg))/1024, time.Since(start).Round(time.Millisecond))

	return result{job: j, dur: dur, bytes: len(ogg)}
}

func report(results []result, outDir string) error {
	var ok, skipped int
	var failed []result
	for _, r := range results {
		switch {
		case r.err != nil:
			failed = append(failed, r)
		case r.skipped:
			skipped++
		default:
			ok++
		}
	}

	fmt.Printf("\n生成: %d 件 / スキップ: %d 件 / 失敗: %d 件\n", ok, skipped, len(failed))
	fmt.Printf("出力先: %s\n", outDir)

	if len(failed) > 0 {
		fmt.Fprintln(os.Stderr, "\n失敗した発話:")
		for _, r := range failed {
			fmt.Fprintf(os.Stderr, "  %s/%s.ogg: %v\n", r.job.Category, r.job.Name, r.err)
		}
		return fmt.Errorf("%d 件の生成に失敗しました", len(failed))
	}
	return nil
}
