package main

import (
	"strings"
	"testing"
)

// TestDetectInvalidColor は5色以外の色名だけを拾うことを確認する (決定124)。
func TestDetectInvalidColor(t *testing.T) {
	cases := map[string]string{
		"茶色が点灯しています":  "茶色",
		"ちゃいろが光ってる":   "ちゃいろ",
		"紫のランプです":     "紫",
		"赤と黄が点いています":  "",
		"緑が点滅、青と白は消灯": "",
		"黄緑っぽいです":     "黄緑",
		"紺のランプ":       "紺",
		// LEDの見え方として言われうる語は拾わない (正しい報告を爆発させない)
		"オレンジが光っています": "",
		"橙色です":        "",
		"水色です":        "",
	}
	for text, want := range cases {
		if got := detectInvalidColor(text); got != want {
			t.Errorf("detectInvalidColor(%q) = %q, want %q", text, got, want)
		}
	}
}

// TestNoteReportCountsPerStage は回数がステージ内で積み上がり、
// 該当しない発話で LastWrongReport が空へ戻り、Reset で数え直すことを確認する。
func TestNoteReportCountsPerStage(t *testing.T) {
	var p StageProgress
	p.NoteReport("茶色です", nil)
	p.NoteReport("ランプが1つ光っています", nil)
	if p.WrongReportCount != 1 || p.LastWrongReport != "" {
		t.Fatalf("count=%d last=%q", p.WrongReportCount, p.LastWrongReport)
	}
	p.NoteReport("やっぱり茶色です", nil)
	if p.WrongReportCount != 2 || p.LastWrongReport != "茶色" {
		t.Fatalf("count=%d last=%q", p.WrongReportCount, p.LastWrongReport)
	}
	p.Reset()
	if p.WrongReportCount != 0 || p.LastWrongReport != "" {
		t.Fatal("Reset で数え直していない")
	}
}

// TestInvalidColorBlockEscalates は1回目は復唱・確認のみ、2回目で
// 不正解の線を名指しすることを確認する。1回目に不正解の線を渡さないこと
// (常に渡すと生成AIが勝手に使いうる)。
func TestInvalidColorBlockEscalates(t *testing.T) {
	session := &BuiltSession{Stages: []*BuiltStage{{Cut: "A"}}}
	first := wrongReportBlock(NavigatorPromptInput{
		Session: session, WrongReport: "茶色", WrongReportCount: 1,
	})
	if !strings.Contains(first, "1回目") || strings.Contains(first, "線を切るよう指示") {
		t.Errorf("1回目の指示がおかしい:\n%s", first)
	}
	if strings.Contains(first, "黄色") {
		t.Errorf("1回目に不正解の線の色が渡っている:\n%s", first)
	}
	second := wrongReportBlock(NavigatorPromptInput{
		Session: session, WrongReport: "茶色", WrongReportCount: 2,
	})
	if !strings.Contains(second, "黄色の線を切るよう指示") {
		t.Errorf("2回目に不正解の線 (黄) を名指ししていない:\n%s", second)
	}
}

// TestUndoReportRollsBackFailedReply は、応答の生成に失敗した報告が
// 数から外れることを確かめる (決定131)。
func TestUndoReportRollsBackFailedReply(t *testing.T) {
	var p StageProgress
	p.NoteReport("茶色が光ってる", nil)
	p.UndoReport(p.LastWrongReport, p.WrongReportCount)
	if p.WrongReportCount != 0 {
		t.Fatalf("取り消し後 = %d, want 0", p.WrongReportCount)
	}

	// 応答待ちの間に次の報告が届いて数が進んでいたら触らない
	p.NoteReport("茶色が光ってる", nil)
	word, count := p.LastWrongReport, p.WrongReportCount
	p.NoteReport("やっぱり茶色", nil)
	p.UndoReport(word, count)
	if p.WrongReportCount != 2 {
		t.Fatalf("後続の報告があるのに取り消した: %d, want 2", p.WrongReportCount)
	}

	// 5色以外の色名が無かった発話は何もしない
	p.Reset()
	p.NoteReport("赤が光ってる", nil)
	p.UndoReport(p.LastWrongReport, p.WrongReportCount)
	if p.WrongReportCount != 0 {
		t.Fatalf("= %d, want 0", p.WrongReportCount)
	}
}

// TestCheckLampReport は、表示と合わない報告 (光らない色・点灯と点滅の
// 取り違え) だけを拾うことを確かめる (決定135・決定137)。線・ボタンの色、
// 消えている色への言及、言い直しの前半、光り方の分からない言い方は拾わない。
func TestCheckLampReport(t *testing.T) {
	// 白が点灯、青が点滅している (304 相当)
	states := LampStates{"E": {lampOn: true}, "D": {lampBlink: true}}
	cases := []struct {
		text    string
		wrong   string
		correct string
	}{
		{"緑が点灯しています", "緑", ""},
		{"緑です。どうぞ", "緑", ""},
		{"みどりのランプがついてます", "緑", ""},
		{"白が点灯しています", "", "白"},
		{"白いランプが1つ光ってます", "", "白"},
		{"白が点滅しています", "白が点滅", ""},
		{"青が点灯しています", "青が点灯", ""},
		{"青がチカチカしてます", "", "青"},
		{"青が点いたり消えたりしてます", "", "青"},
		{"青が光ってます", "", "青"}, // 光り方の分からない言い方は色だけ見る
		{"白が点きっぱなしで、青が点滅してます", "", "白"},
		{"赤は消えています。白が光ってます", "", "白"},
		{"緑じゃなくて白でした", "", "白"},
		{"緑の線を切ればいいですか", "", ""},
		{"赤のボタンを押しました", "", ""},
		{"資料で引いたら赤でした", "", ""},
		{"表を引きました。ダイヤルは1、基準色は緑です", "", ""},
		{"ランプが1つ光っています", "", ""},
		{"オレンジが光ってます", "黄", ""},
		{"黄緑に光ってます", "", ""}, // 5色以外の色名は別に数える
	}
	for _, c := range cases {
		wrong, correct := checkLampReport(c.text, states)
		if wrong != c.wrong || correct != c.correct {
			t.Errorf("checkLampReport(%q) = (%q, %q), want (%q, %q)",
				c.text, wrong, correct, c.wrong, c.correct)
		}
	}
	if wrong, _ := checkLampReport("緑です", nil); wrong != "" {
		t.Errorf("states=nil (判定しない課題) で %q を拾った", wrong)
	}
}

// TestLampStatesFollowFirmware は、光り方の集合がファームの表示に合うことを見る。
// 207 は5色とも点滅 (点灯は誤り)、209 は解除位置で切る線が点滅し危険位置で赤が点灯、
// 201 は押し切ると切る線が点灯する。
func TestLampStatesFollowFirmware(t *testing.T) {
	lib := loadTestLibrary(t)
	build := func(id string) *BuiltStage {
		b, err := simBuildStage(lib, testMissionSheet(), id, 42)
		if err != nil {
			t.Fatal(id, err)
		}
		return b.Stages[0]
	}
	s207 := stageLampStates(build("207"))
	for _, line := range allColors {
		if !s207[line][lampBlink] || s207[line][lampOn] {
			t.Errorf("207 %s: %v (5色とも点滅だけのはず)", line, s207[line])
		}
	}
	st := build("209")
	s209 := stageLampStates(st)
	if !s209[st.Cut][lampBlink] || !s209["A"][lampOn] {
		t.Errorf("209: 切る線の点滅・赤の点灯が無い: %v", s209)
	}
	st = build("201")
	if !stageLampStates(st)[st.Cut][lampOn] {
		t.Errorf("201: 押し切ったあとの切る線の点灯が無い")
	}
}

// TestStageShownColors は、課題ごとの「光りうる色」を確かめる。
// 押し切ると現れる色 (201) と、ダイヤル位置ごとの表示 (209) を含め、
// 表示が押すたびに変わる 204 は判定しない。
func TestStageShownColors(t *testing.T) {
	lib := loadTestLibrary(t)
	for _, id := range lib.StageIDs() {
		for seed := int64(0); seed < 10; seed++ {
			built, err := simBuildStage(lib, testMissionSheet(), id, seed)
			if err != nil {
				t.Fatal(id, err)
			}
			stage := built.Stages[0]
			shown := stageShownColors(stage)
			if id == "204" {
				if shown != nil {
					t.Errorf("204 は判定しない (nil) はず: %v", shown)
				}
				continue
			}
			if len(shown) == 0 {
				t.Errorf("%s seed=%d: 光る色が1つも無い", id, seed)
			}
			// 切る線がランプに現れる課題は、その色を必ず含む
			// (202・301・302・305 は資料から引くのでランプに出るとは限らない)
			switch id {
			case "202", "301", "302", "305":
			default:
				if !shown[stage.Cut] {
					t.Errorf("%s seed=%d: 切る線 %s が光る色に無い: %v", id, seed, stage.Cut, shown)
				}
			}
		}
	}
}

// TestSimScriptsDoNotTriggerUnshownColor は、シミュレーション台本の正しい報告が
// 「光っていない色」と誤判定されないことを確かめる (決定135)。
// 台本は実際の表示に沿って書いてあるので、ここで拾えば誤検知。
// 台本には表示を固定で書いたもの (301 の「黄色と緑」) があるため、
// シミュレーション本番と同じ抽選 (seed 42) で見る。
func TestSimScriptsDoNotTriggerUnshownColor(t *testing.T) {
	lib := loadTestLibrary(t)
	for id, script := range simScripts {
		for _, seed := range []int64{42} {
			built, err := simBuildStage(lib, testMissionSheet(), id, seed)
			if err != nil {
				continue // 無効化されたステージ
			}
			stage := built.Stages[0]
			vars := simStageVars(lib, stage)
			for _, turn := range script.Turns {
				if turn.Player == "" || strings.Contains(turn.Player, "${sim_wrong_color}") {
					continue
				}
				player := expandSimText(turn.Player, vars)
				if wrong, _ := checkLampReport(player, stageLampStates(stage)); wrong != "" {
					t.Errorf("%s seed=%d: 正しい報告を誤判定 (%s): %q", id, seed, wrong, player)
				}
			}
		}
	}
}

// TestNoteReportCountsConsecutive は、表示と合わない報告を**続いた回数**で
// 数えることを確かめる (決定136・決定137)。正しい報告で0に戻り、
// ランプの報告ではない発話では戻らない。正しい言い直しは CorrectedFrom/To に入る。
func TestNoteReportCountsConsecutive(t *testing.T) {
	states := LampStates{"C": {lampOn: true}} // 緑だけが点灯

	var p StageProgress
	p.NoteReport("赤が点灯しています", states)
	p.NoteReport("すみません。緑でした", states)
	if p.CorrectedFrom != "赤" || p.CorrectedTo != "緑" || p.WrongReportCount != 0 {
		t.Errorf("正しい言い直し: from=%q to=%q count=%d", p.CorrectedFrom, p.CorrectedTo, p.WrongReportCount)
	}
	p.NoteReport("赤が点灯しています", states)
	if p.WrongReportCount != 1 {
		t.Errorf("正しい報告のあとの誤り = %d, want 1 (連続ではない)", p.WrongReportCount)
	}
	p.NoteReport("どうすればいいですか", states)
	if p.WrongReportCount != 1 || p.CorrectedTo != "" {
		t.Errorf("報告ではない発話で数が変わった: %d", p.WrongReportCount)
	}
	p.NoteReport("赤が点灯しています", states)
	if p.WrongReportCount != 2 {
		t.Errorf("同じ誤りが続いた = %d, want 2", p.WrongReportCount)
	}
	p.NoteReport("緑が点滅しています", states)
	if p.WrongReportCount != 1 || p.LastWrongReport != "緑が点滅" || !p.LastWrongIsMismatch {
		t.Errorf("違う誤り (光り方の取り違え) は1から: count=%d last=%q", p.WrongReportCount, p.LastWrongReport)
	}
	p.NoteReport("緑がチカチカしてます", states)
	if p.WrongReportCount != 2 {
		t.Errorf("同じ光り方の取り違えが続いた = %d, want 2", p.WrongReportCount)
	}

	p.Reset()
	p.NoteReport("赤が点灯しています", states)
	p.NoteReport("すみません。青でした", states)
	if p.CorrectedTo != "" || p.WrongReportCount != 1 || p.LastWrongReport != "青" {
		t.Errorf("違う色への言い直し (誤り) は1から: to=%q count=%d last=%q", p.CorrectedTo, p.WrongReportCount, p.LastWrongReport)
	}
	p.Reset()
	p.NoteReport("茶色が光ってる", nil)
	p.NoteReport("やっぱり茶です", nil)
	if p.WrongReportCount != 2 {
		t.Errorf("言い方の揺れ (茶色/茶) は同じ内容: %d", p.WrongReportCount)
	}
}

// TestDecoyCutColorAvoidsCutLines は、報告の食い違いが続いたときに切らせる
// 不正解の線が、現在の正解・切り済みの線のどちらでもないことを確認する
// (決定124)。切り済みの線を指すと、切ろうにももう存在しない。
func TestDecoyCutColorAvoidsCutLines(t *testing.T) {
	session := &BuiltSession{Stages: []*BuiltStage{
		{Cut: "A"}, {Cut: "B"}, {Cut: "C"}, {Cut: "D"},
	}}
	for i := range session.Stages {
		decoy := decoyCutColor(session, i)
		if decoy == "" {
			t.Fatalf("stage %d: 候補が無い (最大4ステージなら必ず1本残るはず)", i)
		}
		for j := 0; j <= i; j++ {
			if decoy == session.Stages[j].Cut {
				t.Errorf("stage %d: decoy=%s は切り済み/正解の線", i, decoy)
			}
		}
	}
	if got := decoyCutColor(session, 3); got != "E" {
		t.Errorf("最終ステージの decoy = %q, want E (唯一残る線)", got)
	}
	if got := decoyCutColor(nil, 0); got != "" {
		t.Errorf("session nil で %q", got)
	}
}

// stageShownColors は、この課題で一度でも光る色の線ID (光り方は問わない)。
// テスト用 (本番の判定は stageLampStates で光り方まで見る)。
func stageShownColors(stage *BuiltStage) map[string]bool {
	states := stageLampStates(stage)
	if states == nil {
		return nil
	}
	shown := map[string]bool{}
	for line := range states {
		shown[line] = true
	}
	return shown
}

// TestMorseReportNoteRegroupsAndDecodes は、区切り違い (ZC と MIKE は長短の並びが
// 同じ) を見つけることと、長短の並びでの報告を文字に直すことを確かめる (決定146)。
func TestMorseReportNoteRegroupsAndDecodes(t *testing.T) {
	cases := []struct{ text, want string }{
		{"ゼットシーというものを繰り返すように点灯しています", "MIKE"},
		{"DCAYです", "XRAY"},
		{"長長、短短、長短長、短", "並べると「MIKE」"},
		{"最初は長・長で、次が短短です", "長・長→M、短短→I"},
		{"MANGOでした", "TANGO"},
	}
	for _, c := range cases {
		if got := morseReportNote(c.text); !strings.Contains(got, c.want) {
			t.Errorf("morseReportNote(%q) に %q が無い:\n%s", c.text, c.want, got)
		}
	}
	// 区切り違いの候補があれば、一般論ではなく具体的な違いを渡す (決定147)
	zc := morseReportNote("ZCです")
	if !strings.Contains(zc, "Z(－－・・) → M(－－)+I(・・)") || strings.Contains(zc, "つまずいている所から") {
		t.Errorf("ZC: 区切りの違いが具体的でない、または一般論が混ざっている:\n%s", zc)
	}
	etlfet := morseReportNote("ETLFETです")
	if !strings.Contains(etlfet, "1〜2文字目 E(・)+T(－) → A(・－)") {
		t.Errorf("ETLFET: E+T → A の区切り違いが無い:\n%s", etlfet)
	}
	// 候補の無い短い綴りでは、読み方を基礎から教える
	if !strings.Contains(morseReportNote("QQです"), "つまずいている所から") {
		t.Error("候補の無い2文字の綴りで、読み方を基礎から教える指示が無い")
	}
	if morseStream("ZC") != morseStream("MIKE") {
		t.Error("ZC と MIKE の長短の並びが一致しない")
	}
}

// TestMorseNoteHelpsWithMergedDashes は、長い光の連続を1回に数えた読み違い
// (HOTEL を EEEETEETEE) に、長短の並びで近い単語と回数の違いを渡すことを確かめる (決定148)。
func TestMorseNoteHelpsWithMergedDashes(t *testing.T) {
	note := morseReportNote("EEEETEETEEでした")
	for _, want := range []string{"HOTEL", "長い光5回", "報告は長い光2回", "E と T だけ"} {
		if !strings.Contains(note, want) {
			t.Errorf("%q が無い:\n%s", want, note)
		}
	}
	var p StageProgress
	p.NoteMorseReport(morseReportNote("EEEETEETEEでした"))
	p.NoteMorseReport(morseReportNote("EEEETELですね"))
	if p.MorseMisses != 2 {
		t.Errorf("表に無い報告が2回続いたのに MorseMisses = %d", p.MorseMisses)
	}
	p.NoteMorseReport(morseReportNote("HOTELでした"))
	if p.MorseMisses != 0 {
		t.Errorf("表にある報告で戻らない: %d", p.MorseMisses)
	}
	// 秒数は言わない (点滅を見ながら測れない)。見え方と数え方で教える
	for _, want := range []string{"一呼吸", "指を折って", "「/」"} {
		if !strings.Contains(morseReadingGuide, want) {
			t.Errorf("読み方に %q が無い", want)
		}
	}
}

// TestMorseTipAdvances は、読み違いが続くと伝えるコツが進むことと、
// 読み違いの種類で最初のコツが変わることを確かめる (決定148)。
func TestMorseTipAdvances(t *testing.T) {
	etNote := morseReportNote("EEEETEETEEでした")
	if tip := morseTipFor(etNote, 1); !strings.Contains(tip, "一呼吸") {
		t.Errorf("E と T だけの綴りの最初のコツ = %q", tip)
	}
	if morseTipFor(etNote, 1) == morseTipFor(etNote, 2) {
		t.Error("読み違いが続いてもコツが変わらない")
	}
}

// TestMorseNoteNoviceAndLooseWords は、読み方が分からない様子を拾うことと、
// 「長く」「短く」のような普通の言葉を長短の報告と取り違えないことを確かめる (決定149)。
func TestMorseNoteNoviceAndLooseWords(t *testing.T) {
	if note := morseReportNote("ランプが1つ、長く光ったり短く光ったりしています"); note != "" {
		t.Errorf("普通の言葉を長短の報告と取り違えた:\n%s", note)
	}
	if !strings.Contains(morseReportNote("モールス符号の読み方は分かりません"), morseNoviceMark) {
		t.Error("読み方が分からない様子を拾えていない")
	}
	var p StageProgress
	p.NoteMorseReport(morseReportNote("分かりません"))
	p.NoteMorseReport(morseReportNote("まだよく分からないです"))
	if p.MorseLessons != 2 || morseNoviceTip(1) == morseNoviceTip(2) {
		t.Errorf("教えるコツが進まない: lessons=%d", p.MorseLessons)
	}
}

// TestButtonReportIsNotLampReport は、ボタンを押した報告 (打ち間違いで「押」が無いものも)
// をランプの報告と取り違えないことを確かめる (決定156)。
func TestButtonReportIsNotLampReport(t *testing.T) {
	states := LampStates{"A": {lampOn: true}, "B": {lampBlink: true}}
	for _, text := range []string{"青を添いsました", "青を押しました", "青いけました", "次は青ですか"} {
		if wrong, _ := checkLampReport(text, states); wrong != "" {
			t.Errorf("%q をランプの報告と取り違えた (%s)", text, wrong)
		}
	}
	for _, text := range []string{"青です", "青が光ってます", "青色です。どうぞ"} {
		if wrong, _ := checkLampReport(text, states); wrong != "青" {
			t.Errorf("%q を光っていない色の報告として拾えていない (%q)", text, wrong)
		}
	}
}

// 手順の確認の問いを見分ける。迷いの混じった確認は含めない (決定159)
func TestNoteConfirmQuestion(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"ランプが1つ光っています。この色の線を切ればいいですか。どうぞ", true},
		{"赤の線を切って大丈夫ですか", true},
		{"次は青を押せばよろしいですか", true},
		{"これで合ってますか", true},
		{"ランプが1つ光っています。どうぞ", false},
		{"たぶん赤だと思うんですが、切っていいですか", false},
		{"何色だったか忘れました。切ればいいですか", false},
	}
	for _, c := range cases {
		var p StageProgress
		p.NoteConfirmQuestion(c.text)
		if p.ConfirmAsked != c.want {
			t.Errorf("%q: ConfirmAsked=%v, want %v", c.text, p.ConfirmAsked, c.want)
		}
	}
}
