package main

import (
	"os"
	"testing"
)

// project / location が読めること。
func TestProjectLocationParsing(t *testing.T) {
	path := t.TempDir() + "/v.toml"
	os.WriteFile(path, []byte(`
[defaults]
project  = "radio-terror"
location = "us-central1"
model    = "gemini-3.1-flash-tts-preview"
voice    = "Puck"
scene    = "テスト"
[[uneasy]]
name = "x"
text = "テスト"
`), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Defaults.Project != "radio-terror" || cfg.Defaults.Location != "us-central1" {
		t.Errorf("project=%q location=%q", cfg.Defaults.Project, cfg.Defaults.Location)
	}
}

// project / location が無ければ起動時に落ちること。
// 実行中の初回API呼び出しまで気づかないのを防ぐ。
func TestProjectLocationRequired(t *testing.T) {
	// 環境変数のフォールバックが効かないよう退避する
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")

	for _, body := range []string{
		`location = "us-central1"`,
		`project  = "p"`,
		``,
	} {
		path := t.TempDir() + "/x.toml"
		os.WriteFile(path, []byte("[defaults]\n"+body+`
model = "m"
scene = "s"
[[uneasy]]
name = "x"
text = "t"
`), 0o644)
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("project/location 欠落を検出できていない: %q", body)
		}
	}
}

// 設定ファイルに無い場合は環境変数にフォールバックすること。
func TestProjectLocationFromEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-northeast1")

	path := t.TempDir() + "/e.toml"
	os.WriteFile(path, []byte(`
[defaults]
model = "m"
scene = "s"
[[uneasy]]
name = "x"
text = "t"
`), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("環境変数のフォールバックが効いていない: %v", err)
	}
	if cfg.Defaults.Project != "env-project" {
		t.Errorf("project = %q, want env-project", cfg.Defaults.Project)
	}
	if cfg.Defaults.Location != "asia-northeast1" {
		t.Errorf("location = %q, want asia-northeast1", cfg.Defaults.Location)
	}
}

// 設定ファイルの値が環境変数より優先されること。
func TestConfigTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-northeast1")

	path := t.TempDir() + "/p.toml"
	os.WriteFile(path, []byte(`
[defaults]
project  = "toml-project"
location = "us-central1"
model    = "m"
scene    = "s"
[[uneasy]]
name = "x"
text = "t"
`), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Project != "toml-project" {
		t.Errorf("project = %q, want toml-project (設定ファイルが優先されるべき)",
			cfg.Defaults.Project)
	}
}

// 実際の crosstalk.toml が読めること。
func TestActualConfigLoads(t *testing.T) {
	if _, err := LoadConfig("crosstalk.toml"); err != nil {
		t.Fatalf("crosstalk.toml が読めない: %v", err)
	}
}
