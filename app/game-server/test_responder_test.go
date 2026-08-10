package main

import (
	"os"
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

// TestTestResponderPromptGuards はプロンプトが必要な制約を
// 含んでいることを確かめる。
//
// この応答者は雑談に応じるが、ゲームの正解を知らない立場なので、
// うっかりヒントを出さないことがプロンプト上で担保されている必要がある。
func TestTestResponderPromptGuards(t *testing.T) {
	for _, want := range []string{
		"カラス",         // 名乗り
		"何のことか分かりません", // ゲーム内容への関与を否定
		"知ったかぶり",      // 推測で答えないこと
		"どうぞ",         // 無線の作法
	} {
		if !strings.Contains(testResponderPrompt, want) {
			t.Errorf("プロンプトに %q が含まれていない", want)
		}
	}

	// 雑談に応じる指示があること (テスト用と分かる応答だと会話が続かない)
	for _, want := range []string{
		"話題に普通に応じて", // 雑談への応答
		"繰り返さないで",   // オウム返しの禁止
		"言い切ること",    // 検索結果を次の発話へ持ち越さない
		"声に出して読める",  // 箇条書き・URLを読み上げない
	} {
		if !strings.Contains(testResponderPrompt, want) {
			t.Errorf("プロンプトに %q が含まれていない (雑談に応じられなくなる)", want)
		}
	}

	// 運営視点の語がプレイヤーへの発話に混ざらないこと。
	// カラスは「たまたま同じ周波数にいる無線好き」で、疎通確認の担当ではない。
	for _, ng := range []string{"疎通確認", "運営スタッフ", "通信の確認だけだ"} {
		if strings.Contains(testResponderPrompt, ng) {
			t.Errorf("プロンプトに運営視点の語 %q が含まれている", ng)
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

// TestSearchScopedToCrow は Google 検索がカラス専用であることを確かめる。
//
// ナビゲーターで検索を使うと、カウントダウン中の応答が1往復ぶん遅くなる。
// また正解はセッションJSONで渡されるので外部情報は不要。
func TestSearchScopedToCrow(t *testing.T) {
	crow, err := os.ReadFile("test_responder.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(crow), "GenerateReplyWithSearch") {
		t.Error("カラスが検索版を使っていない")
	}

	nav, err := os.ReadFile("navigator_speaker.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(nav), "GenerateReplyWithSearch") {
		t.Error("ナビゲーターが検索版を使っている (応答が遅くなる)")
	}
}
