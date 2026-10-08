package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// reaskAssetDir は聞き直し音声の置き場 (assets/ 配下)。
// ファイル名は <キャラクターID>_<n>.ogg (n は 1 始まり。キャラクター定義の
// reask_lines の順)。
const reaskAssetDir = "reask"

// reaskBusyMargin は再生完了予定に足す余裕。混線・アナウンスと揃えてある。
const reaskBusyMargin = 2 * time.Second

// reaskClip は聞き直しの音声1本と、その本文。
type reaskClip struct {
	text     string
	data     []byte
	duration time.Duration
}

// ReaskPlayer は「よく聞き取れなかったので、もう一度お願いします」を
// **事前収録の音声で**流す。
//
// 書き起こしや発話の生成が失敗したとき、実行時のAPIを呼ばずに済ませるため
// 事前生成したアセットを再生するだけにする (混線・アナウンスと同じ方針。
// ADR T-10)。障害のさなかでも確実に流せる。失敗したAPIは再試行せず、
// プレイヤーにもう一度話してもらう — 待って撃ち直すより無線の間が短い。
//
// **プレイヤーが話した直後の失敗にだけ使う。** 無応答の声掛けやイベント起点の
// 発話が失敗したときは流さない (話していない相手に「もう一度」と言うことになる)。
type ReaskPlayer struct {
	// clips はキャラクターID → 聞き直しの音声。
	clips map[string][]reaskClip

	logs      *SessionLogStore
	crosstalk *CrosstalkScheduler

	mu sync.Mutex
	// last は device_id → 直近に流した clips の添字 (同じ台詞の連続を避ける)。
	last map[string]int
	// until は device_id → 流している音声の再生終了予定。書き起こしが続けて
	// 失敗したときに聞き直しが重なって鳴るのを防ぐ。
	until map[string]time.Time
	rng   *rand.Rand
}

// NewReaskPlayer は assetDir/reask/ から聞き直し音声を読み込む。
//
// 音声が未配置でも起動は妨げない (無いなら黙ってスキップする。混線アセットと
// 同じ方針)。ただし**無言になることに気づけるよう**、キャラクターごとの
// 読み込み結果を起動ログに残す。
func NewReaskPlayer(assetDir string, characters []NavigatorCharacter, logs *SessionLogStore, crosstalk *CrosstalkScheduler, rng *rand.Rand) *ReaskPlayer {
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	r := &ReaskPlayer{
		clips:     make(map[string][]reaskClip),
		logs:      logs,
		crosstalk: crosstalk,
		last:      make(map[string]int),
		until:     make(map[string]time.Time),
		rng:       rng,
	}
	if assetDir == "" {
		log.Printf("[reask] disabled: asset dir not configured")
		return r
	}

	loaded, covered := 0, 0
	for _, c := range characters {
		got := 0
		for i, text := range c.ReaskLines {
			path := filepath.Join(assetDir, reaskAssetDir, fmt.Sprintf("%s_%d.ogg", c.ID, i+1))
			data, err := os.ReadFile(path)
			if err != nil {
				log.Printf("[reask] asset not found: %s", path)
				continue
			}
			r.clips[c.ID] = append(r.clips[c.ID], reaskClip{
				text:     text,
				data:     data,
				duration: oggOpusDuration(data),
			})
			got++
		}
		loaded += got
		if got > 0 {
			covered++
		} else {
			log.Printf("[reask] no clip for %s (%s): 聞き直しは無言になる", c.Name, c.ID)
		}
	}
	log.Printf("[reask] loaded %d clip(s) for %d/%d character(s)", loaded, covered, len(characters))

	// カラス (疎通確認・終了後の無線の相手)。セッションが無いので会話ログは使わない。
	for i, text := range testResponderReaskLines {
		path := filepath.Join(assetDir, reaskAssetDir, fmt.Sprintf("%s_%d.ogg", crowReaskID, i+1))
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[reask] asset not found: %s", path)
			continue
		}
		r.clips[crowReaskID] = append(r.clips[crowReaskID], reaskClip{
			text: text, data: data, duration: oggOpusDuration(data),
		})
	}
	if len(r.clips[crowReaskID]) == 0 {
		log.Printf("[reask] no clip for crow: カラスの聞き直しは無言になる")
	} else {
		log.Printf("[reask] loaded %d clip(s) for crow", len(r.clips[crowReaskID]))
	}
	return r
}

// crowReaskID はカラスの聞き直し音声のキー (ファイル名 crow_<n>.ogg)。
// ナビゲーターのキャラクターIDと重ならない名前にしてある。
const crowReaskID = "crow"

// PlayCrow はカラスの聞き直しを流す。流せたら true。
//
// セッション未バインドの bridge (開始前の疎通確認・終了後) で、書き起こしや応答の
// 生成に失敗したときに使う。セッションが無いので会話ログへは残さない。
// 鳴っている間は重ねない (bridge ごとに管理する)。
func (r *ReaskPlayer) PlayCrow(sender *AudioSender) bool {
	if r == nil || sender == nil {
		return false
	}
	key := "bridge:" + sender.BridgeID()
	clip, ok := r.pick(key, crowReaskID)
	if !ok {
		return false
	}
	if !sender.Send(oneshot(clip.data)) {
		log.Printf("[reask] send failed (bridge=%s)", sender.BridgeID())
		return false
	}
	log.Printf("[reask] played to %s (crow, %.1fs): %s", sender.BridgeID(), clip.duration.Seconds(), clip.text)

	r.mu.Lock()
	r.until[key] = time.Now().Add(clip.duration + reaskBusyMargin)
	r.mu.Unlock()
	return true
}

// Play は聞き直しを流し、会話ログへ同じ文を残す。流せたら true。
//
// 会話ログへ残すのは、次の発話生成が「こちらが聞き直した」ことを知るため
// (残さないとプレイヤーの発話が2つ続いて見え、聞き直しの文脈が消える)。
//
// 流さない場合: 終了済みのセッション (終幕後はカラスへ引き継ぎ済み)、
// コンソールモード (音声なし)、音声が未配置のキャラクター、直前の聞き直しが
// まだ鳴っている間。
func (r *ReaskPlayer) Play(sender *AudioSender, session *GameSession) bool {
	if r == nil || session == nil {
		return false
	}

	session.mu.Lock()
	finished := session.Finished
	consoleMode := session.ConsoleMode
	session.mu.Unlock()
	if finished || consoleMode {
		return false
	}

	clip, ok := r.pick(session.DeviceID, session.Character.ID)
	if !ok {
		log.Printf("[reask] no clip to play: device=%s character=%s", session.DeviceID, session.Character.ID)
		return false
	}

	if !sender.Send(oneshot(clip.data)) {
		log.Printf("[reask] send failed (bridge=%s)", sender.BridgeID())
		return false
	}
	log.Printf("[reask] played to %s (%s, %.1fs): %s",
		sender.BridgeID(), session.Character.ID, clip.duration.Seconds(), clip.text)

	r.mu.Lock()
	r.until[session.DeviceID] = time.Now().Add(clip.duration + reaskBusyMargin)
	r.mu.Unlock()

	if r.logs != nil {
		r.logs.Append(session.SessionID, ConversationEntry{
			Sender:   session.Character.Name,
			Receiver: senderPlayer,
			Message:  clip.text,
		})
	}
	// 鳴っている間は混線を流さない (ナビの発話と同じ扱い)
	if r.crosstalk != nil && clip.duration > 0 {
		r.crosstalk.SetBusy(session.DeviceID, clip.duration+reaskBusyMargin)
	}
	return true
}

// pick はそのキャラクターの音声から1本選ぶ。直前と同じ台詞は避ける。
// 直前の聞き直しがまだ鳴っている間は選ばない。
func (r *ReaskPlayer) pick(deviceID, characterID string) (reaskClip, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if until, ok := r.until[deviceID]; ok && time.Now().Before(until) {
		return reaskClip{}, false
	}
	clips := r.clips[characterID]
	if len(clips) == 0 {
		return reaskClip{}, false
	}

	idx := r.rng.Intn(len(clips))
	if prev, ok := r.last[deviceID]; ok && len(clips) > 1 && idx == prev {
		idx = (idx + 1 + r.rng.Intn(len(clips)-1)) % len(clips)
	}
	r.last[deviceID] = idx
	return clips[idx], true
}
