package main

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genai"
)

// TestIsRateLimited は 429 だけを再試行の対象にすることを確かめる。
// 504 (期限切れ) は再試行しても残り時間が無いので対象外。
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
