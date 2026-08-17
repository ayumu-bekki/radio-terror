package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// ステージ横断シミュレーション (文字のみ。TTS・音声は通さない)。
//
// 有効な全ステージについて、実際のプロンプト組み立て (BuildNavigatorPrompt) と
// 実際の思考モデル (GenerateNavigatorReply) を使い、L1→L4 の各ヒントレベルで
// 台本どおりのプレイヤー発話を返しながら数ターン交信する。
//
// 目的は「意図どおりに動くか」の確認なので、失敗させずに**所見を集計する**。
// 判定は次の4点:
//
//  1. 正解色の漏れ … L4 未満で cut の日本語色名を口に出していないか
//  2. 出力長        … 目安 (60字。第一声は80字) に対する分布を集計する。
//     目安超えは所見にせず、大きく超えた場合だけ挙げる
//  3. 表情タグ      … allowedTTSTags 以外のタグを使っていないか
//  4. 必須情報      … 装置に現れない情報 (ボタン列・危険位置) を L1 で伝えているか
//
// 実行:
//
//	go test -run TestSimulateAllStages -simulate -timeout 60m -v
//	go test -run TestSimulateAllStages -simulate -sim-stages 101,205 -v
var (
	runStageSim = flag.Bool("simulate", false,
		"実APIを呼ぶステージ横断シミュレーションを実行する")
	simStages = flag.String("sim-stages", "",
		"対象ステージIDをカンマ区切りで指定 (既定: 有効な全ステージ)")
	simCharacter = flag.String("sim-character", "owl",
		"シミュレーションで使うキャラクターID")
	simReport = flag.String("sim-report", "",
		"結果を Markdown で書き出すパス (空なら書き出さない)")
)

// simTurn は台本の1ターン。プレイヤーの発話とサーバー側のトリガーを与える。
type simTurn struct {
	// Trigger は発話トリガー名 (prompt.toml の [triggers] のキー)
	Trigger string
	// Event はトリガーに添える出来事の説明 (game_coordinator.go と同じ文面)
	Event string
	// Player はこのターンでプレイヤーが無線に流した発話。空なら発話なし
	Player string
	// HintLevel はこのターンの許可ヒントレベル
	HintLevel int
}

// simScript はステージごとのプレイヤー台本。
//
// 装置の見え方 (点灯・点滅・モールス) は抽選値で変わるため、台本は
// **抽選値を埋め込めるテンプレート**として書き、実行時に展開する。
type simScript struct {
	// StageID は対象ステージ
	StageID string
	// Turns は台本のターン列
	Turns []simTurn
	// MustMention は L1 の発話に必ず含まれるべき語 (装置に現れない情報)。
	// ${var} で抽選変数を参照できる。
	MustMention []string
}

// simFinding は1件の所見。
type simFinding struct {
	StageID string
	Level   int
	Kind    string
	Detail  string
	Reply   string
}

// simStageResult は1ステージぶんの結果。
type simStageResult struct {
	StageID   string
	StageName string
	Cut       string
	CutJA     string
	Vars      map[string]string
	Turns     []simTurnResult
	Findings  []simFinding
}

type simTurnResult struct {
	Level   int
	Trigger string
	Player  string
	Reply   string
	Runes   int
}

func TestSimulateAllStages(t *testing.T) {
	if !*runStageSim {
		t.Skip("実APIを呼ぶため既定では飛ばす (-simulate で実行)")
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

	character, ok := navCfg.ByID(*simCharacter)
	if !ok {
		t.Fatalf("unknown character id: %s", *simCharacter)
	}

	targets := simTargetStages(t, lib)
	t.Logf("=== シミュレーション対象 %d ステージ (キャラクター: %s) ===",
		len(targets), character.Name)

	results := make([]*simStageResult, 0, len(targets))
	for i, id := range targets {
		result := simulateStage(ctx, t, processor, navCfg, lib, cfg.MissionSheet,
			character, id, i == 0)
		results = append(results, result)
	}

	simSummarize(t, results)
	if *simReport != "" {
		if err := os.WriteFile(*simReport, []byte(simRenderReport(results, character)), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("レポートを書き出しました: %s", *simReport)
	}
}

// simTargetStages は対象ステージIDを決める。
// -sim-stages 指定が無ければ、読み込まれた (= .disabled でない) 全ステージ。
func simTargetStages(t *testing.T, lib *ScenarioLibrary) []string {
	t.Helper()

	if *simStages != "" {
		ids := make([]string, 0)
		for _, id := range strings.Split(*simStages, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, err := lib.Stage(id); err != nil {
				t.Fatalf("Stage(%q): %v", id, err)
			}
			ids = append(ids, id)
		}
		return ids
	}

	ids := make([]string, 0, len(lib.stages))
	for id := range lib.stages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// simBuildStage は指定ステージだけを含むセッションを組み立てる。
//
// 難易度テンプレートを合成して fixed_head に対象ステージだけを置く。
// こうすると通常の Build 経路 (抽選・展開・検証) をそのまま通せるため、
// **本番と同じ組み立て結果**をシミュレーションできる。
func simBuildStage(lib *ScenarioLibrary, sheet MissionSheet, id string, seed int64) (*BuiltSession, error) {
	const simDifficulty = "__sim__"

	base, err := lib.Difficulty(difficultyNormal)
	if err != nil {
		return nil, err
	}
	tmpl := *base
	tmpl.Compose = ComposeRule{FixedHead: []string{id}}
	lib.difficulties[simDifficulty] = &tmpl
	defer delete(lib.difficulties, simDifficulty)

	builder := NewScenarioBuilder(lib, sheet, rand.New(rand.NewSource(seed)))
	return builder.Build("s-sim-"+id, simDifficulty)
}

func simulateStage(
	ctx context.Context,
	t *testing.T,
	processor *GeminiProcessor,
	navCfg *NavigatorConfig,
	lib *ScenarioLibrary,
	sheet MissionSheet,
	character NavigatorCharacter,
	id string,
	first bool,
) *simStageResult {
	t.Helper()

	built, err := simBuildStage(lib, sheet, id, 42)
	if err != nil {
		t.Errorf("[%s] Build: %v", id, err)
		return &simStageResult{StageID: id, Findings: []simFinding{
			{StageID: id, Kind: "build_error", Detail: err.Error()},
		}}
	}
	stage := built.Stages[0]

	// 台本の ${var} を埋めるための抽選値。
	// buildStage と同じ変換 (色コード → 日本語色名) を通す。
	vars := simStageVars(lib, stage)

	result := &simStageResult{
		StageID:   id,
		StageName: stage.Name,
		Cut:       stage.Cut,
		CutJA:     colorNameJA[stage.Cut],
		Vars:      vars,
	}

	t.Logf("")
	t.Logf("########## %s %s (正解=%s)", id, stage.Name, result.CutJA)

	script := simScriptFor(id)
	// 会話ログはターンをまたいで積む (本番の SessionLogStore と同じ役割)
	logs := NewSessionLogStore(nil)
	sessionID := "s-sim-" + id

	// プレイヤーが正解色を口にしたか。以降その色の復唱は漏洩と見なさない。
	playerSaidCut := false
	// L4 で正解色を明かしたか。以降の言及は完了報告なので漏洩と見なさない。
	revealedAtL4 := false
	cutJA := colorNameJA[stage.Cut]

	for _, turn := range script.Turns {
		// session_ready は**セッション開始時に1回だけ**。実機では
		// StartSession から1度呼ばれるきりで、ステージごとには鳴らない
		// (game_coordinator.go の announceReady)。
		//
		// シミュレーターは1ステージ=1セッションとして独立に回すため、
		// そのままだと全ステージの先頭に待機完了が並び、
		// 「毎ステージ名乗り直している」ように見える。
		// 先頭ステージ以外では飛ばして、実機の見え方に合わせる。
		if turn.Trigger == "session_ready" && !first {
			continue
		}

		player := expandSimText(turn.Player, vars)
		if cutJA != "" && strings.Contains(player, cutJA) {
			playerSaidCut = true
		}
		if player != "" {
			logs.Append(sessionID, ConversationEntry{
				Sender: senderPlayer, Receiver: character.Name, Message: player,
			})
		}

		prompt := BuildNavigatorPrompt(NavigatorPromptInput{
			Prompt:      &navCfg.Prompt,
			Character:   character,
			Session:     built,
			StageIndex:  0,
			RemainingMS: 120000,
			HintLevel:   turn.HintLevel,
			RecentEvent: expandSimText(turn.Event, vars),
			History:     logs.Render(sessionID),
		})

		instruction := navCfg.Prompt.TriggerInstruction(turn.Trigger)
		reply, err := processor.GenerateNavigatorReply(ctx, prompt, instruction)
		if err != nil {
			result.Findings = append(result.Findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "api_error", Detail: err.Error(),
			})
			t.Logf("  L%d %-15s ERROR: %v", turn.HintLevel, turn.Trigger, err)
			continue
		}
		logs.Append(sessionID, ConversationEntry{
			Sender: character.Name, Receiver: senderPlayer, Message: stripTTSTags(reply),
		})

		tr := simTurnResult{
			Level: turn.HintLevel, Trigger: turn.Trigger,
			Player: player, Reply: reply, Runes: countRunes(stripTTSTags(reply)),
		}
		result.Turns = append(result.Turns, tr)

		if player != "" {
			t.Logf("  L%d %-15s P> %s", turn.HintLevel, turn.Trigger, player)
			t.Logf("     %-15s N> %s (%d字)", "", reply, tr.Runes)
		} else {
			t.Logf("  L%d %-15s N> %s (%d字)", turn.HintLevel, turn.Trigger, reply, tr.Runes)
		}

		result.Findings = append(result.Findings,
			simCheckTurn(id, stage, turn, reply, script, vars, playerSaidCut, revealedAtL4)...)

		// L4 で色名を出したら、以降の言及は完了報告として扱う
		if turn.HintLevel >= HintL4 && cutJA != "" && strings.Contains(stripTTSTags(reply), cutJA) {
			revealedAtL4 = true
		}
	}

	return result
}

// simStageVars は台本展開用の変数表を作る。
//
// BuiltStage は展開後の文字列しか持たないため、抽選値そのものは
// テンプレートを引き直して再現する。台本は少数の変数しか参照しないので、
// ナビゲーター知識の文面から拾える範囲で十分。
func simStageVars(lib *ScenarioLibrary, stage *BuiltStage) map[string]string {
	vars := map[string]string{
		"cut":   colorNameJA[stage.Cut],
		"cutJA": colorNameJA[stage.Cut],
	}

	// 誤報告の台本用に、正解ではない色を1つ用意する (208 で使う)。
	for _, code := range allColors {
		if code != stage.Cut {
			vars["sim_wrong_color"] = colorNameJA[code]
			break
		}
	}

	// モールス系ステージ (203/308) はプレイヤーが読み上げる語を台本で使う。
	// 展開済みの answer から語を拾う (テンプレートは word/color_word を
	// answer の中でそのまま展開しているため)。
	vars["navi_word_guess"] = simMorseWordFrom(stage)

	// 209 の危険位置。展開済み answer の「ダイヤルN は危険位置」から拾う。
	if m := simForbiddenPattern.FindStringSubmatch(stage.Navigator["answer"]); m != nil {
		vars["sim_forbidden"] = m[1]
	}

	// 押下列の1色目 (102/201/305)。第一声で列が伝わっているかの照合に使う。
	// Core の push_seq から直接取るので、文面の書き方に依存しない。
	if first := simFirstPushColor(stage); first != "" {
		vars["sim_p1"] = colorNameJA[first]
	}

	// answer / procedure / hint_* の展開済み文面を台本から参照できるようにする。
	for key, text := range stage.Navigator {
		vars["navi_"+key] = text
	}
	return vars
}

// simMorseWordPattern は answer に現れる大文字ローマ字の語 (ALFA / MIDORI 等)。
var simMorseWordPattern = regexp.MustCompile(`[A-Z]{2,}`)

// simForbiddenPattern は 209 の answer に現れる危険位置 (「ダイヤル2は危険位置」)。
var simForbiddenPattern = regexp.MustCompile(`ダイヤル(\d)は危険位置`)

// simFirstPushColor は push_seq の1個目の色コードを返す。押下列が無ければ空。
func simFirstPushColor(stage *BuiltStage) string {
	pre, _ := stage.Core["precondition"].(map[string]any)
	if pre == nil {
		return ""
	}
	seq, _ := pre["push_seq"].(map[string]any)
	if seq == nil {
		return ""
	}
	entries, _ := seq["entries"].([]any)
	if len(entries) == 0 {
		return ""
	}
	first, _ := entries[0].(map[string]any)
	if first == nil {
		return ""
	}
	color, _ := first["push"].(string)
	return color
}

// simMorseWordFrom はモールス表示の語を展開済み answer から拾う。
// モールスを使わないステージでは空文字を返す。
func simMorseWordFrom(stage *BuiltStage) string {
	if m := simMorseWordPattern.FindString(stage.Navigator["answer"]); m != "" {
		return m
	}
	return ""
}

var simVarPattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_]+)\}`)

func expandSimText(text string, vars map[string]string) string {
	if text == "" {
		return ""
	}
	return simVarPattern.ReplaceAllStringFunc(text, func(m string) string {
		name := simVarPattern.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

// simTagPattern は発話中の角括弧タグ。
var simTagPattern = regexp.MustCompile(`\[([a-zA-Z_]+)\]`)

// simColorToldByDesign は「切る線の色を伝えるのが仕様」のステージ。
// 装置から色を読み取れないため、伏せるとプレイヤーが手詰まりになる。
//
// 現在は該当なし。106 いくつ光ってる? と 303 暗転がこれに当たったが、
// **色を教えるだけの工程になる**として無効化した (.toml.disabled)。
// 再開する場合はここへ戻す — 登録しないと、仕様どおりに色を伝えた発話が
// 「色漏れ」として検出される。
var simColorToldByDesign = map[string]bool{}

// simCheckTurn は1発話を4つの観点で検査する。
//
// playerSaidCut は、プレイヤーがこのステージで既に正解色を口にしているか。
// 正解色を**先に言ったのがプレイヤー**なら、ナビゲーターの復唱は漏洩ではない
// (208 速さくらべは「報告を照合して復唱する」のが正規の手順)。
func simCheckTurn(
	id string, stage *BuiltStage, turn simTurn, reply string,
	script simScript, vars map[string]string, playerSaidCut, revealedAtL4 bool,
) []simFinding {
	findings := make([]simFinding, 0)
	body := stripTTSTags(reply)

	// 1. 正解色の漏れ (L4 未満)
	//
	// 205 ブループリントは L4 でも色名を言ってはいけないため、L4 も検査する。
	// 逆に切る線の色が装置に現れないステージは検査しない
	// (simColorToldByDesign。現在は該当なし)。
	cutJA := colorNameJA[stage.Cut]
	checkLeak := turn.HintLevel < HintL4 || id == "205"
	if simColorToldByDesign[id] {
		checkLeak = false
	}
	// プレイヤーが先に言った色の復唱は漏洩ではない (205 だけは例外で、
	// 復唱すること自体が禁止されている)。
	if playerSaidCut && id != "205" {
		checkLeak = false
	}
	// **L4 で正当に明かしたあとは漏洩ではない。**
	// 課題突破後の stage_cleared は L1 に戻るが、直前の L4 で
	// 「赤色の線を切ってください」と伝えた以上、
	// 「赤色の線を切断しましたね」は完了報告であって漏洩ではない。
	if revealedAtL4 && id != "205" {
		checkLeak = false
	}
	if checkLeak && cutJA != "" && strings.Contains(body, cutJA) {
		findings = append(findings, simFinding{
			StageID: id, Level: turn.HintLevel, Kind: "answer_leak",
			Detail: fmt.Sprintf("正解色 %q を直言", cutJA), Reply: reply,
		})
	}

	// 2. 出力長
	//
	// **60字は目安であって失格条件ではない** (navigatorMaxRunes のコメント参照)。
	// 口調によって 30〜40 字と幅があり、敬語・高テンションのキャラは
	// 要素が増えると超える。超過ゼロを目指して指示を締めると、
	// 安心させる一言のような「後から足したもの」が削られる (決定37)。
	//
	// そこで**目安超えは所見にせず、集計だけ**にする。
	// 明らかに無線を塞ぐ長さ (excessiveRunes) だけを所見として挙げる。
	//
	// **プレイヤーへの第一声だけ目安が違う** (決定37)。名乗り + 安心させる
	// 一言 + 質問の3つを入れるため 80 字を目安にしている。
	guide := navigatorMaxRunes
	if turn.Trigger == "session_start" {
		guide = openingMaxRunes
	}
	if n := countRunes(body); n > excessiveRunes {
		findings = append(findings, simFinding{
			StageID: id, Level: turn.HintLevel, Kind: "excessive_length",
			Detail: fmt.Sprintf("%d字 (目安 %d を大きく超過)", n, guide), Reply: reply,
		})
	}

	// 3. 表情タグ
	for _, m := range simTagPattern.FindAllStringSubmatch(reply, -1) {
		if !allowedTTSTags[m[1]] {
			findings = append(findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "bad_tag",
				Detail: fmt.Sprintf("許可外のタグ %q", m[0]), Reply: reply,
			})
		}
	}

	// 4. 必須情報 (装置に現れない情報を L1 で伝えているか)
	if turn.HintLevel == HintL1 && turn.Trigger == "session_start" {
		for _, want := range script.MustMention {
			want = expandSimText(want, vars)
			if want != "" && !strings.Contains(body, want) {
				findings = append(findings, simFinding{
					StageID: id, Level: turn.HintLevel, Kind: "missing_required",
					Detail: fmt.Sprintf("第一声に %q が含まれない", want), Reply: reply,
				})
			}
		}
	}

	// 5. 観察を先に求めているか (課題の入り口の発話)
	//
	// 装置を見ないうちから手順を話し始めるのを防ぐ。第一声と
	// 課題突破直後は「ランプはどうなっている?」から入る
	// (docs/navigator_design.md 決定32)。
	if isStageOpening(turn) && !mentionsLampQuestion(body) {
		findings = append(findings, simFinding{
			StageID: id, Level: turn.HintLevel, Kind: "no_observation_first",
			Detail: "課題の入り口でランプの状態を尋ねていない", Reply: reply,
		})
	}

	// 6. 課題突破を「解除完了」と取り違えていないか
	//
	// stage_cleared は課題を1つ抜けただけで、装置はまだ生きている。
	// ここで「解除できました」と言うと、プレイヤーは終わったと誤解する。
	// ヒバリのキャラシートに解除成功時の台詞があり、それを
	// ステージ突破の場面で使っていた (実ログで発覚)。
	if turn.Trigger == "stage_cleared" {
		for _, word := range prematureCompletionWords {
			if strings.Contains(body, word) {
				findings = append(findings, simFinding{
					StageID: id, Level: turn.HintLevel, Kind: "premature_completion",
					Detail: fmt.Sprintf("課題突破の場面で完了を意味する %q を使っている", word),
					Reply:  reply,
				})
				break
			}
		}
	}

	// 7. マネージャーへの応答が簡素か (session_ready)
	//
	// 相手はマネージャーで、カウントダウンはまだ始まっていない。
	// ここで装置の操作や報告を求めると、プレイヤーは時間が動く前に
	// 動き出してしまう (決定36)。
	if turn.Trigger == "session_ready" {
		if mentionsLampQuestion(body) {
			findings = append(findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "ready_asks_operation",
				Detail: "マネージャーへの応答で装置の状態に言及している", Reply: reply,
			})
		}
		if n := countRunes(body); n > readyMaxRunes {
			findings = append(findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "ready_too_long",
				Detail: fmt.Sprintf("%d字 (待機完了の応答は %d 字程度に収める)", n, readyMaxRunes),
				Reply:  reply,
			})
		}
	}

	return findings
}

// excessiveRunes は「目安を大きく超えた」と見なす長さ。
//
// 60字の目安を数字超えただけでは所見にしない (緩い条件として運用する)。
// ここを超えると無線を塞ぐ時間が体感で分かるほど延びるので、そのときだけ挙げる。
const excessiveRunes = 90

// openingMaxRunes は session_start (プレイヤーへの第一声) の想定上限。
// 名乗り + 安心させる一言 + 質問を入れるため、通常の 60 字より緩い (決定37)。
const openingMaxRunes = 80

// readyMaxRunes は session_ready (マネージャーへの応答) の想定上限。
// 「こちらフクロウ。待機完了。どうぞ」程度で足りる (決定36)。
const readyMaxRunes = 30

// isStageOpening は「課題の入り口」の発話かを判定する。
// セッション開始と、課題突破の直後 (次の課題の入り口) が対象。
func isStageOpening(turn simTurn) bool {
	return turn.Trigger == "session_start" || turn.Trigger == "stage_cleared"
}

// lampQuestionForms はランプの状態を尋ねていると見なす語。
// キャラクターごとに語尾が違うため、語幹で照合する。
var lampQuestionForms = []string{"ランプ", "光って", "点いて", "点灯", "点滅"}

// prematureCompletionWords は「装置を解除しきった」ことを意味する語。
// 課題を1つ突破しただけの場面 (stage_cleared) で使うと、
// プレイヤーが終わったと誤解する。
// **「ボタン解除成功」のような部分的な解除は含めない** — 201 の押下列が
// 通ったことを指す正しい表現で、装置全体の完了ではない。
var prematureCompletionWords = []string{
	"解除されちゃいました", "解除できました", "解除完了",
	"すべて解除", "全部解除", "解体完了", "任務完了",
	"お疲れさまでした", "終わりました",
}

// mentionsLampQuestion は発話がランプの状態に言及しているかを返す。
func mentionsLampQuestion(body string) bool {
	for _, form := range lampQuestionForms {
		if strings.Contains(body, form) {
			return true
		}
	}
	return false
}

func simSummarize(t *testing.T, results []*simStageResult) {
	t.Helper()

	byKind := map[string]int{}
	totalTurns := 0
	overGuide := 0
	maxRunes := 0
	sumRunes := 0

	t.Logf("")
	t.Logf("================ 集計 ================")
	for _, r := range results {
		for _, turn := range r.Turns {
			totalTurns++
			sumRunes += turn.Runes
			if turn.Runes > maxRunes {
				maxRunes = turn.Runes
			}
			guide := navigatorMaxRunes
			if turn.Trigger == "session_start" {
				guide = openingMaxRunes
			}
			if turn.Runes > guide {
				overGuide++
			}
		}
		for _, f := range r.Findings {
			byKind[f.Kind]++
		}
		status := "OK"
		if len(r.Findings) > 0 {
			status = fmt.Sprintf("所見%d件", len(r.Findings))
		}
		t.Logf("  %-5s %-14s %-10s (%d発話)", r.StageID, r.StageName, status, len(r.Turns))
		for _, f := range r.Findings {
			t.Logf("        - L%d %s: %s", f.Level, f.Kind, f.Detail)
			if f.Reply != "" {
				t.Logf("          発話: %s", f.Reply)
			}
		}
	}

	t.Logf("")
	t.Logf("  総発話数: %d", totalTurns)
	if totalTurns > 0 {
		// 60字は**目安**なので、超過件数は所見ではなく参考値として出す
		// (navigatorMaxRunes のコメント参照)。
		t.Logf("  平均文字数: %.1f / 最長 %d (目安 %d)",
			float64(sumRunes)/float64(totalTurns), maxRunes, navigatorMaxRunes)
		t.Logf("  目安超え: %d/%d 発話 (%.0f%%) — 緩い条件なので所見にはしない",
			overGuide, totalTurns, float64(overGuide)*100/float64(totalTurns))
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		t.Logf("  所見 %-18s %d件", k, byKind[k])
	}
}

func simRenderReport(results []*simStageResult, character NavigatorCharacter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ステージ横断シミュレーション結果\n\n")
	fmt.Fprintf(&b, "- 実行日時: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- キャラクター: %s (%s)\n", character.Name, character.ID)
	fmt.Fprintf(&b, "- 対象ステージ: %d\n\n", len(results))

	for _, r := range results {
		fmt.Fprintf(&b, "## %s %s\n\n", r.StageID, r.StageName)
		fmt.Fprintf(&b, "正解: %s\n\n", r.CutJA)
		for _, turn := range r.Turns {
			if turn.Player != "" {
				fmt.Fprintf(&b, "- **P** %s\n", turn.Player)
			}
			fmt.Fprintf(&b, "- **N** (L%d/%s, %d字) %s\n",
				turn.Level, turn.Trigger, turn.Runes, turn.Reply)
		}
		if len(r.Findings) > 0 {
			fmt.Fprintf(&b, "\n**所見**\n\n")
			for _, f := range r.Findings {
				fmt.Fprintf(&b, "- L%d `%s` — %s\n", f.Level, f.Kind, f.Detail)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}
