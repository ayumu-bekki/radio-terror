package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// ErrBudgetExhausted は1回の実行のリクエスト上限に達したことを表す。
var ErrBudgetExhausted = errors.New("リクエスト上限に達しました (max_requests)")

// Generator は Gemini TTS を呼ぶ。
// モデルは発話ごとに指定できるため、クライアントだけを使い回す。
//
// Enterprise はレート制限が緩いが、事故で枠を使い切らないよう
// Generator 自身がリクエスト間隔と総リクエスト数の上限を管理する。
type Generator struct {
	client *genai.Client

	mu       sync.Mutex
	interval time.Duration // リクエストの最小間隔
	lastCall time.Time     // 直近にAPIを叩いた時刻
	budget   int           // 残りリクエスト数 (0 で無制限なら negative sentinel を使う)
	limited  bool          // budget を適用するか
	used     int           // 実際に投げた回数
}

// NewGenerator は Gemini Enterprise Agent Platform のクライアントを作る。
//
// 認証は ADC (Application Default Credentials)。
// 環境変数 GOOGLE_APPLICATION_CREDENTIALS でサービスアカウントキーを指すか、
// gcloud auth application-default login を済ませておく。
func NewGenerator(ctx context.Context, cfg *Config) (*Generator, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendEnterprise,
		Project:  cfg.Defaults.Project,
		Location: cfg.Defaults.Location,
	})
	if err != nil {
		return nil, fmt.Errorf("genai.NewClient: %w", err)
	}
	intervalMs, maxRequests := cfg.Defaults.RequestIntervalMs, cfg.Defaults.MaxRequests
	return &Generator{
		client:   client,
		interval: time.Duration(intervalMs) * time.Millisecond,
		budget:   maxRequests,
		limited:  maxRequests > 0,
	}, nil
}

// Used は実際に投げたリクエスト数を返す。
func (g *Generator) Used() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

// acquire は残枠を1つ消費し、前回リクエストから interval が経つまで待つ。
// レート制限に当たらないよう、全ゴルーチンで直列に間隔を空ける。
func (g *Generator) acquire(ctx context.Context) error {
	g.mu.Lock()
	if g.limited {
		if g.budget <= 0 {
			g.mu.Unlock()
			return ErrBudgetExhausted
		}
		g.budget--
	}
	wait := time.Duration(0)
	if !g.lastCall.IsZero() {
		if elapsed := time.Since(g.lastCall); elapsed < g.interval {
			wait = g.interval - elapsed
		}
	}
	// 次の呼び出しが自分の後ろに並ぶよう、先に時刻を進めておく
	g.lastCall = time.Now().Add(wait)
	g.used++
	g.mu.Unlock()

	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Synthesize は1発話を合成して 24kHz mono の PCM を返す。
// レート制限や一時的なエラーに備えてリトライする。
//
// TTS API は同一リクエストに対しても 400 (INVALID_ARGUMENT) を散発的に返す。
// プロンプトの内容・長さ・改行とは無関係で、投げ直せば通ることがある
// (実測: 同一プロンプト15回中3回成功)。このため 400 もリトライ対象に含める。
//
// リトライはレート枠を直接消費するため max_retries は控えめにし、
// 認証エラーなど回復しないエラーは即座に諦める。
//
// 429 (レート超過) は待たないと解消しないので長めに、400 は短い間隔で
// 投げ直す方が速い。
func (g *Generator) Synthesize(ctx context.Context, j Job, maxRetries int) ([]int16, error) {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := retryWait(lastErr, attempt)
			fmt.Printf("[retry] %s/%s (%d/%d) %v後: %v\n",
				j.Category, j.Name, attempt, maxRetries, wait, shortErr(lastErr))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if err := g.acquire(ctx); err != nil {
			// 枠切れ・中断はリトライしても意味がないので即座に返す
			if lastErr != nil && errors.Is(err, ErrBudgetExhausted) {
				return nil, fmt.Errorf("%w (直前のエラー: %s)", err, shortErr(lastErr))
			}
			return nil, err
		}

		pcm, err := g.synthesizeOnce(ctx, j)
		if err == nil {
			return pcm, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 投げ直しても直らないエラーは即座に諦める (レート枠の無駄遣いを防ぐ)
		if isPermanent(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// isPermanent はリトライしても回復しないエラーを判定する。
// APIキー不正・権限不足・モデル名やボイス名の誤りは投げ直しても同じ結果になる。
func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, sig := range []string{
		"PERMISSION_DENIED",
		"could not find default credentials",
		"invalid_grant",
		"is not found",  // モデル名の誤り
		"not supported", // 非対応の指定
		"Unauthenticated",
	} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}

// retryWait は次のリトライまでの待ち時間を決める。
// 429 はレート枠が回復するまで待つ必要があるため指数バックオフ、
// 400 はサーバー側の揺らぎなので短い固定間隔で投げ直す。
func retryWait(err error, attempt int) time.Duration {
	if err != nil && strings.Contains(err.Error(), "429") {
		d := time.Duration(1<<uint(attempt-1)) * 15 * time.Second
		if d > 120*time.Second {
			d = 120 * time.Second
		}
		return d
	}
	return 2 * time.Second
}

// shortErr はリトライログ用にエラーを1行へ切り詰める。
func shortErr(err error) string {
	if err == nil {
		return "nil"
	}
	s := err.Error()
	if i := strings.Index(s, ", Details:"); i > 0 {
		s = s[:i]
	}
	return s
}

func (g *Generator) synthesizeOnce(ctx context.Context, j Job) ([]int16, error) {
	resp, err := g.client.Models.GenerateContent(ctx, j.Model,
		[]*genai.Content{genai.NewContentFromText(j.Prompt, genai.RoleUser)},
		&genai.GenerateContentConfig{
			ResponseModalities: []string{"audio"},
			SpeechConfig: &genai.SpeechConfig{
				VoiceConfig: &genai.VoiceConfig{
					PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
						VoiceName: j.VoiceID,
					},
				},
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("GenerateContent: %w", err)
	}

	if len(resp.Candidates) == 0 ||
		resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty TTS response")
	}
	blob := resp.Candidates[0].Content.Parts[0].InlineData
	if blob == nil {
		return nil, fmt.Errorf("no inline audio data in TTS response")
	}

	pcm, err := parsePCM16(stripWAVHeader(blob.Data))
	if err != nil {
		return nil, fmt.Errorf("parsePCM16: %w", err)
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("TTS returned 0 samples")
	}
	return pcm, nil
}
