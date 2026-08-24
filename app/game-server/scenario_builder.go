package main

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// varPattern は ${name} 形式の抽選変数参照。
var varPattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_]+)\}`)

// MissionSheet は紙資料の物理定数 (docs/puzzle_stage_ideas.md §7)。
// シートの印刷内容は固定のため、サーバー設定に登録した値をテンプレートが参照する。
type MissionSheet struct {
	// TerminalMapX は資料3の**X系統**の端子番号(X1-X5) → 配線色(A-E) の対応。
	// シリアル銘板の下1桁が**奇数**の個体が使う (ADR D-6)。
	TerminalMapX map[string]string `toml:"terminal_map_x"`
	// TerminalMapY は資料3の**Y系統**の端子番号(Y1-Y5) → 配線色(A-E) の対応。
	// シリアル銘板の下1桁が**偶数**の個体が使う。
	//
	// **X とは完全に別の対応にする**。一部だけずらすと系統を取り違えても
	// 偶然当たる場合があり、銘板を見る意味が薄れる。
	TerminalMapY map[string]string `toml:"terminal_map_y"`
	// Documents は紙資料の呼称 (docs/printed_materials.md §1.3)。
	// ナビゲーターが無線で読み上げる名前で、刷り直しで番号が変わったら更新する。
	Documents SheetDocuments `toml:"documents"`
}

// SheetDocuments は紙資料の呼称。A4裏表1枚に印刷した各資料を、
// ナビゲーターが「資料1を見ろ」と**一言で**指せるようにするための番号
// (docs/printed_materials.md §1.3 / ADR D-2)。
//
// **面 (表裏) は持たない**。ナビゲーターは面を指示せず番号だけを言うため、
// 印刷レイアウトを変えてもサーバー設定は変わらない。
type SheetDocuments struct {
	// Morse はモールス対照表 (203/304/305)
	Morse string `toml:"morse"`
	// Codebook は 202 の暗号チェーンを載せた資料 (202)。
	//
	// **変換表 (並び → キーワード) とデコード表 (頭文字 → ダイヤル位置 /
	// 末尾文字 → 基準色) の2つを1つの資料番号にまとめてある** (ADR D-2)。
	// 202 は必ず両方を順にたどるため、片方だけ見る場面が無い。
	Codebook string `toml:"codebook"`
	// Circuit は回路図 (203 ブループリント) + 301 LED照合 の最終分岐表
	Circuit string `toml:"circuit"`
	// Panel はロータリー対照表 (209 配電盤照合)。
	// 見え方 → 現在位置 / 危険位置 / 解除位置 を引く6行の表。
	Panel string `toml:"panel"`
}

// Validate は資料名がすべて設定されているかを確かめる。
//
// 空のまま `${sheet_morse}` を展開すると「を使って解読しろ」という
// 意味の通らない発話になるため、**起動時に落とす**
// (terminal_map の5色必須と同じ性質)。
func (d *SheetDocuments) Validate() error {
	fields := []struct {
		key   string
		value string
	}{
		{"morse", d.Morse},
		{"codebook", d.Codebook},
		{"circuit", d.Circuit},
		{"panel", d.Panel},
	}
	for _, f := range fields {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("[mission_sheet.documents].%s is not configured", f.key)
		}
	}
	return nil
}

// sheetDocumentVars は資料名をナビゲーター向け変数として返す。
//
// **Core向けJSONには入れない** — デバイスは紙資料の存在を知る必要がなく、
// 資料名は無線で読み上げられるナビゲーター知識にのみ現れる。
func (d *SheetDocuments) sheetDocumentVars() map[string]string {
	return map[string]string{
		"sheet_morse":    d.Morse,
		"sheet_codebook": d.Codebook,
		"sheet_circuit":  d.Circuit,
		"sheet_panel":    d.Panel,
	}
}

// TerminalForColor は配線色から端子番号を引く (203 ブループリント用)。
//
// series は "x" (奇数系統) / "y" (偶数系統)。
// ナビゲーターは**両系統の端子番号を並べて**伝え、どちらを使うかは
// プレイヤーがシリアル銘板を見て選ぶ (ADR D-6)。
func (m *MissionSheet) TerminalForColor(series, color string) string {
	table := m.TerminalMapX
	if series == "y" {
		table = m.TerminalMapY
	}
	for terminal, c := range table {
		if c == color {
			return terminal
		}
	}
	return ""
}

// ValidateTerminalMaps は両系統に5色すべてが登録されているかを確かめる。
//
// 登録漏れがあると、その色が正解になったセッションだけが組み立てに失敗する
// (**抽選次第でしか再現しない**ため、起動時に落とす)。
func (m *MissionSheet) ValidateTerminalMaps() error {
	for _, series := range []string{"x", "y"} {
		for _, color := range allColors {
			if m.TerminalForColor(series, color) == "" {
				return fmt.Errorf(
					"[mission_sheet.terminal_map_%s] に色 %q の端子が無い (5色すべて必要)",
					series, color)
			}
		}
	}
	// X と Y が同じ対応だと系統を取り違えても当たってしまう
	same := 0
	for _, color := range allColors {
		x := m.TerminalForColor("x", color)
		y := m.TerminalForColor("y", color)
		if strings.TrimPrefix(x, "X") == strings.TrimPrefix(y, "Y") {
			same++
		}
	}
	if same == len(allColors) {
		return errors.New(
			"[mission_sheet] terminal_map_x と terminal_map_y が同じ対応になっている " +
				"(系統を取り違えても当たるため、別の対応にすること)")
	}
	return nil
}

// BuiltStage は解決済みの1ステージ。Core向けJSONとナビゲーター知識の両方を持つ。
type BuiltStage struct {
	// TemplateID は元になったステージ定義のID (ログ・Web画面用)
	TemplateID string `json:"template_id"`
	Name       string `json:"name"`

	// Core は session_start の stages[] に入るステージ要素
	Core map[string]any `json:"core"`

	// Cut はこのステージの正解線 (検証・混線の色除外に使う)
	Cut string `json:"cut"`

	// Navigator は解決済みのナビゲーター向けステージ知識
	Navigator map[string]string `json:"navigator"`

	// KeepCutSecret は L4 でも切る線の色名を伏せ続けるか (ADR N-38)。
	// 色名を言うと課題そのものが消えるステージで true。
	KeepCutSecret bool `json:"keep_cut_secret"`

	// Hints はこのステージに適用するヒント閾値 (解決済み)。
	//
	// 難易度テンプレートの値を基本に、ステージ定義の `[hints]` があれば
	// それで上書きしてある。**ステージごとに引く**ため、
	// 発話生成時はセッションではなくここを見る (ADR N-36)。
	Hints HintRule `json:"hints"`
}

// BuiltSession は組み立て済みのセッション一式。
//
// (a) Core向けセッションJSON と (b) ナビゲーター向けステージ知識を
// 同一の抽選結果から機械的に生成するため、実機の状態とナビゲーターの知識は
// 構造的に一致する (docs/scenario_design.md §2)。
type BuiltSession struct {
	SessionID       string        `json:"session_id"`
	Difficulty      string        `json:"difficulty"`
	CountdownMS     int           `json:"countdown_ms"`
	DetonateDelayMS int           `json:"detonate_delay_ms"`
	Stages          []*BuiltStage `json:"stages"`

	// StageBudgetMS は 1ステージあたりの予算 (countdown ÷ ステージ数)。
	// ヒント閾値はこれに対する比率で算出する (docs/scenario_design.md §4.1)。
	StageBudgetMS int `json:"stage_budget_ms"`

	Hints     HintRule      `json:"hints"`
	Crosstalk CrosstalkRule `json:"crosstalk"`
}

// SessionStartPayload は Core へ送る session_start メッセージを組み立てる (§6)。
func (s *BuiltSession) SessionStartPayload(deviceID string) map[string]any {
	stages := make([]map[string]any, 0, len(s.Stages))
	for _, stage := range s.Stages {
		stages = append(stages, stage.Core)
	}

	return map[string]any{
		"type":              msgSessionStart,
		"device_id":         deviceID,
		"session_id":        s.SessionID,
		"countdown_ms":      s.CountdownMS,
		"detonate_delay_ms": s.DetonateDelayMS,
		"stages":            stages,
	}
}

// ScenarioBuilder は難易度テンプレートからセッションを組み立てる。
type ScenarioBuilder struct {
	lib   *ScenarioLibrary
	sheet MissionSheet
	rng   *rand.Rand
}

func NewScenarioBuilder(lib *ScenarioLibrary, sheet MissionSheet, rng *rand.Rand) *ScenarioBuilder {
	return &ScenarioBuilder{lib: lib, sheet: sheet, rng: rng}
}

// Build は難易度に対応するシナリオテンプレートからセッションを組み立てる
// (docs/scenario_design.md §2)。
func (b *ScenarioBuilder) Build(sessionID, difficulty string) (*BuiltSession, error) {
	tmpl, err := b.lib.Difficulty(difficulty)
	if err != nil {
		return nil, err
	}

	stageIDs, err := b.composeStages(tmpl, difficulty)
	if err != nil {
		return nil, fmt.Errorf("compose stages: %w", err)
	}

	session := &BuiltSession{
		SessionID:       sessionID,
		Difficulty:      difficulty,
		CountdownMS:     tmpl.CountdownMS,
		DetonateDelayMS: tmpl.DetonateDelayMS,
		Hints:           tmpl.Hints,
		Crosstalk:       tmpl.Crosstalk,
	}

	// 切断線はステージ間で重複しないよう、割り当て済みの色を持ち回る
	// (配線は5本で物理的に1本ずつしか切れないため。docs/scenario_design.md §4)
	//
	// ステージ数は最大4に抑えてあり、色が1本以上余る。加えて色の制約を持つ
	// ステージ (202 暗号電文) も候補語で5色すべてをカバーしているため、
	// 再生順どおりに素直に解決してよい。
	usedLines := make(map[string]bool)

	for _, id := range stageIDs {
		stageTmpl, err := b.lib.Stage(id)
		if err != nil {
			return nil, err
		}
		stage, err := b.buildStage(stageTmpl, usedLines, tmpl.Hints, tmpl.Load)
		if err != nil {
			return nil, fmt.Errorf("stage %s: %w", id, err)
		}
		usedLines[stage.Cut] = true
		session.Stages = append(session.Stages, stage)
	}

	if len(session.Stages) > 0 {
		session.StageBudgetMS = session.CountdownMS / len(session.Stages)
	}

	if err := ValidateSession(session); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return session, nil
}

// composeStages はハイブリッド方式でステージ構成を決める (固定並び + タグ抽選)。
func (b *ScenarioBuilder) composeStages(tmpl *DifficultyTemplate, difficulty string) ([]string, error) {
	forEasy := difficulty == difficultyEasy

	selected := make([]string, 0, 5)
	used := make(map[string]bool)

	appendStage := func(id string) error {
		if used[id] {
			return fmt.Errorf("duplicated stage in compose: %s", id)
		}
		used[id] = true
		selected = append(selected, id)
		return nil
	}

	for _, id := range tmpl.Compose.FixedHead {
		if _, err := b.lib.Stage(id); err != nil {
			return nil, err
		}
		if err := appendStage(id); err != nil {
			return nil, err
		}
	}

	// タグ別の抽選 (同一ステージの重複選出はしない)
	// map の反復順は非決定的なため、タグ名でソートして安定させる
	tags := make([]string, 0, len(tmpl.Compose.Random))
	for tag := range tmpl.Compose.Random {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	randomPart := make([]string, 0, 5)
	for _, tag := range tags {
		count := tmpl.Compose.Random[tag]
		candidates := make([]string, 0)
		for _, id := range b.lib.StagesByTag(tag, forEasy) {
			if !used[id] {
				candidates = append(candidates, id)
			}
		}
		if len(candidates) < count {
			return nil, fmt.Errorf("not enough stages for tag %q: need %d, have %d",
				tag, count, len(candidates))
		}

		b.rng.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		for _, id := range candidates[:count] {
			used[id] = true
			randomPart = append(randomPart, id)
		}
	}

	// 抽選分の並び順もシャッフルする (タグ順に固まらないようにする)
	b.rng.Shuffle(len(randomPart), func(i, j int) {
		randomPart[i], randomPart[j] = randomPart[j], randomPart[i]
	})
	selected = append(selected, randomPart...)

	for _, id := range tmpl.Compose.FixedTail {
		if _, err := b.lib.Stage(id); err != nil {
			return nil, err
		}
		if err := appendStage(id); err != nil {
			return nil, err
		}
	}

	// 切断線を1本以上余らせるため、ステージ数には上限がある
	// (docs/puzzle_stage_ideas.md §5)
	if len(selected) > maxStagesPerSession {
		return nil, fmt.Errorf("too many stages: %d (max %d)", len(selected), maxStagesPerSession)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no stages selected")
	}
	return selected, nil
}

// toJapaneseColorVars は抽選変数の色コード (A-E) を日本語色名へ置き換える。
//
// ナビゲーターの発話はそのまま読み上げられるため、「A色の線」ではなく
// 「赤色の線」にする必要がある。色以外の変数 (ロータリー位置・数値・語句) は
// そのまま残す。
func toJapaneseColorVars(vars map[string]string, keepLiteral map[string]bool) map[string]string {
	converted := make(map[string]string, len(vars))
	for name, value := range vars {
		// **色コード以外の意味で A-E を持つ変数は変換しない** (ADR N-41)。
		//
		// 変換は**値**で判定するため、モールスで表示する1文字 (302 の "E" など)
		// がそのまま色名「白」に化けていた。意味を持つのは変数側なので、
		// 色として扱わない変数を明示的に除外する。
		if keepLiteral[name] {
			converted[name] = value
			continue
		}
		if japanese, ok := colorNameJA[value]; ok {
			converted[name] = japanese
			continue
		}
		// **カンマ区切りの色リストも変換する** (202 の点灯色など)。
		// 単一値だけを変換していたため「ランプはB,Cだ」と読み上げられていた。
		// 全要素が色コードのときだけ置き換える — 一部でも色でなければ
		// 色リストではないので、そのまま残す (ADR N-41 と同じ考え方)。
		if japanese, ok := japaneseColorList(value); ok {
			converted[name] = japanese
			continue
		}
		// **カンマ区切りの数値列も読み上げられる形へ**
		// (206 綱渡り の禁止位置が複数あるときなど)。
		// 「3,5」のままだと TTS が小数や記号として読む。
		if spoken, ok := spokenNumberList(value); ok {
			converted[name] = spoken
			continue
		}
		converted[name] = value
	}
	return converted
}

// spokenNumberList はカンマ区切りの数値列を読み上げられる形へ変換する。
//
// 「3,5」→「3と5」。**全要素が数値のときだけ**変換する。
// 206 綱渡り の禁止位置は難易度で1個にも2個にもなるため、
// 1個のときは元の値のまま (変換不要)。
//
// カンマのまま読み上げると TTS が小数や記号として扱い、
// **危険位置が正しく伝わらない** — このステージは踏むと即爆発するので
// 聞き間違いが直接事故になる。
func spokenNumberList(value string) (string, bool) {
	items := strings.Split(value, ",")
	if len(items) < 2 {
		return "", false
	}
	nums := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if _, err := strconv.Atoi(item); err != nil {
			return "", false
		}
		nums = append(nums, item)
	}
	return strings.Join(nums, "と"), true
}

// japaneseColorList はカンマ区切りの色コード列を日本語の色名列へ変換する。
//
// **全要素が色コードのときだけ**変換する。1つでも色でない要素が混じれば
// 色リストではないため、呼び出し側は元の値を保つ。
// 読み上げるため、区切りは「、」にする。
func japaneseColorList(value string) (string, bool) {
	items := strings.Split(value, ",")
	if len(items) < 2 {
		return "", false
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		japanese, ok := colorNameJA[strings.TrimSpace(item)]
		if !ok {
			return "", false
		}
		names = append(names, japanese)
	}
	return strings.Join(names, "、"), true
}

// buildStage は1ステージの抽選変数を解決し、Core向け要素とナビゲーター知識を生成する。
//
// hints は難易度テンプレートのヒント閾値。ステージ定義に `[hints]` があれば
// それで上書きする (ADR N-36)。
func (b *ScenarioBuilder) buildStage(
	tmpl *StageTemplate, usedLines map[string]bool, hints HintRule, load LoadRule,
) (*BuiltStage, error) {
	vars, err := b.resolveVars(tmpl, usedLines, load)
	if err != nil {
		return nil, err
	}

	core, err := b.expandCore(tmpl.Core, vars)
	if err != nil {
		return nil, err
	}

	cut, _ := core["cut"].(string)
	if cut == "" {
		return nil, fmt.Errorf("core.cut is not resolved")
	}

	// ナビゲーター知識は**無線で読み上げられる**ため、色を日本語名で展開する。
	// Core向けJSON (core) は A-E のままにする — デバイスはその表記で解釈するため。
	naviVars := toJapaneseColorVars(vars, tmpl.LiteralVars())

	// 紙資料の呼称を注入する (`${sheet_morse}` 等)。**ナビゲーター側だけ**に入れ、
	// Core向けJSON (core) には含めない — デバイスは紙資料を知る必要がない。
	// 抽選変数が同名で衝突しないよう、資料名は後から入れず既存値を尊重する。
	for name, value := range b.sheet.Documents.sheetDocumentVars() {
		if _, exists := naviVars[name]; !exists {
			naviVars[name] = value
		}
	}

	navigator := make(map[string]string, len(tmpl.Navigator))
	for key, text := range tmpl.Navigator {
		expanded, err := expandString(text, naviVars)
		if err != nil {
			return nil, fmt.Errorf("navigator.%s: %w", key, err)
		}
		navigator[key] = expanded
	}

	// ステージ定義に [hints] があれば難易度の値を上書きする。
	// **書かれた項目だけを差し替える** — 全項目を書かせると、
	// L4 を塞ぎたいだけのステージが L2・L3 の閾値まで抱え込み、
	// 難易度側を調整しても追従しなくなる。
	hints = tmpl.Hints.Apply(hints)

	return &BuiltStage{
		TemplateID:    tmpl.ID,
		Name:          tmpl.Name,
		Core:          core,
		Cut:           cut,
		Navigator:     navigator,
		Hints:         hints,
		KeepCutSecret: tmpl.KeepCutSecret,
	}, nil
}

// resolveVars は [random] の抽選定義を順に解決する。
//
// 定義同士が ${...} で参照し合うため (exclude など)、解決済みの変数を使って
// 参照を展開しながら進める。参照先が未解決の場合は解決順を入れ替えて再試行する。
func (b *ScenarioBuilder) resolveVars(
	tmpl *StageTemplate, usedLines map[string]bool, load LoadRule,
) (map[string]string, error) {
	vars := make(map[string]string)

	// **難易度の入力量を先に置く**。抽選より前に入れることで
	// `{ pick = "int", min = "${load_color_match_min}" }` のように参照できる。
	// 抽選変数と同名の定義があればそちらが後から上書きする。
	for name, value := range load.loadVars() {
		vars[name] = value
	}

	// 定義名を安定した順序にする
	names := make([]string, 0, len(tmpl.Random))
	for name := range tmpl.Random {
		names = append(names, name)
	}
	sort.Strings(names)

	// 依存関係のため複数パスで解決する
	pending := names
	for pass := 0; pass < len(names)+1 && len(pending) > 0; pass++ {
		next := make([]string, 0, len(pending))
		for _, name := range pending {
			value, err := b.resolveOneVar(name, tmpl.Random[name], vars, usedLines)
			if err != nil {
				if isUnresolvedRef(err) {
					// 参照先が未解決 → 次のパスへ回す
					next = append(next, name)
					continue
				}
				return nil, fmt.Errorf("random.%s: %w", name, err)
			}
			vars[name] = value
		}
		if len(next) == len(pending) {
			return nil, fmt.Errorf("unresolvable variable references: %v", next)
		}
		pending = next
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("unresolved variables: %v", pending)
	}

	// ${rest} は「cut 以外の全色」を表す暗黙変数 (H. 仲間はずれで使う)
	if cut, ok := vars["cut"]; ok {
		rest := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if color != cut {
				rest = append(rest, color)
			}
		}
		vars["rest"] = strings.Join(rest, ",")
	}

	return vars, nil
}

// errUnresolvedRef は参照先の変数がまだ解決されていないことを示す内部エラー。
type errUnresolvedRef struct{ name string }

func (e *errUnresolvedRef) Error() string { return "unresolved reference: " + e.name }

// **ラップされていても見つける。** 途中の導出処理が文脈を足して
// `fmt.Errorf("...: %w", err)` で包むため、型アサーションだけだと
// 未解決参照を取りこぼし、解決順の入れ替えが働かなくなる
// (208 の rank_slot が others の参照を包んで発覚した)。
func isUnresolvedRef(err error) bool {
	var target *errUnresolvedRef
	return errors.As(err, &target)
}
