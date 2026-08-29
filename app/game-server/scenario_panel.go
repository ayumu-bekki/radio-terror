package main

// 209 配電盤照合 で使うロータリー対照表 (docs/printed_materials.md §5)。
//
// **紙資料に印刷する内容そのもの**。表を書き換えたら資料も刷り直す。
// 202 の対照表 (scenario_codebook.go) と同じ方針で、値はここ1箇所にだけ置く。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// panelRow はロータリー対照表の1行。
//
// プレイヤーは**今の見え方**から現在位置を割り出し、同じ行の
// 危険位置と解除位置を読む。
type panelRow struct {
	// lit は現在位置での見え方 (**点灯色の組。2〜3色**)。
	//
	// **点滅は使わない** (決定91)。点滅は「切る線」の合図に予約してあり、
	// 見え方に点滅を混ぜると **cut と衝突して回す前に答えが見える**。
	// 見え方は点灯色の組合せだけで一意にする。
	lit []string
	// position はこの見え方が示すロータリー位置 (0-5)
	position int
	// forbidden は止まると即爆発する位置
	forbidden int
	// release は切断できる位置。**ここでの表示は表に載せない** —
	// 載せると回す前に切る線が分かってしまう
	release int
}

// panelTable はロータリー位置6通りの対照表。
//
// **点灯色の組が現在位置を一意に示す**。プレイヤーは
// 「赤と黄色と緑が点いています」と報告するだけでよく、
// ナビゲーターが位置を割り出せる。
//
// **点滅は載せない** (決定91)。点滅は解除位置でのみ出る「切る線」の合図に
// 予約してある。こうすると **cut は5色どれでも使える** —
// 見え方に点滅が無いので、どの色を切ることになっても
// 現在位置の表示と衝突しない。
//
// **赤だけ点灯 (1色) は使わない** — 危険位置に止まっている合図に
// 予約してある (panelDangerLit)。全行を2色以上にして紛れを防ぐ。
// 上限は3色 — 無線で数えて報告できる量に収める。
//
// **危険位置は必ず現在位置と解除位置の「間」に置く。** 経路の外にあると
// 素通りできてしまい、「止まらずに通り抜ける」という 209 の緊張が消える。
// ロータリーは直線配置 (0-5) なので、間に置けば必ず通過する。
//
// **色の登場回数は全色3回で均等**にしてある。偏らせると
// 「この色が出たらあの辺」と位置を連想できてしまう。
var panelTable = []panelRow{
	{lit: []string{"B", "E"}, position: 0, forbidden: 1, release: 3},      // 黄+白
	{lit: []string{"A", "E"}, position: 1, forbidden: 4, release: 5},      // 赤+白
	{lit: []string{"A", "B", "C"}, position: 2, forbidden: 3, release: 4}, // 赤+黄+緑
	{lit: []string{"A", "C", "D"}, position: 3, forbidden: 1, release: 0}, // 赤+緑+青
	{lit: []string{"C", "D"}, position: 4, forbidden: 2, release: 1},      // 緑+青
	{lit: []string{"B", "D", "E"}, position: 5, forbidden: 3, release: 2}, // 黄+青+白
}

// panelDangerLit は「危険位置に止まっている」ことを示す表示色。
//
// **この色が単独で点灯していたら危険位置**。対照表の行はすべて
// 2色以上なので、1色だけの表示は必ずこの意味になる。
const panelDangerLit = "A" // 赤

// panelLitMin/Max は現在位置の表示に使う点灯色数の範囲。
//
//	下限2 … 1色は危険位置の合図に予約 (panelDangerLit)
//	上限3 … 無線で数えて報告できる量に収める
const (
	panelLitMin = 2
	panelLitMax = 3
)

// panelRowByPosition は現在位置から対照表の行を引く。
func panelRowByPosition(position int) (panelRow, error) {
	for _, row := range panelTable {
		if row.position == position {
			return row, nil
		}
	}
	return panelRow{}, fmt.Errorf("panel: 位置 %d が対照表に無い", position)
}

// panelRowsText は対照表をナビゲーター向けの1行テキストにする。
//
// **表の中身をナビゲーターに渡さない** — 資料を読むのはプレイヤーの仕事で、
// ナビゲーターが表を持つと「0番ですね、危険は4です」と全部言えてしまう。
// 渡すのは**抽選された1行だけ**。
func (r panelRow) spokenAppearance() string {
	names := make([]string, 0, len(r.lit))
	for _, c := range r.lit {
		names = append(names, colorNameJA[c])
	}
	return fmt.Sprintf("%s色が点灯", strings.Join(names, "・"))
}

// litKey は点灯色の組を比較用のキーにする (順序を問わない)。
func (r panelRow) litKey() string {
	sorted := append([]string(nil), r.lit...)
	sort.Strings(sorted)
	return strings.Join(sorted, "")
}

// validatePanelTable は対照表が謎として成立するかを起動時に確かめる。
//
// **抽選次第でしか再現しない不具合**になるため、組み立て時ではなく起動時に落とす。
func validatePanelTable() error {
	if len(panelTable) != rotaryPositionNum {
		return fmt.Errorf("panel: 対照表は %d 行必要 (現在 %d 行)", rotaryPositionNum, len(panelTable))
	}

	seenPos := make(map[int]bool, len(panelTable))
	seenLook := make(map[string]bool, len(panelTable))
	for _, row := range panelTable {
		if row.position < 0 || rotaryPositionNum <= row.position {
			return fmt.Errorf("panel: 位置 %d が範囲外 (0-%d)", row.position, rotaryPositionNum-1)
		}
		if seenPos[row.position] {
			return fmt.Errorf("panel: 位置 %d が重複している", row.position)
		}
		seenPos[row.position] = true

		// **見え方が重複すると現在位置を一意に割り出せない**
		look := row.litKey()
		if seenLook[look] {
			return fmt.Errorf("panel: 見え方 %s が重複している — 現在位置を特定できない", look)
		}
		seenLook[look] = true

		// **点灯色数の範囲**。1色は危険位置の合図に予約してあり、
		// 4色以上は無線で数えきれない。
		if len(row.lit) < panelLitMin || panelLitMax < len(row.lit) {
			return fmt.Errorf("panel: 位置 %d の点灯色が %d 色 — %d〜%d 色にする "+
				"(1色は危険位置の合図、4色以上は数えられない)",
				row.position, len(row.lit), panelLitMin, panelLitMax)
		}
		// **1色だけの表示は危険位置の合図**なので、対照表の行が
		// その見え方と一致してはいけない (デバイス側 kPanelDangerColor と対)
		if len(row.lit) == 1 && row.lit[0] == panelDangerLit {
			return fmt.Errorf("panel: 位置 %d が危険位置の合図 (%s 単独) と同じ見え方",
				row.position, panelDangerLit)
		}

		// 同じ色を重複して並べない (leds が1エントリに潰れる)
		seenColor := make(map[string]bool, len(row.lit))
		for _, c := range row.lit {
			if seenColor[c] {
				return fmt.Errorf("panel: 位置 %d の点灯色 %s が重複している", row.position, c)
			}
			seenColor[c] = true
			if colorNameJA[c] == "" {
				return fmt.Errorf("panel: 位置 %d に未知の色 %q", row.position, c)
			}
		}

		if row.forbidden == row.position {
			return fmt.Errorf("panel: 位置 %d の危険位置が自分自身", row.position)
		}
		if row.release == row.position {
			return fmt.Errorf("panel: 位置 %d の解除位置が自分自身 — 回す工程が消える", row.position)
		}
		if row.release == row.forbidden {
			return fmt.Errorf("panel: 位置 %d の解除位置が危険位置と同じ — 到達できない", row.position)
		}
		// **危険位置は現在位置と解除位置の「間」にあること。**
		// 経路の外だと素通りでき、「止まらずに通り抜ける」緊張が消える。
		lo, hi := row.position, row.release
		if hi < lo {
			lo, hi = hi, lo
		}
		if row.forbidden <= lo || hi <= row.forbidden {
			return fmt.Errorf(
				"panel: 位置 %d の危険位置 %d が経路 (%d〜%d) の外 — "+
					"素通りできてしまう", row.position, row.forbidden, lo, hi)
		}
		if row.forbidden < 0 || rotaryPositionNum <= row.forbidden {
			return fmt.Errorf("panel: 位置 %d の危険位置 %d が範囲外", row.position, row.forbidden)
		}
		if row.release < 0 || rotaryPositionNum <= row.release {
			return fmt.Errorf("panel: 位置 %d の解除位置 %d が範囲外", row.position, row.release)
		}
	}
	return nil
}

// rotaryPositionNum はロータリーの接点数 (0-5)。
// ファームの kRotaryPositionNum と揃える。
const rotaryPositionNum = 6

// resolvePanel は現在位置を抽選し、"position,forbidden,release" を返す
// (209 配電盤照合)。
//
// **cut は別に抽選する**。解除位置での表示は表に載っていないので、
// 切る線と対照表は独立でよい。
// **現在位置は抽選しない** (決定91)。ロータリーは前のプレイから
// 物理的に位置が残っており、サーバーがそこへ動かす手段が無い。
// 抽選するとツマミの実位置とずれ、プレイヤーが正しく資料を読んでも
// 別の行を引くことになる (実運用で爆発した)。
//
// 代わりに **6行すべてを Core へ渡し、Core がステージ開始時点の
// 実位置で行を確定する**。ここでは検証だけ行う。
func (b *ScenarioBuilder) resolvePanel(def map[string]any, vars map[string]string) (string, error) {
	// cut がどの行とも衝突しないことを確かめる。
	//
	// **見え方は点灯のみ**なので、cut が点灯色に現れても答えは見えない
	// (点滅こそが「切れ」の合図)。したがって 5色どれでも使えるが、
	// 表を書き換えて点滅を混ぜた場合に気づけるよう検査を残す。
	cut, err := expandAny(def["cut"], vars)
	if err != nil {
		return "", fmt.Errorf("panel.cut: %w", err)
	}
	cut = strings.TrimSpace(cut)
	if colorNameJA[cut] == "" {
		return "", fmt.Errorf("panel: 切る線の色 %q が不正", cut)
	}
	return panelRowsSpec(), nil
}

// panelRowsSpec は6行ぶんの "position:forbidden:release" を並べた文字列を返す。
// Core はこれを受け取り、**開始時点の実位置**に対応する行を使う。
func panelRowsSpec() string {
	parts := make([]string, 0, len(panelTable))
	for _, row := range panelTable {
		parts = append(parts, fmt.Sprintf("%d:%d:%d", row.position, row.forbidden, row.release))
	}
	return strings.Join(parts, ",")
}

// derivePanelField は選ばれた行から1項目を取り出す (209 配電盤照合)。
func (b *ScenarioBuilder) derivePanelField(def map[string]any, vars map[string]string) (string, error) {
	from, err := expandAny(def["from"], vars)
	if err != nil {
		return "", fmt.Errorf("panel_field.from: %w", err)
	}
	field, err := expandAny(def["field"], vars)
	if err != nil {
		return "", fmt.Errorf("panel_field.field: %w", err)
	}

	parts := strings.Split(from, ",")
	if len(parts) != 3 {
		return "", fmt.Errorf("panel_field: from が不正: %q", from)
	}
	position, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return "", fmt.Errorf("panel_field: 位置が数値でない: %q", parts[0])
	}
	row, err := panelRowByPosition(position)
	if err != nil {
		return "", err
	}

	switch strings.TrimSpace(field) {
	case "position":
		return strconv.Itoa(row.position), nil
	case "forbidden":
		return strconv.Itoa(row.forbidden), nil
	case "release":
		return strconv.Itoa(row.release), nil
	case "lit":
		return strings.Join(row.lit, ","), nil
	case "appearance":
		return row.spokenAppearance(), nil
	}
	return "", fmt.Errorf("panel_field.field が不正: %q "+
		"(position/forbidden/release/lit/appearance)", field)
}

// rotaryLedsPrefix は rotary_leds の展開結果であることを示す目印。
// expandCore がこの接頭辞を見て core["rotary_leds"] へ差し込む。
const rotaryLedsPrefix = "\x00rotary\x00"

// resolveRotaryLeds は**ロータリー位置ごとのLED表示**を組み立てる (209)。
//
// **表示は対照表そのもの**にする。位置 p の表示 = 対照表の p 行目の点灯色。
//
//	どの位置でも … その位置の点灯色 (2〜3色) が点灯
//
// **解除位置・危険位置の表示は Core が実行時に差し替える** (決定91)。
// 開始位置が分からないと解除位置も危険位置も決まらないため、
// サーバーは「どの位置に居ても読める表」だけを渡し、
// Core が開始時点の行を確定してから上書きする。
//
//	解除位置 … **cut だけが点滅** (Core が差し替え)
//	危険位置 … **赤だけ点灯** (Core が差し替え)
//
// **解除位置の表示は対照表に載せない。** 載せると回す前に切る線が分かる。
func (b *ScenarioBuilder) resolveRotaryLeds(def map[string]any, vars map[string]string) (string, error) {
	cut, err := expandAny(def["cut"], vars)
	if err != nil {
		return "", fmt.Errorf("rotary_leds.cut: %w", err)
	}
	cut = strings.TrimSpace(cut)
	if colorNameJA[cut] == "" {
		return "", fmt.Errorf("rotary_leds: 切る線の色 %q が不正", cut)
	}

	table := make(map[string]any, rotaryPositionNum)
	for pos := 0; pos < rotaryPositionNum; pos++ {
		row, err := panelRowByPosition(pos)
		if err != nil {
			return "", err
		}
		leds := make(map[string]any, len(row.lit))
		for _, c := range row.lit {
			leds[c] = "on"
		}
		table[strconv.Itoa(pos)] = leds
	}

	encoded, err := json.Marshal(table)
	if err != nil {
		return "", fmt.Errorf("rotary_leds: %w", err)
	}
	return rotaryLedsPrefix + string(encoded), nil
}
