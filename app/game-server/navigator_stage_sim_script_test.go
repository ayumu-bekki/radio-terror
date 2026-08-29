package main

import (
	"strings"
	"testing"
)

// ステージごとのプレイヤー台本 (navigator_stage_sim_test.go から使う)。
//
// 台本は「装置を見たプレイヤーが無線で報告しそうな内容」を書く。
// **正解の色名は台本に書かない** — ナビゲーターが誘導できているかを見たいので、
// プレイヤーは観察できる事実 (点灯数・点滅の速さ・モールスの文字) だけを報告する。
// 抽選値は ${cut} 等で参照できるが、正解に触れる報告は最終ターンだけにしてある。
//
// ヒントレベルは L1 から始め、ターンが進むにつれ上げる (実際は経過時間で上がる)。
//
// **ターン順は実機で起こりうる並びにする。** トリガーには前提がある —
// `stage_cleared` は課題を解いたあとにしか届かないので、台本でも最後に置く。
// `session_start` の直後に置いたことがあり、「まだ何も突破していないのに
// 『やりましたね、ナイスです!』」という**実機では起こりえない場面**を
// 検証していた (docs/navigator_design.md 決定32)。

// simDefaultScript は個別の台本を持たないステージの共通台本。
// 「観察 → 報告 → 指示待ち」の最小の往復を回す。
func simDefaultScript(id string) simScript {
	return simScript{
		StageID: id,
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "こちら現場。装置の前に立ちました。何をすればいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "ランプを見ています。どこに注目すればいいですか。どうぞ"},
			{Trigger: "silence", HintLevel: HintL3},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "手順は分かりました。切る線はどれですか。どうぞ"},
			// **台本の最後に置く**。課題を解いたあとの遷移なので、
			// これより前に置くと「まだ何も突破していないのに称賛する」
			// ありえない場面になる (実際に一度そう書いてしまった)。
			// 次の課題も「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	}
}

// simScriptFor はステージIDに対応する台本を返す。
func simScriptFor(id string) simScript {
	if script, ok := simScripts[id]; ok {
		return script
	}
	return simDefaultScript(id)
}

// **200番台以上に L4 の台本を書かない。** ノーマル・ハードは `l4_pct = 0`
// なので (ADR N-39)、L4 は本番で到達しない。台本が L4 と書けばシミュレーションは
// L4 で走ってしまい、**到達しない状態を検証する**ことになる
// (実測: 204 で「正解は緑色の線ですよ」と直言する所見を拾った)。
//
// 「答えを直接聞く」圧力のターンは**削除せず L3 へ降格**させてある。
// ノーマル以上の上限が L3 である以上、**L3 で答えを迫られても言わないこと**が
// 検証したい性質そのもので、圧力自体は残す価値がある。
// 逸脱は `unreachable_hint_level` として検出する。
var simScripts = map[string]simScript{
	// --- イージー ---

	// 101 解体デビュー: ダイヤルを 0 → via1 → final の順に誘導させる。
	// 第一声で「0」を数字で言えているかを見る (hint_l1 の要求)。
	"101": {
		StageID: "101",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			// **まずランプを報告する**。ここでダイヤルを回すと、
			// 報告→指示の往復を飛ばしてしまい検証にならない (決定44)。
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが1つ光っています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ダイヤルを0に戻しました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "合わせました。次はどうしますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "ダイヤルは指示どおりに合わせました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "ランプが1つ光っています。この色の線を切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 102 ホールド&カット: まずランプ状態を報告させる誘導ができているか。
	"102": {
		StageID: "102",
		// **第一声で「押しながら」と「ダイヤル」を両方伝える** (ADR N-47)。
		// 『点滅が押すボタン、点灯が切る線』と名詞で並べただけだと
		// **1回押して離す**と解釈され、実測5回中5回で聞き返された。
		// ダイヤルだけ・押しながらだけに寄った発話も6回中3回出たため両方見る。
		MustMention: []string{"押しながら|押したまま|押さえたまま", "${sim_rotary}"},
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが2つ光っています。片方は点滅しています。どうぞ"},
			// **プレイヤーからダイヤルを聞かせない**。ここで尋ねさせると
			// 「ダイヤルを先に伝えたか」の検査にならない (実運用ではプレイヤーは
			// ダイヤルの存在を知らず、押して切るだけだと思い込んでいた)。
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "えっ? 1回押せばいいんですか? どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "ダイヤルを合わせて、ボタンを押さえました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "このまま切っていいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 103 コール&レスポンス: 点灯色の報告から対話が始まる。
	"103": {
		StageID: "103",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "点灯しているランプが1つ、点滅しているランプが1つあります。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "1つ目のボタンを押しました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "2つ目も押しました。次はどうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "どの線を切りますか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 104 早い者勝ち: procedure が無いステージ。hint_* だけで誘導できるかを見る。
	"104": {
		StageID: "104",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが2つ点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "点滅の速さが違うように見えます。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "速いほうと遅いほうが分かりました。どちらを切りますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "速いほうの色を切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// --- ノーマル ---

	// 201 復唱: 5個の列は装置に現れない。第一声から読み上げるのが要件。
	// 第一声で列の1色目が伝わっているかを見る。
	"201": {
		StageID: "201",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			// **まずランプを報告する**。ここで列を要求させると、
			// 報告→照合の往復を飛ばしてしまい検証にならない (決定44)。
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが1つ光っています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "もう一度、順番を言ってください。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "3つ目まで押しました。残りをお願いします。どうぞ"},
			{Trigger: "wrong_action", HintLevel: HintL3,
				Event:  "プレイヤーが誤操作をした。叱咤しつつ励まし、注意を促す。",
				Player: "間違えました。最初からやり直しですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "5個とも押し終わりました。どうぞ"},
			// **押し切るとランプの色が変わる** (reveal_cut_on_complete / ADR C-13)。
			// 押す前に見た色を切らせないか、変化を報告させられるかを見る。
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "ランプの色が変わりました。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 301 LED照合: 資料2 (変換表 → デコード表) → 資料3 とたどらせる。
	// **各段でプレイヤーに引かせられるか**を見る。キーワードもダイヤル位置も
	// ナビが先に言ってしまうと、資料をたどる工程が丸ごと消える。
	"301": {
		StageID: "301",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプは黄色と緑が点いています。赤は消えています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "表を見ました。キーワードは${sim_keyword}です。どうぞ"},
			// **ダイヤル位置は抽選値を使う。** 固定値を書くと、実際の位置と
			// 食い違ったときにナビが『1ですね、オッケーです!…ダイヤルを5に』と
			// **誤答を肯定した直後に別の数字を指示する**形になり、
			// ログを読んで不具合と誤解する (実測 2026-08-25)。
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "表を引きました。ダイヤルは${sim_rotary}、基準色は白です。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "基準色が白のときはどう見ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 202 暗号電文: モールス → 語 → 頭文字 → 対照表の色。
	// 音声認識の誤変換 (「三毛」等) を吸収できるかも見る。
	"202": {
		StageID: "202",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "1つのランプが長く光ったり短く光ったりしています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "モールスですね。シートの対照表で読んでみます。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "読めました。${navi_word_guess}という単語だと思います。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "頭文字を対照表で引きました。この色でいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 203 ブループリント: **L4 でも色名を言ってはいけない**ステージ。
	// 端子番号だけで指示できているかが最大の確認点。
	"203": {
		StageID: "203",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが5つとも全部光っています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "回路図シートを見ています。どこを見ますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "端子の番号と色の表がありました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "どの端子の線を切りますか。色で教えてください。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 204 色合わせ: color_match_completed の後に「最後に押した色」を思い出させる。
	"204": {
		StageID: "204",
		// **「最後に押した色を覚えておく」を入り口で必ず伝える** (ADR N-42 の裏)。
		// 押し切るとランプが全部消えるので、思い出せなければ手がかりがゼロになる。
		// プレイヤーが先回りして『同じ色のボタンを押せばいいですか?』と聞くと
		// 肯定するだけで返して**注意が落ちた** (実測 2026-08-25)。
		MustMention: []string{"覚え"},
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "1つだけ光っています。同じ色のボタンを押せばいいですか。どうぞ"},
			{Trigger: "color_match_completed", HintLevel: HintL2,
				Event: "プレイヤーが色合わせを完了した。最後に押した色が次の手がかりになる。"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "最後に押した色は覚えています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "その色の線を切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 205 速さくらべ: 点灯1色を基準外と伝え、**${rank}番目の色**を
	// プレイヤーに先に言わせる。answer に「照合の材料として使え」と
	// 書いてある指示が効いているかを見る。
	//
	// 正解の順位は毎回変わる (ADR N-40) ため、台本は
	// **順位に依存しない言い回し**にしてある。「一番速いのは〜」と書くと
	// 順位が2〜4番のときに台本自体が的外れな報告になる。
	"205": {
		StageID: "205",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "1つだけ点きっぱなしで、残り4つが点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "点滅している4つを見比べています。どうぞ"},
			// **わざと間違った色を報告する**。ナビゲーターは装置を見ていないので
			// **正誤を告げず、切らせもしない**のが正しい (ADR N-50)。
			// 『それが4番目だ、切ってください』と応じると誤答のまま即爆発する。
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "数えてみました。${sim_wrong_color}色だと思います。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "もう一度数え直しました。${cut}色でした。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 206 綱渡り: 危険位置は**課題の入り口で必ず伝える** (伏せると事故になる)。
	// 訊かれるまで伝わらないと即爆発の事故になる。
	//
	// **第一声ではなく、ランプの報告への返しで伝える** (ADR N-49)。
	// 装置を見る前に数字だけ聞かされても頭に入らない。MustMention は
	// 入り口の発話 (session_start と最初の player_message) のどれかに
	// 出ていれば満たすので、この変更でも検査はそのまま働く。
	"206": {
		StageID:     "206",
		MustMention: []string{"${sim_forbidden}"},
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			// **ランプの報告から始める。** 第一声で危険位置を告げる規則は
			// 廃止した (ADR N-49) ので、プレイヤーはまだ危険位置を知らない。
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが1つ光っています。どうぞ"},
			// 報告への返しで**回す指示と危険位置の警告が揃うか**を見る。
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "分かりました。どこまで回せばいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "指示の位置に合わせました。止まらずに回せました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "どの線を切りますか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 207 息が合わない: 「速さ」ではなく「揃っているか」へ観点を切り替えさせる。
	"207": {
		StageID: "207",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "5つとも同じ速さで点滅しているように見えます。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "速さは全部同じです。何を見ればいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "1つだけ光る長さが短いものがあります。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "それを切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 209 配電盤照合: 見え方から現在位置を当て、資料4へ誘導できるか。
	//
	// **危険位置を言っていないか**が要件 (ADR N-44)。資料を読むこと自体が
	// 謎なので、ナビが数字を言うと丸ごと消える。206 綱渡り とは**逆**。
	// 検査は MustNotMention 側で行う。
	"209": {
		StageID: "209",
		// **見え方は点灯のみ**になった (決定91)。点滅は「切る線」の合図に
		// 予約してあるので、現在位置の報告に点滅は出ない。
		//
		// **ナビは現在位置を知らない。** サーバーは開始時のロータリー位置を
		// 知らず、Core が実位置で行を確定する。ナビが位置を当てにいくと、
		// ずれたときに正しい報告を否定してしまう (実運用で爆発した)。
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			// **開始位置は -sim-panel-start で変わる** (決定91)。
			// 実機ではツマミの残り位置がそのまま開始位置になるため、
			// 0-5 のどこから始まっても成立しなければならない。
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "${sim_panel_lit}が点いています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "資料を見ました。${sim_panel_start}番ですね。危険位置は${sim_panel_forbidden}、解除位置は${sim_panel_release}です。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "${sim_panel_release}まで回しました。今度は${sim_panel_cut}が点滅しています。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// --- ハード (L4 は無効。L3 止まりで誘導しきれるかを見る) ---

	// 208 ジャストカット: 「今だ」とリアルタイム指示をしないことが要件。
	"208": {
		StageID: "208",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが1つ光っています。タイマーが動いています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "タイマーのどの桁を見ますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "小数点の左の数字ですね。いくつになったら切りますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "構えました。合図をください。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 303 追いかけダイヤル: 十の位に追従させる。届かない時間帯の待ちを伝えるか。
	"303": {
		StageID: "303",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "1つのランプが点滅しています。タイマーも見えています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "タイマーの数字とダイヤルですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "十の位が今8です。ダイヤルは5までしかありません。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "5になったので合わせました。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 304 我慢比べ: 保持 + タイミング。ここでも「今だ」と言わないことが要件。
	"304": {
		StageID: "304",
		// **押下の指示を第一声で落とさない** (ADR N-48)。
		// タイマーの話だけで返すと何を押さえるのか分からず手が止まる
		// (実測3回中1回で押下に一切触れなかった)。
		// 押すボタンの色は**点滅で装置に現れる**ので言ってよい (ADR N-4)。
		MustMention: []string{"押さえ|押しながら|押したまま"},
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "点滅しているランプと点灯しているランプがあります。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "点滅している色のボタンを押さえました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "押さえたままです。いつ切りますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "構えています。合図をください。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 305 ローマ字電文: 途中まで読めた報告を補って解読を急がせられるか。
	// L3 でも「色名そのものは言わず、その読みで合っていると確認する」のが仕様。
	"305": {
		StageID: "305",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "1つのランプが長短で点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "対照表で読んでいます。最初の文字が読めました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "2文字目まで読めました。続きが読めません。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "全部読めました。色の名前になっています。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 302 暗号電文・ジャミング: 妨害の中からモールスを見つけさせる。
	// **第一声で探し方を指示していないか**、切る線の色を直言していないかを見る。
	// 解き方は 202 暗号電文と同じなので、妨害の扱いだけが違いになる。
	"302": {
		StageID: "302",
		// **解読した語 (ECHO 等) を言ってはいけない** (ADR N-45)。
		// これは切る線の色ではなく**中間の答え**で、色漏れ検査に掛からない。
		// 実測で「デルタやない、ECHOや」と正解の語を教えてしまった
		// (2026-08-26)。誤答は読み直しを促すだけにする。
		MustNotMention: []string{"${navi_word_guess}"},
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプがばらばらに光っています。点きっぱなしのものもあります。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "長短が混ざっているランプが1つありました。どうぞ"},
			// **誤った語を報告する。** ここでナビが正解の語を教えていないか、
			// かつ断定して突き放していないかを見る (決定87)。
			// 正しい形は「本当にデルタか?」と疑いをかけて資料へ戻すこと。
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "読めました。デルタです。どうぞ"},
			// **今度は正しく読めた場合。** 疑いをかけるのは違っているときだけで、
			// 正解には「せやな」と認めて先へ進めるかを見る。
			// ここで毎回疑い返すと、正しく読めても先へ進めない。
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "確かめ直しました。${navi_word_guess}でした。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},
}

// TestScriptsReportLampsFirst は、各台本の**最初のプレイヤー発話**が
// ランプの状態報告になっていることを確かめる (決定44)。
//
// 101・102・201 で、報告を飛ばして「ダイヤルを0に戻しました」
// 「順番を言ってください」から始まる台本になっていた (決定22 時代の名残)。
// これだと**報告 → 照合 → 指示**の往復が検証されない。
// ナビゲーター側は正しく尋ねているのに、台本が答えていない状態だった。
//
// このテストは**台本の検査**であって発話の検査ではない。
// 実際の発話は `no_observation_first` 検査が見る。
func TestScriptsReportLampsFirst(t *testing.T) {
	// ランプの状態に言及していると見なす語。
	//
	// **「点いて」を入れておく** — 209 は点灯色だけを報告する設計 (決定91) で、
	// 「赤と黄色が点いています」が正規の第一報になる。
	// 「点灯」しか見ていないとこれを取りこぼす。
	observed := []string{"ランプ", "光", "点滅", "点灯", "点いて", "消え"}

	// 206 綱渡り は危険位置を第一声で警告する設計 (決定22) なので、
	// プレイヤーがそれを聞き返すところから始まるのが自然。
	exempt := map[string]string{
		"206": "危険位置の警告を聞き返す形が自然なため",
	}

	for id, script := range simScripts {
		if reason, ok := exempt[id]; ok {
			t.Logf("%s: 検査対象外 (%s)", id, reason)
			continue
		}

		first := ""
		for _, turn := range script.Turns {
			if turn.Player != "" {
				first = turn.Player
				break
			}
		}
		if first == "" {
			continue // プレイヤー発話が無い台本
		}

		found := false
		for _, w := range observed {
			if strings.Contains(first, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: 最初のプレイヤー発話がランプの報告になっていない — "+
				"報告→照合→指示の往復が検証されない:\n  %s", id, first)
		}
	}
}

// TestMustNotMentionExemptionIsNarrow は、MustNotMention の
// 「プレイヤーが先に言った語は復唱なので免除」が**広すぎない**ことを確かめる。
//
// 302 は誤答を差し戻したあと、プレイヤーが正しい語を報告してくる。
// そこを復唱するのは漏洩ではないので免除が要る (決定87)。
// だが免除が雑だと、**ナビが自分から言った語まで見逃す**。
func TestMustNotMentionExemptionIsNarrow(t *testing.T) {
	script := simScript{
		Turns: []simTurn{
			{Player: "読めました。デルタです。どうぞ"},
			{Player: "確かめ直しました。${navi_word_guess}でした。どうぞ"},
		},
	}
	vars := map[string]string{"navi_word_guess": "ECHO"}

	// プレイヤーが報告した語 → 復唱なので免除される
	if !simPlayerSaid(script, vars, "ECHO") {
		t.Error("プレイヤーが言った語が免除されていない — 正当な復唱が所見になる")
	}
	// プレイヤーが一度も言っていない語 → 免除されない (漏洩として拾う)
	if simPlayerSaid(script, vars, "FOXTROT") {
		t.Error("プレイヤーが言っていない語まで免除している — 漏洩を見逃す")
	}
}
