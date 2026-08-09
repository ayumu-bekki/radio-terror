package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 接続先が揃っているかを起動時に弾けること。
// 実行中の初回API呼び出しまで気づかないのを防ぐ。
func TestGeminiConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     GeminiConfig
		wantErr bool
	}{
		{"project/location あり",
			GeminiConfig{Project: "radio-terror", Location: "us-central1"}, false},
		{"project なし", GeminiConfig{Location: "us-central1"}, true},
		{"location なし", GeminiConfig{Project: "radio-terror"}, true},
		{"どちらも なし", GeminiConfig{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.cfg.Validate(); (err != nil) != c.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// 設定ファイルに無い場合は環境変数にフォールバックすること。
func TestProjectLocationFromEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-northeast1")

	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("[gemini]\ntts_model = \"m\"\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gemini.Project != "env-project" {
		t.Errorf("project = %q, want env-project", cfg.Gemini.Project)
	}
	if cfg.Gemini.Location != "asia-northeast1" {
		t.Errorf("location = %q, want asia-northeast1", cfg.Gemini.Location)
	}
	if err := cfg.Gemini.Validate(); err != nil {
		t.Errorf("環境変数だけで検証を通せるべき: %v", err)
	}
}

// 設定ファイルの値が環境変数より優先されること。
func TestConfigTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-northeast1")

	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(`
[gemini]
project  = "toml-project"
location = "us-central1"
`), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gemini.Project != "toml-project" {
		t.Errorf("project = %q, want toml-project (設定ファイルが優先されるべき)",
			cfg.Gemini.Project)
	}
	if cfg.Gemini.Location != "us-central1" {
		t.Errorf("location = %q, want us-central1", cfg.Gemini.Location)
	}
}

// 環境変数もファイルも無ければ検証で落ちること。
func TestMissingProjectFailsValidation(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")

	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("[gemini]\ntts_model = \"m\"\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Gemini.Validate(); err == nil {
		t.Error("project/location 欠落を検出できていない")
	}
}
