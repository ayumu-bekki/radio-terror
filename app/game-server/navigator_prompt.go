package main

import (
	"fmt"
	"strings"
)

// 交信スタイル (docs/navigator_design.md §2.6)。
// サーバーが残り時間から判定し、プロンプトの [C] ブロックに含める。
const (
	commStyleNormal = "通常"
	commStyleUrgent = "緊迫"
)

// NavigatorPromptInput はプロンプト組み立てに必要な動的情報。
type NavigatorPromptInput struct {
	// Prompt は共通のプロンプト定義 (navigator/prompt.toml)
	Prompt    *NavigatorPromptConfig
	Character NavigatorCharacter
	Session   *BuiltSession

	// StageIndex は現在のステージ番号 (0始まり)
	StageIndex int
	// RemainingMS は Core から報告された残り時間
	RemainingMS int
	// RecentEvent は直近のゲームイベントの説明 (stage_cleared 等)。空でもよい
	RecentEvent string
	// AnnounceUrgent は「残り時間が僅少になったことを、この発話で初めて伝える」
	// かどうか。セッション中に1回だけ true になる (GameSession.urgentNoticed)。
	AnnounceUrgent bool
	// History は直近の無線のやり取り
	History string

	// WrongReport は直前のプレイヤー発話の、装置の表示と合わない報告
	// (「茶色」「緑」「赤が点滅」。StageProgress.LastWrongReport)。
	// 空なら該当なし。player_message のときだけ渡す。
	WrongReport string
	// WrongReportCount は同じ誤った報告が続いた回数 (決定138)。2 で不正解の線。
	WrongReportCount int
	// WrongReportMismatch は WrongReport が5色の中の色についての食い違い
	// (光らない色・点灯と点滅の取り違え) か。偽なら5色以外の色名 (決定135・137)。
	WrongReportMismatch bool
	// CorrectedFrom / CorrectedTo は、確かめ直しのあとプレイヤーが色を言い直したとき、
	// 取り消された色と新しい色 (決定136)。
	CorrectedFrom string
	CorrectedTo   string

	// JustAdvanced は課題の突破後、最初のプレイヤー発話への返答であることを示す
	// (決定127)。「切れました」を前の課題の報告として受けさせる。
	JustAdvanced bool
}

// BuildNavigatorPrompt は思考モデルへ渡すプロンプトを組み立てる (§3.3)。
//
//	[A. 共通役割定義] [B. キャラシート] [C. ヒントポリシー]
//	[D. セッション状態] [E. 会話ログ] [F. 出力ルール]
func BuildNavigatorPrompt(in NavigatorPromptInput) string {
	var b strings.Builder

	// [A] 共通役割定義 (設定ファイルから)
	b.WriteString(strings.TrimSpace(in.Prompt.Role))
	b.WriteString("\n\n")

	// [B] キャラシート (セッション中固定)
	b.WriteString("# あなたのキャラクター\n")
	b.WriteString(in.Character.Sheet)
	b.WriteString("\n\n")

	// [C] 進め方の方針 + 交信スタイル (ヒントレベルは廃止。決定129)
	stage := in.currentStage()
	if stage != nil {
		b.WriteString(stageGuidanceText)
	}

	style := commStyleNormal
	if in.RemainingMS > 0 && in.RemainingMS <= in.Prompt.UrgentThresholdMS {
		style = commStyleUrgent
	}
	b.WriteString(fmt.Sprintf("# 交信スタイル: %s\n", style))
	if style == commStyleUrgent {
		b.WriteString("緊迫時の崩し方: ")
		b.WriteString(in.Character.UrgentStyle)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// 残り時間が僅少になったことを**この発話で初めて伝える**場合だけ足す。
	//
	// **独立したブロックにする。** 交信スタイルの中に混ぜると、口調の
	// 指示に紛れて落ちる (N-51: 長い指針は末尾が落ちる)。
	//
	// **字数の目安に数えない**と明記する。数えに含めると、この一言を
	// 入れたぶん手順や警告が削られる (N-22 と同じ構図)。
	if in.AnnounceUrgent {
		b.WriteString(urgentNoticeBlock(in.RemainingMS))
		b.WriteString("\n")
	}

	// [D] セッション状態 (動的)
	b.WriteString("# 現在の状況\n")
	if in.Session != nil {
		total := len(in.Session.Stages)
		b.WriteString(fmt.Sprintf("- 難易度: %s\n", in.Session.Difficulty))
		// 全ステージ完了後は StageIndex が末尾を越える。「3 / 2 番目の課題」と
		// 書くと生成AIが存在しない課題があると解釈するため、完了と明示する。
		if in.StageIndex >= total {
			b.WriteString(fmt.Sprintf("- 進行: 全%d課題を完了\n", total))
		} else {
			b.WriteString(fmt.Sprintf("- 進行: %d / %d 番目の課題\n",
				in.StageIndex+1, total))
		}
	}
	b.WriteString(fmt.Sprintf("- 残り時間: 約%d秒\n", in.RemainingMS/1000))

	if stage != nil {
		b.WriteString("\n## 今の課題\n")
		// 前の課題の色は今の課題の答えではない。104 で「白の方が早い」に
		// 前の課題の「赤い色の線を切って」をなぞって返し、爆発した (決定139)。
		if in.StageIndex > 0 {
			b.WriteString("- **会話ログの「(装置) ステージ" + fmt.Sprint(in.StageIndex+1) +
				"開始」より前のやり取りは、前の課題のものです。**そこに出てきた色名・" +
				"番号・手順を、今の課題の指示に使わないでください。\n")
		}
		if briefing := stage.Navigator["briefing"]; briefing != "" {
			b.WriteString("- 内容: " + briefing + "\n")
		}
		// ナビゲーターは正解を知っている状態で話す (§3.1)。
		//
		// ただし**正解を渡すこと自体が漏洩の原因**になる。実運用で
		// 「次は緑色の線を切ってください」と色名を直言した事例が出た
		// (answer の文をほぼそのままなぞっていた)。
		//
		// **切る線の色名は常にプロンプトから伏せる** (決定40・決定129)。
		// 「書いてあるが言うな」は守られないことがある — 目の前にある語は
		// なぞられる。無い語は言いようがないので、これが最も確実。
		// 以前は L4 (直言) でだけ外していたが、ヒントレベルごと廃止した。
		if answer := stage.Navigator["answer"]; answer != "" {
			b.WriteString("- 正解(あなただけが知っている): " +
				redactCutColor(answer, stage.Cut) + "\n")
			// 「伏せてあります」と書くと、ナビは「知っているが伏せている」立場を取り
			// 「色は教えられん」と断る (フクロウ 9回中3回。ADR N-6 に反する)。
			// 実際に色名はここに無いので、**知らない立場**の事実として書く (決定134)。
			fmt.Fprintf(&b, "  ⚠ 上の「%s」は、**あなたにも分からない色**です。"+
				"あなたは装置を見ていないので、切る線が何色かは、プレイヤーがランプや"+
				"資料から確かめるまで分かりません。何色かを推測して言うこともしません。"+
				"プレイヤーが色名を報告してきたら、進め方にある手がかり"+
				"(どのランプと同じ色か、資料のどこを引くか)に沿っているかで受け止めます。\n",
				redactedColorMark)
		}
		if procedure := stage.Navigator["procedure"]; procedure != "" {
			b.WriteString("- 進め方: " + procedure + "\n")
		}

		// [必ず言うこと] 落とすと課題が詰む一言を**独立したブロック**で渡す。
		//
		// `procedure` に「必ず両方伝える」と書いても、指針が長くなるほど
		// **末尾の項目が落ちる** (204 色合わせ で8回中2回、
		// 「最後に押した色を覚えておく」が欠けた)。
		// 散文の中の但し書きではなく、**単独の要求**として置き直す。
		//
		// ここに書くのは**落とすと手詰まりになるもの**だけ。
		// 何でも入れると、また埋もれて同じことになる。
		if mustSay := stage.Navigator["must_say"]; mustSay != "" {
			b.WriteString("\n## この課題で必ず言うこと\n")
			b.WriteString("- " + mustSay + "\n")
			b.WriteString("**これを落とすとプレイヤーが手詰まりになります。**" +
				"字数を超えてもよいので、**課題の入り口で必ず言ってください**。\n" +
				"プレイヤーが手順を先に言い当ててきた場合も、" +
				"肯定するだけで終わらせず**これを足してください**。\n")
		}

		// [課題の切り替わり直後] プレイヤーの「切れた」は**前の課題**の報告 (決定127)。
		//
		// ナビは今の課題の知識しか持たないため、201 (押し切ってから切る) では
		// 「押す前に切ってしまった」と誤解して叱った (「馬鹿野郎」)。
		// 切り替わったことを明示し、正しい操作として受けさせる。
		// **例文は置かない** (ADR N-52)。
		if in.JustAdvanced {
			b.WriteString("\n## 課題の切り替わり直後\n" +
				"- 直前に**前の課題の線が切れ**、今の課題に移ったばかりです。" +
				"プレイヤーの「切れた」「切りました」という報告は**前の課題**のもので、" +
				"**正しい操作**です。今の課題の手順と照らして誤りとして扱わないでください" +
				"(叱らない・驚かない)。\n" +
				"- 受け止めたうえで、ランプの報告がまだ無ければ" +
				"**今のランプの状態を尋ねてください**。\n")
		}

	} else {
		// ステージ知識が無い状態 (全ステージ完了後など)。
		//
		// 何も書かないと、生成AIは「次の課題へ進む」といった指示だけを頼りに
		// **存在しない課題を捏造する** (実運用で、解除済みの装置に対して
		// 「もう一本、赤の線を切ってください」と指示した)。
		// 操作を促してはいけないことを明示する。
		b.WriteString("\n## 今の課題\n")
		b.WriteString("- **今は指示する課題がありません。**装置の操作 (線を切る・" +
			"ボタンを押す・ダイヤルを回す) を促してはいけません。" +
			"色や番号を挙げて何かを切らせる発言は禁止です。" +
			"直近の出来事への短い受け答えだけにとどめてください。\n")
	}

	if in.RecentEvent != "" {
		b.WriteString("\n## 直近の出来事\n")
		b.WriteString(in.RecentEvent + "\n")
	}
	b.WriteString("\n")

	// [E] 会話ログ (動的)
	if in.History != "" {
		b.WriteString("# 直近の交信\n")
		b.WriteString(in.History)
		b.WriteString("\n\n")
	}

	// [誤った色の報告] サーバーが文字起こしから機械的に検出する (決定124・決定135)。
	//
	// 判定も回数も生成AIに任せない — 「成り立たない報告か」の判断が揺れ、
	// 回数も会話ログから数えると取り違える (シミュレーションで両方発生)。
	// **不正解の線はこの場面でだけ渡す**。常に渡しておくと勝手に使われうる。
	//
	// **会話ログの後ろに置く。** 課題の欄に置くと、2回目で直前の自分の
	// 聞き返しをなぞって繰り返した (15回中2回。決定135)。
	if in.WrongReport != "" && in.Session != nil && in.StageIndex < len(in.Session.Stages) {
		b.WriteString("# この返答で最優先すること\n")
		b.WriteString(wrongReportBlock(in))
		b.WriteString("\n")
	} else if in.CorrectedTo != "" {
		// [報告の言い直し] 確かめ直しのあとの正しい報告。指示が無いと、ナビは
		// 会話ログの自分の「赤だな」をなぞり、取り消された色の線を切らせた (決定136)。
		b.WriteString("# この返答で最優先すること\n")
		b.WriteString("\n## 報告の言い直し\n")
		fmt.Fprintf(&b, "- プレイヤーは確かめ直して、ランプの色を「%s」から**「%s」に言い直しました**。"+
			"**今の報告は%sです。**前の「%s」は取り消されています。\n",
			in.CorrectedFrom, in.CorrectedTo, in.CorrectedTo, in.CorrectedFrom)
		fmt.Fprintf(&b, "- 次の順で、1つの発話にまとめてください。\n"+
			"  1. **%s**と復唱して受ける\n"+
			"  2. 「進め方」に沿って、次にやることを伝える\n", in.CorrectedTo)
		fmt.Fprintf(&b, "- 「%s」は、この先の復唱にも指示にも使いません。\n\n", in.CorrectedFrom)
	}

	// [F] 出力ルール (設定ファイルから)
	b.WriteString(strings.TrimSpace(in.Prompt.Output))

	return b.String()
}

// redactedColorMark は伏せた正解色の代わりに入れる印。
//
// 「◯◯色」のように**色名の形**を残す。単に消すと文が壊れて
// 「何を切るのか」が読み取れなくなり、進め方の指示まで曖昧になる。
const redactedColorMark = "◯◯"

// redactCutColor は answer から正解の色名を伏せる (決定40)。
//
// **目の前に無い語は言えない。** 「書いてあるが言うな」という指示は
// 守られないことがあり (決定19・27)、実測でも 616発話中1件残っていた。
// 色名そのものをプロンプトへ入れないのが最も確実 (常に伏せる。決定129)。
//
// 押すボタン・押さえるボタンの色は伏せない — それらは伝えてよい情報で、
// 伏せると手順が成立しなくなる。伏せるのは**切る線の色**だけ。
func redactCutColor(answer, cut string) string {
	name, ok := colorNameJA[cut]
	if !ok {
		return answer
	}
	// 「赤色」→「◯◯色」、単独の「赤」→「◯◯」の順で置き換える。
	// 先に「赤色」を処理しないと「◯◯色色」になる。
	redacted := strings.ReplaceAll(answer, name+"色", redactedColorMark+"色")
	return strings.ReplaceAll(redacted, name, redactedColorMark)
}

// wrongReportBlock は装置の表示と合わない報告への指示を組み立てる
// (ADR N-9b。判定はサーバー: navigator_color_check.go / navigator_stage_progress.go)。
//
// 1回目は**聞こえた内容を復唱して**確かめさせる。復唱すれば、プレイヤーは
// 言い間違い・聞き違いに自分で気づける。同じ誤りを繰り返したら不正解の線を
// 切らせる (誤った報告をすると正しく解体できない、というゲーム性)。
//
// **例文は置かない** (ADR N-52)。全キャラ共通に渡るブロックのため。
func wrongReportBlock(in NavigatorPromptInput) string {
	var b strings.Builder
	decoy := decoyCutColor(in.Session, in.StageIndex)
	if (in.WrongReportCount < 2 || decoy == "") && in.WrongReportMismatch {
		// 表示と合わない報告 (光らない色・点灯と点滅の取り違え。決定137)。
		// ナビに「その色は光っていない」とは教えない —
		// 教えると「緑は光ってないはず」と否定する (決定132 と同じ構図)。
		// 念のための確かめ直しとして頼ませる (決定135)。
		b.WriteString("\n## 色の報告の確かめ直し (1回目)\n")
		fmt.Fprintf(&b, "- プレイヤーの直前の報告に「%s」がありました。"+
			"念のため、ランプを確かめ直してもらう場面です。"+
			"**この指示は「進め方」より優先します。この発話では手順"+
			"(ダイヤル・ボタン・タイマー・切る線)を一切伝えません。**\n", in.WrongReport)
		b.WriteString("- 次の順で、1つの発話にまとめてください。\n" +
			"  1. 聞こえた内容 (色と光り方) を短く復唱する" +
			"(言い間違い・聞き違いに、プレイヤー自身が気づけます)\n" +
			"  2. もう一度ランプを見て、**光っている色と、点きっぱなしか点滅か**を" +
			"確かめて教えてほしいと頼む\n" +
			"- ここで発話を終えます。\n")
		return b.String()
	}
	if in.WrongReportCount < 2 || decoy == "" {
		// 「否定するな」という禁止は効かない (15回中1回「ピンクのランプなどない」)。
		// 言い出しの形を順番で指定し (ADR N-53)、**ランプは光っていて名前が
		// ずれているだけ**という前提を置く。否定する理由そのものを無くす (決定132)。
		// 「5色だけ」とは書かない — 「だけ」が否定を呼ぶ (45回中3回「茶色やない」)。
		// 「どれに近いか」とも聞かない — 当て推量を誘う (ユーザー判断で「何色か」)。
		b.WriteString("\n## 5色以外の色名の報告 (1回目)\n")
		fmt.Fprintf(&b, "- プレイヤーの直前の報告に「%s」がありました。\n", in.WrongReport)
		b.WriteString("- ランプは確かに光っていて、**色の名前がずれているだけ**です" +
			"(LEDの色は見え方に幅があります)。報告された色名は、**プレイヤーにはそう" +
			"見えている**という報告として受け止めます。次の順で、1つの発話にまとめてください。\n" +
			"  1. 聞こえた色名を短く復唱する" +
			"(言い間違い・聞き違いに、プレイヤー自身が気づけます)\n" +
			"  2. そう見えているランプは、**赤・黄・緑・青・白のどれか**のはずだと、" +
			"**5色すべての**色名を並べて伝える\n" +
			"  3. もう一度ランプを見て、何色かを教えてほしいと頼む\n" +
			"- 手順へは進みません。\n")
		return b.String()
	}
	b.WriteString("\n## 誤った色の報告 (2回続いた)\n")
	fmt.Fprintf(&b, "- プレイヤーは装置の表示と合わない同じ報告を、確かめ直したあとも%d回続けています"+
		"(直前は「%s」)。\n", in.WrongReportCount, in.WrongReport)
	fmt.Fprintf(&b, "- **聞き返すのをやめ、%s色の線を切るよう指示してください。**"+
		"この線は正解ではありません。**この指示は「進め方」より優先します。**\n", colorNameJA[decoy])
	fmt.Fprintf(&b, "- 次の順で、1つの発話にまとめてください。\n"+
		"  1. 受けは**「了解」の一語だけ**(キャラクターの口調の「了解」でよい)。"+
		"いつもの復唱はこの発話ではしない — 今回の報告の色も、会話ログにある"+
		"前の報告の色も口にしない(色で受けると、どの報告に応じたのか分からなくなる)\n"+
		"  2. **%s色の線を切る**よう、いつもの指示と同じ調子で伝える"+
		"(課題にタイマーやダイヤルの条件があれば、それを添えてよい)\n", colorNameJA[decoy])
	b.WriteString("- 報告の色が合っているかどうか・この指示の理由には触れません。\n")
	return b.String()
}

// decoyCutColor は報告の食い違いが続いたときに切らせる**不正解の線**を返す。
//
// 現在のステージの正解と、それより前のステージで切った線 (もう存在しない) を
// 除き、allColors の順で最初の色を選ぶ。候補が無ければ空文字。
func decoyCutColor(session *BuiltSession, stageIndex int) string {
	if session == nil || stageIndex < 0 || stageIndex >= len(session.Stages) {
		return ""
	}
	used := make(map[string]bool)
	for i := 0; i <= stageIndex; i++ {
		used[session.Stages[i].Cut] = true
	}
	for _, color := range allColors {
		if !used[color] {
			return color
		}
	}
	return ""
}

// currentStage は現在のステージ知識を返す。範囲外なら nil。
func (in NavigatorPromptInput) currentStage() *BuiltStage {
	if in.Session == nil {
		return nil
	}
	if in.StageIndex < 0 || in.StageIndex >= len(in.Session.Stages) {
		return nil
	}
	return in.Session.Stages[in.StageIndex]
}

// urgentNoticeBlock は「残り時間が僅少になった」ことを一度だけ伝えさせる指示。
//
// **セッション中に1回しか渡らない** (GameSession.urgentNoticed)。以前は
// `time_warning` という独立した発話トリガーを定義していたが、鳴らす実装が
// 無いまま残っていた。単独で鳴らすと**プレイヤーの手を止めて無線を塞ぐ**ため、
// **次の返答へ一言添える**形にした。返答はどのみち流れるので、無線の占有が増えない。
//
// **例文は置かない** (ADR N-21c)。ここは全キャラ共通に渡るブロックなので、
// 台詞を書くとその口調に全員が寄る (方言のキャラが標準語に戻る)。
// 言い方はキャラシートの「残り60秒」の例に委ねる。
//
// **字数の目安から外す**と明記する。含めると、この一言を足したぶん
// 手順や危険の警告が削られる (ADR N-22)。
func urgentNoticeBlock(remainingMS int) string {
	var b strings.Builder
	b.WriteString("# 残り時間の告知 (この発話で1回だけ)\n")
	b.WriteString(fmt.Sprintf(
		"残り時間は約 %d 秒です。**プレイヤーはまだこれを知りません** — "+
			"時間の表示は装置の前でしか読めず、手元を見ていない間は気づけません。\n",
		remainingMS/1000))
	b.WriteString("**この発話に、残り時間が少ないことを伝える一言を添えてください。**\n")
	b.WriteString("- **添えるだけです。** 本来の受け答えと次にやることは、そのまま伝えてください。\n")
	b.WriteString("- **この一言は発話の長さの目安に数えません。** " +
		"添えるために手順・数字・危険の警告を削らないでください。\n")
	b.WriteString("- **急かすだけにしないでください。** 手を止めさせると逆に遅くなります。\n")
	b.WriteString("- **言い方はキャラシートの「残り60秒」の例に従ってください** — " +
		"ここに例文は置きません。\n")
	return b.String()
}

// stageGuidanceText はステージの進め方の前提となる共通の方針ブロック。
//
// ステージ固有の内容 (briefing / answer / procedure / must_say) は
// BuildNavigatorPrompt の「今の課題」が渡す。ここは全ステージ共通の構え方だけ。
// 以前はヒントレベルごとに文面を変えていたが、レベルごと廃止した (決定129)。
const stageGuidanceText = `# 進め方の方針
- 「今の課題」の**進め方に従ってください**。進め方には「こう報告されたら、
  これを伝える」という**条件**が書いてあります。**条件が満たされる前に先の内容を
  言わない**でください(プレイヤーが装置を見て報告する体験が消えます)。
- **切る線の色名はプレイヤーより先に発話に入れない。**色を確定させるのはプレイヤーの仕事で、
  あなたはそこへ導きます。線は「光っているランプと同じ色の線」のように
  ランプの見え方で指すか、何色かを尋ねます。色名を出さない理由や、
  自分が何を伝えて何を伝えないかは**プレイヤーに説明しない**でください。
  プレイヤーが色名を報告してきたら、合っているかを照合して認めてかまいません
  (進め方が照合を禁じている課題を除く)。
- 装置を見ても分からない情報 (ボタンを押す順番、危険な位置など) は、進め方が
  伝えるよう指示していれば伝えます。伏せるとプレイヤーが手詰まりになります。
- プレイヤーの質問を待つだけでなく、進行イベント・沈黙・残り時間をトリガーに
  自分から声を掛けてください。
- 常に解除成功へ導く姿勢を保ってください。

`
