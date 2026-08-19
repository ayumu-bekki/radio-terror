package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// announceIntervalDefault は自動送信局アナウンスの周期。
//
// 特小無線は誰でも使える共用チャンネルで、こちらが長時間占有していると
// 他の利用者の通信を妨げる。**同じチャンネルを使いたい人が申し出られる**よう、
// 定期的に「これは自動送信局である」と名乗る。
//
// 15分は「気づける程度に繰り返し、うるさくない」間隔として選んだ。
const announceIntervalDefault = 15 * time.Minute

// announceFile はアナウンス音声のファイル名 (assets/announce/ 配下)。
//
// 混線音声と同じく**事前生成したアセット**を再生するだけにする
// (ADR T-10 と同じ方針)。実行時 TTS にすると、毎回わずかに文言や
// 間が変わるうえ、API 障害時に名乗れなくなる。
const announceFile = "station_id.ogg"

// announceSpeakMargin は再生完了予定に足す余裕。
// 混線側の crosstalkBusyMargin と揃えてある。
const announceSpeakMargin = 2 * time.Second

// AnnounceScheduler は「体験中でない無線」へ定期的に自動送信局アナウンスを流す。
//
// **混線 (CrosstalkScheduler) とは逆向きの仕組み**である。混線は
// セッションごとに開始・停止するが、こちらはセッションが無い bridge を対象にする。
// そのためサーバー全体で1つだけ動かし、周期ごとに宛先を選び直す。
//
// 送出先は**セッションが紐づいていない bridge のみ**。ゲーム中の無線に
// 割り込むと体験を壊すため、バインド済みの bridge は必ず除外する
// (docs/operation_flow.md §7.3)。
type AnnounceScheduler struct {
	binder   *SessionBinder
	bridges  *BridgeRegistry
	interval time.Duration

	// path はアナウンス音声のパス。空なら機能ごと無効。
	path string

	mu sync.Mutex
	// busyUntil は bridge_id → 送出した音声の再生終了予定時刻。
	//
	// 混線側が device_id で持っているのと同じ理屈だが、こちらは
	// **セッションが無い bridge が相手なので device_id が存在しない**。
	// そのため bridge_id で持つ。
	busyUntil map[string]time.Time
}

// NewAnnounceScheduler はアナウンス送出を組み立てる。
//
// assetDir は assets ディレクトリ。この配下の announce/ を見る。
// 音声が未配置の場合は**無効なスケジューラを返す** (起動は妨げない)。
// 混線アセットと同じく、無いなら黙ってスキップする方が運用しやすい。
func NewAnnounceScheduler(assetDir string, binder *SessionBinder, bridges *BridgeRegistry, interval time.Duration) *AnnounceScheduler {
	if interval <= 0 {
		interval = announceIntervalDefault
	}

	s := &AnnounceScheduler{
		binder:    binder,
		bridges:   bridges,
		interval:  interval,
		busyUntil: make(map[string]time.Time),
	}

	if assetDir == "" {
		log.Printf("[announce] disabled: asset dir not configured")
		return s
	}

	path := filepath.Join(assetDir, "announce", announceFile)
	if _, err := os.Stat(path); err != nil {
		// 未配置でも起動は妨げない。件数ではなく**有無**をログに残し、
		// 「流れないはずなのに流れない」を切り分けられるようにする。
		log.Printf("[announce] disabled: asset not found (%s)", path)
		return s
	}

	s.path = path
	log.Printf("[announce] loaded asset: %s (interval %v)", path, interval)
	return s
}

// Enabled はアナウンス音声が使える状態かを返す (Web画面・起動ログ用)。
func (s *AnnounceScheduler) Enabled() bool {
	return s != nil && s.path != ""
}

// Interval は送出周期を返す (Web画面用)。
func (s *AnnounceScheduler) Interval() time.Duration {
	if s == nil {
		return 0
	}
	return s.interval
}

// Run は周期送出を開始する。ctx が終わるまで動き続ける。
//
// **周期はサーバー共通**にしてある。bridge ごとに独立させると同じ会場で
// バラバラに鳴り、運営が「今どれが鳴ったのか」を追えなくなる。
func (s *AnnounceScheduler) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.broadcast()
		}
	}
}

// broadcast は条件を満たす全 bridge へアナウンスを送る。
func (s *AnnounceScheduler) broadcast() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		log.Printf("[announce] read asset error: %v", err)
		return
	}
	duration := oggOpusDuration(data)

	for _, bridgeID := range s.bridges.IDs() {
		if !s.shouldSend(bridgeID) {
			continue
		}

		sender := NewAudioSender(s.bridges, bridgeID)
		if !sender.Send(oneshot(data)) {
			// 送出先が落ちた直後など。次の周期で拾い直す
			log.Printf("[announce] send failed: %s", bridgeID)
			continue
		}
		s.markBusy(bridgeID, duration+announceSpeakMargin)
		log.Printf("[announce] sent to %s (%.1fs)", bridgeID, duration.Seconds())
	}
}

// shouldSend はその bridge へ送ってよいかを判定する。
//
// **体験中の無線には流さない**。セッションが紐づいている bridge は
// カウントダウン中かもしれず、そこへ自動送信が割り込むと体験を壊す。
//
// 直前に自分が送った音声を再生中の場合も見送る。次の周期まで待てばよい
// (アナウンスは急ぐ性質のものではない)。
func (s *AnnounceScheduler) shouldSend(bridgeID string) bool {
	if s.binder != nil && s.binder.SessionForBridge(bridgeID) != nil {
		return false
	}
	return s.busyFor(bridgeID) <= 0
}

func (s *AnnounceScheduler) markBusy(bridgeID string, d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busyUntil[bridgeID] = time.Now().Add(d)
}

// busyFor はその bridge が塞がっている残り時間を返す。
func (s *AnnounceScheduler) busyFor(bridgeID string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	until, ok := s.busyUntil[bridgeID]
	if !ok {
		return 0
	}
	if remain := time.Until(until); remain > 0 {
		return remain
	}
	delete(s.busyUntil, bridgeID)
	return 0
}
