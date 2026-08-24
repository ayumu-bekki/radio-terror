package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
// LiteralVars は**色として変換してはいけない**抽選変数の名前を返す。
//
// ナビゲーター向けの展開は値が A-E なら色名へ置き換えるが (toJapaneseColorVars)、
// モールスで表示する1文字のように**色以外の意味で A-E を持つ**変数がある。
// 302 で "E" が色名「白」に化けて「白を探せ」と言い出した (ADR N-41)。
//
// 判定は**抽選の種類**から機械的に行う。TOML に書かせる方式にすると
// 書き忘れた時に同じ事故が静かに再発するため。
func (t *StageTemplate) LiteralVars() map[string]bool {
	literal := map[string]bool{}
	for name, def := range t.Random {
		kind, _ := def["pick"].(string)
		if kind == "morse_letters" {
			literal[name] = true
		}
		// nth は元の変数の性質を継ぐ (letters から取り出した1文字も色ではない)
		if derive, _ := def["derive"].(string); derive == "nth" {
			if from, ok := def["from"].(string); ok {
				if src := varPattern.FindStringSubmatch(from); len(src) == 2 {
					if srcDef, ok := t.Random[src[1]]; ok {
						if k, _ := srcDef["pick"].(string); k == "morse_letters" {
							literal[name] = true
						}
					}
				}
			}
		}
	}
	return literal
}

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

	// KeepCutSecret は**L4 (直言) でも切る線の色名を伏せ続ける**指定 (ADR N-38)。
	//
	// 色名を言うと**課題そのものが消える**ステージに付ける。
	// 203 ブループリント (回路図シートを読む工程)、301 LED照合 (資料3枚の読み解き)、
	// 202 暗号電文 (モールス解読) が該当する。
	//
	// **散文の但し書きに頼らない。** これらのステージは answer に
	// 「こちらからは言わない」と書いてあったが、L4 では answer が
	// 生の色名込みでプロンプトに載り、ヒントポリシーが
	// 「正解をそのまま伝えてよい」と指示するため**但し書きが負ける**。
	// 目の前にある語はなぞられる (N-1)。フラグで機械的に伏せる。
	KeepCutSecret bool `toml:"keep_cut_secret"`

	// Hints は難易度テンプレートのヒント閾値に対する**ステージ単位の上書き**。
	//
	// 既定 (未指定) は難易度テンプレートの値をそのまま使う。
	// 上書きが要るのは「そのレベルがステージを壊す」場合だけ (ADR N-36)。
	// 204 色合わせは**正解がプレイヤーの記憶の中にしかない**ため、
	// L4 (正解の直言) が課題そのものを消してしまう。`l4_pct = 0` で塞ぐ。
	Hints StageHintOverride `toml:"hints"`
}

// StageHintOverride はヒント閾値のステージ単位の上書き (ADR N-36)。
//
// **項目ごとに任意**。書いた項目だけが上書きされ、残りは難易度テンプレートの
// 値をそのまま使う。`0` は「そのレベルの無効化」という**意味を持つ値**
// (ハードの `l4_pct = 0`) なので、「0 なら未指定」とは読めない。
// ポインタにして**書かれたかどうか**を型で区別する。
type StageHintOverride struct {
	L2Pct *int `toml:"l2_pct"`
	L3Pct *int `toml:"l3_pct"`
	L4Pct *int `toml:"l4_pct"`
}

// Apply は難易度のヒント閾値へこの上書きを適用した結果を返す。
// 未指定の項目は base のまま。
func (o StageHintOverride) Apply(base HintRule) HintRule {
	if o.L2Pct != nil {
		base.L2Pct = *o.L2Pct
	}
	if o.L3Pct != nil {
		base.L3Pct = *o.L3Pct
	}
	if o.L4Pct != nil {
		base.L4Pct = *o.L4Pct
	}
	return base
}

// DifficultyTemplate は難易度テンプレート (scenarios/difficulty/*.toml)。
type DifficultyTemplate struct {
	Name            string `toml:"name"`
	CountdownMS     int    `toml:"countdown_ms"`
	DetonateDelayMS int    `toml:"detonate_delay_ms"`

	Compose   ComposeRule   `toml:"compose"`
	Crosstalk CrosstalkRule `toml:"crosstalk"`
	Hints     HintRule      `toml:"hints"`
	Load      LoadRule      `toml:"load"`
}

// LoadRule は**難易度で変えたい入力量**。
//
// ステージ定義は `${load_color_match_min}` のように参照する
// (docs/scenario_design.md §3.1)。**ステージ側に数値を直書きしない** —
// 直書きすると難易度を跨いで同じ負荷になり、イージーとハードで
// 同じ回数を押させることになる。
//
// 0 のままだと「未設定」と区別できないため、参照された時点で
// **0 は組み立てエラーにする**。難易度テンプレートに書き忘れたまま
// 抽選が通ってしまうのを防ぐ。
type LoadRule struct {
	// ColorMatchMin / ColorMatchMax は色合わせ (206) で押す回数の範囲。
	//
	// **無線で口頭確認できない量**にはしない。押し終えるまで手がかりが
	// 増えないため、多すぎると「まだ終わらない」だけの時間になる。
	ColorMatchMin int `toml:"color_match_min"`
	ColorMatchMax int `toml:"color_match_max"`

	// ForbiddenRotaryCount は 206 綱渡り の禁止位置の数。
	//
	// 1つなら「関門を1回通過する」、2つなら**配置によって質が変わる**
	// (隣接=連続通過 / 離れている=2段構え / 目的地を挟む=行き過ぎ厳禁)。
	// 配置は `pick = "rotary_layout"` が成立するものだけを列挙して引く。
	ForbiddenRotaryCount int `toml:"forbidden_rotary_count"`

	// PushSeqLen は 201 復唱 のボタン列の長さ。
	//
	// **無線で1回聞いて覚えられる長さ**が基準 (ノーマル5個)。
	// ハードは8個まで伸ばす。伸ばしすぎると記憶ではなく
	// 「読み上げを何度も聞き直す」時間になる。
	PushSeqLen int `toml:"push_seq_len"`

	// SpeedRankMS は 205 速さくらべ の点滅速度 (速い順に4段階)。
	//
	// **隣接比を詰めるほど難しい**。ノーマルは 150/300/550/1000
	// (隣接1.82〜2.0倍)、ハードは 150/210/294/412 (隣接1.4倍均一)。
	//
	// ハードは 104 早い者勝ち で決めた下限 (1.5倍) を**意図的に下回る**。
	// 104 は2択だが 205 は4色を並べ替えるため、同じ隣接比でも難しい。
	// **実機で見え方を確認してから確定させること**。
	SpeedRankMS []int `toml:"speed_rank_ms"`
}

// Validate は入力量が設定済みかを確かめる。
//
// **0 のまま参照されると「押す回数0回」という成立しないステージになる**。
// 難易度テンプレートへの書き忘れは抽選次第でしか現れないため、
// 起動時に落とす (terminal_map の5色必須と同じ性質)。
func (l LoadRule) Validate(difficulty string) error {
	if l.ColorMatchMin <= 0 || l.ColorMatchMax <= 0 {
		return fmt.Errorf(
			"difficulty %q: [load] color_match_min/max が未設定 (どちらも1以上が必要)",
			difficulty)
	}
	if l.ColorMatchMax < l.ColorMatchMin {
		return fmt.Errorf(
			"difficulty %q: [load] color_match_max (%d) < color_match_min (%d)",
			difficulty, l.ColorMatchMax, l.ColorMatchMin)
	}
	// 禁止位置は 1 か 2 のみ。3つ以上にすると 0-5 の直線配置では
	// 逃げ場が足りず、成立する配置がほとんど残らない。
	if l.ForbiddenRotaryCount != 1 && l.ForbiddenRotaryCount != 2 {
		return fmt.Errorf(
			"difficulty %q: [load] forbidden_rotary_count は 1 か 2 (現在 %d)",
			difficulty, l.ForbiddenRotaryCount)
	}
	// 押下列は最低3個 (2個以下だと「順番を覚える」課題にならない)。
	// 上限は8個 — それ以上は無線で1回聞いて覚えられず、
	// 読み上げを聞き直す時間だけが伸びる。
	if l.PushSeqLen < 3 || l.PushSeqLen > 8 {
		return fmt.Errorf(
			"difficulty %q: [load] push_seq_len は 3〜8 (現在 %d)",
			difficulty, l.PushSeqLen)
	}
	// 205 速さくらべ は点滅4色。速い順に並んでいること。
	if len(l.SpeedRankMS) != 4 {
		return fmt.Errorf(
			"difficulty %q: [load] speed_rank_ms は4段階 (現在 %d個)",
			difficulty, len(l.SpeedRankMS))
	}
	for i, ms := range l.SpeedRankMS {
		if ms <= 0 {
			return fmt.Errorf("difficulty %q: [load] speed_rank_ms[%d] が %d", difficulty, i, ms)
		}
		if 0 < i && ms <= l.SpeedRankMS[i-1] {
			return fmt.Errorf(
				"difficulty %q: [load] speed_rank_ms は**速い順**に並べる (%v)",
				difficulty, l.SpeedRankMS)
		}
	}
	return nil
}

// loadVars は難易度の入力量をテンプレート変数として返す。
//
// **ナビゲーター知識にもCore向けJSONにも同じ値が入る** — 回数は
// 装置の挙動そのもので、両者がずれると事故になる。
func (l LoadRule) loadVars() map[string]string {
	return map[string]string{
		"load_color_match_min":        strconv.Itoa(l.ColorMatchMin),
		"load_color_match_max":        strconv.Itoa(l.ColorMatchMax),
		"load_forbidden_rotary_count": strconv.Itoa(l.ForbiddenRotaryCount),
		"load_push_seq_len":           strconv.Itoa(l.PushSeqLen),
		"load_speed_rank_1":           speedRankAt(l.SpeedRankMS, 0),
		"load_speed_rank_2":           speedRankAt(l.SpeedRankMS, 1),
		"load_speed_rank_3":           speedRankAt(l.SpeedRankMS, 2),
		"load_speed_rank_4":           speedRankAt(l.SpeedRankMS, 3),
	}
}

// speedRankAt は速度段階を文字列で返す。範囲外は空文字
// (Validate が起動時に落とすので、ここでは値を作らない)。
func speedRankAt(ms []int, i int) string {
	if i < 0 || len(ms) <= i {
		return ""
	}
	return strconv.Itoa(ms[i])
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
