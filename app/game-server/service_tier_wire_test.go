package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// serviceTier が**実際のリクエストJSONに載る**ことを確認する。
//
// 設定を足しただけでは意味がなく、SDK が送出して初めて効く。
// しかも serviceTier は generationConfig の**中ではなくトップレベル**に置かれる
// (genai v1.68.0 の generateContentConfigToVertex が parentObject へ入れる)。
// 置き場所が変わると黙って標準ティアで課金されるだけになり気づけないため、
// ダミーサーバーで実リクエストを捕まえて検証する。
//
// 実APIは呼ばない (BaseURL をダミーサーバーへ向ける)。
func newCapturingClient(t *testing.T, body *string, streaming bool) *genai.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*body = string(b)
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}],\"role\":\"model\"}}]}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"}}]}`)
	}))
	t.Cleanup(srv.Close)

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     "p",
		Location:    "global",
		APIKey:      "dummy",
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// 一括呼び出し (文字起こし・発話生成の経路) に serviceTier が載ること。
func TestServiceTierReachesWireOnGenerateContent(t *testing.T) {
	var body string
	client := newCapturingClient(t, &body, false)

	cfg := &genai.GenerateContentConfig{
		ServiceTier: GeminiConfig{ServiceTier: "priority"}.GenAIServiceTier(),
	}
	_, err := client.Models.GenerateContent(context.Background(), "gemini-3.5-flash-lite",
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, cfg)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if !strings.Contains(body, `"serviceTier":"priority"`) {
		t.Errorf("リクエストに serviceTier が載っていない: %s", body)
	}
}

// ストリーミング呼び出し (TTS の経路) にも serviceTier が載ること。
// TTS はストリーミング固定なので (ADR T-1)、この経路の確認は必須。
func TestServiceTierReachesWireOnStream(t *testing.T) {
	var body string
	client := newCapturingClient(t, &body, true)

	cfg := &genai.GenerateContentConfig{
		ResponseModalities: []string{"audio"},
		ServiceTier:        GeminiConfig{ServiceTier: "priority"}.GenAIServiceTier(),
	}
	for _, err := range client.Models.GenerateContentStream(context.Background(),
		"gemini-3.1-flash-tts-preview",
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, cfg) {
		if err != nil {
			t.Fatalf("GenerateContentStream: %v", err)
		}
		break
	}

	if !strings.Contains(body, `"serviceTier":"priority"`) {
		t.Errorf("ストリーミングのリクエストに serviceTier が載っていない: %s", body)
	}
}

// 未設定なら serviceTier を**送らない** (Gemini 側の既定に委ねる)。
// 空文字を送ると弾かれる可能性があるため、載らないことを確認する。
func TestServiceTierOmittedWhenUnset(t *testing.T) {
	var body string
	client := newCapturingClient(t, &body, false)

	cfg := &genai.GenerateContentConfig{
		ServiceTier: GeminiConfig{}.GenAIServiceTier(),
	}
	_, err := client.Models.GenerateContent(context.Background(), "gemini-3.5-flash-lite",
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, cfg)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if strings.Contains(body, "serviceTier") {
		t.Errorf("未設定なのに serviceTier が載っている: %s", body)
	}
}
