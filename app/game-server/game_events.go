package main

// デバイスからの進行イベントを受けて演出へつなぐ。
//
// Core は Wi-Fi が切れても単体でゲームを完遂する設計のため、ここに来るのは
// **既に起きたことの通知**であってゲームの判定ではない
// (docs/game_session_design.md)。サーバーはそれを交信ログとナビゲーターの
// 発話へ変換する役に徹する。
//
// **発話しないイベントがある**のが要点 — ナビゲーターは無線の向こうにいて
// 装置を見ていない。押下の進捗や色合わせのミスに反応すると、手元が
// 見えていることになり前提が崩れる (ADR N-26)。

import (
	"context"
	"fmt"
	"log"
	"time"
)

// HandleDeviceMessage は Core からの進行イベントを受け、ナビゲーター演出へ接続する
// (docs/game_session_design.md §7.2 / docs/navigator_design.md §3.5)。
// onStageCleared はステージ突破を記録する。
//
// **発話は伴わない** (下のコメント参照)。そのため sender を取らない。
//
// msg.StageIndex は**クリアした**ステージの番号 (デバイスは送信後に進める)。
func (c *GameCoordinator) onStageCleared(session *GameSession, msg *deviceMessage) {
	c.logEvent(session, EventStageCleared,
		fmt.Sprintf("✓ ステージ%d クリア: %s", msg.StageIndex+1, c.stageName(session, msg.StageIndex)),
		msg.StageIndex, msg.RemainingMS)

	// **最終ステージのクリアでは何も起こさない。**
	//
	// デバイスは最後の1本を切ると stage_cleared に続けて defused を送る。
	// 完了の演出は直後に届く defused が担当する。
	nextName := c.stageName(session, msg.StageIndex+1)
	if nextName == "" {
		log.Printf("[game] final stage cleared: device=%s (defused を待つ)", msg.DeviceID)
		return
	}

	c.logEvent(session, EventStageStart,
		fmt.Sprintf("ステージ%d開始: %s", msg.StageIndex+2, nextName),
		msg.StageIndex+1, msg.RemainingMS)

	// **突破では発話しない** (ADR N-26 の延長)。
	//
	// ナビゲーターは無線の向こうにいて装置を見ていない。線が切れたことも
	// 次の課題に移ったことも**知りようがない**。ここで「切れたか?
	// 次のランプはどうだ?」と切り出すと、突破の瞬間を見ていることになり、
	// 押下の進捗・色合わせの完了に反応しないと決めた線が、
	// **ステージの区切りでだけ破れる**。テストプレイでも違和感が出た。
	//
	// 代わりに「突破したがまだ何も聞いていない」印を立てる。プレイヤーが
	// 報告してくれば player_message で応じ、黙ったままなら無応答の
	// 声掛け (SilenceWatcher) が状況を尋ねる (ADR P-9)。
	// 声掛けの待ち時間もここから短縮する — 突破直後に手が止まると、
	// 通常の幅では最大60秒、無線に何も流れない。
	//
	// **無応答の計測は突破の瞬間から数え直す。**
	//
	// 以前は「突破の直前にプレイヤーが喋っていれば、その時点からの経過を
	// そのまま引き継ぐ」設計だった。しかし手を止めてから
	// (装置を眺める・資料を読むなどで) stageClearedWaitScale による
	// 短縮後の閾値をとうに超えていた場合、**突破の直後にいきなり
	// 声を掛けてしまう** (実測: 突破の2秒後に発話。2026-09-06)。
	// プレイヤーは次の課題を眺め始めたばかりで、急かされたように感じる。
	// 突破そのものは装置からの通知なので、ここでリセットしても
	// 「ナビ自身の発話では計測を延ばさない」原則には触れない。
	session.mu.Lock()
	session.awaitingStageReport = true
	session.mu.Unlock()
	c.silence.NoticeStageCleared(msg.DeviceID)

	log.Printf("[game] stage cleared (発話しない): device=%s next=%s", msg.DeviceID, nextName)
}

// onWrongAction は誤操作を記録する。
//
// **発話しない** (決定48の拡張)。ナビゲーターは装置を見ていないので、
// どの色が光っていたかも押し間違えたかも分からない。ミスは上面LEDの
// 赤い閃光とブザー・残り時間の減りで既に伝わっている。ログには残すので、
// 後から何が起きたかは追える。
func (c *GameCoordinator) onWrongAction(session *GameSession, msg *deviceMessage) {
	session.mu.Lock()
	session.progress.WrongActions++
	session.mu.Unlock()

	c.logEvent(session, EventWrongAction,
		fmt.Sprintf("✗ %s%s", describeWrongAction(msg), describePenalty(msg.PenaltyMS)),
		msg.StageIndex, msg.RemainingMS)
}

func (c *GameCoordinator) HandleDeviceMessage(ctx context.Context, msg *deviceMessage) {
	session := c.sessionFor(msg.DeviceID)
	if session == nil {
		// バインド前・復元前のデバイスからの報告 (device_status など) は状態更新のみ
		return
	}

	sender := NewAudioSender(c.bridges, session.BridgeID)

	// stage_cleared の stage_index は「**クリアした**ステージ」の番号
	// (デバイスは送信後に AdvanceStage する)。そのまま代入すると
	// session.StageIndex がクリア済みのステージを指したままになり、
	// ナビゲーターが**次の課題の知識を持たずに喋る**
	// (実運用で発生: ステージ2でランプに気づかせるヒントが出なかった)。
	// この1件だけ +1 して次のステージを指す。
	nextStage := msg.StageIndex
	if msg.Type == msgStageCleared {
		nextStage = msg.StageIndex + 1
	}

	session.mu.Lock()
	previousStage := session.StageIndex
	session.StageIndex = nextStage
	session.RemainingMS = msg.RemainingMS
	if msg.State != "" {
		session.State = msg.State
	}
	stageChanged := nextStage != previousStage
	session.mu.Unlock()

	// ステージが切り替わったらヒントレベルを L1 にリセットする
	// (docs/navigator_design.md §3.2)
	if stageChanged {
		session.mu.Lock()
		session.progress.Reset(time.Now())
		session.mu.Unlock()
	}

	switch msg.Type {
	case msgDeviceStatus:
		// 再同期のみ。演出は行わない (§7.3)
		return

	case msgSessionAccepted:
		log.Printf("[game] session accepted by device %s", msg.DeviceID)
		return

	case msgSessionRejected:
		log.Printf("[game] session REJECTED by device %s: reason=%s detail=%s",
			msg.DeviceID, msg.Reason, msg.Detail)
		c.binder.Release(msg.DeviceID)
		return

	case msgStageCleared:
		c.onStageCleared(session, msg)

	case msgColorMatchCompleted:
		c.logEvent(session, EventColorMatchDone, "色合わせ完了", msg.StageIndex, msg.RemainingMS)

		// **発話しない** (ADR N-26)。
		//
		// 色合わせの完了はボタンを押し切ったという**装置の中の出来事**で、
		// ナビゲーターは装置を見ていないので知りようがない。ここで
		// 「色合わせが終わったな」と喋ると、押下に反応しないと決めた
		// `push_progress` (下) と同じ矛盾が最後の1押しでだけ起きる。
		//
		// 加えて実害がある — 完了直後はプレイヤーが「最後に押した色」を
		// 反芻している最中で、そこへ無線が入ると**記憶を上書きする**
		// (204 は正解がプレイヤーの記憶にしかない。ADR N-36)。
		//
		// プレイヤーが「押し終わりました」と報告してきたときに
		// `player_message` で応じればよい。

	case msgPushProgress:
		// ログは毎回残す (後から入力の進み方を追えるようにする)
		c.logEvent(session, EventPushProgress,
			fmt.Sprintf("ボタン入力 %d個目まで正解", msg.SeqIndex), msg.StageIndex, msg.RemainingMS)

		// **発話しない** (決定48)。
		//
		// ナビゲーターは無線の向こうにいて装置を見ていない。ボタンを押した
		// だけで「今ので合ってる」と反応するのは**手元が見えている**ことに
		// なり、無線で状況を伝え合う前提が崩れる。
		// プレイヤーが報告してきたときに `player_message` で応じればよい。

	case msgWrongAction:
		c.onWrongAction(session, msg)

	case msgExploded:
		log.Printf("[game] exploded: device=%s reason=%s", msg.DeviceID, msg.Reason)
		c.logEvent(session, EventExploded,
			fmt.Sprintf("✗✗ 爆発 (%s) — 解体失敗", describeExplodeReason(msg)),
			msg.StageIndex, msg.RemainingMS)
		c.finishSession(ctx, session, 0)
		// 最終メッセージを流し終えてからバインドを解放し、以後はカラスに引き継ぐ
		c.speakAsyncThen(ctx, sender, session, "exploded",
			"解体は失敗し、装置が起動してしまった。失敗を受け止めるメッセージを返す。",
			func() { c.releaseAfterFinish(context.WithoutCancel(ctx), session) })

		// 他チームのCoreの爆発を契機に「別現場の通信」を流す (§5.1 イベント駆動)
		if c.crosstalk != nil {
			c.crosstalk.NotifyExplosion(ctx, session.DeviceID, c.binder.PlayingSessions(session.DeviceID))
		}

	case msgDefused:
		log.Printf("[game] defused: device=%s remaining=%dms", msg.DeviceID, msg.RemainingMS)
		c.logEvent(session, EventDefused,
			fmt.Sprintf("★ 解除成功 — スコア(残り時間) %.1f秒", float64(msg.RemainingMS)/1000),
			msg.StageIndex, msg.RemainingMS)
		c.finishSession(ctx, session, msg.RemainingMS)
		// 最終メッセージを流し終えてからバインドを解放し、以後はカラスに引き継ぐ
		c.speakAsyncThen(ctx, sender, session, "defused",
			fmt.Sprintf("解除に成功した!残り時間%d秒でクリア。祝福する。", msg.RemainingMS/1000),
			func() { c.releaseAfterFinish(context.WithoutCancel(ctx), session) })
	}

	c.persist(ctx, session)
}
