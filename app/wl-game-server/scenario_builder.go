package main

import (
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
	// SymbolCount はミッションシート上の記号(⚠等)の数
	SymbolCount int `toml:"symbol_count"`
	// NumberSum はシート上の数字の合計
	NumberSum int `toml:"number_sum"`
	// TerminalMap は回路図シートの端子番号(T1-T5) → 配線色(A-E) の対応
	TerminalMap map[string]string `toml:"terminal_map"`
}

// TerminalForColor は配線色から端子番号を引く (E. ブループリント用)。
func (m *MissionSheet) TerminalForColor(color string) string {
	for terminal, c := range m.TerminalMap {
		if c == color {
			return terminal
		}
	}
	return ""
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
	// ステージ (09 暗号電文) も候補語で5色すべてをカバーしているため、
	// 再生順どおりに素直に解決してよい。
	usedLines := make(map[string]bool)

	for _, id := range stageIDs {
		stageTmpl, err := b.lib.Stage(id)
		if err != nil {
			return nil, err
		}
		stage, err := b.buildStage(stageTmpl, usedLines)
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

// buildStage は1ステージの抽選変数を解決し、Core向け要素とナビゲーター知識を生成する。
func (b *ScenarioBuilder) buildStage(tmpl *StageTemplate, usedLines map[string]bool) (*BuiltStage, error) {
	vars, err := b.resolveVars(tmpl, usedLines)
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

	navigator := make(map[string]string, len(tmpl.Navigator))
	for key, text := range tmpl.Navigator {
		expanded, err := expandString(text, vars)
		if err != nil {
			return nil, fmt.Errorf("navigator.%s: %w", key, err)
		}
		navigator[key] = expanded
	}

	return &BuiltStage{
		TemplateID: tmpl.ID,
		Name:       tmpl.Name,
		Core:       core,
		Cut:        cut,
		Navigator:  navigator,
	}, nil
}

// resolveVars は [random] の抽選定義を順に解決する。
//
// 定義同士が ${...} で参照し合うため (exclude など)、解決済みの変数を使って
// 参照を展開しながら進める。参照先が未解決の場合は解決順を入れ替えて再試行する。
func (b *ScenarioBuilder) resolveVars(tmpl *StageTemplate, usedLines map[string]bool) (map[string]string, error) {
	vars := make(map[string]string)

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

func isUnresolvedRef(err error) bool {
	_, ok := err.(*errUnresolvedRef)
	return ok
}

// resolveOneVar は1つの抽選定義を解決する。
func (b *ScenarioBuilder) resolveOneVar(name string, def map[string]any, vars map[string]string, usedLines map[string]bool) (string, error) {
	// derive: 他の変数から機械的に導出する (抽選ではない)
	if deriveKind, ok := def["derive"].(string); ok {
		return b.deriveVar(deriveKind, def, vars)
	}

	pickKind, _ := def["pick"].(string)

	// exclude: 除外する色のリスト (${...} 参照を含みうる)
	excluded := make(map[string]bool)
	if raw, ok := def["exclude"].([]any); ok {
		for _, item := range raw {
			text, ok := item.(string)
			if !ok {
				continue
			}
			expanded, err := expandString(text, vars)
			if err != nil {
				return "", err
			}
			for _, color := range strings.Split(expanded, ",") {
				excluded[strings.TrimSpace(color)] = true
			}
		}
	}

	switch pickKind {
	case "line":
		// 切断線: ステージ間で重複しないよう、既に使われた色を除く
		candidates := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if !usedLines[color] && !excluded[color] {
				candidates = append(candidates, color)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no line available (all %d colors used)", len(allColors))
		}
		return candidates[b.rng.Intn(len(candidates))], nil

	case "color":
		// 任意の色 (切断線の重複制約は受けない)
		candidates := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if !excluded[color] {
				candidates = append(candidates, color)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no color available")
		}
		return candidates[b.rng.Intn(len(candidates))], nil

	case "colors":
		// 複数色をまとめて選ぶ (カンマ区切りで返す)
		countText, err := expandAny(def["count"], vars)
		if err != nil {
			return "", err
		}
		count, err := strconv.Atoi(countText)
		if err != nil {
			return "", fmt.Errorf("colors.count is not a number: %q", countText)
		}
		candidates := make([]string, 0, len(allColors))
		for _, color := range allColors {
			if !excluded[color] {
				candidates = append(candidates, color)
			}
		}
		if len(candidates) < count {
			return "", fmt.Errorf("colors.count %d exceeds available %d", count, len(candidates))
		}
		b.rng.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		picked := candidates[:count]
		sort.Strings(picked)
		return strings.Join(picked, ","), nil

	case "int":
		minText, err := expandAny(def["min"], vars)
		if err != nil {
			return "", err
		}
		maxText, err := expandAny(def["max"], vars)
		if err != nil {
			return "", err
		}
		min, err1 := strconv.Atoi(minText)
		max, err2 := strconv.Atoi(maxText)
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("int.min/max are not numbers: %q %q", minText, maxText)
		}
		if max < min {
			return "", fmt.Errorf("int.max (%d) < int.min (%d)", max, min)
		}

		// exclude された数値は避ける
		candidates := make([]int, 0, max-min+1)
		for v := min; v <= max; v++ {
			if !excluded[strconv.Itoa(v)] {
				candidates = append(candidates, v)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no int available in [%d,%d]", min, max)
		}
		return strconv.Itoa(candidates[b.rng.Intn(len(candidates))]), nil

	case "choice":
		candidates, err := expandStringList(def["candidates"], vars)
		if err != nil {
			return "", err
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("choice.candidates is empty")
		}
		return candidates[b.rng.Intn(len(candidates))], nil

	case "morse_word":
		// NATOフォネティックコードの語を選ぶ。頭文字が対照表で色に対応するため、
		// 既に使われた切断線と衝突しない語だけを候補にする。
		candidates, err := expandStringList(def["candidates"], vars)
		if err != nil {
			return "", err
		}
		available := make([]string, 0, len(candidates))
		for _, word := range candidates {
			color := morseWordColor(word)
			if color != "" && !usedLines[color] {
				available = append(available, word)
			}
		}
		if len(available) == 0 {
			return "", fmt.Errorf("no morse word available (all mapped colors used)")
		}
		return available[b.rng.Intn(len(available))], nil

	default:
		return "", fmt.Errorf("unknown pick kind: %q", pickKind)
	}
}

// deriveVar は他の変数から値を機械的に導出する。
func (b *ScenarioBuilder) deriveVar(kind string, def map[string]any, vars map[string]string) (string, error) {
	switch kind {
	case "morse_word_color":
		// 語の頭文字 → モールス対照表の色 (09. 暗号電文)
		word, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		color := morseWordColor(word)
		if color == "" {
			return "", fmt.Errorf("cannot map morse word %q to a color", word)
		}
		return color, nil

	case "terminal_for_color":
		// 配線色 → 回路図シートの端子番号 (E. ブループリント)
		color, err := expandAny(def["from"], vars)
		if err != nil {
			return "", err
		}
		terminal := b.sheet.TerminalForColor(color)
		if terminal == "" {
			return "", fmt.Errorf("no terminal mapped to color %q (check [mission_sheet].terminal_map)", color)
		}
		return terminal, nil

	case "sheet_threshold":
		// 07. 運命の二択: シート実測値から閾値を決める。
		// 実測値と大きく離した閾値にして、数え間違いで誤答しないようにする
		// (docs/puzzle_stage_ideas.md §2.2)。
		branch, err := expandAny(def["branch"], vars)
		if err != nil {
			return "", err
		}
		actual, err := b.sheetValue(branch)
		if err != nil {
			return "", err
		}
		// 実測値から2離した値を閾値にする (28個なら30)
		return strconv.Itoa(actual + 2), nil

	case "sheet_direction":
		// 閾値との比較方向。実測値は必ず閾値未満になるよう組み立てるため "lt" 固定。
		// (テンプレートが閾値を実測値+2に置くため、シート集計は常に未満側に落ちる)
		branch, err := expandAny(def["branch"], vars)
		if err != nil {
			return "", err
		}
		if _, err := b.sheetValue(branch); err != nil {
			return "", err
		}
		return "lt", nil

	default:
		return "", fmt.Errorf("unknown derive kind: %q", kind)
	}
}

// sheetValue は分岐種別に対応するミッションシートの実測値を返す。
func (b *ScenarioBuilder) sheetValue(branch string) (int, error) {
	switch branch {
	case "symbol":
		if b.sheet.SymbolCount <= 0 {
			return 0, fmt.Errorf("[mission_sheet].symbol_count is not configured")
		}
		return b.sheet.SymbolCount, nil
	case "number":
		if b.sheet.NumberSum <= 0 {
			return 0, fmt.Errorf("[mission_sheet].number_sum is not configured")
		}
		return b.sheet.NumberSum, nil
	default:
		return 0, fmt.Errorf("unknown sheet branch: %q", branch)
	}
}

// morseWordColor は語の頭文字からモールス対照表の色を引く。
//
// 対照表は A-Z の各文字に5色を循環割当した固定印刷
// (docs/puzzle_stage_ideas.md §6): A=A(赤), B=B(黄), C=C(緑), D=D(青), E=E(白),
// F=A(赤), G=B(黄), ... となる。
func morseWordColor(word string) string {
	if word == "" {
		return ""
	}
	head := strings.ToUpper(word)[0]
	if head < 'A' || head > 'Z' {
		return ""
	}
	return allColors[int(head-'A')%len(allColors)]
}
