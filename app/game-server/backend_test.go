package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// タイムアウト設定が未指定なら既定値、指定があればその値になること。
//
// 上限を切らないと呼び出し側の ctx はプロセス終了まで生き、
// TTS が 58 秒詰まって無線が沈黙する事象が起きた (docs/navigator_design.md)。
func TestGeminiTimeouts(t *testing.T) {
	var zero GeminiConfig
	if got := zero.TranscribeTimeout(); got != defaultTranscribeTimeout {
		t.Errorf("TranscribeTimeout() = %v, want %v", got, defaultTranscribeTimeout)
	}
	if got := zero.ReplyTimeout(); got != defaultReplyTimeout {
		t.Errorf("ReplyTimeout() = %v, want %v", got, defaultReplyTimeout)
	}
	if got := zero.TTSTimeout(); got != defaultTTSTimeout {
		t.Errorf("TTSTimeout() = %v, want %v", got, defaultTTSTimeout)
	}

	set := GeminiConfig{
		TranscribeTimeoutSec: 5,
		ReplyTimeoutSec:      7,
		TTSTimeoutSec:        9,
	}
	if got := set.TranscribeTimeout(); got != 5*time.Second {
		t.Errorf("TranscribeTimeout() = %v, want 5s", got)
	}
	if got := set.ReplyTimeout(); got != 7*time.Second {
		t.Errorf("ReplyTimeout() = %v, want 7s", got)
	}
	if got := set.TTSTimeout(); got != 9*time.Second {
		t.Errorf("TTSTimeout() = %v, want 9s", got)
	}

	// 負値は設定ミス。0 と同じく既定値へ倒す (タイムアウト即発火を避ける)
	neg := GeminiConfig{TTSTimeoutSec: -1}
	if got := neg.TTSTimeout(); got != defaultTTSTimeout {
		t.Errorf("負値の TTSTimeout() = %v, want %v", got, defaultTTSTimeout)
	}
}

// TOML から読んだタイムアウトが反映されること。
func TestTimeoutsFromTOML(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "p")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "global")

	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("[gemini]\ntts_timeout_sec = 30\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Gemini.TTSTimeout(); got != 30*time.Second {
		t.Errorf("TTSTimeout() = %v, want 30s", got)
	}
	// 未指定のものは既定値のまま
	if got := cfg.Gemini.ReplyTimeout(); got != defaultReplyTimeout {
		t.Errorf("ReplyTimeout() = %v, want %v", got, defaultReplyTimeout)
	}
}

// TTS の試行回数が未指定なら既定値、指定があればその値になること。
func TestTTSAttemptCount(t *testing.T) {
	var zero GeminiConfig
	if got := zero.TTSAttemptCount(); got != defaultTTSAttempts {
		t.Errorf("TTSAttemptCount() = %d, want %d", got, defaultTTSAttempts)
	}

	if got := (GeminiConfig{TTSAttempts: 5}).TTSAttemptCount(); got != 5 {
		t.Errorf("TTSAttemptCount() = %d, want 5", got)
	}

	// 0以下は設定ミス。既定値へ倒す (0回だと発話が出ない)
	if got := (GeminiConfig{TTSAttempts: -1}).TTSAttemptCount(); got != defaultTTSAttempts {
		t.Errorf("負値の TTSAttemptCount() = %d, want %d", got, defaultTTSAttempts)
	}
	if got := (GeminiConfig{TTSAttempts: 0}).TTSAttemptCount(); got != defaultTTSAttempts {
		t.Errorf("0 の TTSAttemptCount() = %d, want %d", got, defaultTTSAttempts)
	}
}
