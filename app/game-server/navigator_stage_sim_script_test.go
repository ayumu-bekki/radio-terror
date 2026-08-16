package main

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

	// 102 シグナル: ボタンの並びは装置に現れない。第一声で1色目を言うのが要件。
	// 第一声で1色目が色名で伝わっているかを見る。「ボタン」という単語の
	// 有無ではなく**色が伝わったか**が要件 (決定22)。
	"102": {
		StageID: "102",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "push_progress", HintLevel: HintL1,
				Event:  "プレイヤーがボタン入力を1個目まで正しく進めた。短く反応する。",
				Player: "1つ目のボタンを押しました。次は何色ですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "2つ目も押しました。次はどうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "3つ目まで押し終わりました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "ランプが1つだけ点いています。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 103 ホールド&カット: まずランプ状態を報告させる誘導ができているか。
	"103": {
		StageID: "103",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが2つ光っています。片方は点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "点滅しているほうのボタンを押さえればいいんですね。ダイヤルはどこですか。どうぞ"},
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

	// 104 コール&レスポンス: 点灯色の報告から対話が始まる。
	"104": {
		StageID: "104",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "点灯しているランプが1つ、点滅しているランプが1つあります。どうぞ"},
			{Trigger: "push_progress", HintLevel: HintL2,
				Event:  "プレイヤーがボタン入力を1個目まで正しく進めた。短く反応する。",
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

	// 105 早い者勝ち: procedure が無いステージ。hint_* だけで誘導できるかを見る。
	"105": {
		StageID: "105",
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

	// 106 いくつ光ってる?: 点灯数を数えさせ、ダイヤルへ結び付けさせる。
	// **現在は無効化されている** (.toml.disabled)。再開に備えて台本は残す。
	// 再開する場合は simColorToldByDesign への登録も必要
	// (切る線の色が装置から読み取れないため)。
	"106": {
		StageID: "106",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが何個か光っています。数えればいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "数えました。この数はどう使いますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "ダイヤルをその数に合わせました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "どの線を切りますか。どうぞ"},
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
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "もう一度、順番を言ってください。どうぞ"},
			{Trigger: "push_progress", HintLevel: HintL2,
				Event:  "プレイヤーがボタン入力を3個目まで正しく進めた。短く反応する。",
				Player: "3つ目まで押しました。残りをお願いします。どうぞ"},
			{Trigger: "wrong_action", HintLevel: HintL3,
				Event:  "プレイヤーが誤操作をした。叱咤しつつ励まし、注意を促す。",
				Player: "間違えました。最初からやり直しですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "5個とも押し終わりました。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 202 運命の二択: 紙資料を数えさせる。集計値の報告に応じて分岐できるか。
	"202": {
		StageID: "202",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが1つ点滅しています。シートを見ればいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "シートを開きました。何を数えますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "数え終わりました。28個ありました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "この数だとどちらの線ですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 203 暗号電文: モールス → 語 → 頭文字 → 対照表の色。
	// 音声認識の誤変換 (「三毛」等) を吸収できるかも見る。
	"203": {
		StageID: "203",
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
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "頭文字を対照表で引きました。この色でいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 204 切るな危険: 消灯している線を切らせない警告ができているか。
	"204": {
		StageID: "204",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "2つのランプが点滅していて、3つは消えています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "消えている線は切らないほうがいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "点滅している2つを見比べました。速さが違います。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "遅いほうを切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 205 ブループリント: **L4 でも色名を言ってはいけない**ステージ。
	// 端子番号だけで指示できているかが最大の確認点。
	"205": {
		StageID: "205",
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
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "どの端子の線を切りますか。色で教えてください。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 206 モグラ叩き: whack_completed の後に「最後に押した色」を思い出させる。
	"206": {
		StageID: "206",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが次々光ります。同じ色のボタンを押せばいいですか。どうぞ"},
			{Trigger: "whack_completed", HintLevel: HintL2,
				Event: "プレイヤーがモグラ叩きを完了した。最後に押した色が次の手がかりになる。"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "最後に押した色は覚えています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "その色の線を切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 207 仲間はずれ: 5色から周期の違う1色を探させる。
	"207": {
		StageID: "207",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "5つとも点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "よく見ると速さが同じではない気がします。どうぞ"},
			{Trigger: "silence", HintLevel: HintL3},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "1つだけ速いのが見つかりました。これを切りますか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 208 速さくらべ: 点灯1色を基準外と伝え、最速色を**プレイヤーに先に言わせる**。
	// answer に「照合の材料として使え」と書いてある指示が効いているかを見る。
	"208": {
		StageID: "208",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "1つだけ点きっぱなしで、残り4つが点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "点滅している4つを見比べています。どうぞ"},
			// **わざと間違った色を報告する**。色名を出さずに探し直させられるか。
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "一番速いのは${sim_wrong_color}色だと思います。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "もう一度見比べました。一番速いのは${cut}色でした。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 209 綱渡り: 危険位置は L1 で必ず伝える必要がある (伏せると事故になる)。
	// 危険位置 (${sim_forbidden}) が第一声に出ているかを直接見る。
	// 訊かれるまで伝わらないと即爆発の事故になるため。
	"209": {
		StageID:     "209",
		MustMention: []string{"${sim_forbidden}"},
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "危険な位置があるんですか。どこですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "分かりました。どこまで回せばいいですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "指示の位置に合わせました。止まらずに回せました。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "どの線を切りますか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 210 暗算ダイヤル: 「今切れ」とリアルタイム指示をしていないかを見る。
	"210": {
		StageID: "210",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "ランプが光っています。タイマーも動いています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "ランプの数は数えました。タイマーとどう関係しますか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "足すんですね。でも6以上になる時間があります。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "合わせました。いつ切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 212 息が合わない: 「速さ」ではなく「揃っているか」へ観点を切り替えさせる。
	"212": {
		StageID: "212",
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
			{Trigger: "player_message", HintLevel: HintL4,
				Player: "それを切ればいいですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// --- ハード (L4 は無効。L3 止まりで誘導しきれるかを見る) ---

	// 301 ジャストカット: 「今だ」とリアルタイム指示をしないことが要件。
	"301": {
		StageID: "301",
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

	// 302 追いかけダイヤル: 十の位に追従させる。届かない時間帯の待ちを伝えるか。
	"302": {
		StageID: "302",
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

	// 303 暗転: 全消灯の瞬間を待たせる。
	// **現在は無効化されている** (.toml.disabled)。再開に備えて台本は残す。
	// 再開する場合は simColorToldByDesign への登録も必要
	// (切る線の色が装置から読み取れないため)。
	"303": {
		StageID: "303",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "5つのランプがバラバラに点滅しています。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "たまに全部消える瞬間がある気がします。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "そのタイミングで切ればいいんですね。どの線ですか。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "ニッパーを構えました。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 305 二重復唱: 1周目は読み上げ、2周目は伏せる。切り分けができているか。
	// 第一声で1周目の列の1色目が伝わっているかを見る。
	"305": {
		StageID: "305",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "もう一度、順番を言ってください。どうぞ"},
			{Trigger: "push_progress", HintLevel: HintL2,
				Event:  "プレイヤーがボタン入力を5個目まで正しく進めた。短く反応する。",
				Player: "5個目まで押しました。次はどうしますか。どうぞ"},
			// **2周目は伏せる**のが仕様。ここで読み上げたら所見。
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "2周目の順番を忘れました。教えてください。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "やっぱり分かりません。順番をお願いします。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 306 三点確保: ボタン2つを押さえたまま切らせる。
	"306": {
		StageID: "306",
		Turns: []simTurn{
			// マネージャーへの応答 (カウントダウン開始前)。決定36。
			{Trigger: "session_ready", HintLevel: HintL1},
			{Trigger: "session_start", HintLevel: HintL1},
			{Trigger: "player_message", HintLevel: HintL1,
				Player: "点滅しているランプが2つ、点灯しているランプが1つあります。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL2,
				Player: "点滅している2つのボタンを押さえるんですね。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "2つとも押さえました。指が届きにくいです。どうぞ"},
			{Trigger: "player_message", HintLevel: HintL3,
				Player: "このまま切る線はどれですか。どうぞ"},
			// 台本の最後。課題を解いたあとの遷移で、次の課題も
			// 「ランプはどうなっている?」から入るかを見る (決定32)。
			{Trigger: "stage_cleared", HintLevel: HintL1,
				Event: "プレイヤーが1番目の課題を突破した。次の課題へ進む。"},
		},
	},

	// 307 我慢比べ: 保持 + タイミング。ここでも「今だ」と言わないことが要件。
	"307": {
		StageID: "307",
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

	// 308 ローマ字電文: 途中まで読めた報告を補って解読を急がせられるか。
	// L3 でも「色名そのものは言わず、その読みで合っていると確認する」のが仕様。
	"308": {
		StageID: "308",
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
}
