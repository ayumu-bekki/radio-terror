package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// runTTSProbe は実 API を叩くレイテンシ調査を実行するかのフラグ。
// 課金が発生し数分かかるため、既定では飛ばす。
var runTTSProbe = flag.Bool("ttsprobe", false,
	"TTS のレイテンシ調査を実行する (実APIを呼ぶ。課金あり)")

// ttsProbeRepeat は各条件を何回試すか。
// 遅延は**外れ値**として現れる (通常2〜6秒に対し19秒・58秒) ため、
// 1回では判定できない。既定5回。
var ttsProbeRepeat = flag.Int("ttsprobe-n", 5, "各条件の試行回数")

// TestTTSLatencyProbe は「どの入力で TTS が詰まるか」を切り分ける。
//
// 実運用で 19.01秒 と 58.97秒 の呼び出しが観測された。通常は 2.2〜5.7秒。
// 過去にも角括弧タグ入りの本文で同種の不安定さが出ており
// (通常2.6秒が最大21.7秒。tts_prompt.go)、**特定の入力で詰まる**
// 性質があると分かっている。何が引き金かを実測で絞る。
//
// 実行:
//
//	go test -run TestTTSLatencyProbe -ttsprobe -timeout 30m -v
//	go test -run TestTTSLatencyProbe -ttsprobe -ttsprobe-n 10 -timeout 60m -v
//
// 事前に config.toml (project/location) と ADC が必要。
func TestTTSLatencyProbe(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v (config.toml が必要)", err)
	}

	// 調査中に打ち切られると分布が歪むため、上限を長めに取る
	cfg.Gemini.TTSTimeoutSec = 120

	ctx := context.Background()
	client, err := NewTTSClient(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewTTSClient: %v", err)
	}

	// 実際に詰まった本文と、同じ場面で正常だった本文を軸に組む。
	// 実運用のログ (2026-08-14 18:08) より:
	//   遅い: 「さあ、光の色を告げよ。どうぞ」        → 19.01秒
	//   速い: 「ワタシハ、ヨタカ。……私は見ている。」 → 3.89秒
	const style = "中性的な声。ピッチと速度を一定に保った機械的な読み方で。エフェクトはかけず、素の声で。"
	const note = "任務の開始を告げる場面。落ち着いて、頼りになる調子で。"
	const voice = "Iapetus"

	cases := []struct {
		name   string
		prompt string
	}{
		// --- 実運用で観測された2つをそのまま ---
		{"実測:遅かった本文", buildTTSPrompt(style, note, "さあ、光の色を告げよ。どうぞ")},
		{"実測:速かった本文", buildTTSPrompt(style, note, "ワタシハ、ヨタカ。……私は見ている。")},

		// --- 「どうぞ」の有無だけを変える ---
		{"どうぞ あり", buildTTSPrompt(style, note, "さあ、光の色を告げよ。どうぞ")},
		{"どうぞ なし", buildTTSPrompt(style, note, "さあ、光の色を告げよ。")},

		// --- 三点リーダの有無 (ヨタカの口調に含まれる) ---
		{"三点リーダ あり", buildTTSPrompt(style, note, "……私は見ている。どうぞ")},
		{"三点リーダ なし", buildTTSPrompt(style, note, "私は見ている。どうぞ")},

		// --- 前置き (声質指定・場面説明) の影響 ---
		{"前置きなし 本文のみ", "さあ、光の色を告げよ。どうぞ"},
		{"声質指定のみ", buildTTSPrompt(style, "", "さあ、光の色を告げよ。どうぞ")},

		// --- 命令形かどうか (「告げよ」は古風な命令形) ---
		{"命令形 告げよ", buildTTSPrompt(style, note, "さあ、光の色を告げよ。どうぞ")},
		{"平易な言い方", buildTTSPrompt(style, note, "さあ、光の色を教えてください。どうぞ")},

		// --- 短い本文 (長さの影響を見る) ---
		{"ごく短い", buildTTSPrompt(style, note, "了解だ。どうぞ")},
	}

	type stat struct {
		name    string
		samples []time.Duration
		errs    int
	}
	stats := make([]stat, 0, len(cases))

	for _, c := range cases {
		s := stat{name: c.name}
		for i := 0; i < *ttsProbeRepeat; i++ {
			start := time.Now()
			pcm, err := client.GeneratePCM24kFromPrompt(ctx, c.prompt, voice)
			elapsed := time.Since(start)

			if err != nil {
				s.errs++
				t.Logf("  %s [%d] ERROR after %v: %v", c.name, i+1, elapsed, err)
				continue
			}
			s.samples = append(s.samples, elapsed)
			t.Logf("  %s [%d] %.2fs (音声 %.1fs)",
				c.name, i+1, elapsed.Seconds(), float64(len(pcm))/sampleRate)
		}
		stats = append(stats, s)
	}

	// --- 集計 ---
	t.Log("")
	t.Log("=== TTS レイテンシ調査 ===")
	t.Logf("%-24s %8s %8s %8s %8s %6s", "条件", "中央値", "最小", "最大", "平均", "失敗")
	for _, s := range stats {
		if len(s.samples) == 0 {
			t.Logf("%-24s %8s %8s %8s %8s %6d", s.name, "-", "-", "-", "-", s.errs)
			continue
		}
		sorted := append([]time.Duration(nil), s.samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

		median := sorted[len(sorted)/2]
		min, max := sorted[0], sorted[len(sorted)-1]
		var sum time.Duration
		for _, d := range sorted {
			sum += d
		}
		mean := sum / time.Duration(len(sorted))

		t.Logf("%-24s %8s %8s %8s %8s %6d", s.name,
			fmtSec(median), fmtSec(min), fmtSec(max), fmtSec(mean), s.errs)
	}
	t.Log("")
	t.Log("外れ値 (中央値の3倍以上) が特定の条件に偏っていれば、それが引き金。")
	t.Log("どの条件にも均等に出るなら、入力ではなくモデル側の問題。")
}

func fmtSec(d time.Duration) string {
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// TestTTSConcurrencyProbe は**同時リクエスト数**がレイテンシに与える影響を測る。
//
// 実運用では発話を句点で分割し、チャンクを並列に投げている。
// 19.01秒・58.97秒はいずれも**並列で投げたうちの1本**だった。
// 一方 TestTTSLatencyProbe (直列) でも 46.96秒の外れ値が出たため、
// 並行が唯一の原因ではない。悪化させているかを切り分ける。
//
// 実行:
//
//	go test -run TestTTSConcurrencyProbe -ttsprobe -timeout 40m -v
func TestTTSConcurrencyProbe(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Gemini.TTSTimeoutSec = 120

	ctx := context.Background()
	client, err := NewTTSClient(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewTTSClient: %v", err)
	}

	const style = "中性的な声。ピッチと速度を一定に保った機械的な読み方で。エフェクトはかけず、素の声で。"
	const note = "任務の開始を告げる場面。落ち着いて、頼りになる調子で。"
	const voice = "Iapetus"

	// 実運用と同じ形: 1発話を句点で割った複数チャンク
	texts := []string{
		"ワタシハ、ヨタカ。……私は見ている。",
		"さあ、光の色を告げよ。どうぞ",
		"了解だ。次は4に合わせろ。どうぞ",
	}

	// 同時本数を変えて、1本あたりの所要時間の分布を比べる
	for _, parallel := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("同時%d本", parallel), func(t *testing.T) {
			var all []time.Duration
			var errs int

			for round := 0; round < *ttsProbeRepeat; round++ {
				type result struct {
					d   time.Duration
					err error
				}
				ch := make(chan result, parallel)

				for i := 0; i < parallel; i++ {
					prompt := buildTTSPrompt(style, note, texts[i%len(texts)])
					go func() {
						start := time.Now()
						_, err := client.GeneratePCM24kFromPrompt(ctx, prompt, voice)
						ch <- result{d: time.Since(start), err: err}
					}()
				}
				for i := 0; i < parallel; i++ {
					r := <-ch
					if r.err != nil {
						errs++
						t.Logf("  同時%d本 round%d ERROR after %v: %v",
							parallel, round+1, r.d, r.err)
						continue
					}
					all = append(all, r.d)
				}
			}

			if len(all) == 0 {
				t.Errorf("同時%d本: 全て失敗 (%d件)", parallel, errs)
				return
			}
			sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

			var sum time.Duration
			for _, d := range all {
				sum += d
			}
			// 外れ値の数 (中央値の3倍以上)
			median := all[len(all)/2]
			outliers := 0
			for _, d := range all {
				if d > median*3 {
					outliers++
				}
			}

			t.Logf("同時%d本: n=%d 中央値=%s 最小=%s 最大=%s 平均=%s 外れ値=%d 失敗=%d",
				parallel, len(all), fmtSec(median), fmtSec(all[0]),
				fmtSec(all[len(all)-1]), fmtSec(sum/time.Duration(len(all))),
				outliers, errs)
		})
	}
}

// TestTTSStreamingProbe は**ストリーミング受信**が遅延を改善するかを測る。
//
// 観測された遅延は「音声は5秒ぶんなのに待ちが24〜56秒」という形で、
// 一括応答 (GenerateContent) の待ちで詰まっている可能性がある。
// GenerateContentStream なら先頭チャンクが早く届くかもしれない。
//
// 先頭チャンクまでの時間 (TTFB) と全体の時間を比べる。
// TTFB が短ければ受信方式の問題、TTFB も長ければモデル側の問題。
//
// 実行:
//
//	go test -run TestTTSStreamingProbe -ttsprobe -ttsprobe-n 15 -timeout 40m -v
func TestTTSStreamingProbe(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	ctx := context.Background()
	client, err := NewGenAIClient(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewGenAIClient: %v", err)
	}
	model := cfg.Gemini.TTSModel
	if model == "" {
		model = defaultTTSModel
	}

	const style = "中性的な声。ピッチと速度を一定に保った機械的な読み方で。エフェクトはかけず、素の声で。"
	const note = "任務の開始を告げる場面。落ち着いて、頼りになる調子で。"
	const voice = "Iapetus"
	prompt := buildTTSPrompt(style, note, "さあ、光の色を教えてください。どうぞ")

	genConfig := &genai.GenerateContentConfig{
		ResponseModalities: []string{"audio"},
		SpeechConfig: &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: voice},
			},
		},
	}

	var ttfbs, totals []time.Duration
	errs := 0

	for i := 0; i < *ttsProbeRepeat; i++ {
		start := time.Now()
		var ttfb time.Duration
		var bytes int
		var chunks int
		var failed error

		for resp, err := range client.Models.GenerateContentStream(ctx, model,
			[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, genConfig) {
			if err != nil {
				failed = err
				break
			}
			if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
				continue
			}
			for _, part := range resp.Candidates[0].Content.Parts {
				if part.InlineData == nil {
					continue
				}
				if chunks == 0 {
					ttfb = time.Since(start)
				}
				chunks++
				bytes += len(part.InlineData.Data)
			}
		}
		total := time.Since(start)

		if failed != nil {
			errs++
			t.Logf("  [%d] ERROR after %v: %v", i+1, total, failed)
			continue
		}
		if chunks == 0 {
			errs++
			t.Logf("  [%d] 音声が返らなかった (%v)", i+1, total)
			continue
		}

		ttfbs = append(ttfbs, ttfb)
		totals = append(totals, total)
		t.Logf("  [%d] TTFB=%.2fs 全体=%.2fs (%d chunks, %d bytes, 音声 %.1fs)",
			i+1, ttfb.Seconds(), total.Seconds(), chunks, bytes,
			float64(bytes/2)/sampleRate)
	}

	if len(totals) == 0 {
		t.Fatalf("全て失敗 (%d件)", errs)
	}

	report := func(label string, ds []time.Duration) {
		sorted := append([]time.Duration(nil), ds...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		var sum time.Duration
		for _, d := range sorted {
			sum += d
		}
		over10 := 0
		for _, d := range sorted {
			if d > 10*time.Second {
				over10++
			}
		}
		t.Logf("%s: n=%d 中央値=%s 最小=%s 最大=%s 平均=%s 10秒超=%d",
			label, len(sorted), fmtSec(sorted[len(sorted)/2]), fmtSec(sorted[0]),
			fmtSec(sorted[len(sorted)-1]), fmtSec(sum/time.Duration(len(sorted))), over10)
	}

	t.Log("")
	t.Log("=== ストリーミング受信 ===")
	report("TTFB (先頭チャンク)", ttfbs)
	report("全体", totals)
	t.Logf("失敗: %d", errs)
	t.Log("")
	t.Log("TTFB が安定して短ければ受信方式の問題 → ストリーミングへ移行する価値あり。")
	t.Log("TTFB も外れ値を出すならモデル側の問題 → 打ち切り+リトライで対処するしかない。")
}

// TestTTSStreamingProducesValidAudio はストリーミング受信に切り替えた
// GeneratePCM24kFromPrompt が、一括受信と同等の音声を返すことを確かめる。
//
// ストリーミングでは音声が細かいチャンク (実測150個程度) に分かれて届く。
// 連結を誤ると、長さがずれる・ノイズが混じる・途中で切れるといった形で
// 壊れる。長さと波形の素性を見て、破綻していないことを押さえる。
//
// 実行:
//
//	go test -run TestTTSStreamingProducesValidAudio -ttsprobe -timeout 10m -v
func TestTTSStreamingProducesValidAudio(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	ctx := context.Background()
	client, err := NewTTSClient(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewTTSClient: %v", err)
	}

	const style = "低音で落ち着いた男性の声。テンポは遅め。無駄なく淡々と、しかし芯のある話し方で。"
	const note = "任務の開始を告げる場面。落ち着いて、頼りになる調子で。"

	texts := []string{
		"こちらフクロウ。慌てるな、まず光っている色を言え。どうぞ",
		"了解だ。どうぞ",
	}

	for _, text := range texts {
		prompt := buildTTSPrompt(style, note, text)
		pcm, err := client.GeneratePCM24kFromPrompt(ctx, prompt, "Charon")
		if err != nil {
			t.Fatalf("GeneratePCM24kFromPrompt(%q): %v", text, err)
		}

		seconds := float64(len(pcm)) / sampleRate

		// 読み上げに対して短すぎ/長すぎないこと。
		// 日本語は概ね 5〜8 文字/秒。極端に外れていたら連結を誤っている。
		runes := countRunes(text)
		minSec := float64(runes) / 12.0
		maxSec := float64(runes) / 2.5
		if seconds < minSec || seconds > maxSec {
			t.Errorf("%q: 音声 %.1fs は %d文字に対して不自然 (期待 %.1f〜%.1fs)",
				text, seconds, runes, minSec, maxSec)
		}

		// 無音だけになっていないこと (連結ミスでゼロ埋めされる事故を検出)
		nonZero := 0
		var peak int16
		for _, s := range pcm {
			if s != 0 {
				nonZero++
			}
			if s > peak {
				peak = s
			}
		}
		if nonZero*100/len(pcm) < 50 {
			t.Errorf("%q: 非ゼロサンプルが %d%% しかない (無音混じり)",
				text, nonZero*100/len(pcm))
		}
		if peak < 1000 {
			t.Errorf("%q: 振幅のピークが %d と小さすぎる", text, peak)
		}

		// Opus に通せること (実際の送出経路と同じ)
		ogg, err := encodePCMToOggOpus(pcm)
		if err != nil {
			t.Fatalf("%q: encodePCMToOggOpus: %v", text, err)
		}
		// granule から読んだ尺が PCM と一致すること
		got := oggOpusDuration(ogg)
		want := time.Duration(len(pcm)) * time.Second / sampleRate
		diff := got - want
		if diff < 0 {
			diff = -diff
		}
		if diff > 100*time.Millisecond {
			t.Errorf("%q: Ogg の尺 %v が PCM の %v と食い違う", text, got, want)
		}

		t.Logf("OK %q: %.1fs, 非ゼロ %d%%, ピーク %d, ogg %d bytes",
			text, seconds, nonZero*100/len(pcm), peak, len(ogg))
	}
}

// TestTTSEmotionTagProbe は表情タグを本文に混ぜた場合の挙動を測る。
//
// 表情タグは「応答が不安定になる」として廃止された
// (通常2.6秒が8回中2回20秒超、最大21.7秒。tts_prompt.go)。
// しかしその計測は**一括受信の時代**のもので、一括受信は入力と無関係に
// 27%が10秒超になることが後の調査で分かった (決定15)。
// つまり当時の不安定さはタグではなく受信方式のせいだった可能性がある。
//
// ストリーミング受信でタグを含めた場合の
//
//	(1) レイテンシ  — 不安定さが再現するか
//	(2) 音声の長さ  — タグが読み上げられていないか
//
// を見る。タグがそのまま読まれると音声が不自然に伸びる。
//
// 実行:
//
//	go test -run TestTTSEmotionTagProbe -ttsprobe -ttsprobe-n 10 -timeout 30m -v
func TestTTSEmotionTagProbe(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// 打ち切られると分布が歪むため長めに取る
	cfg.Gemini.TTSTimeoutSec = 120
	cfg.Gemini.TTSAttempts = 1

	ctx := context.Background()
	client, err := NewTTSClient(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewTTSClient: %v", err)
	}

	const style = "低音で落ち着いた男性の声。テンポは遅め。無駄なく淡々と、しかし芯のある話し方で。"
	const note = "解除に成功した瞬間。喜びと安堵をはっきり出して。"
	const voice = "Charon"

	cases := []struct {
		name string
		// note を渡すか (ディレクターズノート方式)
		note string
		// 本文 (タグ入りかどうか)
		text string
	}{
		{"現行: ノートのみ", note, "よくやった。見事な手際だ。どうぞ"},
		{"タグのみ", "", "[relieved] よくやった。見事な手際だ。どうぞ"},
		{"ノート+タグ", note, "[relieved] よくやった。見事な手際だ。どうぞ"},
		{"タグ複数", "", "[relieved] よくやった。[warm] 見事な手際だ。どうぞ"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var samples []time.Duration
			var seconds []float64
			errs := 0

			for i := 0; i < *ttsProbeRepeat; i++ {
				// stripTTSTags を通さず、タグを残したまま投げる
				prompt := buildTTSPromptRaw(style, c.note, c.text)

				start := time.Now()
				pcm, err := client.GeneratePCM24kFromPrompt(ctx, prompt, voice)
				elapsed := time.Since(start)

				if err != nil {
					errs++
					t.Logf("  [%d] ERROR after %v: %v", i+1, elapsed, err)
					continue
				}
				samples = append(samples, elapsed)
				seconds = append(seconds, float64(len(pcm))/sampleRate)
			}

			if len(samples) == 0 {
				t.Errorf("%s: 全て失敗", c.name)
				return
			}

			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			var sum time.Duration
			over10 := 0
			for _, d := range samples {
				sum += d
				if d > 10*time.Second {
					over10++
				}
			}
			// 音声長の平均 (タグが読まれると伸びる)
			var secSum float64
			maxSec := 0.0
			for _, s := range seconds {
				secSum += s
				if s > maxSec {
					maxSec = s
				}
			}

			t.Logf("%s: n=%d 中央値=%s 最大=%s 平均=%s 10秒超=%d | 音声 平均%.1fs 最大%.1fs",
				c.name, len(samples), fmtSec(samples[len(samples)/2]),
				fmtSec(samples[len(samples)-1]), fmtSec(sum/time.Duration(len(samples))),
				over10, secSum/float64(len(seconds)), maxSec)
		})
	}

	t.Log("")
	t.Log("判断材料:")
	t.Log("  レイテンシがタグ有無で変わらなければ、廃止理由は受信方式の問題だった。")
	t.Log("  音声長がタグ入りで伸びるなら、タグが読み上げられている (使えない)。")
}

// buildTTSPromptRaw は stripTTSTags を通さずにプロンプトを組み立てる。
// 表情タグの検証用 (通常経路の buildTTSPrompt はタグを除去してしまう)。
func buildTTSPromptRaw(style, note, chunk string) string {
	var b strings.Builder
	b.WriteString(style)
	if note != "" {
		b.WriteString("\n")
		b.WriteString(note)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "次のセリフを読み上げてください:\n%s", chunk)
	return b.String()
}

// TestTTSEmotionTagAudioDump は表情タグ入り/なしの音声をファイルに書き出す。
//
// タグが読み上げられていないかは**聴いて確かめる**しかない。
// 音声長の差 (タグ入りで平均0.4秒、最大1.3秒長い) がタグの読み上げなのか、
// 演技が変わったことによる自然な差なのかを判断するため。
//
// 実行:
//
//	go test -run TestTTSEmotionTagAudioDump -ttsprobe -timeout 10m -v
//
// 出力先は環境変数 TTS_DUMP_DIR (未設定なら t.TempDir() でログに出す)。
func TestTTSEmotionTagAudioDump(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	ctx := context.Background()
	client, err := NewTTSClient(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewTTSClient: %v", err)
	}

	outDir := os.Getenv("TTS_DUMP_DIR")
	if outDir == "" {
		outDir = t.TempDir()
	}

	const style = "低音で落ち着いた男性の声。テンポは遅め。無駄なく淡々と、しかし芯のある話し方で。"
	const note = "解除に成功した瞬間。喜びと安堵をはっきり出して。"
	const voice = "Charon"

	cases := []struct{ name, note, text string }{
		{"01_note_only", note, "よくやった。見事な手際だ。どうぞ"},
		{"02_tag_only", "", "[relieved] よくやった。見事な手際だ。どうぞ"},
		{"03_note_and_tag", note, "[relieved] よくやった。見事な手際だ。どうぞ"},
		{"04_tag_multi", "", "[relieved] よくやった。[warm] 見事な手際だ。どうぞ"},
	}

	for _, c := range cases {
		prompt := buildTTSPromptRaw(style, c.note, c.text)
		pcm, err := client.GeneratePCM24kFromPrompt(ctx, prompt, voice)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		ogg, err := encodePCMToOggOpus(pcm)
		if err != nil {
			t.Errorf("%s: encode: %v", c.name, err)
			continue
		}
		path := filepath.Join(outDir, c.name+".ogg")
		if err := os.WriteFile(path, ogg, 0o644); err != nil {
			t.Errorf("%s: write: %v", c.name, err)
			continue
		}
		t.Logf("%s: %.1fs -> %s", c.name, float64(len(pcm))/sampleRate, path)
	}
	t.Logf("再生して「リリーブド」等が読まれていないか確認する: open %s", outDir)
}

// TestNavigatorEmitsEmotionTags はナビゲーターが実際に表情タグを出すか、
// 出したタグが許可リストに収まっているかを確かめる。
//
// プロンプトで指示しても生成AIが従うとは限らない。また一覧外の語を
// 使われるとサーバー側で落とされ、表情が付かない。
//
// 実行:
//
//	go test -run TestNavigatorEmitsEmotionTags -ttsprobe -ttsprobe-n 5 -timeout 20m -v
func TestNavigatorEmitsEmotionTags(t *testing.T) {
	if !*runTTSProbe {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ttsprobe で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	ctx := context.Background()
	processor, err := NewGeminiProcessor(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewGeminiProcessor: %v", err)
	}

	navCfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	lib, err := LoadScenarioLibrary("scenarios")
	if err != nil {
		t.Fatalf("LoadScenarioLibrary: %v", err)
	}
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-probe", difficultyEasy)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// 表情が出やすいトリガーで試す
	triggers := []string{"session_start", "defused", "wrong_action", "time_warning"}

	tagPattern := regexp.MustCompile(`\[([^\[\]]*)\]`)
	totals := map[string]int{}
	withTag := 0
	attempts := 0
	unknown := map[string]int{}

	for _, id := range []string{"owl", "heron", "lark", "nightjar"} {
		character, ok := navCfg.ByID(id)
		if !ok {
			t.Fatalf("キャラクター %q が無い", id)
		}
		for _, trigger := range triggers {
			for i := 0; i < *ttsProbeRepeat; i++ {
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt:     &navCfg.Prompt,
					Character:  character,
					Session:    built,
					StageIndex: 0,
					// time_warning のため残り時間は少なめに
					RemainingMS: 45000,
					HintLevel:   HintL1,
				})
				text, err := processor.GenerateNavigatorReply(
					ctx, prompt, navCfg.Prompt.TriggerInstruction(trigger))
				if err != nil {
					t.Logf("  %s/%s: %v", id, trigger, err)
					continue
				}
				attempts++

				found := tagPattern.FindAllStringSubmatch(text, -1)
				if len(found) > 0 {
					withTag++
				}
				for _, m := range found {
					name := strings.ToLower(strings.TrimSpace(m[1]))
					totals[name]++
					if !allowedTTSTags[name] {
						unknown[name]++
					}
				}
				t.Logf("  %s/%s: %s", id, trigger, text)
			}
		}
	}

	t.Log("")
	t.Logf("=== 表情タグの出現 (%d 発話) ===", attempts)
	t.Logf("タグ付き発話: %d/%d (%d%%)", withTag, attempts, withTag*100/max(attempts, 1))
	for tag, n := range totals {
		mark := "OK"
		if !allowedTTSTags[tag] {
			mark = "★一覧外"
		}
		t.Logf("  [%s] %d回 %s", tag, n, mark)
	}
	if len(unknown) > 0 {
		t.Errorf("一覧外のタグが使われた: %v (prompt.toml か allowedTTSTags を見直す)", unknown)
	}
	if withTag == 0 {
		t.Error("表情タグが1度も出なかった (プロンプトの指示が効いていない)")
	}
}
