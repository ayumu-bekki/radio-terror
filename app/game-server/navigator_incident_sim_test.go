package main

import (
	"context"
	"flag"
	"regexp"
	"strings"
	"testing"
)

// runIncidentSim は、実運用で事故になった場面を実APIで再現するテスト群を
// 有効にするフラグ。204 で正解色を言わない・迷いに切断を後押ししない・
// 誤った報告への二段構え (確かめ直し → 同じ誤りなら不正解の線)。
var runIncidentSim = flag.Bool("incident", false,
	"実運用で事故になった場面を実APIで再現する (TestColorMatchNeverRevealsColor ほか)")

// TestColorMatchNeverRevealsColor は 204 色合わせで、プレイヤーが
// 「最後に押した色を忘れた」と言っても正解色を言わないことを確かめる
// (ADR N-36 / 実運用ログ 2026-08-23)。
//
// **実運用ではここで爆発した。** 当時は L4 の閾値をまたいだ直後に正解色を
// 直言し、プレイヤーの正しい記憶を上書きして誤切断させた。ヒントレベルは
// 廃止し色名は常に伏せている (決定129) が、記憶頼みの場面で漏れないことを見る。
func TestColorMatchNeverRevealsColor(t *testing.T) {
	if !*runIncidentSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-incident で実行)")
	}

	// 実運用ログのやり取りをそのまま入れてある。
	// 「多目的ホール」は音声認識が崩れた発話で、**色の報告ではない**。
	players := []string{
		"えっと、最後におっしゃった色って何色だったっけ? どうぞ。",
		"最後に押した色を忘れてしまいました。どうぞ。",
		"多目的ホールだと思ってたんだけどまあ切ってみますどうぞ",
		"たぶん青だったと思うけど切ってみますどうぞ",
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

	built, err := simBuildStage(lib, cfg.MissionSheet, "204", 42)
	if err != nil {
		t.Fatalf("simBuildStage(206): %v", err)
	}
	stage := built.Stages[0]
	cutJA := colorNameJA[stage.Cut]
	if cutJA == "" {
		t.Fatalf("正解色が引けない: %q", stage.Cut)
	}

	// 全キャラクターで見る。
	// 口調によって出る問題が違うため4人とも回す (CLAUDE.md)。
	for _, character := range navCfg.Characters {
		for _, player := range players {
			logs := NewSessionLogStore(nil)
			sessionID := "cm-" + character.ID
			logs.Append(sessionID, ConversationEntry{
				Sender: character.Name, Receiver: senderPlayer,
				Message: "ボタンを押し終えたんだね。最後に押した色と同じ色の線を切るといい。どうぞ",
			})
			logs.Append(sessionID, ConversationEntry{
				Sender: senderPlayer, Receiver: character.Name, Message: player,
			})

			prompt := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt: &navCfg.Prompt, Character: character, Session: built,
				StageIndex: 0, RemainingMS: 30000,
				History: logs.Render(sessionID),
			})
			gen, err := processor.GenerateNavigatorReply(ctx, prompt,
				navCfg.Prompt.TriggerInstruction("player_message"))
			if err != nil {
				t.Errorf("%s %q: %v", character.ID, player, err)
				continue
			}
			if strings.Contains(stripTTSTags(gen.Reply), cutJA) {
				t.Errorf("%s が正解色 %q を漏らした\n  P> %s\n  N> %s",
					character.Name, cutJA, player, gen.Reply)
				continue
			}
			t.Logf("OK %s\n  P> %s\n  N> %s", character.Name, player, gen.Reply)
		}
	}
}

// TestUncertainCutIsNotEncouraged は、プレイヤーが迷いを見せているときに
// ナビゲーターが切断を後押ししないことを確かめる (ADR N-37)。
//
// **切った線は戻せず、間違えれば即爆発する** (ADR S-2b)。
// 実運用ログ 2026-08-23 で、音声認識が崩れた「多目的ホールだと思ってたんだけど
// まあ切ってみます」に対し「それを切るといい」と後押しし、誤切断で失敗した。
func TestUncertainCutIsNotEncouraged(t *testing.T) {
	if !*runIncidentSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-incident で実行)")
	}

	// 迷いを見せている発話。いずれも「切れ」と後押ししてはいけない。
	players := []string{
		"多目的ホールだと思ってたんだけどまあ切ってみますどうぞ",
		"たぶん青だったと思うけど切ってみますどうぞ",
		"最後に押した色を忘れましたが、とりあえず切ってみます。どうぞ",
	}

	// 後押しと読める語。**否定形と併せて出るのは可** (「まだ切るな」)。
	pushes := []string{"切れ", "切って", "切るといい", "切ってください", "切りましょう"}
	// 止めていると読める語。これがあれば後押しではない。
	stops := []string{"待", "だめ", "ダメ", "いけません", "いけない", "まだ",
		"確か", "本当", "止め", "やめ", "落ち着", "慎重", "認めません", "急がな"}

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
	built, err := simBuildStage(lib, cfg.MissionSheet, "204", 42)
	if err != nil {
		t.Fatalf("simBuildStage(206): %v", err)
	}

	for _, character := range navCfg.Characters {
		for _, player := range players {
			logs := NewSessionLogStore(nil)
			sessionID := "unc-" + character.ID
			logs.Append(sessionID, ConversationEntry{
				Sender: character.Name, Receiver: senderPlayer,
				Message: "最後に押した色と同じ色の線を切るといい。どうぞ",
			})
			logs.Append(sessionID, ConversationEntry{
				Sender: senderPlayer, Receiver: character.Name, Message: player,
			})

			prompt := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt: &navCfg.Prompt, Character: character, Session: built,
				StageIndex: 0, RemainingMS: 30000,
				History: logs.Render(sessionID),
			})
			gen, err := processor.GenerateNavigatorReply(ctx, prompt,
				navCfg.Prompt.TriggerInstruction("player_message"))
			if err != nil {
				t.Errorf("%s %q: %v", character.ID, player, err)
				continue
			}
			reply := stripTTSTags(gen.Reply)

			pushed := false
			for _, w := range pushes {
				if strings.Contains(reply, w) {
					pushed = true
					break
				}
			}
			stopped := false
			for _, w := range stops {
				if strings.Contains(reply, w) {
					stopped = true
					break
				}
			}
			if pushed && !stopped {
				t.Errorf("%s が迷っている相手に切断を後押しした\n  P> %s\n  N> %s",
					character.Name, player, reply)
				continue
			}
			t.Logf("OK %s\n  P> %s\n  N> %s", character.Name, player, reply)
		}
	}
}

// TestInvalidColorFirstReplyAsksAgain は、5色以外の色名が初めて報告されたとき、
// ナビゲーターが否定・断定せずに5色を並べて確かめ直させることを確かめる
// (決定124・決定132)。
//
// ナビゲーターは装置を見ていないので、「その色は無い」と言い切れる立場にない。
// 突き放すと、プレイヤーは自分の目を疑うより先にナビを疑う。
// 否定の判定は機械では難しいため、**返答はすべてログに出す** — 目で確かめる。
func TestInvalidColorFirstReplyAsksAgain(t *testing.T) {
	if !*runIncidentSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-incident で実行)")
	}

	reports := []struct{ word, text string }{
		{"茶色", "茶色のランプが光ってます。どうぞ"},
		{"紫", "紫のランプが点滅してます。どうぞ"},
		{"ピンク", "ピンクっぽいランプがついてます。どうぞ"},
	}
	// 報告された色を否定・断定する形。
	denial := regexp.MustCompile(`(無い|ない|ありません|あらへん|あれへん|ねえ|存在し|見当たら)`)

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
	built, err := simBuildStage(lib, cfg.MissionSheet, "101", 42)
	if err != nil {
		t.Fatalf("simBuildStage(101): %v", err)
	}

	denied := 0
	total := 0
	for _, character := range navCfg.Characters {
		for _, report := range reports {
			logs := NewSessionLogStore(nil)
			sessionID := "inv-" + character.ID
			logs.Append(sessionID, ConversationEntry{
				Sender: character.Name, Receiver: senderPlayer,
				Message: "ランプはどうなっている? どうぞ",
			})
			logs.Append(sessionID, ConversationEntry{
				Sender: senderPlayer, Receiver: character.Name, Message: report.text,
			})

			prompt := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt: &navCfg.Prompt, Character: character, Session: built,
				StageIndex: 0, RemainingMS: 120000,
				History:     logs.Render(sessionID),
				WrongReport: report.word, WrongReportCount: 1,
			})
			gen, err := processor.GenerateNavigatorReply(ctx, prompt,
				navCfg.Prompt.TriggerInstruction("player_message"))
			if err != nil {
				t.Errorf("%s %q: %v", character.ID, report.text, err)
				continue
			}
			total++
			reply := stripTTSTags(gen.Reply)

			named := 0
			for _, c := range []string{"赤", "黄", "緑", "青", "白"} {
				if strings.Contains(reply, c) {
					named++
				}
			}
			if named < 5 {
				t.Errorf("%s: 5色を並べていない (%d色)\n  P> %s\n  N> %s",
					character.Name, named, report.text, reply)
			}
			if strings.Contains(reply, "切") {
				t.Errorf("%s: 1回目で切る話をした\n  P> %s\n  N> %s",
					character.Name, report.text, reply)
			}
			mark := "  "
			if denial.MatchString(reply) {
				denied++
				mark = "? "
			}
			t.Logf("%s%s\n  P> %s\n  N> %s", mark, character.Name, report.text, reply)
		}
	}
	// 否定形は目視で判断する (「分からない」のような無害な形も拾うため)
	t.Logf("否定形の候補: %d/%d (行頭 ? の返答を目で確かめる)", denied, total)
}

// TestWrongReportTwoStep は、光っていない色を報告したとき
// 1回目は確かめ直しを頼み、2回目も光っていない色なら不正解の線を切らせ、
// 光っている色に言い直したらその色で受けることを確かめる
// (決定135・決定136。実運用: 208 で「緑が点灯」に切断の指示を返した /
// 「赤」→「青」と2回誤ったら「赤だな、了解」と前の色で受けた)。
func TestWrongReportTwoStep(t *testing.T) {
	if !*runIncidentSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-incident で実行)")
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
	built, err := simBuildStage(lib, cfg.MissionSheet, "208", 42)
	if err != nil {
		t.Fatalf("simBuildStage(208): %v", err)
	}
	stage := built.Stages[0]
	states := stageLampStates(stage)
	shown := stageShownColors(stage)
	decoy := colorNameJA[decoyCutColor(built, 0)]
	lit := colorNameJA[stage.Cut]
	var wrongs []string
	for _, code := range allColors {
		if !shown[code] && colorNameJA[code] != decoy {
			wrongs = append(wrongs, colorNameJA[code])
		}
	}
	first, second := wrongs[0], wrongs[1]
	t.Logf("光っている色=%s 誤報告=%s,%s 不正解の線=%s", lit, first, second, decoy)

	// expect は各回の期待: recheck=確かめ直し (切る話をしない) /
	// decoy=不正解の線を切らせる (報告の色を口にしない) / correct=新しい色で受ける
	type step struct{ report, expect string }
	patterns := []struct {
		name  string
		steps []step
	}{
		{"同じ誤り", []step{{first + "が点灯しています。どうぞ", "recheck"}, {first + "が点灯しています。どうぞ", "decoy"}}},
		{"別の誤り", []step{{first + "が点灯しています。どうぞ", "recheck"}, {"すみません。" + second + "でした。どうぞ", "recheck"}, {second + "が点灯しています。どうぞ", "decoy"}}},
		{"光り方の取り違え", []step{{lit + "が点滅しています。どうぞ", "recheck"}, {lit + "が点滅しています。どうぞ", "decoy"}}},
		{"正しく言い直す", []step{{first + "が点灯しています。どうぞ", "recheck"}, {"すみません。" + lit + "でした。どうぞ", "correct"}}},
		{"間に正しい報告", []step{{first + "が点灯しています。どうぞ", "recheck"}, {"すみません。" + lit + "でした。どうぞ", "correct"}, {"あれ、" + first + "が点灯しています。どうぞ", "recheck"}}},
	}

	for _, pat := range patterns {
		for _, character := range navCfg.Characters {
			logs := NewSessionLogStore(nil)
			sessionID := "uns-" + character.ID
			var progress StageProgress
			logs.Append(sessionID, ConversationEntry{
				Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ",
			})
			for round, st := range pat.steps {
				logs.Append(sessionID, ConversationEntry{
					Sender: senderPlayer, Receiver: character.Name, Message: st.report,
				})
				progress.NoteReport(st.report, states)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000,
					History:             logs.Render(sessionID),
					WrongReport:         progress.LastWrongReport,
					WrongReportCount:    progress.WrongReportCount,
					WrongReportMismatch: progress.LastWrongIsMismatch,
					CorrectedFrom:       progress.CorrectedFrom,
					CorrectedTo:         progress.CorrectedTo,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s %s #%d: %v", pat.name, character.ID, round+1, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				logs.Append(sessionID, ConversationEntry{
					Sender: character.Name, Receiver: senderPlayer, Message: reply,
				})
				t.Logf("[%s] %s #%d (%s)\n  P> %s\n  N> %s", pat.name, character.Name, round+1, st.expect, st.report, reply)

				switch st.expect {
				case "recheck":
					if strings.Contains(reply, "切") {
						t.Errorf("[%s] %s #%d: 確かめ直しの場面で切る話をした", pat.name, character.Name, round+1)
					}
				case "decoy":
					if !(strings.Contains(reply, decoy) && strings.Contains(reply, "切")) {
						t.Errorf("[%s] %s #%d: 不正解の線 (%s) を切らせていない", pat.name, character.Name, round+1, decoy)
					}
					if strings.Contains(reply, first) || strings.Contains(reply, second) {
						t.Errorf("[%s] %s #%d: 報告の色を口にした", pat.name, character.Name, round+1)
					}
				case "correct":
					if strings.Contains(reply, first) {
						t.Errorf("[%s] %s #%d: 取り消された色 (%s) を口にした", pat.name, character.Name, round+1, first)
					}
				}
			}
		}
	}
}

// TestPreviousStageColorNotReused は、前の課題の色を今の課題の指示に使わないことを
// 確かめる (決定139。実運用: 104 で「白の方が早い」に、前の課題 201 の
// 「赤い色の線を切って」をなぞって「その赤い色の線を切って」と返し、爆発した)。
func TestPreviousStageColorNotReused(t *testing.T) {
	if !*runIncidentSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-incident で実行)")
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

	// 前の課題 (201) の正解が、今の課題 (104) のランプに出てこない抽選を探す
	// (出てくる色だと、正しく口にしても区別できない)
	var prev, cur *BuiltStage
	for seed := int64(1); seed < 50 && cur == nil; seed++ {
		a, err1 := simBuildStage(lib, cfg.MissionSheet, "201", seed)
		b, err2 := simBuildStage(lib, cfg.MissionSheet, "104", seed+100)
		if err1 == nil && err2 == nil && !stageShownColors(b.Stages[0])[a.Stages[0].Cut] {
			prev, cur = a.Stages[0], b.Stages[0]
		}
	}
	session := &BuiltSession{Difficulty: "easy", Stages: []*BuiltStage{prev, cur}}
	prevJA, fastJA := colorNameJA[prev.Cut], colorNameJA[cur.Cut]
	slowJA := ""
	for line := range stageShownColors(cur) {
		if line != cur.Cut {
			slowJA = colorNameJA[line]
		}
	}
	t.Logf("前の課題の正解=%s 今の課題: 速い=%s 遅い=%s", prevJA, fastJA, slowJA)

	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			sessionID := "prev-" + character.ID
			add := func(sender, receiver, msg string) {
				logs.Append(sessionID, ConversationEntry{Sender: sender, Receiver: receiver, Message: msg})
			}
			add(senderPlayer, character.Name, prevJA+"が点灯しています。どうぞ")
			add(character.Name, senderPlayer, prevJA+"ですね。"+prevJA+"い色の線を切ってください。どうぞ")
			logs.Append(sessionID, ConversationEntry{Kind: EntryKindEvent, Event: EventStageCleared, Message: "✓ ステージ1 クリア: 復唱"})
			logs.Append(sessionID, ConversationEntry{Kind: EntryKindEvent, Event: EventStageStart, Message: "ステージ2開始: 早い者勝ち"})
			add(senderPlayer, character.Name, "切りました。どうぞ")
			add(character.Name, senderPlayer, "切れたんですね。ランプはどうなっていますか? どうぞ")
			add(senderPlayer, character.Name, fastJA+"と"+slowJA+"が点滅しているように見えます。どうぞ")
			add(character.Name, senderPlayer, "点滅の速さに違いがないか見比べてみてください。どうぞ")
			report := fastJA + "の方が早い気がします。どうぞ"
			add(senderPlayer, character.Name, report)

			prompt := BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt: &navCfg.Prompt, Character: character, Session: session,
				StageIndex: 1, RemainingMS: 90000,
				History: logs.Render(sessionID),
			})
			gen, err := processor.GenerateNavigatorReply(ctx, prompt,
				navCfg.Prompt.TriggerInstruction("player_message"))
			if err != nil {
				t.Errorf("%s: %v", character.ID, err)
				continue
			}
			reply := stripTTSTags(gen.Reply)
			t.Logf("%s #%d\n  P> %s\n  N> %s", character.Name, round+1, report, reply)
			if strings.Contains(reply, prevJA) {
				t.Errorf("%s: 前の課題の色 (%s) を口にした", character.Name, prevJA)
			}
			if !strings.Contains(reply, "切") {
				t.Errorf("%s: 速い方の線を切る指示が無い", character.Name)
			}
		}
	}
}
