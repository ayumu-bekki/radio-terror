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

// endingAssetDir は終幕音声の置き場 (assets/ 配下)。
// ファイル名は <キャラクターID>_<defused|exploded>_<n>.ogg (n は 1 始まり。
// キャラクター定義の defused_lines / exploded_lines の順)。
const endingAssetDir = "ending"

// 終幕の種類。ファイル名とキャラクター定義のキーに使う。
const (
	endingDefused  = "defused"
	endingExploded = "exploded"
)

// 効果音アセット (docs/operation_flow.md §6)。
// 終幕の台詞は効果音と連結して再生する。
const (
	sfxSuccessFile = "success.ogg"
	sfxFailureFile = "failure.ogg"
)

// endingBusyMargin は再生完了予定に足す余裕。聞き直し・混線と揃えてある。
const endingBusyMargin = 2 * time.Second

// endingClip は終幕の音声1本と、その本文。
type endingClip struct {
	// text は会話ログへ残す本文 (表情タグを除いたもの)。
	text     string
	data     []byte
	duration time.Duration
}

// EndingPlayer は解除成功・爆発の最終メッセージを**事前収録の音声で**流す。
//
// 以前は終幕も実行時に生成 (reply + TTS) していたが、生成に十数秒かかり、
// 成功・失敗の瞬間から声が出るまで間が空いた。聞き直し (ReaskPlayer) と同じく
// 事前生成のアセットを選んで流すだけにする (ADR G-9)。実行時のAPIを使わないので
// 障害のさなかでも確実に流せる。
//
// 台詞は「直前の交信に触れない」形で書いてある (どの回にも流せるように)。
type EndingPlayer struct {
	// clips はキャラクターID → 種類 → 音声。
	clips map[string]map[string][]endingClip

	// sfxDir は効果音アセットのディレクトリ。終幕の音声の前へ連結する。
	sfxDir string

	logs      *SessionLogStore
	crosstalk *CrosstalkScheduler

	mu  sync.Mutex
	rng *rand.Rand
}

// NewEndingPlayer は assetDir/ending/ から終幕音声を読み込む。
//
// 音声が未配置でも起動は妨げない (その終幕は効果音だけになる)。ただし
// **無言に近くなることに気づけるよう**、キャラクターごとの読み込み結果を
// 起動ログに残す。
func NewEndingPlayer(assetDir string, characters []NavigatorCharacter, logs *SessionLogStore, crosstalk *CrosstalkScheduler, rng *rand.Rand) *EndingPlayer {
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	e := &EndingPlayer{
		clips:     make(map[string]map[string][]endingClip),
		logs:      logs,
		crosstalk: crosstalk,
		rng:       rng,
	}
	if assetDir == "" {
		log.Printf("[ending] disabled: asset dir not configured")
		return e
	}
	e.sfxDir = filepath.Join(assetDir, "sfx")

	loaded, covered := 0, 0
	for _, c := range characters {
		e.clips[c.ID] = make(map[string][]endingClip)
		complete := true
		for kind, lines := range map[string][]string{endingDefused: c.DefusedLines, endingExploded: c.ExplodedLines} {
			got := 0
			for i, text := range lines {
				path := filepath.Join(assetDir, endingAssetDir, fmt.Sprintf("%s_%s_%d.ogg", c.ID, kind, i+1))
				data, err := os.ReadFile(path)
				if err != nil {
					log.Printf("[ending] asset not found: %s", path)
					continue
				}
				e.clips[c.ID][kind] = append(e.clips[c.ID][kind], endingClip{
					text:     stripTTSTags(text),
					data:     data,
					duration: oggOpusDuration(data),
				})
				got++
			}
			loaded += got
			if got == 0 {
				complete = false
				log.Printf("[ending] no %s clip for %s (%s): 効果音だけになる", kind, c.Name, c.ID)
			}
		}
		if complete {
			covered++
		}
	}
	log.Printf("[ending] loaded %d clip(s), %d/%d character(s) have both defused and exploded",
		loaded, covered, len(characters))
	return e
}

// Play は終幕を流し、会話ログへ同じ文を残す。台詞の音声を流せたら true。
//
// 効果音は台詞の前へ連結して**1つの音声として**送る (間に無音を挟まない)。
// 台詞が無い・デコードできないときは効果音だけ、効果音も無いときは台詞だけを送る。
//
// コンソールモード (音声なし) では会話ログへ台詞を残すだけにする。
func (e *EndingPlayer) Play(sender *AudioSender, session *GameSession, kind string) bool {
	if e == nil || session == nil {
		return false
	}

	session.mu.Lock()
	consoleMode := session.ConsoleMode
	session.mu.Unlock()

	clip, ok := e.pick(session.Character.ID, kind)
	if !ok {
		log.Printf("[ending] no clip to play: device=%s character=%s kind=%s",
			session.DeviceID, session.Character.ID, kind)
	}

	if ok && e.logs != nil {
		e.logs.Append(session.SessionID, ConversationEntry{
			Sender:   session.Character.Name,
			Receiver: senderPlayer,
			Message:  clip.text,
		})
	}
	if consoleMode {
		return ok
	}

	sfxName := sfxFailureFile
	if kind == endingDefused {
		sfxName = sfxSuccessFile
	}
	data, duration := e.compose(clip, ok, loadSFXPCM(e.sfxDir, sfxName))
	if len(data) == 0 {
		log.Printf("[ending] nothing to send: device=%s kind=%s", session.DeviceID, kind)
		return false
	}
	if !sender.Send(oneshot(data)) {
		log.Printf("[ending] send failed (bridge=%s)", sender.BridgeID())
		return false
	}
	log.Printf("[ending] played to %s (%s/%s, %.1fs): %s",
		sender.BridgeID(), session.Character.ID, kind, duration.Seconds(), clip.text)

	// 鳴っている間は混線を流さない (ナビの発話と同じ扱い)
	if e.crosstalk != nil && duration > 0 {
		e.crosstalk.SetBusy(session.DeviceID, duration+endingBusyMargin)
	}
	return ok
}

// compose は効果音と台詞を1本の Ogg Opus にまとめる。
// 連結できない (台詞が無い・デコード不能・効果音が無い) ときは、あるものだけを返す。
func (e *EndingPlayer) compose(clip endingClip, hasClip bool, sfx []int16) ([]byte, time.Duration) {
	if !hasClip && len(sfx) == 0 {
		return nil, 0
	}
	if len(sfx) == 0 {
		return clip.data, clip.duration
	}

	pcm := sfx
	if hasClip {
		speech, err := decodeOggOpusToPCM(clip.data)
		if err != nil {
			// 台詞が読めない。効果音を捨てて台詞だけ送ると演出が欠けるので、
			// 台詞のほうを優先する (本文が無線に乗ることが終幕の役目)。
			log.Printf("[ending] WARN decode failed, sending clip without sfx: %v", err)
			return clip.data, clip.duration
		}
		pcm = append(append(make([]int16, 0, len(sfx)+len(speech)), sfx...), speech...)
	}
	ogg, err := encodePCMToOggOpus(pcm)
	if err != nil {
		log.Printf("[ending] WARN encode failed: %v", err)
		if hasClip {
			return clip.data, clip.duration
		}
		return nil, 0
	}
	return ogg, time.Duration(len(pcm)) * time.Second / sampleRate
}

// pick はそのキャラクターの音声から1本をランダムに選ぶ。
func (e *EndingPlayer) pick(characterID, kind string) (endingClip, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	clips := e.clips[characterID][kind]
	if len(clips) == 0 {
		return endingClip{}, false
	}
	return clips[e.rng.Intn(len(clips))], true
}

// loadSFXPCM は dir/name の効果音を 24kHz mono の PCM へデコードする。
// 未制作・デコード不能の場合は nil を返し、台詞だけを送る
// (効果音が無くてもゲームは続行できるため、ここで失敗させない)。
func loadSFXPCM(dir, name string) []int16 {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[ending] sfx not available (%s): %v", path, err)
		return nil
	}
	pcm, err := decodeOggOpusToPCM(data)
	if err != nil {
		// レート違い等で連結できない。アセットを 24kHz mono で作り直す必要がある。
		log.Printf("[ending] WARN sfx decode failed (%s): %v", path, err)
		return nil
	}
	return pcm
}
