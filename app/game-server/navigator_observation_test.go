package main

import (
	"context"
	"flag"
	"testing"
)

// runObservationSim は observed 判定の実API検証を有効にするフラグ。
var runObservationSim = flag.Bool("observed", false,
	"observed 判定を実APIで検証する (TestObservationJudgement)")

// obsCase は observed 判定の1ケース。
type obsCase struct {
	// Stage は対象ステージID
	Stage string
	// Player はプレイヤーの報告 (会話ログへ積む)
	Player string
	// Want は期待する observed
	Want bool
	// Why はこのケースを置いた理由 (失敗時に表示する)
	Why string
}

// TestObservationJudgement は observation の判定が意図どおり働くかを実APIで確かめる
// (docs/navigator_design.md 決定61 / ADR N-35)。
//
// **実運用で空振りした報告をそのまま入れてある。** 105 早い者勝ちの
// 「緑がゆっくり全滅していて、青が早く全滅してます」は音声認識が
// 「点滅」を「全滅」と誤変換したもので、**この揺れごと true にならないと
// 実運用では効かない**。
//
// 生成AIはばらつくため、ADR V-1 のとおり判定は1回で確定させない。
// -observed-runs で回数を指定し、**全回一致**を要求する。
func TestObservationJudgement(t *testing.T) {
	if !*runObservationSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-observed で実行)")
	}

	cases := []obsCase{
		// --- 105 早い者勝ち ---
		{"105", "ランプが2つ点滅しています。どうぞ", false,
			"状態だけの報告。速さの差に触れていないので観察は未完了"},
		{"105", "えっと、緑がゆっくり全滅していて、青が早く全滅してます。どうぞ。", true,
			"実運用ログ 2026-08-23。『点滅』が『全滅』へ誤変換されているが速さの差は伝わっている"},
		{"105", "点滅の速さが違うように見えます。どうぞ", true,
			"色名は無いが速さの差を報告できている"},

		// --- 204 切るな危険 ---
		{"204", "2つのランプが点滅していて、3つは消えています。どうぞ", true,
			"点滅2・消灯3 を報告できている"},
		{"204", "ランプを見ています。どうぞ", false,
			"何も報告していない"},

		// --- 207 仲間外れ ---
		{"207", "5つとも点滅しています。どうぞ", true,
			"5つ点滅を報告できている"},

		// --- 205 ブループリント ---
		{"205", "全部点いてます。どうぞ", true,
			"全灯を報告できている。言い回しは問わない"},
		{"205", "まだよく見えません。どうぞ", false,
			"報告になっていない"},
	}

	ctx := context.Background()
	cfg, err := LoadConfig("config.toml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
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
	character := navCfg.Characters[0]

	for _, c := range cases {
		built, err := simBuildStage(lib, cfg.MissionSheet, c.Stage, 42)
		if err != nil {
			t.Fatalf("simBuildStage(%s): %v", c.Stage, err)
		}

		// ナビゲーターが尋ね、プレイヤーが答えた形の会話ログを作る。
		logs := NewSessionLogStore(nil)
		sessionID := "obs-" + c.Stage
		logs.Append(sessionID, ConversationEntry{
			Sender: character.Name, Receiver: senderPlayer,
			Message: "こちらフクロウ。ランプはどうなっている? どうぞ",
		})
		logs.Append(sessionID, ConversationEntry{
			Sender: senderPlayer, Receiver: character.Name, Message: c.Player,
		})

		prompt := BuildNavigatorPrompt(NavigatorPromptInput{
			Prompt: &navCfg.Prompt, Character: character, Session: built,
			StageIndex: 0, RemainingMS: 120000, HintLevel: HintL1,
			History: logs.Render(sessionID),
		})
		instruction := navCfg.Prompt.TriggerInstruction("player_message")

		gen, err := processor.GenerateNavigatorReply(ctx, prompt, instruction)
		if err != nil {
			t.Errorf("%s %q: %v", c.Stage, c.Player, err)
			continue
		}
		if gen.Observed != c.Want {
			t.Errorf("%s: observed = %v, want %v — %s\n  P> %s\n  N> %s",
				c.Stage, gen.Observed, c.Want, c.Why, c.Player, gen.Reply)
			continue
		}
		t.Logf("OK %s observed=%-5v P> %s\n              N> %s",
			c.Stage, gen.Observed, c.Player, gen.Reply)
	}
}
