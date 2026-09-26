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

// TestMorseMisreadNotCorrected は、モールスの読み違いに対してナビが
// 「違う」と言い切らず、表にある単語を「無い」と言わず、近い単語や
// 読み方を示して導くことを確かめる (決定141〜143)。
// 実運用: RLFA に「アルファのことや」、ORAVA→OSCAR に「違う」を繰り返して
// 「BRAVOを探せ」、MANGO に「MIKE は表に載ってない」と言って手詰まりにした。
// 近い単語を挙げること自体は許す (ユーザー判断。決定143)。返答は目でも確かめる。
func TestMorseMisreadNotCorrected(t *testing.T) {
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
	build := func(word string) *BuiltSession {
		for seed := int64(1); seed < 500; seed++ {
			b, err := simBuildStage(lib, cfg.MissionSheet, "302", seed)
			if err == nil && b.Stages[0].Navigator["secret_word"] == word {
				return b
			}
		}
		t.Fatalf("%s を引く抽選が見つからない", word)
		return nil
	}

	cases := []struct {
		word    string
		reports []string
	}{
		{"ALFA", []string{
			"アールエルエフエーとなりました。どうぞ",
			"先頭がアールでしたが大丈夫ですか?どうぞ",
		}},
		{"BRAVO", []string{
			"ORAVAとなりました。これをどうすればよい?どうぞ",
			"ORAVOでした。該当する単語がないです。どうぞ",
		}},
		{"TANGO", []string{
			"MANGOでした。マンゴー?どうぞ",
			"MANGOがありません。MIKEでしょうか?どうぞ",
			"だからないのです。どうしましょう?どうぞ",
		}},
	}
	// 言い切り。問いかけの形 (「ちゃうか」「間違いじゃないか」) は除く
	assert := regexp.MustCompile(`違う[。、]|違います|ちゃう[。、わで]|間違って(る|いる)`)
	echoWorry := regexp.MustCompile(`違う(くらい|ても|て|のは)`)
	// 表にある単語を「無い」と言っていないか (「MIKE…載ってない」)
	var tableWords []string
	for _, r := range morseSheetRows {
		tableWords = append(tableWords, r.word)
	}
	absentClaim := regexp.MustCompile(`(` + strings.Join(tableWords, "|") + `)[はも]?(表に)?(載ってない|載っていない|載ってへん|無い|ありません)`)

	for _, c := range cases {
		built := build(c.word)
		for _, character := range navCfg.Characters {
			logs := NewSessionLogStore(nil)
			id := "morse-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: "黄色が点滅しています。どうぞ"})
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "それはモールス信号で1つの単語を繰り返している。フォネティックコードだ。手元の資料1で解読してくれ。どうぞ"})
			for _, report := range c.reports {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: report})
				// 本番 (NotePlayerReport) と同じく、表と照合して記録する
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], report))
				progress.NotePlayerMorseKnowledge(report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("[%s] %s\n  P> %s\n  N> %s", c.word, character.Name, report, reply)
				if m := assert.FindString(echoWorry.ReplaceAllString(reply, "")); m != "" {
					t.Errorf("[%s] %s: 正誤を言い切った (%s)", c.word, character.Name, m)
				}
				if m := absentClaim.FindString(reply); m != "" {
					t.Errorf("[%s] %s: 表にある単語を無いと言った (%s)", c.word, character.Name, m)
				}
			}
		}
	}
}

// TestMorseLeadsToCut は、読み違いから正しい単語にたどり着いたプレイヤーを、
// ナビが「同じ行の色の線を切る」まで導くことを確かめる (決定144)。
// 実運用: 202 (正解=INDIA) で「ANDIO」→「どうすればよい?」→「INDIAだったよ?」に、
// 「符号を見直して」「見比べて」を繰り返し、切る指示が出るまで5往復かかった。
func TestMorseLeadsToCut(t *testing.T) {
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
	var built *BuiltSession
	for seed := int64(1); seed < 500 && built == nil; seed++ {
		b, err := simBuildStage(lib, cfg.MissionSheet, "302", seed)
		if err == nil && b.Stages[0].Navigator["secret_word"] == "INDIA" {
			built = b
		}
	}
	if built == nil {
		t.Fatal("INDIA を引く抽選が見つからない")
	}

	// mustCut は、その返答で「切る」まで伝えるべきか
	steps := []struct {
		report  string
		mustCut bool
	}{
		{"点滅しています。ANDIOというモールス信号ですね。どうぞ", false},
		{"確かにそうかも。それでどうすればよい?どうぞ", true},
		{"INDIAだったよ。どうぞ", true},
	}
	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			id := "lead-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ"})
			for i, st := range steps {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: st.report})
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], st.report))
				progress.NotePlayerMorseKnowledge(st.report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("%s #%d-%d\n  P> %s\n  N> %s", character.Name, round+1, i+1, st.report, reply)
				if st.mustCut && !strings.Contains(reply, "切") {
					t.Errorf("%s #%d-%d: 同じ行の色の線を切る、まで伝えていない", character.Name, round+1, i+1)
				}
			}
		}
	}
}

// TestMorseExplainsFlow は、最初の発話がいきなり綴りでも、ナビが課題の全体像
// (モールス → 資料1の表の符号で読む → 単語の行 → その色の線) を伝え、
// 「〜って何?」には流れを説明し直すことを確かめる (決定145)。
// 実運用: 「DCAYというモールス符号が読み取れます」に照合結果だけを返し、
// 「同じ行って何?」「資料って何?」に結論の言い換えを繰り返した。
func TestMorseExplainsFlow(t *testing.T) {
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
	var built *BuiltSession
	for seed := int64(1); seed < 500 && built == nil; seed++ {
		b, err := simBuildStage(lib, cfg.MissionSheet, "302", seed)
		if err == nil && b.Stages[0].Navigator["secret_word"] == "XRAY" {
			built = b
		}
	}
	if built == nil {
		t.Fatal("XRAY を引く抽選が見つからない")
	}
	sheet := cfg.MissionSheet.Documents.Morse

	// プレイヤーは自分からモールスだと言う (勘の良いプレイヤー。決定145)
	// want は返答に含まれるべき語 (いずれかの組ごとに1つ以上)
	steps := []struct {
		report string
		want   [][]string
	}{
		{"DCAYというモールス符号がランプから読み取れます。どうぞ",
			[][]string{{"表", "一覧"}, {"行"}, {"切"}}},
		// 聞かれた言葉が流れのどこに当たるかを示せていればよい
		{"同じ行って何?どうぞ",
			[][]string{{"並", "横", "列"}, {"色"}}},
		{"資料って何?どうぞ",
			[][]string{{sheet, "紙", "手元"}}},
	}
	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			id := "flow-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ"})
			for i, st := range steps {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: st.report})
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], st.report))
				progress.NotePlayerMorseKnowledge(st.report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("%s #%d-%d\n  P> %s\n  N> %s", character.Name, round+1, i+1, st.report, reply)
				for _, group := range st.want {
					if !hasAnyForm(reply, group) {
						t.Errorf("%s #%d-%d: %v のどれも言っていない", character.Name, round+1, i+1, group)
					}
				}
			}
		}
	}
}

// TestMorseTeachesBasics は、文字の区切りを見落とした報告 (MIKE を ZC と読んだ) に、
// ナビが責めずに区切りの見分け方を教え、長短の並びの報告から MIKE へ導くことを
// 確かめる (決定146。実運用: 「ZCなんて表にない言うてるやろ」を繰り返し、
// プレイヤーが「くたばれ、この役立たず」と怒った)。
func TestMorseTeachesBasics(t *testing.T) {
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
	var built *BuiltSession
	for seed := int64(1); seed < 500 && built == nil; seed++ {
		b, err := simBuildStage(lib, cfg.MissionSheet, "302", seed)
		if err == nil && b.Stages[0].Navigator["secret_word"] == "MIKE" {
			built = b
		}
	}
	if built == nil {
		t.Fatal("MIKE を引く抽選が見つからない")
	}

	steps := []struct {
		report string
		want   []string // どれか1つ以上
	}{
		{"モールス信号でゼットシーというものを繰り返すように点灯しています。どうぞ", []string{"区切", "消え", "間", "MIKE", "マイク"}},
		{"いや、ZCなんですが。どうぞ", []string{"区切", "消え", "間", "MIKE", "マイク", "長短"}},
		{"長長、短短、長短長、短。どうぞ", []string{"MIKE", "マイク"}},
	}
	hostile := regexp.MustCompile(`言うてるやろ|言ってるだろ|何度も|いい加減|しつこい`)
	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			id := "basics-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ"})
			for i, st := range steps {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: st.report})
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], st.report))
				progress.NotePlayerMorseKnowledge(st.report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("%s #%d-%d\n  P> %s\n  N> %s", character.Name, round+1, i+1, st.report, reply)
				if !hasAnyForm(reply, st.want) {
					t.Errorf("%s #%d-%d: %v のどれも言っていない", character.Name, round+1, i+1, st.want)
				}
				if m := hostile.FindString(reply); m != "" {
					t.Errorf("%s #%d-%d: 責める言い方 (%s)", character.Name, round+1, i+1, m)
				}
			}
		}
	}
}

// TestMorseRegroupIsConcrete は、区切り違いの読み違いに、ナビが候補の単語と
// どこをつなげるかを具体的に伝えることを確かめる (決定147。実運用: ALFA を
// ETLFET と読んだプレイヤーに「区切りを見落としとらんか?」と一般論を繰り返した)。
func TestMorseRegroupIsConcrete(t *testing.T) {
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
	var built *BuiltSession
	for seed := int64(1); seed < 500 && built == nil; seed++ {
		b, err := simBuildStage(lib, cfg.MissionSheet, "302", seed)
		if err == nil && b.Stages[0].Navigator["secret_word"] == "ALFA" {
			built = b
		}
	}
	if built == nil {
		t.Fatal("ALFA を引く抽選が見つからない")
	}

	reports := []string{
		"ランプがモールス信号で点滅しています。ETLFETというワードが現れています。どうぞ",
		"ETLFETが存在しません。どうぞ",
		"うーん。ETLFETとしか読めませんね。どうぞ",
	}
	hostile := regexp.MustCompile(`言うてるやろ|言ってるだろ|何度も|いい加減|しつこい|通らん`)
	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			id := "regroup-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ"})
			concrete := false
			for i, report := range reports {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: report})
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], report))
				progress.NotePlayerMorseKnowledge(report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("%s #%d-%d\n  P> %s\n  N> %s", character.Name, round+1, i+1, report, reply)
				// 候補の単語 (ALFA) を挙げ、つなげる箇所 (E と T / A) に触れていれば具体的
				if hasAnyForm(reply, []string{"ALFA", "アルファ"}) && hasAnyForm(reply, []string{"E", "T", "A", "つなげ", "1文字"}) {
					concrete = true
				}
				if m := hostile.FindString(reply); m != "" {
					t.Errorf("%s #%d-%d: 責める言い方 (%s)", character.Name, round+1, i+1, m)
				}
			}
			if !concrete {
				t.Errorf("%s #%d: 3往復で、候補の単語とつなげる箇所を具体的に伝えなかった", character.Name, round+1)
			}
		}
	}
}

// TestMorseTeachesConcretely は、読み違いが続くプレイヤーに、ナビが見え方のたとえや
// 数え方の工夫で具体的に教え (秒数は言わない)、同じ返しを繰り返さないことを確かめる
// (決定148。実運用: HOTEL を EEEETEETEE と読んだプレイヤーに「区切りを確かめて、
// もう一度長短を教えて」を繰り返し、「壊れたラジオかよ」と言われた)。
func TestMorseTeachesConcretely(t *testing.T) {
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
	var built *BuiltSession
	for seed := int64(1); seed < 500 && built == nil; seed++ {
		b, err := simBuildStage(lib, cfg.MissionSheet, "302", seed)
		if err == nil && b.Stages[0].Navigator["secret_word"] == "HOTEL" {
			built = b
		}
	}
	if built == nil {
		t.Fatal("HOTEL を引く抽選が見つからない")
	}

	reports := []string{
		"ランプが点滅しています。どうぞ",
		"EEEETEETEEでした。これ表にないのですが。どうぞ",
		"EEEETELですね。どうぞ",
		"EEEETELにしか見えません。どうすればよいですか?どうぞ",
	}
	// 具体的な手がかり: 見え方のたとえ・数え方の工夫・長い光の回数・候補の単語。
	// 秒数は言わない (点滅を見ながら測れない。決定148)
	concrete := []string{"一呼吸", "指", "/", "スラッシュ", "紙", "パッ", "パーッ", "トン", "ツー",
		"しばらく消え", "最初の1文字", "最初の文字", "一緒", "回", "HOTEL", "ホテル", "長い光"}
	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			id := "concrete-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ"})
			prev := ""
			for i, report := range reports {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: report})
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], report))
				progress.NotePlayerMorseKnowledge(report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("%s #%d-%d\n  P> %s\n  N> %s", character.Name, round+1, i+1, report, reply)
				if i >= 1 && !hasAnyForm(reply, concrete) {
					t.Errorf("%s #%d-%d: 見え方・数え方・候補のどれも言っていない (一般論だけ)", character.Name, round+1, i+1)
				}
				if strings.Contains(reply, "秒") {
					t.Errorf("%s #%d-%d: 秒数を言った", character.Name, round+1, i+1)
				}
				if i >= 2 && reply == prev {
					t.Errorf("%s #%d-%d: 前回と同じ返し", character.Name, round+1, i+1)
				}
				prev = reply
			}
		}
	}
}

// TestMorseAsksIfReadable は、最初の説明でモールス信号の読み方が分かるかを尋ね、
// 分からないと答えたら読み方を最初のステップから教えること、すでに綴りを
// 報告したプレイヤーには尋ねないことを確かめる (決定149。ユーザー要望)。
func TestMorseAsksIfReadable(t *testing.T) {
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
	built, err := simBuildStage(lib, cfg.MissionSheet, "202", 42)
	if err != nil {
		t.Fatalf("simBuildStage(202): %v", err)
	}

	asks := regexp.MustCompile(`読み方[^。]{0,12}(分か|わか|知って|大丈夫)`)
	tips := []string{"パッ", "パーッ", "トン", "ツー", "一呼吸", "しばらく消え", "指", "周", "短い光", "長い光", "短く", "長く"}
	type step struct {
		report string
		check  func(reply string) string // 問題があれば説明を返す
	}
	flows := map[string][]step{
		"読めない人": {
			// 実運用 (決定150): 「点滅していますね」にモールスの説明を飛ばした
			{"点滅していますね。どうぞ", func(r string) string {
				if !strings.Contains(r, "モールス") || !strings.Contains(r, "単語") {
					return "点滅がモールス信号で単語を表していると説明していない"
				}
				if !asks.MatchString(r) {
					return "読み方が分かるかを尋ねていない"
				}
				return ""
			}},
			{"モールス符号の読み方は分かりません。どうぞ", func(r string) string {
				if !hasAnyForm(r, tips) {
					return "読み方を教えていない"
				}
				if strings.Contains(r, "秒") {
					return "秒数を言った"
				}
				return ""
			}},
		},
		"読める人": {
			{"DCAYというモールス符号がランプから読み取れます。どうぞ", func(r string) string {
				if asks.MatchString(r) {
					return "綴りを報告した人に読み方が分かるかを尋ねた"
				}
				return ""
			}},
		},
	}
	for name, steps := range flows {
		for _, character := range navCfg.Characters {
			for round := 0; round < 2; round++ {
				logs := NewSessionLogStore(nil)
				id := "ask-" + character.ID
				var progress StageProgress
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
					Message: "表面のランプはどうなってる? どうぞ"})
				for i, st := range steps {
					logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: st.report})
					progress.NoteMorseReport(morseNoteForStage(built.Stages[0], st.report))
					progress.NotePlayerMorseKnowledge(st.report)
					prompt := BuildNavigatorPrompt(NavigatorPromptInput{
						Prompt: &navCfg.Prompt, Character: character, Session: built,
						StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
						MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold, MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons, MorseMentioned: progress.MorseMentioned,
					})
					gen, err := processor.GenerateNavigatorReply(ctx, prompt,
						navCfg.Prompt.TriggerInstruction("player_message"))
					if err != nil {
						t.Errorf("%s: %v", character.ID, err)
						break
					}
					reply := stripTTSTags(gen.Reply)
					progress.NoteNavigatorReply(reply)
					logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
					t.Logf("[%s] %s #%d-%d\n  P> %s\n  N> %s", name, character.Name, round+1, i+1, st.report, reply)
					if problem := st.check(reply); problem != "" {
						t.Errorf("[%s] %s #%d-%d: %s", name, character.Name, round+1, i+1, problem)
					}
				}
			}
		}
	}
}

// TestMorseLetterStage は、202 (英字1文字。決定152) で、ナビが1文字だと説明し、
// 2文字に分けて読んだ報告 (NT = K) にはつなげると1文字になると伝え、
// 読めた文字の行の色の線を切るよう導くことを確かめる。
func TestMorseLetterStage(t *testing.T) {
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
	var built *BuiltSession
	for seed := int64(1); seed < 500 && built == nil; seed++ {
		b, err := simBuildStage(lib, cfg.MissionSheet, "202", seed)
		if err == nil && b.Stages[0].Navigator["secret_word"] == "K" {
			built = b
		}
	}
	if built == nil {
		t.Fatal("K を引く抽選が見つからない")
	}

	steps := []struct {
		report string
		want   [][]string // 組ごとにどれか1つ以上
	}{
		{"点滅しています。どうぞ", [][]string{{"モールス"}, {"1文字", "一文字", "1つの文字"}}},
		{"NTと読めました。どうぞ", [][]string{{"K", "ケー"}, {"つなげ", "1文字", "一文字", "区切"}}},
		{"Kでした。どうぞ", [][]string{{"行"}, {"切"}}},
	}
	for _, character := range navCfg.Characters {
		for round := 0; round < 2; round++ {
			logs := NewSessionLogStore(nil)
			id := "letter-" + character.ID
			var progress StageProgress
			logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer,
				Message: "表面のランプはどうなってる? どうぞ"})
			for i, st := range steps {
				logs.Append(id, ConversationEntry{Sender: senderPlayer, Receiver: character.Name, Message: st.report})
				progress.NoteMorseReport(morseNoteForStage(built.Stages[0], st.report))
				progress.NotePlayerMorseKnowledge(st.report)
				prompt := BuildNavigatorPrompt(NavigatorPromptInput{
					Prompt: &navCfg.Prompt, Character: character, Session: built,
					StageIndex: 0, RemainingMS: 120000, History: logs.Render(id),
					MorseReportNote: progress.LastMorseNote, MorseGoalTold: progress.MorseGoalTold,
					MorseMisses: progress.MorseMisses, MorseLessons: progress.MorseLessons,
					MorseMentioned: progress.MorseMentioned,
				})
				gen, err := processor.GenerateNavigatorReply(ctx, prompt,
					navCfg.Prompt.TriggerInstruction("player_message"))
				if err != nil {
					t.Errorf("%s: %v", character.ID, err)
					break
				}
				reply := stripTTSTags(gen.Reply)
				progress.NoteNavigatorReply(reply)
				logs.Append(id, ConversationEntry{Sender: character.Name, Receiver: senderPlayer, Message: reply})
				t.Logf("%s #%d-%d\n  P> %s\n  N> %s", character.Name, round+1, i+1, st.report, reply)
				for _, group := range st.want {
					if !hasAnyForm(reply, group) {
						t.Errorf("%s #%d-%d: %v のどれも言っていない", character.Name, round+1, i+1, group)
					}
				}
			}
		}
	}
}
