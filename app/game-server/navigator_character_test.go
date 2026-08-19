package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func loadTestNavigator(t *testing.T) *NavigatorConfig {
	t.Helper()
	cfg, err := LoadNavigatorConfig("navigator")
	if err != nil {
		t.Fatalf("LoadNavigatorConfig: %v", err)
	}
	return cfg
}

// TestNavigatorConfigLoads は設定一式が読めることを確かめる。
func TestNavigatorConfigLoads(t *testing.T) {
	cfg := loadTestNavigator(t)

	// docs/navigator_design.md §2 の4キャラクター
	if len(cfg.Characters) != 4 {
		t.Fatalf("キャラクター数 = %d, want 4 (%v)", len(cfg.Characters), cfg.Names())
	}

	wantIDs := map[string]string{
		"owl": "フクロウ", "lark": "ヒバリ", "heron": "アオサギ", "thrush": "ツグミ",
	}
	for id, name := range wantIDs {
		c, ok := cfg.ByID(id)
		if !ok {
			t.Errorf("キャラクター %q が読み込まれていない", id)
			continue
		}
		if c.Name != name {
			t.Errorf("%s の名前 = %q, want %q", id, c.Name, name)
		}
		// 必須項目が埋まっていること
		if strings.TrimSpace(c.Sheet) == "" {
			t.Errorf("%s: sheet が空", id)
		}
		if c.UrgentStyle == "" {
			t.Errorf("%s: urgent_style が空", id)
		}
		if c.TTSVoice == "" || c.TTSStyle == "" {
			t.Errorf("%s: TTS設定が空", id)
		}
	}

	// プロンプト定義
	if !strings.Contains(cfg.Prompt.Role, "ナビゲーター") {
		t.Error("role に役割定義が入っていない")
	}
	if !strings.Contains(cfg.Prompt.Output, "出力ルール") {
		t.Error("output に出力ルールが入っていない")
	}
	if cfg.Prompt.UrgentThresholdMS != 60000 {
		t.Errorf("urgent_threshold_ms = %d, want 60000", cfg.Prompt.UrgentThresholdMS)
	}
}

// TestNavigatorPromptContentPreserved は外部ファイル化の前後で
// プロンプトの内容が変わっていないことを確かめる。
// 口調・ヒント制約の文言が欠けると挙動が変わるため、要点を照合する。
func TestNavigatorPromptContentPreserved(t *testing.T) {
	cfg := loadTestNavigator(t)

	// [A] 共通役割定義に含まれるべき要点 (docs/navigator_design.md §3.4)
	for _, want := range []string{
		"解除手順(正解)をすべて知っています",
		"許可ヒントレベル",
		"復唱して確認",
		"もう一度どうぞ",
		"見捨てず",
		"混線のせいにして受け流して",
	} {
		if !strings.Contains(cfg.Prompt.Role, want) {
			t.Errorf("role に %q が含まれていない", want)
		}
	}

	// [F] 出力ルールに含まれるべき要点。
	// 文数・文字数の上限は無線を塞がないための制約なので、指示が
	// 消えていないことを押さえる (docs/navigator_design.md)。
	for _, want := range []string{
		"2文以内", "60文字以内", "コールサイン", "どうぞ", "口調", "システム用語",
	} {
		if !strings.Contains(cfg.Prompt.Output, want) {
			t.Errorf("output に %q が含まれていない", want)
		}
	}
}

// TestNavigatorTriggerInstructions は発話トリガーの指示が
// 実装が使う全トリガー分そろっていることを確かめる (§3.5)。
func TestNavigatorTriggerInstructions(t *testing.T) {
	cfg := loadTestNavigator(t)

	// GameCoordinator / AudioPipeline が実際に渡すトリガー名
	for _, trigger := range []string{
		"session_start", "stage_cleared", "color_match_completed", "push_progress",
		"wrong_action", "exploded", "defused", "player_message",
		"silence", "time_warning",
	} {
		got := cfg.Prompt.TriggerInstruction(trigger)
		if got == "" {
			t.Errorf("トリガー %q の指示が空", trigger)
		}
		if got == cfg.Prompt.Triggers["fallback"] {
			t.Errorf("トリガー %q が定義されておらず fallback になっている", trigger)
		}
	}

	// 未定義のトリガーは fallback になること
	if got := cfg.Prompt.TriggerInstruction("no_such_trigger"); got != cfg.Prompt.Triggers["fallback"] {
		t.Errorf("未定義トリガーが fallback にならない: %q", got)
	}
}

// TestNavigatorCharacterAssignment は割り当てが候補内から選ばれ、
// IDから復元できることを確かめる。
func TestNavigatorCharacterAssignment(t *testing.T) {
	cfg := loadTestNavigator(t)

	picked := map[string]bool{}
	for seed := int64(0); seed < 200; seed++ {
		c := cfg.Pick(rand.New(rand.NewSource(seed)))
		if _, ok := cfg.ByID(c.ID); !ok {
			t.Fatalf("候補外のキャラクターが選ばれた: %q", c.ID)
		}
		picked[c.ID] = true
	}
	// 十分な試行で全員が選ばれること (偏りがないことの確認)
	if len(picked) != len(cfg.Characters) {
		t.Errorf("選ばれたのは %d 種類のみ (全 %d 種類)", len(picked), len(cfg.Characters))
	}
}

// TestBuildPromptUsesConfig は組み立てたプロンプトに
// 設定ファイルの内容が反映されることを確かめる。
func TestBuildPromptUsesConfig(t *testing.T) {
	cfg := loadTestNavigator(t)
	lib := loadTestLibrary(t)

	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-test", difficultyNormal)
	if err != nil {
		t.Fatal(err)
	}

	character, _ := cfg.ByID("owl")
	prompt := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:      &cfg.Prompt,
		Character:   character,
		Session:     built,
		StageIndex:  0,
		RemainingMS: 120000,
		HintLevel:   HintL1,
	})

	// [A][B][F] が入っていること
	for _, want := range []string{
		"危険物解体ゲーム",        // role
		"フクロウ",            // キャラシート
		"出力ルール",           // output
		"許可ヒントレベル",        // ヒントポリシー
		"正解(あなただけが知っている)", // セッション状態
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("プロンプトに %q が含まれていない", want)
		}
	}

	// 通常時は緊迫スタイルにならないこと
	if strings.Contains(prompt, "交信スタイル: 緊迫") {
		t.Error("残り120秒で緊迫スタイルになっている")
	}

	// 残り時間が閾値を切ると緊迫スタイルになること
	urgent := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:      &cfg.Prompt,
		Character:   character,
		Session:     built,
		StageIndex:  0,
		RemainingMS: 30000,
		HintLevel:   HintL1,
	})
	if !strings.Contains(urgent, "交信スタイル: 緊迫") {
		t.Error("残り30秒で緊迫スタイルになっていない")
	}
	if !strings.Contains(urgent, character.UrgentStyle) {
		t.Error("緊迫時の崩し方が含まれていない")
	}
}

// TestNavigatorMaxRunesMatchesPrompt は発話長の上限が
// プロンプトの指示とサーバー側の警告閾値で一致していることを確かめる。
//
// 片方だけ変えると、プロンプトは守られているのに WARN が出続ける
// (またはその逆) という分かりにくい状態になる。
func TestNavigatorMaxRunesMatchesPrompt(t *testing.T) {
	cfg := loadTestNavigator(t)

	want := fmt.Sprintf("%d文字以内", navigatorMaxRunes)
	if !strings.Contains(cfg.Prompt.Output, want) {
		t.Errorf("output に %q が含まれていない (navigatorMaxRunes=%d と揃えること)",
			want, navigatorMaxRunes)
	}
}

// TestPromptWarnsAgainstLeakingAnswer は、L4 未満では正解行に
// 「口に出すな」の警告が併記されることを確かめる。
//
// 実運用で L3 のときにナビゲーターが正解の色名を直言した
// (docs/navigator_design.md §5 決定19)。正解をプロンプトに渡す以上、
// **禁止指示を正解と同じ場所に置かない限り引きずられる**。
func TestPromptWarnsAgainstLeakingAnswer(t *testing.T) {
	cfg := loadTestNavigator(t)
	lib := loadTestLibrary(t)

	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-test", difficultyEasy)
	if err != nil {
		t.Fatal(err)
	}
	character, _ := cfg.ByID("owl")

	build := func(level int) string {
		return BuildNavigatorPrompt(NavigatorPromptInput{
			Prompt:      &cfg.Prompt,
			Character:   character,
			Session:     built,
			StageIndex:  0,
			RemainingMS: 120000,
			HintLevel:   level,
		})
	}

	// 文面ではなく**意図**で照合する。L4 未満は色名を伏せたうえで
	// 「直言するな」と警告する (決定40)。
	const warning = "正解の色名は伏せてあります"

	for _, level := range []int{HintL1, HintL2, HintL3} {
		p := build(level)
		if !strings.Contains(p, warning) {
			t.Errorf("L%d に警告がない", level)
		}
		// 現在のレベルが明示されること
		if !strings.Contains(p, fmt.Sprintf("現在は L%d", level)) {
			t.Errorf("L%d の表示がない", level)
		}
	}

	// L4 は直言してよい段階なので警告を出さない
	if p := build(HintL4); strings.Contains(p, warning) {
		t.Error("L4 に不要な警告が入っている (直言してよい段階)")
	}
}

// TestTutorialStageNeverNamesCutColor は 101 の進め方が
// **色名を言わずに「光っているランプと同じ色」で指示する**形に
// なっていることを確かめる。
//
// 101 は正解の色だけが点灯するため、この言い方で一意に決まる。
// ナビゲーターが色名を言うとプレイヤーは装置を見ずに従うだけになり、
// 「装置を見て操作する」というチュートリアルの目的が果たせない。
//
// かつては「何色が光っているか尋ね、報告を復唱してから切らせる」形だったが、
// 最後に1往復増えるだけで冗長だった (実プレイで確認)。ゴールを最初に伝え、
// ダイヤルが合ったらそのまま切らせる形へ変更した。
func TestTutorialStageNeverNamesCutColor(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))

	built, err := builder.Build("s-test", difficultyEasy)
	if err != nil {
		t.Fatal(err)
	}
	stage := built.Stages[0]
	if stage.TemplateID != "101" {
		t.Fatalf("先頭ステージ = %s, want 101", stage.TemplateID)
	}

	procedure := stage.Navigator["procedure"]
	// 「光っているランプと同じ色」という指示の形になっていること
	if !strings.Contains(procedure, "光っているランプと同じ色") {
		t.Errorf("procedure が『光っているランプと同じ色』の形で指示していない: %s", procedure)
	}
	// 色名をこちらから言わない方針が明記されていること
	if !strings.Contains(procedure, "色名はこちらから言わない") {
		t.Errorf("procedure に色名禁止の指示がない: %s", procedure)
	}

	// 正解の色名が procedure に**展開されていない**こと。
	// ここに色名が入ると、ナビゲーターがそれを読み上げてしまう。
	if name, ok := colorNameJA[stage.Cut]; ok && strings.Contains(procedure, name) {
		t.Errorf("procedure に正解の色名 %q が展開されている: %s", name, procedure)
	}

	// hint_l3 は色名を言わない方針であること
	hintL3 := stage.Navigator["hint_l3"]
	if !strings.Contains(hintL3, "色名を自分から言ってはいけない") {
		t.Errorf("hint_l3 に色名禁止の指示がない: %s", hintL3)
	}
}

// TestTutorialGivesConcreteDialPositions は 101 の指示が
// **ダイヤルの位置を数字で伝える**形になっていることを確かめる。
//
// 発話は「2文・60文字以内」に制限されているため、ゴールの説明
// (「光っているランプと同じ色の線を切る」) を前に置くと、生成AIが
// **具体的な数字を押し出して**「まずダイヤルを回し、光っているランプと
// 同じ色の線を切れ」のような、どこへ合わせるか分からない発話になる
// (実プレイで発生)。数字の指示が最優先であることを指針に明記しておく。
func TestTutorialGivesConcreteDialPositions(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))

	built, err := builder.Build("s-test", difficultyEasy)
	if err != nil {
		t.Fatal(err)
	}
	stage := built.Stages[0]
	if stage.TemplateID != "101" {
		t.Fatalf("先頭ステージ = %s, want 101", stage.TemplateID)
	}

	// hint_l1 が「数字で指示する」ことを求めていること。
	//
	// **語尾は問わない**。ステージ知識は4キャラ共通で使われるため、
	// 命令形の例文を書くと命令形を使わないキャラクター (ツグミ) が
	// それを写して口調が崩れる (決定31)。ここで確かめたいのは
	// 「具体的な位置を数字で言わせているか」であって命令形かどうかではない。
	hintL1 := stage.Navigator["hint_l1"]
	for _, want := range []string{"数字", "ダイヤルを0"} {
		if !strings.Contains(hintL1, want) {
			t.Errorf("hint_l1 に %q がない — 位置を数字で言う指示が抜けている: %s", want, hintL1)
		}
	}

	// procedure に3つの位置が全て展開されていること
	procedure := stage.Navigator["procedure"]
	if !strings.Contains(procedure, "数字で言うこと") {
		t.Errorf("procedure に数字指示の強調がない: %s", procedure)
	}
	if strings.Contains(procedure, "${") {
		t.Errorf("procedure に未解決の変数が残っている: %s", procedure)
	}
}

// TestPromptWithoutStageForbidsOperations は**ステージ知識が無いとき**の
// プロンプトが「装置の操作を促すな」と明示することを確かめる。
//
// 全ステージ完了後など、参照するステージが無い状態で発話させると、
// 生成AIは「次の課題へ進む」等の指示だけを頼りに**存在しない課題を捏造する**。
// 実運用では解除成功の直後に「もう一本、赤の線を切ってください」と、
// 既に解除済みの装置へ指示を出した。
func TestPromptWithoutStageForbidsOperations(t *testing.T) {
	lib := loadTestLibrary(t)
	builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(1)))
	built, err := builder.Build("s-1", difficultyEasy)
	if err != nil {
		t.Fatal(err)
	}

	// 全ステージ完了後 (StageIndex が末尾を越えている)
	prompt := BuildNavigatorPrompt(NavigatorPromptInput{
		Prompt:      &NavigatorPromptConfig{},
		Session:     built,
		StageIndex:  len(built.Stages),
		RemainingMS: 54700,
		HintLevel:   HintL1,
		RecentEvent: "解除に成功した!祝福する。",
	})

	if !strings.Contains(prompt, "今は指示する課題がありません") {
		t.Errorf("課題が無い旨の明示がない:\n%s", prompt)
	}
	if !strings.Contains(prompt, "促してはいけません") {
		t.Errorf("操作を促さない指示がない:\n%s", prompt)
	}
	// 「3 / 2 番目の課題」のような、存在しない課題を示唆する表示をしない
	if strings.Contains(prompt, "3 / 2 番目") {
		t.Errorf("範囲外の進行表示が残っている:\n%s", prompt)
	}
	if !strings.Contains(prompt, "全2課題を完了") {
		t.Errorf("完了の表示になっていない:\n%s", prompt)
	}
}

// TestPromptRedactsCutColorBelowL4 は、L4 未満のプロンプトに
// **正解の色名そのものが入らない**ことを確かめる (決定40)。
//
// 「書いてあるが言うな」は守られないことがある — 目の前にある語はなぞられる
// (決定19・27)。実測でも 616発話中1件残っていた。無い語は言いようがないので、
// L4 未満では色名を伏せ字に置き換える。
func TestPromptRedactsCutColorBelowL4(t *testing.T) {
	cfg := loadTestNavigator(t)
	lib := loadTestLibrary(t)

	// 抽選を変えて複数の正解色で確かめる
	for seed := int64(0); seed < 20; seed++ {
		builder := NewScenarioBuilder(lib, testMissionSheet(), rand.New(rand.NewSource(seed)))
		built, err := builder.Build("s-test", difficultyEasy)
		if err != nil {
			t.Fatal(err)
		}
		stage := built.Stages[0]
		cutJA := colorNameJA[stage.Cut]
		character, _ := cfg.ByID("owl")

		build := func(level int) string {
			return BuildNavigatorPrompt(NavigatorPromptInput{
				Prompt: &cfg.Prompt, Character: character, Session: built,
				StageIndex: 0, RemainingMS: 120000, HintLevel: level,
			})
		}

		// L4 未満: 正解行に色名が出ないこと
		for _, level := range []int{HintL1, HintL2, HintL3} {
			answerLine := extractAnswerLine(build(level))
			if answerLine == "" {
				t.Fatalf("seed=%d L%d: 正解行が見つからない", seed, level)
			}
			if strings.Contains(answerLine, cutJA) {
				t.Errorf("seed=%d L%d: 正解行に色名 %q が残っている:\n  %s",
					seed, level, cutJA, answerLine)
			}
			if !strings.Contains(answerLine, redactedColorMark) {
				t.Errorf("seed=%d L%d: 伏せ字が入っていない:\n  %s", seed, level, answerLine)
			}
		}

		// L4 は直言してよい段階なので、色名がそのまま入ること
		if line := extractAnswerLine(build(HintL4)); !strings.Contains(line, cutJA) {
			t.Errorf("seed=%d L4: 正解行に色名 %q が無い:\n  %s", seed, cutJA, line)
		}
	}
}

// extractAnswerLine はプロンプトから「正解(...)」の行を取り出す。
func extractAnswerLine(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "- 正解(") {
			return line
		}
	}
	return ""
}

// TestRedactCutColorKeepsOtherColors は、伏せるのが**切る線の色だけ**で、
// 押すボタンなど伝えてよい色は残ることを確かめる。
//
// すべての色を伏せると手順が成立しなくなる (102 のボタン列など)。
func TestRedactCutColorKeepsOtherColors(t *testing.T) {
	answer := "正解はボタンを緑→青の順に押してから、赤色の線を切ること。"
	got := redactCutColor(answer, "A") // A = 赤

	if strings.Contains(got, "赤") {
		t.Errorf("切る線の色が残っている: %s", got)
	}
	for _, keep := range []string{"緑", "青"} {
		if !strings.Contains(got, keep) {
			t.Errorf("伝えてよい色 %q まで消えている: %s", keep, got)
		}
	}
	// 「赤色」→「◯◯色」であって「◯◯色色」にならないこと
	if strings.Contains(got, "色色") {
		t.Errorf("置換が二重になっている: %s", got)
	}
}

// TestReassuranceStylesAreDistinct は、第一声の「安心のさせ方」が
// キャラクターごとに**別の型**になっていることを確かめる (決定43)。
//
// 当初はプロンプトに「『自分がついているから大丈夫だ』という趣旨の一言」と
// **型まで指定**していたため、4人とも「私(自分)がついている/慣れている →
// だから大丈夫」という同じ言い方になり、語尾しか違わなかった。
// 型は決定28 (何を見ているか) に沿ってキャラごとに変える。
func TestReassuranceStylesAreDistinct(t *testing.T) {
	cfg := loadTestNavigator(t)

	// 各キャラシートに【安心のさせ方】があること
	for _, c := range cfg.Characters {
		if !strings.Contains(c.Sheet, "【安心のさせ方】") {
			t.Errorf("%s: 【安心のさせ方】が無い — 型が指定されないと"+
				"他キャラと同じ言い方に寄る", c.Name)
		}
	}

	// **「私がついている」型はヒバリだけ**。他キャラの開始例に出てはいけない。
	for _, c := range cfg.Characters {
		if c.ID == "lark" {
			continue
		}
		opening := extractOpeningExample(c.Sheet)
		if opening == "" {
			t.Errorf("%s: 開始の台詞例が無い", c.Name)
			continue
		}
		for _, ng := range []string{"ついてます", "ついている", "付いて"} {
			if strings.Contains(opening, ng) {
				t.Errorf("%s: 開始例に %q が入っている — これはヒバリの型:\n  %s",
					c.Name, ng, opening)
			}
		}
	}

	// プロンプト側が特定の型を指定していないこと
	inst := cfg.Prompt.TriggerInstruction("session_start")
	if strings.Contains(inst, "自分がついているから") {
		t.Error("session_start が安心のさせ方の型を指定している — " +
			"全キャラが同じ言い方になる")
	}
}

// extractOpeningExample はキャラシートから「開始:」の台詞例を取り出す。
func extractOpeningExample(sheet string) string {
	for _, line := range strings.Split(sheet, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- 開始:") {
			return line
		}
	}
	return ""
}

// TestClosingMessagesHaveNoDouzo は、交信を終える場面 (解除成功・失敗) で
// **「どうぞ」を付けない**指示があることを確かめる (決定46)。
//
// 「どうぞ」は相手に応答を促す無線の作法。もう返事を求めない場面で付くと
// 不自然で、締めくくりにならない。
func TestClosingMessagesHaveNoDouzo(t *testing.T) {
	cfg := loadTestNavigator(t)

	// 出力ルールに例外が書かれていること
	if !strings.Contains(cfg.Prompt.Output, "交信が終わる場面") {
		t.Error("output に「どうぞ」の例外が書かれていない — " +
			"全発話に付けるルールだけだと締めくくりにも付く")
	}

	// 終了トリガーが明示的に禁じていること
	for _, trigger := range []string{"defused", "exploded"} {
		inst := cfg.Prompt.TriggerInstruction(trigger)
		if !strings.Contains(inst, "「どうぞ」は付けない") {
			t.Errorf("%s に「どうぞ」を付けない指示が無い", trigger)
		}
	}
}

// TestAllCharactersHaveClosingLines は、全キャラクターが
// **解除成功の台詞例**を持っていることを確かめる (決定46)。
//
// 以前はヒバリだけが持っており、しかも黒幕セクションの中にあった。
// 他3人は「クリア」(ステージ突破) しかなく、ゲーム最後の見せ場が
// 用意されていなかった。例文が無いと生成AIは短い汎用の祝福を返す (決定14)。
func TestAllCharactersHaveClosingLines(t *testing.T) {
	cfg := loadTestNavigator(t)

	for _, c := range cfg.Characters {
		if !strings.Contains(c.Sheet, "**解除成功**") {
			t.Errorf("%s: 解除成功の台詞例が無い — "+
				"ゲーム最後の見せ場が汎用文になる", c.Name)
		}
		// 締めくくりに「どうぞ」を含む例を置かないこと
		for _, line := range strings.Split(c.Sheet, "\n") {
			if strings.Contains(line, "解除成功") && strings.Contains(line, "どうぞ") &&
				!strings.Contains(line, "付けない") {
				t.Errorf("%s: 解除成功の例に「どうぞ」が入っている:\n  %s", c.Name, line)
			}
		}
	}
}

// TestExplodedIsTreatedAsDeath は、爆発が**プレイヤーの死**として
// 演じられる指示になっていることを確かめる (決定47)。
//
// 決定46 の時点では「次は間に合います」「お疲れさまでした」と
// **生存を前提にした講評**になっていた。爆発は解体失敗であり、
// 無線の向こうで返事が返ってこなくなった場面として演じる。
func TestExplodedIsTreatedAsDeath(t *testing.T) {
	cfg := loadTestNavigator(t)
	inst := cfg.Prompt.TriggerInstruction("exploded")

	// 安否が分からない場面だと伝えていること
	for _, want := range []string{"安否", "応答"} {
		if !strings.Contains(inst, want) {
			t.Errorf("exploded に %q が無い — 死を伝える場面にならない", want)
		}
	}

	// **第一声も締め方も統一しないこと** (決定47b)。
	// 「現地へ向かう」を全員に求めると、新人で手配できないヒバリや
	// 温度を上げないツグミがキャラクターに反する対応をする。
	//
	// **呼びかけも同様**。全員に安否確認をさせると、手順へ切り替える
	// アオサギや、返事を待たず本音が漏れるヒバリの形が潰れる。
	// キャラシートへ委ねる指示になっていることを確かめる。
	if !strings.Contains(inst, "キャラシートの「爆発」の例に従ってください") {
		t.Error("exploded がキャラシートへ委ねていない — " +
			"全キャラが同じ対応をしてキャラクターが潰れる")
	}
	if !strings.Contains(inst, "全員が呼びかけるわけではありません") {
		t.Error("exploded が呼びかけを全員に強いている — " +
			"アオサギ・ヒバリの形が潰れる")
	}

	// 生存前提の言葉を禁じていること
	for _, want := range []string{"次がある前提", "無事に終わった前提"} {
		if !strings.Contains(inst, want) {
			t.Errorf("exploded に %q の禁止が無い", want)
		}
	}

	// 全キャラクターが爆発時の台詞例を持つこと (例文が出力を決める)
	for _, c := range cfg.Characters {
		if !strings.Contains(c.Sheet, "**爆発**") {
			t.Errorf("%s: 爆発時の台詞例が無い — 汎用の失敗メッセージになる", c.Name)
		}
	}
}
