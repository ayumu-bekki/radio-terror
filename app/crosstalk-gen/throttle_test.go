package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// acquire がリクエスト間隔を守ることを確認する (API は呼ばない)。
func TestAcquireThrottles(t *testing.T) {
	g := &Generator{interval: 100 * time.Millisecond}
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := g.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// 1回目は待たない → 3間隔ぶん
	if el := time.Since(start); el < 280*time.Millisecond {
		t.Errorf("4回で %v しか経っていない (300ms前後を期待)", el)
	}
	if g.Used() != 4 {
		t.Errorf("Used = %d, want 4", g.Used())
	}
}

// max_requests がレート枠の使いすぎを止めることを確認する。
func TestAcquireBudget(t *testing.T) {
	g := &Generator{budget: 2, limited: true}
	for i := 0; i < 2; i++ {
		if err := g.acquire(context.Background()); err != nil {
			t.Fatalf("%d回目で枠切れ: %v", i+1, err)
		}
	}
	err := g.acquire(context.Background())
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("3回目 = %v, want ErrBudgetExhausted", err)
	}
	if g.Used() != 2 {
		t.Errorf("Used = %d, want 2 (超過分は数えない)", g.Used())
	}
}

// 並列に呼んでも間隔が守られること (直列化されるか)。
func TestAcquireSerializesAcrossGoroutines(t *testing.T) {
	g := &Generator{interval: 50 * time.Millisecond}
	start := time.Now()
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() { g.acquire(context.Background()); done <- struct{}{} }()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if el := time.Since(start); el < 140*time.Millisecond {
		t.Errorf("並列4回が %v で完了 (間隔が守られていない)", el)
	}
}

// 上限0は無制限。
func TestBudgetUnlimited(t *testing.T) {
	g := &Generator{limited: false}
	for i := 0; i < 50; i++ {
		if err := g.acquire(context.Background()); err != nil {
			t.Fatalf("無制限のはずが %v", err)
		}
	}
}

// 設定が安全側になっているか (事故防止の回帰テスト)。
func TestConfigRateLimitsAreConservative(t *testing.T) {
	cfg, err := LoadConfig("crosstalk.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Project == "" || cfg.Defaults.Location == "" {
		t.Error("project/location が未設定")
	}
	if cfg.Defaults.Concurrency > 2 {
		t.Errorf("concurrency=%d は高すぎる (429を誘発する)", cfg.Defaults.Concurrency)
	}
	if cfg.Defaults.MaxRetries > 5 {
		t.Errorf("max_retries=%d は多すぎる (レート枠を食い潰す)", cfg.Defaults.MaxRetries)
	}
	if cfg.Defaults.RequestIntervalMs < 1000 {
		t.Errorf("request_interval_ms=%d は短すぎる", cfg.Defaults.RequestIntervalMs)
	}
	if cfg.Defaults.MaxRequests == 0 {
		t.Error("max_requests が無制限。事故時に枠を使い切る")
	}
}

func TestIsPermanent(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"PERMISSION_DENIED", true},
		{"could not find default credentials", true},
		{"invalid_grant: account not found", true},
		{"models/foo is not found", true},
		{"Error 400, Message: Request contains an invalid argument.", false}, // 散発的な400 → 再試行する
		{"Error 429, quota exceeded", false},
	}
	for _, c := range cases {
		if got := isPermanent(errors.New(c.msg)); got != c.want {
			t.Errorf("isPermanent(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
