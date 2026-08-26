package main

import (
	"context"
	"flag"
	"strings"
	"testing"
)

// 終幕 (解除成功 / 爆発) のシミュレーション。
//
// `TestSimulateAllStages` は課題の進行だけを見るため、**ゲームの締めくくりを
// 一度も通らない**。だが `defused` / `exploded` は
// **キャラクターごとの差が最も大きい場面**で、指示もキャラシートへ
// 委ねてある (ADR N-25・決定47b)。ここが崩れると印象が最後に壊れる。
//
// 直前の交信を踏まえて生成させたいので、**その場で作った会話ログを渡す**
// (両トリガーとも「直前の交信に触れる」ことを求めている)。
//
// 生成AIはばらつくため既定で3回ずつ回す (ADR V-1)。
//
// 実行:
//
//	go test -run TestSimulateEndings -ending -ending-character shrike -v
var (
	runEndingSim  = flag.Bool("ending", false, "実APIを呼ぶ終幕シミュレーションを実行する")
	endingChar    = flag.String("ending-character", "shrike", "キャラクターID")
	endingRepeat  = flag.Int("ending-repeat", 3, "各パターンの試行回数")
	endingStageID = flag.String("ending-stage", "104", "直前の課題として使うステージ")
)

// endingScene は終幕1パターン分の設定。
type endingScene struct {
	Trigger string
	Label   string
	// History は直前の交信 (この場面に至るまでの流れ)
	History []ConversationEntry
	// Event は直近の出来事 (game_coordinator.go が渡すのと同じ文面)。
	//
	// **本番は必ずこれを渡す。** 省くと、プロンプト上の一番新しい事実が
	// 「プレイヤーが青で行くと言った」になり、生成AIがそこへ引っ張られる
	// (実測で8回中8回、直前の色名から話し始めた)。
	Event string
	// Forbid は出てはいけない語と、その理由
	Forbid map[string]string
	// StageIndex はセッション内の位置。解除成功は全課題を終えた状態で渡す
	AllCleared bool
}

func TestSimulateEndings(t *testing.T) {
	if !*runEndingSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-ending で実行)")
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
	character, ok := navCfg.ByID(*endingChar)
	if !ok {
		t.Fatalf("unknown character id: %s", *endingChar)
	}

	built, err := simBuildStage(lib, testMissionSheet(), *endingStageID, 42)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	nav := character.Name
	scenes := []endingScene{
		{
			Trigger:    "defused",
			Label:      "解除成功",
			AllCleared: true,
			Event:      "解除に成功した!祝福する。",
			History: []ConversationEntry{
				{Sender: nav, Receiver: senderPlayer, Message: "2つ点滅しとるな、了解。速いほうと遅いほう、どっちが速いか見比べてみ。どうぞ"},
				{Sender: senderPlayer, Receiver: nav, Message: "たぶん青のほうが速いです。どうぞ"},
				{Sender: nav, Receiver: senderPlayer, Message: "たぶんはあかん。もういっぺん見て、はっきりしてから言うてくれ。どうぞ"},
				{Sender: senderPlayer, Receiver: nav, Message: "確認しました。青が速いです。切ります。どうぞ"},
			},
			Forbid: map[string]string{
				"どうぞ": "交信を終える場面では付けない (ADR 決定46)",
			},
		},
		{
			Trigger: "exploded",
			Label:   "爆発 (失敗)",
			Event:   "解体は失敗し、装置が起動してしまった。失敗を受け止めるメッセージを返す。",
			History: []ConversationEntry{
				{Sender: nav, Receiver: senderPlayer, Message: "2つ点滅しとるな、了解。速いほうと遅いほう、どっちが速いか見比べてみ。どうぞ"},
				{Sender: senderPlayer, Receiver: nav, Message: "たぶん青のほうが速いです。どうぞ"},
				{Sender: nav, Receiver: senderPlayer, Message: "たぶんはあかん。もういっぺん見て、はっきりしてから言うてくれ。どうぞ"},
				{Sender: senderPlayer, Receiver: nav, Message: "やっぱり青で行きます。切りました。どうぞ"},
			},
			Forbid: map[string]string{
				"どうぞ":  "応答が返ってこない場面では付けない (ADR 決定46)",
				"お疲れ":  "無事に終わった前提のねぎらい (ADR N-25)",
				"次は":   "次がある前提の言葉 (ADR N-25)",
				"また挑戦": "次がある前提の言葉 (ADR N-25)",
				"もう一度": "次がある前提の言葉 (ADR N-25)",
				"再挑戦":  "次がある前提の言葉 (ADR N-25)",
				// **ナビゲーターは無線の向こう側にいる。** 爆風も音圧も
				// 届いていないので、身体が受けた被害を語るのは成立しない
				// (現場にいるのはプレイヤーだけ)。禁止しても言い換えて
				// 再発するため、機械的に拾う (決定90)。
				"耳鳴り": "ナビは現場にいない。身体の被害は語れない (決定90)",
				"煙が":  "ナビは現場にいない。現場の様子は見えない (決定90)",
				// **プレイヤーを責める語法。** 「〜よってからに」「〜しくさって」は
				// 非難の形で、失敗した相手に向けると責める発話になる (ADR N-25)。
				"よってからに": "プレイヤーを責める語法 (ADR N-25)",
				"しくさっ":   "プレイヤーを責める語法 (ADR N-25)",
			},
		},
	}

	t.Logf("=== 終幕シミュレーション (キャラクター: %s) 各%d回 ===", character.Name, *endingRepeat)

	for _, scene := range scenes {
		t.Logf("")
		t.Logf("########## %s (%s)", scene.Label, scene.Trigger)

		logs := NewConversationLog(20)
		for _, e := range scene.History {
			logs.Append(e)
		}

		stageIndex := 0
		if scene.AllCleared {
			stageIndex = len(built.Stages)
		}

		for i := 0; i < *endingRepeat; i++ {
			prompt := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt:      &navCfg.Prompt,
				Character:   character,
				Session:     built,
				StageIndex:  stageIndex,
				RemainingMS: 43000,
				HintLevel:   HintL1,
				RecentEvent: scene.Event,
				History:     logs.Render(),
			})
			instruction := navCfg.Prompt.TriggerInstruction(scene.Trigger)

			gen, err := processor.GenerateNavigatorReply(ctx, prompt, instruction)
			if err != nil {
				t.Errorf("  #%d ERROR: %v", i+1, err)
				continue
			}
			body := stripTTSTags(gen.Reply)
			t.Logf("  #%d (%d字) %s", i+1, countRunes(body), gen.Reply)

			// 禁止語
			for word, why := range scene.Forbid {
				if strings.Contains(body, word) {
					t.Errorf("    ✗ %q が出た — %s", word, why)
				}
			}
			// 表情タグは許可された語のみ
			for _, tag := range extractTTSTags(gen.Reply) {
				if !allowedTTSTags[tag] {
					t.Errorf("    ✗ 未許可の表情タグ [%s]", tag)
				}
			}
			// 長さ (締めくくりは長めに許容。解除成功は100字程度まで)
			limit := 100
			if scene.Trigger == "exploded" {
				limit = 90
			}
			if n := countRunes(body); n > limit {
				t.Errorf("    ✗ %d字 (目安 %d字を超過)", n, limit)
			}
		}
	}
}

// extractTTSTags は発話に含まれる表情タグ名を取り出す。
func extractTTSTags(reply string) []string {
	tags := make([]string, 0)
	rest := reply
	for {
		open := strings.Index(rest, "[")
		if open < 0 {
			break
		}
		close := strings.Index(rest[open:], "]")
		if close < 0 {
			break
		}
		tags = append(tags, rest[open+1:open+close])
		rest = rest[open+close:]
	}
	return tags
}
