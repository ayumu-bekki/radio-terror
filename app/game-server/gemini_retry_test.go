package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/genai"
)

// TestIsRateLimited は 429 だけを 429 と判定することを確かめる。
func TestIsRateLimited(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{genai.APIError{Code: 429}, true},
		{fmt.Errorf("wrap: %w", genai.APIError{Code: 429}), true},
		{&genai.APIError{Code: 429}, true},
		{genai.APIError{Code: 504}, false},
		{genai.APIError{Code: 400}, false},
		{errors.New("other"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isRateLimited(c.err); got != c.want {
			t.Errorf("isRateLimited(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestIsRetryable は撃ち直して通る見込みのあるエラーだけを対象にすることを確かめる。
// 400/401/403/404 は設定や入力の誤りなので撃ち直さない。
func TestIsRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{genai.APIError{Code: 429}, true},
		{genai.APIError{Code: 408}, true},
		{genai.APIError{Code: 500}, true},
		{genai.APIError{Code: 502}, true},
		{&genai.APIError{Code: 503}, true},
		{fmt.Errorf("wrap: %w", genai.APIError{Code: 504}), true},
		{genai.APIError{Code: 400}, false},
		{genai.APIError{Code: 401}, false},
		{genai.APIError{Code: 403}, false},
		{genai.APIError{Code: 404}, false},
		{context.DeadlineExceeded, true},
		{errors.New("connection reset"), true},
		{nil, false},
	}
	for _, c := range cases {
		if got := isRetryable(c.err); got != c.want {
			t.Errorf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestRetryCallDefaultsToSingleAttempt は試行回数1 (既定) なら再試行しないことを確かめる。
func TestRetryCallDefaultsToSingleAttempt(t *testing.T) {
	calls := 0
	_, err := retryCall(context.Background(), "test", 1, time.Second, func(ctx context.Context) (int, error) {
		calls++
		return 0, genai.APIError{Code: 502}
	})
	if err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// TestRetryCallRetriesUpToAttempts は 502 を試行回数まで撃ち直し、通ったら止めることを確かめる。
func TestRetryCallRetriesUpToAttempts(t *testing.T) {
	calls := 0
	got, err := retryCall(context.Background(), "test", 3, time.Second, func(ctx context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", genai.APIError{Code: 502}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v), want (ok, nil)", got, err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}

	calls = 0
	_, err = retryCall(context.Background(), "test", 2, time.Second, func(ctx context.Context) (string, error) {
		calls++
		return "", genai.APIError{Code: 503}
	})
	if err == nil || calls != 2 {
		t.Errorf("err = %v, calls = %d, want error after 2 calls", err, calls)
	}
}

// TestRetryCallSkipsNonRetryable は 400 系を撃ち直さないことを確かめる。
func TestRetryCallSkipsNonRetryable(t *testing.T) {
	calls := 0
	_, err := retryCall(context.Background(), "test", 3, time.Second, func(ctx context.Context) (int, error) {
		calls++
		return 0, genai.APIError{Code: 400}
	})
	if err == nil || calls != 1 {
		t.Errorf("err = %v, calls = %d, want error after 1 call", err, calls)
	}
}

// TestRetryCallTimesOutPerAttempt は期限が1回の試行ごとに切られ、
// 切れたあとは新しい期限で撃ち直せることを確かめる。
func TestRetryCallTimesOutPerAttempt(t *testing.T) {
	calls := 0
	got, err := retryCall(context.Background(), "test", 2, 30*time.Millisecond, func(ctx context.Context) (string, error) {
		calls++
		if calls == 1 {
			<-ctx.Done() // 1回目は期限まで待たされる
			return "", ctx.Err()
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || calls != 2 {
		t.Errorf("got (%q, %v) after %d calls, want (ok, nil) after 2", got, err, calls)
	}
}

// TestRetryCallStopsWhenParentCancelled は呼び出し元がやめたら撃ち直さないことを確かめる。
func TestRetryCallStopsWhenParentCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryCall(ctx, "test", 3, time.Second, func(ctx context.Context) (int, error) {
		calls++
		cancel()
		return 0, genai.APIError{Code: 502}
	})
	if err == nil || calls != 1 {
		t.Errorf("err = %v, calls = %d, want error after 1 call", err, calls)
	}
}

// TestAttemptDefaults は書き起こし・発話生成の既定が「再試行しない」であることを確かめる。
func TestAttemptDefaults(t *testing.T) {
	var cfg GeminiConfig
	if cfg.TranscribeAttemptCount() != 1 || cfg.ReplyAttemptCount() != 1 {
		t.Errorf("default attempts = %d/%d, want 1/1", cfg.TranscribeAttemptCount(), cfg.ReplyAttemptCount())
	}
	cfg = GeminiConfig{TranscribeAttempts: 2, ReplyAttempts: 3}
	if cfg.TranscribeAttemptCount() != 2 || cfg.ReplyAttemptCount() != 3 {
		t.Errorf("configured attempts = %d/%d, want 2/3", cfg.TranscribeAttemptCount(), cfg.ReplyAttemptCount())
	}
}
