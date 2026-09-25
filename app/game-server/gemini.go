package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"google.golang.org/genai"
)

// TranscriptionItem は1つの発話の書き起こし。
//
// 以前は送信者・受信者のコールサインも抽出していたが、無線側で名乗りを
// 強制する運用をやめたため message のみにした。開始申告・秘密ワードの
// 判定は元々 message だけを見ており、話者の推定は不要。
type TranscriptionItem struct {
	Message string `json:"message"`
}

type TranscriptionResult struct {
	Items []TranscriptionItem `json:"item"`
}

type GeminiProcessor struct {
	cfg              GeminiConfig
	client           *genai.Client
	transcribePrompt string
	transcribeSchema *genai.Schema

	// health は外部APIの成否を記録し、マネージャー向け Web 画面で
	// 障害を検知できるようにする (docs/game_session_design.md §9)。
	health *APIHealth
}

// SetHealth は外部API状況の記録先を設定する。
func (p *GeminiProcessor) SetHealth(health *APIHealth) { p.health = health }

// noteResult は API 呼び出しの成否を記録する。
func (p *GeminiProcessor) noteResult(err error) {
	if p.health == nil {
		return
	}
	if err != nil {
		p.health.NoteError(err)
		return
	}
	p.health.NoteSuccess()
}

func NewGeminiProcessor(ctx context.Context, cfg GeminiConfig) (*GeminiProcessor, error) {
	client, err := NewGenAIClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("genai.NewClient: %w", err)
	}

	prompt, err := os.ReadFile(cfg.TranscribePromptFile)
	if err != nil {
		return nil, fmt.Errorf("read prompt file %q: %w", cfg.TranscribePromptFile, err)
	}

	schemaBytes, err := os.ReadFile(cfg.TranscribeSchemaFile)
	if err != nil {
		return nil, fmt.Errorf("read schema file %q: %w", cfg.TranscribeSchemaFile, err)
	}
	schema, err := parseSchema(schemaBytes)
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}

	return &GeminiProcessor{
		cfg:              cfg,
		client:           client,
		transcribePrompt: string(prompt),
		transcribeSchema: schema,
	}, nil
}

func (p *GeminiProcessor) Close() {
	// 新SDKの Client には Close メソッドがないためno-op
}

// warmupTimeout はウォームアップ1回あたりの上限。
//
// 通常のタイムアウト (既定20秒) をそのまま使うと、コールドスタートが
// 想定以上に長い日に起動そのものが遅延し、Docker の healthcheck
// (5s間隔×5回=25秒の猶予) より先に倒れかねない。実測のコールドスタートは
// 最大 10秒程度 (2026-09-06) なので、余裕を見つつ健全性チェックの
// 猶予内に収まる値にしてある。超えたら諦めて本番の初回リクエストに委ねる。
const warmupTimeout = 15 * time.Second

// Warmup は起動直後にダミー呼び出しを行い、初回リクエストにだけ乗る
// 接続確立コストを前払いする。
//
// 実測 (2026-09-06、Raspberry Pi): プロセス起動後の1回目だけ
// Transcribe が 6〜10秒、2回目以降は 2秒前後に落ちる。DNS・TLS・CPU
// (RSA署名) はいずれも数百ms未満で健全なため、原因は genai.Client の
// 内部初期化 (認証トークン取得やコネクション確立) にあると見ている。
// 本番中にプレイヤーへこの遅延を負わせないため、受付開始前に潰す。
//
// transcribeModel と reasoningModel は別モデルなので、片方だけ叩いても
// もう片方の初回コストが残る可能性がある。両方を軽いテキストのみの
// 呼び出しで温める (Transcribe は音声必須なため、代わりに
// transcribeModel へ直接テキストを投げて経路だけ温める)。**並行に呼ぶ**
// (直列だと最悪 warmupTimeout の2倍、起動が延びる)。
//
// 失敗しても起動は止めない。ウォームアップの失敗は本番の障害率
// (APIHealth) に混ぜたくないため noteResult は呼ばない。
func (p *GeminiProcessor) Warmup(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, warmupTimeout)
	defer cancel()

	contents := []*genai.Content{
		genai.NewContentFromText("ok", genai.RoleUser),
	}

	errCh := make(chan error, 2)
	go func() {
		_, err := p.client.Models.GenerateContent(ctx, p.cfg.TranscribeModel, contents, nil)
		if err != nil {
			err = fmt.Errorf("warmup transcribe model: %w", err)
		}
		errCh <- err
	}()
	go func() {
		_, err := p.client.Models.GenerateContent(ctx, p.cfg.ReasoningModel, contents, nil)
		if err != nil {
			err = fmt.Errorf("warmup reasoning model: %w", err)
		}
		errCh <- err
	}()

	var firstErr error
	for range 2 {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *GeminiProcessor) Transcribe(ctx context.Context, oggData []byte) (*TranscriptionResult, error) {
	contents := []*genai.Content{
		genai.NewContentFromParts([]*genai.Part{
			genai.NewPartFromText(p.transcribePrompt),
			genai.NewPartFromBytes(oggData, "audio/ogg"),
		}, genai.RoleUser),
	}

	config := &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
		ResponseSchema:   p.transcribeSchema,
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.TranscribeTimeout())
	defer cancel()

	start := time.Now()
	resp, err := p.generateContent(ctx, p.cfg.TranscribeModel, contents, config)
	log.Printf("[gemini] Transcribe latency: %v", time.Since(start))
	p.noteResult(err)
	if err != nil {
		return nil, fmt.Errorf("GenerateContent: %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 ||
		resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	text := resp.Candidates[0].Content.Parts[0].Text
	if text == "" {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	var result TranscriptionResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("json.Unmarshal: %w", err)
	}
	return &result, nil
}

// parseSchema は JSON バイト列を genai.Schema に変換する。
func parseSchema(data []byte) (*genai.Schema, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return convertSchema(raw)
}

func convertSchema(raw map[string]any) (*genai.Schema, error) {
	s := &genai.Schema{}

	if t, ok := raw["type"].(string); ok {
		switch t {
		case "object":
			s.Type = genai.TypeObject
		case "array":
			s.Type = genai.TypeArray
		case "string":
			s.Type = genai.TypeString
		case "number":
			s.Type = genai.TypeNumber
		case "integer":
			s.Type = genai.TypeInteger
		case "boolean":
			s.Type = genai.TypeBoolean
		}
	}

	if props, ok := raw["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for k, v := range props {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			child, err := convertSchema(vm)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", k, err)
			}
			s.Properties[k] = child
		}
	}

	if items, ok := raw["items"].(map[string]any); ok {
		child, err := convertSchema(items)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		s.Items = child
	}

	if required, ok := raw["required"].([]any); ok {
		for _, r := range required {
			if rs, ok := r.(string); ok {
				s.Required = append(s.Required, rs)
			}
		}
	}

	return s, nil
}

// NavigatorReply はナビゲーターの1発話ぶんの生成結果。
//
// 以前は観察の報告の判定 (observed) も同じ呼び出しで返させていたが、
// ヒントレベルの前倒しにしか使っていなかったため、レベルごと廃止した (決定129)。
type NavigatorReply struct {
	// Reply は無線へ流す発話本文
	Reply string `json:"reply"`
}

// navigatorReplySchema は GenerateNavigatorReply の構造化出力スキーマ。
var navigatorReplySchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"reply": {Type: genai.TypeString},
	},
	Required: []string{"reply"},
}

// rateLimitRetryDelay は 429 (Resource exhausted) を受けてから再試行するまでの待ち時間。
//
// Vertex の共有クォータは混雑で一時的に 429 を返す。すぐ撃ち直すと
// 同じ混雑に当たりやすいので少し空ける。長く待つとプレイヤーの応答待ちが
// 延びるだけなので1秒に留める。
const rateLimitRetryDelay = time.Second

// generateContent は GenerateContent を呼び、**429 のときだけ1回再試行**する。
//
// 429 は一時的な混雑で、1回撃ち直せば通ることが多い (シミュレーションで
// 1回に数件出ていた)。それ以外のエラーは再試行しない — 504 は ctx の期限
// (reply_timeout_sec) をサーバー側で使い切った結果で、撃ち直しても
// 残り時間が無い。再試行も呼び出し元の ctx の期限内で行う。
func (p *GeminiProcessor) generateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	resp, err := p.client.Models.GenerateContent(ctx, model, contents, config)
	if !isRateLimited(err) {
		return resp, err
	}
	log.Printf("[gemini] 429 (resource exhausted), retrying once after %v", rateLimitRetryDelay)
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(rateLimitRetryDelay):
	}
	return p.client.Models.GenerateContent(ctx, model, contents, config)
}

// isRateLimited は err が 429 (Resource exhausted) かを返す。
func isRateLimited(err error) bool {
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == 429
	}
	var apiErrPtr *genai.APIError
	if errors.As(err, &apiErrPtr) {
		return apiErrPtr.Code == 429
	}
	return false
}

// GenerateNavigatorReply はナビゲーターの発話を1つ生成する
// (docs/navigator_design.md §3.3 のプロンプト構成)。
//
// systemPrompt は BuildNavigatorPrompt が組み立てた [A]〜[F] の全ブロック、
// instruction は発話トリガーごとの指示 (§3.5)。
// 会話ターンごとに呼ぶため、低レイテンシの ReasoningModel を使う。
func (p *GeminiProcessor) GenerateNavigatorReply(ctx context.Context, systemPrompt, instruction string) (*NavigatorReply, error) {
	contents := []*genai.Content{
		genai.NewContentFromText(instruction, genai.RoleUser),
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    navigatorReplySchema,
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.ReplyTimeout())
	defer cancel()

	start := time.Now()
	resp, err := p.generateContent(ctx, p.cfg.ReasoningModel, contents, config)
	log.Printf("[gemini] reply latency: %v", time.Since(start))
	p.noteResult(err)
	if err != nil {
		return nil, fmt.Errorf("GenerateContent (Navigator): %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 ||
		resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from Gemini Navigator")
	}

	text := resp.Candidates[0].Content.Parts[0].Text
	if text == "" {
		return nil, fmt.Errorf("empty text from Gemini Navigator")
	}

	var reply NavigatorReply
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return nil, fmt.Errorf("json.Unmarshal (Navigator): %w", err)
	}
	if reply.Reply == "" {
		return nil, fmt.Errorf("empty reply text from Gemini Navigator")
	}
	return &reply, nil
}

// GenerateReplyWithSearch は Google 検索を許可して発話を1つ生成する。
//
// 天気・店の営業時間など、モデルが知り得ない実世界の情報を扱う相手
// (カラス) 向け。検索は1往復ぶんレイテンシが増えるため、
// **ゲーム中のナビゲーターには使わない**(カウントダウン中の体験を損なう)。
func (p *GeminiProcessor) GenerateReplyWithSearch(ctx context.Context, systemPrompt, instruction string) (string, error) {
	return p.generateReply(ctx, systemPrompt, instruction, true)
}

// GenerateReply は検索なしで発話を1つ生成する。
//
// 検索が要らない相手 (開始申告の差し戻しなど) 向け。マネージャーを
// 待たせる場面なので、1往復ぶんのレイテンシを足さない。
func (p *GeminiProcessor) GenerateReply(ctx context.Context, systemPrompt, instruction string) (string, error) {
	return p.generateReply(ctx, systemPrompt, instruction, false)
}

func (p *GeminiProcessor) generateReply(ctx context.Context, systemPrompt, instruction string, useSearch bool) (string, error) {
	contents := []*genai.Content{
		genai.NewContentFromText(instruction, genai.RoleUser),
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
	}
	if useSearch {
		config.Tools = []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}}
	}

	// 検索ありは1往復増えるぶん余裕を持たせる (ゲーム中は使わない経路)
	timeout := p.cfg.ReplyTimeout()
	if useSearch {
		timeout *= 2
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	resp, err := p.generateContent(ctx, p.cfg.ReasoningModel, contents, config)
	// 検索の有無をログに残す (どちらの経路を通ったか運用中に判別できるように)
	mode := "reply"
	if useSearch {
		mode = "reply+search"
	}
	log.Printf("[gemini] %s latency: %v", mode, time.Since(start))
	p.noteResult(err)
	if err != nil {
		return "", fmt.Errorf("GenerateContent (Navigator): %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 ||
		resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from Gemini Navigator")
	}

	text := resp.Candidates[0].Content.Parts[0].Text
	if text == "" {
		return "", fmt.Errorf("empty text from Gemini Navigator")
	}
	return text, nil
}
