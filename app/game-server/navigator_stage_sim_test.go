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
	// MustNotMention は**どのレベルでも**言ってはいけない語。
	//
	// 資料を読むこと自体が謎のステージ (209 配電盤照合) で、ナビが
	// 数字を先に言うと謎が丸ごと消える (ADR N-44)。
	// **同じ情報でもステージによって扱いが逆になる** — 206 綱渡り は
	// 危険位置を第一声で必ず伝える (MustMention 側)。
	MustNotMention []string

	// MustMention は L1 の発話に必ず含まれるべき語 (装置に現れない情報)。
	// ${var} で抽選変数を参照できる。
	//
	// **`|` 区切りで言い換えを並べられる**。1つでも出ていれば満たしたとみなす。
	// 「押しながら」と「押したまま」のように**意味が同じで表現が違う**場合に使う
	// (完全一致だけだと、正しく伝わっているのに所見になる。実測 2026-08-25)。
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

	// **ステージの difficulty タグに合った難易度で組む。**
	// 常にノーマルで組むと、イージーのステージまで `l4_pct = 0` になり
	// (ADR N-39)、L4 の台本が本番と食い違う。
	// 102/103/104 は L4 (直言) が有効な難易度で検証したい。
	diff := difficultyNormal
	if tmplStage, sErr := lib.Stage(id); sErr == nil && tmplStage.Difficulty != "" {
		if _, err := lib.Difficulty(tmplStage.Difficulty); err == nil {
			diff = tmplStage.Difficulty
		}
	}

	base, err := lib.Difficulty(diff)
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
	// 課題の入り口の発話を数える。`session_start` と**最初の** `player_message`
	// の2つを入り口とみなし、MustMention はここまでにしか要求しない。
	// 204 色合わせは注意事項を**観察報告への返し**で伝えるため
	// (session_start の時点ではまだ装置を見ていない)、session_start だけでは足りない。
	// 一方 3回目以降の L1 発話にまで要求すると毎回の復唱を強いることになる。
	entryTurns := 0
	mentionSeen := map[string]bool{}
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

		// **台本のヒントレベルがステージ設定と矛盾していないか検査する。**
		// `HintLevel` は台本が直接指定するため、`l4_pct = 0` で L4 を塞いだ
		// ステージ (204 色合わせ / ADR N-36) でも台本が L4 と書けば L4 で走る。
		// **本番では到達しない状態を検証していた**ことがあり、実際に
		// 204 で「正解は緑色の線ですよ」と直言する所見を拾ってしまった。
		if turn.HintLevel >= HintL4 && built.Hints.L4Pct == 0 {
			result.Findings = append(result.Findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "unreachable_hint_level",
				Detail: "l4_pct = 0 のステージに L4 の台本がある。本番では到達しない",
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
		gen, err := processor.GenerateNavigatorReply(ctx, prompt, instruction)
		if err != nil {
			result.Findings = append(result.Findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "api_error", Detail: err.Error(),
			})
			t.Logf("  L%d %-15s ERROR: %v", turn.HintLevel, turn.Trigger, err)
			continue
		}
		reply := gen.Reply
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
			simCheckTurn(id, stage, turn, reply, script, vars,
				playerSaidCut, revealedAtL4, entryTurns < 2)...)

		// 入り口の発話 (session_start と最初の player_message) で
		// MustMention の語が出たかを集計する。
		if turn.HintLevel == HintL1 && entryTurns < 2 {
			for _, want := range script.MustMention {
				if simMentionHit(expandSimText(want, vars), stripTTSTags(reply)) {
					mentionSeen[want] = true
				}
			}
		}
		if turn.Trigger == "session_start" || turn.Trigger == "player_message" {
			entryTurns++
		}

		// L4 で色名を出したら、以降の言及は完了報告として扱う
		if turn.HintLevel >= HintL4 && cutJA != "" && strings.Contains(stripTTSTags(reply), cutJA) {
			revealedAtL4 = true
		}
	}

	// 入り口の発話を通しても MustMention の語が出なかったら所見にする。
	for _, want := range script.MustMention {
		if w := expandSimText(want, vars); w != "" && !mentionSeen[want] {
			result.Findings = append(result.Findings, simFinding{
				StageID: id, Level: HintL1, Kind: "missing_required",
				Detail: fmt.Sprintf("課題の入り口で %q に触れていない", w),
			})
		}
	}

	return result
}

// simPlayerReportedWrongColor は、このターンでプレイヤーが
// **正解でない色**を報告したかを見る。台本の ${sim_wrong_color} を使う。
func simPlayerReportedWrongColor(turn simTurn, vars map[string]string, stage *BuiltStage) bool {
	wrong := vars["sim_wrong_color"]
	if wrong == "" || turn.Player == "" {
		return false
	}
	// 台本のプレイヤー発話が誤答の色を含み、かつ正解色を含まないこと。
	player := expandSimText(turn.Player, vars)
	if !strings.Contains(player, wrong) {
		return false
	}
	if cutJA := colorNameJA[stage.Cut]; cutJA != "" && strings.Contains(player, cutJA) {
		return false
	}
	return true
}

// simAuthorizesCut は**その色を正解と認めたうえで**切らせる言い方を拾う。
//
// 危険なのは「合っている」と請け合うこと。205 のナビゲーターは装置を見ておらず
// 答え合わせができないので、**正解だと認めた時点で誤り**になる (ADR N-50)。
//
// 『数え直して自信が持てたら切ってくれ』は**判断を委ねている**ので安全 —
// 切る話をしていても当たらない。条件節 (「たら」「なら」) や
// 確認を促す語が同じ文にあれば承認とみなさない。
var simCutApprovePattern = regexp.MustCompile(
	`それが[0-9０-９]番目` +
		`|(合って|正解|間違いな|それでいい|そのとおり)`)

var simCutDeferPattern = regexp.MustCompile(
	`(自信|確か|数え直|見比べ|もう一度|もういちど)`)

func simAuthorizesCut(body string) bool {
	if simCutDeferPattern.MatchString(body) {
		return false
	}
	return simCutApprovePattern.MatchString(body)
}

// simMentionHit は want (｜区切りで言い換えを並べられる) が body に出ているかを見る。
// 「押しながら|押したまま」のように**意味が同じで表現が違う**場合、
// どれか1つ出ていれば満たしたとみなす。
func simMentionHit(want, body string) bool {
	if want == "" {
		return false
	}
	for _, alt := range strings.Split(want, "|") {
		if alt != "" && strings.Contains(body, alt) {
			return true
		}
	}
	return false
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

	// 誤報告の台本用に、正解ではない色を1つ用意する (205 で使う)。
	//
	// **点灯しっぱなしの基準色を選んではいけない** (205)。基準色を誤答にすると
	// ナビゲーターの「それは基準だから数に入れない」が**正しい応答**になり、
	// 「誤答に理由を付け足していないか」を検査できない。
	// 実際にこれで「ナビが誤った理由を返している」と誤検知した。
	steadyJA := ""
	if leds, ok := stage.Core["leds"].(map[string]any); ok {
		for code, spec := range leds {
			if spec == "on" {
				steadyJA = colorNameJA[code]
			}
		}
	}
	for _, code := range allColors {
		if code != stage.Cut && colorNameJA[code] != steadyJA {
			vars["sim_wrong_color"] = colorNameJA[code]
			break
		}
	}

	// モールス系ステージ (203/305) はプレイヤーが読み上げる語を台本で使う。
	// 展開済みの answer から語を拾う (テンプレートは word/color_word を
	// answer の中でそのまま展開しているため)。
	vars["navi_word_guess"] = simMorseWordFrom(stage)

	// 301 LED照合 のキーワード (資料の変換表から引く語)。
	// **台本に固定値を書かない** — 抽選値と食い違うと、ナビが
	// 『キーワードは違うみたいです』と正しい報告を差し戻す形になる
	// (実測 2026-08-25)。
	if m := simCodebookWordPattern.FindStringSubmatch(stage.Navigator["answer"]); len(m) > 1 {
		vars["sim_keyword"] = m[1]
	}

	// 危険位置 (206 綱渡り / 209 配電盤照合)。
	//
	// **Core向けJSON から直接取る。** 以前は answer の文面を正規表現で
	// 拾っていたが、ステージごとに言い回しが違う (「ダイヤル4で止まるな」/
	// 「危険位置は**4**」) ため**どちらにも当たらず**、
	// `${sim_forbidden}` が未展開のまま照合されて誤検出になっていた。
	// 文面の書き方に依存しない形にする (押下列を push_seq から取るのと同じ)。
	if positions := simForbiddenPositions(stage); len(positions) > 0 {
		vars["sim_forbidden"] = positions[0]
	}

	// ダイヤルの指定位置。**ステージによって意味が逆**なので2つの名前で出す。
	//   sim_release  — 209 配電盤照合の解除位置。**ナビが言ってはいけない**数字
	//   sim_rotary   — 102 などの指定位置。**第一声で必ず伝える**数字
	if pre, ok := stage.Core["precondition"].(map[string]any); ok {
		if r, ok := pre["rotary"]; ok {
			vars["sim_release"] = fmt.Sprintf("%v", r)
			vars["sim_rotary"] = fmt.Sprintf("%v", r)
		}
	}

	// 押下列の1色目 (102/201)。第一声で列が伝わっているかの照合に使う。
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

// simPressBeforeLook は「押させてからランプを尋ねる」形を拾う。
// 『黄色のボタンを押さえたまま、ランプはどうなってますか?』のような並び。
var simPressBeforeLook = regexp.MustCompile(`ボタン[^。!?！?]{0,12}押[^。!?！?]{0,16}(ランプ|どうなって)`)

// simDialBeforeLook は第一声のダイヤル手順を拾う。
// 「N に合わせろ」だけでなく、**危険位置の警告 (「2で止まるな」) も対象**
// — 回す指示と一緒に伝える形にしたので、第一声に単独で出てはいけない (ADR N-49)。
var simDialBeforeLook = regexp.MustCompile(
	`(ダイヤル|ロータリー)[^。!?！?]{0,10}[0-9０-９][^。!?！?]{0,8}(合わせ|回し|セット|止ま|止め)` +
		`|[0-9０-９][^。!?！?]{0,6}(で止まるな|では止まるな|で止めるな|で止めないで)`)

// simCodebookWordPattern は 301 の answer から変換表のキーワードを拾う。
var simCodebookWordPattern = regexp.MustCompile(`キーワードは\*\*([A-Z]+)\*\*`)

// simForbiddenPositions は forbidden_rotary の禁止位置を文字列で返す。
// 無ければ空。**文面ではなく Core向けJSON から取る**ので、
// ステージごとの言い回しの違いに影響されない。
func simForbiddenPositions(stage *BuiltStage) []string {
	forbidden, ok := stage.Core["forbidden_rotary"].(map[string]any)
	if !ok {
		return nil
	}
	positions, ok := forbidden["positions"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(positions))
	for _, p := range positions {
		out = append(out, fmt.Sprintf("%v", p))
	}
	return out
}

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
// **現在は該当なし。** これに当たる案は「色を教えるだけの工程が謎の隣に残る」
// として採用を見送った (ADR S-5)。
// 該当するステージを作った場合はここへ登録する — 登録しないと、
// 仕様どおりに色を伝えた発話が「色漏れ」として検出される。
var simColorToldByDesign = map[string]bool{}

// simCheckTurn は1発話を4つの観点で検査する。
//
// playerSaidCut は、プレイヤーがこのステージで既に正解色を口にしているか。
// 正解色を**先に言ったのがプレイヤー**なら、ナビゲーターの復唱は漏洩ではない
// (205 速さくらべは「報告を照合して復唱する」のが正規の手順)。
func simCheckTurn(
	id string, stage *BuiltStage, turn simTurn, reply string,
	script simScript, vars map[string]string, playerSaidCut, revealedAtL4, firstReply bool,
) []simFinding {
	findings := make([]simFinding, 0)
	body := stripTTSTags(reply)

	// 1. 正解色の漏れ (L4 未満)
	//
	// 203 ブループリントは L4 でも色名を言ってはいけないため、L4 も検査する。
	// 逆に切る線の色が装置に現れないステージは検査しない
	// (simColorToldByDesign。現在は該当なし)。
	cutJA := colorNameJA[stage.Cut]
	checkLeak := turn.HintLevel < HintL4 || id == "203"
	if simColorToldByDesign[id] {
		checkLeak = false
	}
	// プレイヤーが先に言った色の復唱は漏洩ではない (205 だけは例外で、
	// 復唱すること自体が禁止されている)。
	if playerSaidCut && id != "203" {
		checkLeak = false
	}
	// **L4 で正当に明かしたあとは漏洩ではない。**
	// 課題突破後の stage_cleared は L1 に戻るが、直前の L4 で
	// 「赤色の線を切ってください」と伝えた以上、
	// 「赤色の線を切断しましたね」は完了報告であって漏洩ではない。
	if revealedAtL4 && id != "203" {
		checkLeak = false
	}
	// **課題突破後の完了報告も漏洩ではない。**
	// `stage_cleared` は**その線が既に切られた**ことを意味する。
	// 「赤い線が切れて」は起きたことの描写で、答えを教える発話ではない。
	//
	// L4 経由の免除だけでは足りない — **ノーマル以上は `l4_pct = 0`**
	// (ADR N-39) で L4 に到達しないため、正常な完了報告が毎回
	// 漏洩として検出されてしまう。
	if turn.Trigger == "stage_cleared" && id != "203" {
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

	// 4. 必須情報 / 禁止情報
	//
	// **同じ情報でもステージによって扱いが逆になる。**
	// 206 綱渡り は危険位置を第一声で必ず伝えるが (MustMention)、
	// 209 配電盤照合 は資料を読ませるので言ってはいけない (MustNotMention)。

	// 言ってはいけない語 (資料を読ませるステージの答えなど)
	for _, ng := range script.MustNotMention {
		ng = expandSimText(ng, vars)
		if ng == "" {
			continue
		}
		if strings.Contains(body, ng) {
			findings = append(findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "told_answer",
				Detail: fmt.Sprintf("資料から読ませるべき %q を言っている", ng), Reply: reply,
			})
		}
	}

	// **第一声だけでなく、課題の入り口の返答も見る。**
	// 204 色合わせは「最後に押した色を覚えておけ」を**観察報告への返し**で
	// 伝える必要がある (session_start の時点ではまだ装置を見ていない)。
	// プレイヤーが先回りして『同じ色のボタンを押せばいいですか?』と聞くと、
	// 肯定するだけで返して**注意が落ちた**ため検査を広げた (実測 2026-08-25)。
	// `firstReply` は課題の入り口 — session_start か、最初の player_message。
	// **2回目以降の L1 発話には要求しない** (毎回の復唱を強いることになる)。
	// MustMention は**入り口の発話のどれかに1回出れば足りる**。
	// 発話ごとに要求すると、まだ装置を見ていない session_start にまで
	// 「覚えておけ」を求めることになり、毎回の復唱も強いてしまう。
	// 判定は呼び出し側で集計する (simMentionSeen)。
	_ = firstReply

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

	// 5.1 観察を求める前に操作させていないか
	//
	// ランプを尋ねてはいても、**同じ発話で先に押させる**ことがある
	// (実測: 304 で『黄色のボタンを押さえたまま、ランプはどうなってますか?』)。
	// 装置を見る前に操作させると、何を見ているのか分からないまま手が動く。
	//
	// **押すボタンの色は点滅として装置に現れる**ので、第一声で言う必要がない
	// (危険位置のように装置に現れない情報とは扱いが違う。ADR N-10 / N-4)。
	if isStageOpening(turn) && simPressBeforeLook.MatchString(body) {
		findings = append(findings, simFinding{
			StageID: id, Level: turn.HintLevel, Kind: "press_before_observation",
			Detail: "観察を求める前にボタンを押させている", Reply: reply,
		})
	}

	// 5.15 誤報告のあとに切らせていないか
	//
	// **切った線は戻せず即爆発する。** プレイヤーが正解でない色を報告したのに
	// 『それが N 番目だ、切ってください』と応じると、**そのまま爆死する**。
	//
	// 205 速さくらべ で実測8回中1〜5回発生した。散文の指示を4通り書き直しても
	// ゼロにはならなかったため、**検査で必ず落とす**。
	//
	// 台本が誤答を報告するターン (`sim_wrong_color`) の直後だけを見る。
	if simPlayerReportedWrongColor(turn, vars, stage) && simAuthorizesCut(body) {
		findings = append(findings, simFinding{
			StageID: id, Level: turn.HintLevel, Kind: "cut_after_wrong_report",
			Detail: "誤った色の報告に対して切る指示を出している(即爆発)", Reply: reply,
		})
	}

	// 5.2 第一声で手順を言っていないか
	//
	// **手順は第一声に入れない** (ADR N-49)。プレイヤーはまだ装置に触っていないので、
	// 報告を待つ間に事故は起きない。装置を見る前に数字を並べても頭に入らず、
	// **見る前に手が動く**ことになる。
	//
	// **危険位置も例外ではない。** 206 綱渡り は「回す指示と一緒に警告する」形にした
	// (回せと言う前に危険位置だけを告げると、何のための数字か分からない)。
	// そのため**全ステージが対象**で、危険位置の有無で分岐しない。
	//
	// 実測: 102 で『まずはダイヤルを2に合わせてください。ランプはどうなってますか?』、
	// 206 で『ダイヤル2で止まるな。ランプはどうなってますか?』となった。
	if turn.Trigger == "session_start" && simDialBeforeLook.MatchString(body) {
		findings = append(findings, simFinding{
			StageID: id, Level: turn.HintLevel, Kind: "procedure_before_observation",
			Detail: "第一声で手順(ダイヤル)を言っている", Reply: reply,
		})
	}

	// 5.5 指針をそのまま書き出していないか
	//
	// 「切る線の色名は言わない」「出力例:」のように、**従うべき指示を
	// 読み上げてしまう**ことがある (実測で136字の例があった)。無線に流れると
	// ナビゲーターが内部の指示を音読することになる。
	for _, form := range metaOutputForms {
		if strings.Contains(body, form) {
			findings = append(findings, simFinding{
				StageID: id, Level: turn.HintLevel, Kind: "meta_output",
				Detail: fmt.Sprintf("指針をそのまま出力している (%q)", form), Reply: reply,
			})
			break
		}
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

// metaOutputForms は「発話ではなく指示の書き出し」を示す語。
//
// ナビゲーターは指針に**従う**のであって、読み上げるのではない。
var metaOutputForms = []string{
	"出力例", "言わないこと", "伝える必要がある", "してはいけません",
	"という指示", "指針:", "ヒントレベル",
}

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
