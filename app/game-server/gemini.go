package main

import (
	"context"
	"encoding/json"
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
		ServiceTier:      p.cfg.GenAIServiceTier(),
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.TranscribeTimeout())
	defer cancel()

	start := time.Now()
	resp, err := p.client.Models.GenerateContent(ctx, p.cfg.TranscribeModel, contents, config)
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

// GenerateNavigatorReply はナビゲーターの発話を1つ生成する
// (docs/navigator_design.md §3.3 のプロンプト構成)。
//
// systemPrompt は BuildNavigatorPrompt が組み立てた [A]〜[F] の全ブロック、
// instruction は発話トリガーごとの指示 (§3.5)。
// 会話ターンごとに呼ぶため、低レイテンシの ReasoningModel を使う。
func (p *GeminiProcessor) GenerateNavigatorReply(ctx context.Context, systemPrompt, instruction string) (string, error) {
	return p.generateReply(ctx, systemPrompt, instruction, false)
}

// GenerateReplyWithSearch は Google 検索を許可して発話を1つ生成する。
//
// 天気・店の営業時間など、モデルが知り得ない実世界の情報を扱う相手
// (カラス) 向け。検索は1往復ぶんレイテンシが増えるため、
// **ゲーム中のナビゲーターには使わない**(カウントダウン中の体験を損なう)。
func (p *GeminiProcessor) GenerateReplyWithSearch(ctx context.Context, systemPrompt, instruction string) (string, error) {
	return p.generateReply(ctx, systemPrompt, instruction, true)
}

func (p *GeminiProcessor) generateReply(ctx context.Context, systemPrompt, instruction string, useSearch bool) (string, error) {
	contents := []*genai.Content{
		genai.NewContentFromText(instruction, genai.RoleUser),
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		ServiceTier:       p.cfg.GenAIServiceTier(),
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
	resp, err := p.client.Models.GenerateContent(ctx, p.cfg.ReasoningModel, contents, config)
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
