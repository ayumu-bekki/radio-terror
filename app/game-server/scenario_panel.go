package main

// 209 配電盤照合 で使うロータリー対照表 (docs/printed_materials.md §5)。
//
// **紙資料に印刷する内容そのもの**。表を書き換えたら資料も刷り直す。
// 202 の対照表 (scenario_codebook.go) と同じ方針で、値はここ1箇所にだけ置く。

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// panelRow はロータリー対照表の1行。
//
// プレイヤーは**今の見え方**から現在位置を割り出し、同じ行の
// 危険位置と解除位置を読む。
type panelRow struct {
	// lit / blink は現在位置での見え方 (点灯色 / 点滅色)
	lit   string
	blink string
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
// **「点灯色 + 点滅色」の組が現在位置を一意に示す**。プレイヤーは
// 「赤が点いて青が点滅しています」と報告するだけでよく、
// ナビゲーターが「ロータリーが0番にありませんか」と当てられる。
//
// **赤だけ点灯 (点滅なし) は使わない** — 危険位置に止まっている合図に
// 予約してある (panelDangerLit)。ダミー表示にも出さない。
//
// **危険位置は必ず現在位置と解除位置の「間」に置く。** 経路の外にあると
// 素通りできてしまい、「止まらずに通り抜ける」という 209 の緊張が消える。
// ロータリーは直線配置 (0-5) なので、間に置けば必ず通過する。
var panelTable = []panelRow{
	{lit: "B", blink: "A", position: 0, forbidden: 4, release: 5}, // 黄/赤
	{lit: "A", blink: "D", position: 1, forbidden: 4, release: 5}, // 赤/青
	{lit: "E", blink: "B", position: 2, forbidden: 3, release: 4}, // 白/黄
	{lit: "A", blink: "C", position: 3, forbidden: 1, release: 0}, // 赤/緑
	{lit: "D", blink: "A", position: 4, forbidden: 3, release: 1}, // 青/赤
	{lit: "B", blink: "E", position: 5, forbidden: 2, release: 0}, // 黄/白
}

// panelDangerLit は「危険位置に止まっている」ことを示す表示色。
//
// **この色が単独で点灯していたら危険位置**。対照表の行はすべて
// 点灯+点滅の2色なので、1色だけの表示は必ずこの意味になる。
const panelDangerLit = "A" // 赤

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
	return fmt.Sprintf("%s色が点灯・%s色が点滅", colorNameJA[r.lit], colorNameJA[r.blink])
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
		look := row.lit + "/" + row.blink
		if seenLook[look] {
			return fmt.Errorf("panel: 見え方 %s が重複している — 現在位置を特定できない", look)
		}
		seenLook[look] = true

		if row.lit == row.blink {
			return fmt.Errorf("panel: 位置 %d の点灯色と点滅色が同じ (%s) — "+
				"leds が1エントリに潰れる", row.position, row.lit)
		}
		// **赤だけ点灯は危険位置の合図**。表の行に単独の赤は現れない
		// (2色あるので単独にはならないが、点滅なしの行を作らせない歯止め)
		if row.blink == "" {
			return fmt.Errorf("panel: 位置 %d に点滅色が無い — "+
				"1色だけの表示は危険位置の合図に予約してある", row.position)
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
func (b *ScenarioBuilder) resolvePanel(def map[string]any, vars map[string]string) (string, error) {
	// **cut が対照表の点滅色と重なる行を避ける。**
	// 重なると現在位置の表示で cut が点滅し、解除位置へ回す前に
	// 答えが見えてしまう (回す工程が消える)。
	cut, err := expandAny(def["cut"], vars)
	if err != nil {
		return "", fmt.Errorf("panel.cut: %w", err)
	}
	cut = strings.TrimSpace(cut)

	candidates := make([]panelRow, 0, len(panelTable))
	for _, row := range panelTable {
		if row.blink == cut {
			continue
		}
		candidates = append(candidates, row)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("panel: 切る線 %q が全ての行の点滅色と重なる", cut)
	}
	row := candidates[b.rng.Intn(len(candidates))]
	return strings.Join([]string{
		strconv.Itoa(row.position),
		strconv.Itoa(row.forbidden),
		strconv.Itoa(row.release),
	}, ","), nil
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
		return row.lit, nil
	case "blink":
		return row.blink, nil
	case "appearance":
		return row.spokenAppearance(), nil
	}
	return "", fmt.Errorf("panel_field.field が不正: %q "+
		"(position/forbidden/release/lit/blink/appearance)", field)
}

// rotaryLedsPrefix は rotary_leds の展開結果であることを示す目印。
// expandCore がこの接頭辞を見て core["rotary_leds"] へ差し込む。
const rotaryLedsPrefix = "\x00rotary\x00"

// resolveRotaryLeds は**ロータリー位置ごとのLED表示**を組み立てる (209)。
//
// 位置ごとの見え方:
//
//	現在位置   … 対照表どおり (点灯色 + 点滅色)。ここから表を引く
//	解除位置   … **cut だけが点滅**。他はランダムに点灯/消灯
//	危険位置   … **赤だけ点灯**。止まると即爆発の合図
//	その他     … ランダム (ダミー。手がかりにならない)
//
// **解除位置の表示は対照表に載せない。** 載せると回す前に切る線が分かる。
func (b *ScenarioBuilder) resolveRotaryLeds(def map[string]any, vars map[string]string) (string, error) {
	cut, err := expandAny(def["cut"], vars)
	if err != nil {
		return "", fmt.Errorf("rotary_leds.cut: %w", err)
	}
	cut = strings.TrimSpace(cut)

	panel, err := expandAny(def["panel"], vars)
	if err != nil {
		return "", fmt.Errorf("rotary_leds.panel: %w", err)
	}
	parts := strings.Split(panel, ",")
	if len(parts) != 3 {
		return "", fmt.Errorf("rotary_leds: panel が不正: %q", panel)
	}
	position, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	forbidden, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	release, _ := strconv.Atoi(strings.TrimSpace(parts[2]))

	row, err := panelRowByPosition(position)
	if err != nil {
		return "", err
	}

	blinkMS, err := intFromDef(def, "blink_ms", 500, vars)
	if err != nil {
		return "", err
	}

	table := make(map[string]any, rotaryPositionNum)
	for pos := 0; pos < rotaryPositionNum; pos++ {
		leds := make(map[string]any, len(allColors))

		switch pos {
		case position:
			// 対照表どおりの見え方。ここから現在位置を割り出す。
			//
			// **cut が点滅色と重なると、回す前に答えが見えてしまう。**
			// 抽選側で cut を対照表の点滅色から外してあるが、
			// ここでも検査して取りこぼさない。
			leds[row.lit] = "on"
			leds[row.blink] = map[string]any{
				"pattern": "blink", "on_ms": blinkMS, "off_ms": blinkMS,
			}
		case release:
			// **cut だけが点滅**。他はランダムに点灯/消灯 (答えを埋もれさせる)
			leds[cut] = map[string]any{
				"pattern": "blink", "on_ms": blinkMS, "off_ms": blinkMS,
			}
			for _, color := range allColors {
				if color == cut {
					continue
				}
				if b.rng.Intn(2) == 0 {
					leds[color] = "on"
				}
			}
		case forbidden:
			// **赤だけ点灯** = 危険位置に止まっている合図
			leds[panelDangerLit] = "on"
		default:
			// ダミー。**1色だけの点灯にはしない** — 危険位置の合図と紛れる
			lit := b.rng.Intn(2) + 2 // 2〜3色
			shuffled := append([]string(nil), allColors...)
			b.rng.Shuffle(len(shuffled), func(i, j int) {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			})
			for i := 0; i < lit; i++ {
				leds[shuffled[i]] = "on"
			}
		}
		table[strconv.Itoa(pos)] = leds
	}

	encoded, err := json.Marshal(table)
	if err != nil {
		return "", fmt.Errorf("rotary_leds: %w", err)
	}
	return rotaryLedsPrefix + string(encoded), nil
}
