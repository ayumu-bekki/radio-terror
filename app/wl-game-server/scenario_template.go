package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// 難易度タグ (docs/operation_flow.md §9 決定1)
const (
	difficultyEasy   = "easy"
	difficultyNormal = "normal"
	difficultyHard   = "hard"
)

// 配線・LED・ボタン共通の色系統 (A=赤, B=黄, C=緑, D=青, E=白)
var allColors = []string{"A", "B", "C", "D", "E"}

// maxStagesPerSession は1セッションのステージ数の上限。
//
// 配線は5本あるが、5ステージにすると全色を使い切ってしまい、切断線の抽選に
// 余地が無くなる(最後のステージは残り1色に固定され、色の制約を持つステージが
// 組み立て不能になる)。**1本以上余らせる**ことでランダム性を保つ
// (docs/puzzle_stage_ideas.md §5)。
const maxStagesPerSession = 4

// colorNameJA は無線での読み上げ・ナビゲーター知識用の日本語色名。
var colorNameJA = map[string]string{
	"A": "赤",
	"B": "黄",
	"C": "緑",
	"D": "青",
	"E": "白",
}

// StageTemplate は1ステージの定義ファイル (scenarios/stages/*.toml)。
type StageTemplate struct {
	ID         string `toml:"id"`
	Name       string `toml:"name"`
	Difficulty string `toml:"difficulty"`
	// EasyOnly が true のステージはイージー以外では選出しない (101 解体デビュー)
	EasyOnly bool `toml:"easy_only"`

	// Random は組み立て時に解決される抽選変数の定義。
	// 値は { pick = "line" } のようなテーブル。
	Random map[string]map[string]any `toml:"random"`

	// Core はセッションJSONのステージ要素の生成規則 (${...} を含む)。
	Core map[string]any `toml:"core"`

	// Navigator はナビゲーター向けステージ知識 (${...} を含む)。
	Navigator map[string]string `toml:"navigator"`
}

// DifficultyTemplate は難易度テンプレート (scenarios/difficulty/*.toml)。
type DifficultyTemplate struct {
	Name            string `toml:"name"`
	CountdownMS     int    `toml:"countdown_ms"`
	DetonateDelayMS int    `toml:"detonate_delay_ms"`

	Compose   ComposeRule   `toml:"compose"`
	Crosstalk CrosstalkRule `toml:"crosstalk"`
	Hints     HintRule      `toml:"hints"`
}

// ComposeRule はステージ構成のハイブリッド指定 (固定並び + タグ抽選)。
type ComposeRule struct {
	FixedHead []string       `toml:"fixed_head"`
	FixedTail []string       `toml:"fixed_tail"`
	Random    map[string]int `toml:"random"` // タグ → 抽選数
}

// CrosstalkRule は混線の系統別再生回数上限 (docs/operation_flow.md §5.1)。
type CrosstalkRule struct {
	Jamming int `toml:"jamming"` // 邪魔者系
	Ambient int `toml:"ambient"` // 環境ボイス系
	Uneasy  int `toml:"uneasy"`  // 不穏系
}

// HintRule はヒント閾値のステージ予算に対する比率(%) (docs/navigator_design.md §3.2)。
// 0 の場合はそのレベルを無効にする (ハードの L4 など)。
type HintRule struct {
	L2Pct int `toml:"l2_pct"`
	L3Pct int `toml:"l3_pct"`
	L4Pct int `toml:"l4_pct"`
}

// ScenarioLibrary はロード済みのステージ・難易度テンプレートを保持する。
type ScenarioLibrary struct {
	stages       map[string]*StageTemplate
	difficulties map[string]*DifficultyTemplate
}

// LoadScenarioLibrary は scenarios/ 以下のテンプレートを読み込む。
//
// ステージ定義は1ステージ1ファイル (docs/scenario_design.md §1)。
// 拡張子が .toml のものだけを読むため、保留中のステージは .toml.disabled に
// しておけば置いたまま無効化できる。
func LoadScenarioLibrary(root string) (*ScenarioLibrary, error) {
	lib := &ScenarioLibrary{
		stages:       make(map[string]*StageTemplate),
		difficulties: make(map[string]*DifficultyTemplate),
	}

	stageDir := filepath.Join(root, "stages")
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return nil, fmt.Errorf("read stage dir %q: %w", stageDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		path := filepath.Join(stageDir, entry.Name())

		var stage StageTemplate
		if _, err := toml.DecodeFile(path, &stage); err != nil {
			return nil, fmt.Errorf("decode stage %q: %w", path, err)
		}
		if stage.ID == "" {
			return nil, fmt.Errorf("stage %q: id is required", path)
		}
		if _, dup := lib.stages[stage.ID]; dup {
			return nil, fmt.Errorf("stage %q: duplicated id %q", path, stage.ID)
		}
		lib.stages[stage.ID] = &stage
	}

	difficultyDir := filepath.Join(root, "difficulty")
	for _, name := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		path := filepath.Join(difficultyDir, name+".toml")

		var tmpl DifficultyTemplate
		if _, err := toml.DecodeFile(path, &tmpl); err != nil {
			return nil, fmt.Errorf("decode difficulty %q: %w", path, err)
		}
		lib.difficulties[name] = &tmpl
	}

	return lib, nil
}

// Difficulty は難易度テンプレートを返す。
func (l *ScenarioLibrary) Difficulty(name string) (*DifficultyTemplate, error) {
	tmpl, ok := l.difficulties[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errUnknownDifficulty, name)
	}
	return tmpl, nil
}

// Stage は ID からステージテンプレートを返す。
func (l *ScenarioLibrary) Stage(id string) (*StageTemplate, error) {
	stage, ok := l.stages[id]
	if !ok {
		return nil, fmt.Errorf("unknown stage id: %s", id)
	}
	return stage, nil
}

// StagesByTag は難易度タグに一致するステージIDを返す (ID順で決定的)。
// forEasy が false の場合、EasyOnly のステージは除外する。
func (l *ScenarioLibrary) StagesByTag(tag string, forEasy bool) []string {
	ids := make([]string, 0, len(l.stages))
	for id, stage := range l.stages {
		if stage.Difficulty != tag {
			continue
		}
		if stage.EasyOnly && !forEasy {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// StageCount はロード済みステージ数を返す (起動ログ・Web画面用)。
func (l *ScenarioLibrary) StageCount() int { return len(l.stages) }
