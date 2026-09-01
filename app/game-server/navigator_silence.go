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

// run は無応答を監視し、待ち時間を超えたら声を掛ける。
func (w *SilenceWatcher) run(ctx context.Context, session *GameSession) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[silence] panic: %v", rec)
		}
	}()

	for {
		wait := w.nextWait()

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// 終了済みのセッションでは声を掛けない。爆発・解除の最終メッセージの
		// あとに「どうした?」と続くと、締めた交信が台無しになる。
		session.mu.Lock()
		finished := session.Finished
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
		if silent < wait {
			continue
		}

		// 無線が塞がっている間は待つ。半二重なので、混線やナビ自身の発話に
		// 重ねると**プレイヤーの送信を潰す**(docs/operation_flow.md §5.1)。
		if w.crosstalk != nil {
			if busy := w.crosstalk.BusyFor(session.DeviceID); busy > 0 {
				continue
			}
		}

		log.Printf("[silence] no reply for %v: device=%s", silent.Round(time.Second), session.DeviceID)

		sender := NewAudioSender(w.bridges, session.BridgeID)
		if err := w.speaker.Speak(ctx, sender, session, "silence", ""); err != nil {
			log.Printf("[silence] speak error: %v", err)
			continue
		}

		// 声を掛けた時点から数え直す。掛け直すまでの間隔も同じ幅で引く。
		w.mu.Lock()
		if _, watching := w.cancels[session.DeviceID]; watching {
			w.lastHeard[session.DeviceID] = time.Now()
		}
		w.mu.Unlock()
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
