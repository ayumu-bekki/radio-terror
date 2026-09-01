package main

import (
	"context"
	"flag"
	"strings"
	"testing"
)

// 無応答時の声掛け (`silence`) のシミュレーション。
//
// `TestSimulateAllStages` はプレイヤーが必ず応答する台本なので、
// **このトリガーを一度も通らない**。だが実機では最も自然に起きる場面で、
// **黙り込んだプレイヤーに最初に届く声**になる。
//
// 見るのは3点 (ADR P-9):
//
//  1. 状況を**尋ねている**か (装置を見ていないので断定できない)
//  2. **新しい手順を足していない**か (進める場面ではなく確かめる場面)
//  3. **急かしていない**か (考えている最中かもしれない)
//
// 生成AIはばらつくため既定で3回ずつ回す (ADR V-1)。
//
// 実行:
//
//	go test -run TestSimulateSilence -silence -silence-character shrike -v
var (
	runSilenceSim  = flag.Bool("silence", false, "実APIを呼ぶ無応答シミュレーションを実行する")
	silenceChar    = flag.String("silence-character", "owl", "キャラクターID")
	silenceRepeat  = flag.Int("silence-repeat", 3, "各場面の試行回数")
	silenceStageID = flag.String("silence-stage", "104", "対象ステージ")
)

// silenceScene は無応答1パターン分の設定。
type silenceScene struct {
	Label string
	// History は途絶える直前までの交信
	History []ConversationEntry
	// Forbid は出てはいけない語と理由
	Forbid map[string]string
}

func TestSimulateSilence(t *testing.T) {
	if !*runSilenceSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-silence で実行)")
	}

	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	ctx := context.Background()
	processor, err := NewGeminiProcessor(ctx, cfg.Gemini)
	if err != nil {
		t.Fatalf("NewGeminiProcessor: %v", err)
	}
	navCfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	lib, err := LoadScenarioLibrary("scenarios")
	if err != nil {
		t.Fatalf("LoadScenarioLibrary: %v", err)
	}
	character, ok := navCfg.ByID(*silenceChar)
	if !ok {
		t.Fatalf("unknown character id: %s", *silenceChar)
	}

	built, err := simBuildStage(lib, testMissionSheet(), *silenceStageID, 42)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	nav := character.Name

	// **責める語・急かす語**は場面を壊す。プレイヤーは考えている最中か、
	// 資料を読んでいる最中かもしれない (ADR P-9)。
	commonForbid := map[string]string{
		"早く":   "急かす語 (考えている最中かもしれない)",
		"何をやっ": "責める語法",
		"聞いてる": "詰問になる",
	}

	scenes := []silenceScene{
		{
			// **手順の途中で途絶えた場面。** どこで止まっているかを尋ねる。
			Label: "指示の途中で途絶えた",
			History: []ConversationEntry{
				{Sender: nav, Receiver: senderPlayer, Message: "ランプはどうなっている? どうぞ"},
				{Sender: senderPlayer, Receiver: nav, Message: "2つ点滅しています。どうぞ"},
				{Sender: nav, Receiver: senderPlayer, Message: "点滅が2つだな、了解。速いほうと遅いほうを見比べろ。どうぞ"},
			},
			Forbid: commonForbid,
		},
		{
			// **第一声のあと、何も返ってこない場面。** まだ報告が無い。
			Label: "第一声のあと応答が無い",
			History: []ConversationEntry{
				{Sender: nav, Receiver: senderPlayer, Message: "ランプはどうなっている? どうぞ"},
			},
			Forbid: commonForbid,
		},
	}

	t.Logf("=== 無応答シミュレーション (キャラクター: %s) 各%d回 ===",
		character.Name, *silenceRepeat)

	cutJA := colorNameJA[built.Stages[0].Cut]

	for _, scene := range scenes {
		t.Logf("")
		t.Logf("########## %s", scene.Label)

		logs := NewConversationLog(20)
		for _, e := range scene.History {
			logs.Append(e)
		}

		for i := 0; i < *silenceRepeat; i++ {
			prompt := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt:      &navCfg.Prompt,
				Character:   character,
				Session:     built,
				StageIndex:  0,
				RemainingMS: 180000,
				HintLevel:   HintL1,
				RecentEvent: "",
				History:     logs.Render(),
			})
			instruction := navCfg.Prompt.TriggerInstruction("silence")

			gen, err := processor.GenerateNavigatorReply(ctx, prompt, instruction)
			if err != nil {
				t.Errorf("  #%d ERROR: %v", i+1, err)
				continue
			}
			body := stripTTSTags(gen.Reply)
			t.Logf("  #%d (%d字) %s", i+1, countRunes(body), gen.Reply)

			for word, why := range scene.Forbid {
				if strings.Contains(body, word) {
					t.Errorf("    ✗ %q が出た — %s", word, why)
				}
			}

			// **正解色を漏らしていないか** (L1 なので伏せる。ADR N-1)
			if cutJA != "" && strings.Contains(body, cutJA) {
				t.Errorf("    ✗ 正解色 %q が漏れた (L1)", cutJA)
			}

			// 表情タグは許可された語のみ (ADR T-5)
			for _, tag := range extractTTSTags(gen.Reply) {
				if !allowedTTSTags[tag] {
					t.Errorf("    ✗ 未許可の表情タグ [%s]", tag)
				}
			}

			// **応答を求める場面なので「どうぞ」で締める。**
			// 付けないとプレイヤーが送信してよいか分からない。
			if !strings.Contains(body, "どうぞ") {
				t.Errorf("    ✗ 「どうぞ」が無い (応答を求める場面)")
			}
		}
	}
}
