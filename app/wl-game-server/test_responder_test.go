package main

import (
	"strings"
	"testing"
)

// TestIsTestResponderTarget は応答すべき発話の判定を確かめる。
// 無音・雑音のみ(空文字)には応答しない (docs/navigator_design.md §3.6 と同じ方針)。
func TestIsTestResponderTarget(t *testing.T) {
	cases := []struct {
		name  string
		items []TranscriptionItem
		want  bool
	}{
		{"発話あり", []TranscriptionItem{{Message: "こちらマネージャー。テスト。どうぞ"}}, true},
		{"複数のうち1つ発話あり", []TranscriptionItem{{Message: ""}, {Message: "聞こえるか"}}, true},
		{"空文字のみ", []TranscriptionItem{{Message: ""}}, false},
		{"空白のみ", []TranscriptionItem{{Message: "   "}}, false},
		{"アイテムなし", nil, false},
	}
	for _, c := range cases {
		got := isTestResponderTarget(&TranscriptionResult{Items: c.items})
		if got != c.want {
			t.Errorf("%s: isTestResponderTarget = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTestResponderLogIsolation は bridge ごとに交信ログが独立することを確かめる。
// 複数チームを並行運用するため、別チームの発話が混ざってはいけない。
func TestTestResponderLogIsolation(t *testing.T) {
	r := NewTestResponder(nil, nil)

	r.logFor("BR01").Append(ConversationEntry{Sender: "相手", Message: "こちらBR01"})
	r.logFor("BR02").Append(ConversationEntry{Sender: "相手", Message: "こちらBR02"})

	if got := r.logFor("BR01").Render(); !strings.Contains(got, "BR01") || strings.Contains(got, "BR02") {
		t.Errorf("BR01 のログに他 bridge の発話が混ざっている: %s", got)
	}
	if got := r.logFor("BR02").Render(); !strings.Contains(got, "BR02") || strings.Contains(got, "BR01") {
		t.Errorf("BR02 のログに他 bridge の発話が混ざっている: %s", got)
	}
}

// TestTestResponderReset はセッション開始時に疎通確認の文脈が破棄されることを確かめる。
// 残っていると本番の交信に設営時の雑談が混ざる。
func TestTestResponderReset(t *testing.T) {
	r := NewTestResponder(nil, nil)

	r.logFor("BR01").Append(ConversationEntry{Sender: "相手", Message: "マイクのテスト中"})
	if r.logFor("BR01").Render() == "" {
		t.Fatal("追記できていない")
	}

	r.Reset("BR01")

	if got := r.logFor("BR01").Render(); got != "" {
		t.Errorf("Reset 後もログが残っている: %s", got)
	}
}

// TestTestResponderPromptGuards はプロンプトが疎通確認の役割から
// 逸脱しないよう制約を含んでいることを確かめる。
//
// この応答者はゲームの正解を知らない立場なので、うっかりヒントを
// 出さないことがプロンプト上で担保されている必要がある。
func TestTestResponderPromptGuards(t *testing.T) {
	for _, want := range []string{
		"カラス",     // 名乗り
		"疎通確認",    // 役割
		"一切知りません", // ゲーム内容への関与を否定
		"どうぞ",     // 無線の作法
	} {
		if !strings.Contains(testResponderPrompt, want) {
			t.Errorf("プロンプトに %q が含まれていない", want)
		}
	}

	// ナビゲーターのキャラクター名と衝突しないこと
	// (無線で聞いたときに本番の相手と混同されないようにする)
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	for _, c := range cfg.Characters {
		if c.Name == TestResponderCallsign {
			t.Errorf("疎通確認のコールサイン %q がナビゲーターと重複している", c.Name)
		}
	}
}
