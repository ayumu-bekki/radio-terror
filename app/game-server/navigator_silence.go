package main

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"
)

// SilenceWatcher はプレイヤーの応答が途絶えたときにナビゲーターから
// 声を掛ける (docs/navigator_design.md §3.5 の silence トリガー)。
//
// 無線は**プレイヤーから話しかけるもの**という前提で作ってあるため、
// 手が止まって黙り込むと**サーバー側に何も届かず、時間だけが減る**。
// 装置の前で固まっているのか、資料を読んでいるのか、こちらからは
// 区別できない。区別できない以上、**声を掛けて状況を尋ねる**しかない。
//
// 待ち時間は幅を持たせて毎回引き直す (固定間隔にしない。prompt.toml)。
type SilenceWatcher struct {
	speaker   NavigatorSpeaker
	bridges   *BridgeRegistry
	crosstalk *CrosstalkScheduler

	minWait time.Duration
	maxWait time.Duration

	rng   *rand.Rand
	rngMu sync.Mutex

	mu sync.Mutex
	// cancels は device_id → 監視停止関数
	cancels map[string]context.CancelFunc
	// lastHeard は device_id → 最後にプレイヤーの発話を受け取った時刻
	lastHeard map[string]time.Time
}

// stageClearedWaitScale は課題突破の直後に待ち時間を詰める割合。
//
// **突破は無線に何も流れない** (ADR N-26 の延長。ナビゲーターは装置を
// 見ていないので突破を知らない)。プレイヤーが自分から報告してくれば
// player_message で応じるが、黙って次の課題を眺め始めると、通常の幅
// (40〜60秒) では**最大60秒、無線が完全に無音**になる。切れたかどうかも
// 分からないまま時間だけが減るので、最初の1回だけ早めに声を掛ける。
//
// **詰めすぎない。** 突破直後は次の装置を見回している最中で、
// そこへすぐ被せると考える時間を奪う (silence_min_ms と同じ理由)。
// 0.55 は 40〜60秒 → 22〜33秒。
const stageClearedWaitScale = 0.55

func NewSilenceWatcher(
	speaker NavigatorSpeaker,
	bridges *BridgeRegistry,
	crosstalk *CrosstalkScheduler,
	minWait, maxWait time.Duration,
	rng *rand.Rand,
) *SilenceWatcher {
	if maxWait < minWait {
		maxWait = minWait
	}
	return &SilenceWatcher{
		speaker:   speaker,
		bridges:   bridges,
		crosstalk: crosstalk,
		minWait:   minWait,
		maxWait:   maxWait,
		rng:       rng,
		cancels:   make(map[string]context.CancelFunc),
		lastHeard: make(map[string]time.Time),
	}
}

// Start はセッションの無応答監視を開始する。
func (w *SilenceWatcher) Start(ctx context.Context, session *GameSession) {
	if w == nil || w.speaker == nil {
		return
	}

	w.Stop(session.DeviceID)

	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.mu.Lock()
	w.cancels[session.DeviceID] = cancel
	w.lastHeard[session.DeviceID] = time.Now()
	w.mu.Unlock()

	go w.run(watchCtx, session)
}

// Stop は監視を停止する。
func (w *SilenceWatcher) Stop(deviceID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if cancel, ok := w.cancels[deviceID]; ok {
		cancel()
		delete(w.cancels, deviceID)
	}
	delete(w.lastHeard, deviceID)
}

// NoticeStageCleared は課題突破の瞬間に無応答の計測をやり直す。
//
// 通常の無応答計測は**やり直さない** — 突破の直前にプレイヤーが喋っていれば、
// その時点からの経過をそのまま引き継ぐ設計だった。しかしこれだと、
// プレイヤーが手を止めてから (装置を眺める・資料を読むなどで)
// stageClearedWaitScale による短縮後の閾値を超えるまで黙っていた場合、
// **突破の直後にいきなり声を掛けてしまう** (実測: 突破の2秒後に発話。
// 2026-09-06)。プレイヤーは次の課題を眺め始めたばかりで、急かされたように
// 感じる。突破そのものは装置からの通知であってナビゲーターの発話ではないため、
// Notice (プレイヤーの発話でのみ呼ぶ) とは別に、ここでも計測をリセットする。
func (w *SilenceWatcher) NoticeStageCleared(deviceID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, watching := w.cancels[deviceID]; !watching {
		return
	}
	w.lastHeard[deviceID] = time.Now()
}

// Notice は「プレイヤーの声が届いた」ことを記録し、無応答の計測を最初からやり直す。
//
// **ナビゲーター自身の発話では呼ばない。** ナビが喋ったことを応答と見なすと、
// プレイヤーが黙ったままでも計測が延び続け、二度と声を掛けられなくなる。
func (w *SilenceWatcher) Notice(deviceID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, watching := w.cancels[deviceID]; !watching {
		return
	}
	w.lastHeard[deviceID] = time.Now()
}

// silenceTickMax は無応答の判定を見直す間隔の上限。
//
// 待ち時間 (wait) は眠りに入る前に引くが、**眠っている間に課題が突破される**
// ことがある。突破すると閾値が縮む (stageClearedWaitScale) ので、引いた時点の
// 値のまま眠り続けると縮んだ意味が無い。刻んで起き、そのつど現在の閾値と
// 突き合わせる。
//
// 2秒あれば十分。閾値は数十秒の単位なので、この粒度のずれは体感に出ない。
const silenceTickMax = 2 * time.Second

// tick は判定を見直す間隔を返す。
//
// 設定された待ち時間が短い場合 (テスト) は、上限そのままだと最初の判定が
// 待ち時間より後になり、いつまでも声を掛けられない。閾値を割り込まない
// 細かさまで落とす。
func (w *SilenceWatcher) tick() time.Duration {
	// 突破後は閾値が縮むので、縮んだ側に合わせる
	shortest := time.Duration(float64(w.minWait) * stageClearedWaitScale)
	if step := shortest / 4; step < silenceTickMax {
		if step < time.Millisecond {
			step = time.Millisecond
		}
		return step
	}
	return silenceTickMax
}

// run は無応答を監視し、待ち時間を超えたら声を掛ける。
func (w *SilenceWatcher) run(ctx context.Context, session *GameSession) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[silence] panic: %v", rec)
		}
	}()

	wait := w.nextWait()
	tick := w.tick()

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}

		// 終了済みのセッションでは声を掛けない。爆発・解除の最終メッセージの
		// あとに「どうした?」と続くと、締めた交信が台無しになる。
		session.mu.Lock()
		finished := session.Finished
		// 「課題を突破したが、まだプレイヤーの声を聞いていない」印。
		// **セッション側が正本**で、プレイヤーの発話 (NoteReply) で下りる。
		cleared := session.awaitingStageReport
		session.mu.Unlock()
		if finished {
			return
		}

		// 最後に声を聞いてからの経過。待っている間にプレイヤーが
		// 喋っていれば (Notice で更新される) 計測はやり直しになる。
		silent, watching := w.silentFor(session.DeviceID)
		if !watching {
			return
		}

		// 課題を突破したのに何も聞こえてこない場合は早めに声を掛ける。
		// 突破そのものは無線に流れないため (ADR N-26 の延長)、
		// 通常の幅で待つと最大60秒、無線が無音になる。
		threshold := wait
		if cleared {
			threshold = time.Duration(float64(wait) * stageClearedWaitScale)
		}
		if silent < threshold {
			continue
		}

		// 無線が塞がっている間は待つ。半二重なので、混線やナビ自身の発話に
		// 重ねると**プレイヤーの送信を潰す**(docs/operation_flow.md §5.1)。
		if w.crosstalk != nil {
			if busy := w.crosstalk.BusyFor(session.DeviceID); busy > 0 {
				continue
			}
		}

		// **突破の直後かどうかでトリガーを分ける。**
		//
		// 突破後にプレイヤーが黙っている場合、ナビゲーターが知りたいのは
		// 「切れたのか」「次のランプはどうなっているか」で、通常の
		// 「どこで止まっている?」とは尋ねる中身が違う。
		trigger := "silence"
		if cleared {
			trigger = "silence_after_stage"
		}

		log.Printf("[silence] no reply for %v: device=%s trigger=%s",
			silent.Round(time.Second), session.DeviceID, trigger)

		sender := NewAudioSender(w.bridges, session.BridgeID)
		if err := w.speaker.Speak(ctx, sender, session, trigger, ""); err != nil {
			log.Printf("[silence] speak error: %v", err)
			continue
		}

		// **突破の印はここで下ろす。** 尋ね終えた以上、次も同じ問いかけを
		// 繰り返すのは無意味 (返事が無いのは装置の話ではなく、
		// 聞こえていないか手が離せないかのどちらか)。
		session.mu.Lock()
		session.awaitingStageReport = false
		session.mu.Unlock()

		// 声を掛けた時点から数え直す。掛け直すまでの間隔も同じ幅で引き直す。
		w.mu.Lock()
		if _, watching := w.cancels[session.DeviceID]; watching {
			w.lastHeard[session.DeviceID] = time.Now()
		}
		w.mu.Unlock()

		wait = w.nextWait()
	}
}

// silentFor は最後にプレイヤーの声を聞いてからの経過時間を返す。
// 監視対象でなければ watching = false。
func (w *SilenceWatcher) silentFor(deviceID string) (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	last, ok := w.lastHeard[deviceID]
	if !ok {
		return 0, false
	}
	return time.Since(last), true
}

// nextWait は次に声を掛けるまでの待ち時間を幅の中から引く。
func (w *SilenceWatcher) nextWait() time.Duration {
	if w.maxWait <= w.minWait {
		return w.minWait
	}
	w.rngMu.Lock()
	defer w.rngMu.Unlock()

	span := int64(w.maxWait - w.minWait)
	return w.minWait + time.Duration(w.rng.Int63n(span+1))
}
