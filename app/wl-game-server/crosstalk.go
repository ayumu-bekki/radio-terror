package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 混線の系統 (docs/operation_flow.md §5.1)
const (
	crosstalkJamming = "jamming" // 邪魔者系: ゲームに絡む罠。色を指定する
	crosstalkAmbient = "ambient" // 環境ボイス系: 無害な生活ノイズ
	crosstalkUneasy  = "uneasy"  // 不穏系: 世界観を深める。色・操作の指示はしない
)

// crosstalkEventFile は「別現場の通信」のアセット名。
// ランダム再生に加え、他チームのCoreの exploded を契機にイベント駆動でも流す。
const crosstalkEventFile = "uneasy_bessgenba"

// CrosstalkLibrary は事前生成済みの混線音声アセットを管理する。
//
// 混線音声はすべて事前生成 (TTSの事前レンダリングまたは収録) したアセットで、
// サーバーは再生時にファイル選択のみ行う (§5.1 共通ルール)。
//
// ディレクトリ構成:
//
//	assets/crosstalk/
//	├── jamming/   … 邪魔者系。色バリエーションを持つため {name}_{色}.ogg
//	│   ├── aserase_A.ogg  aserase_B.ogg ... aserase_E.ogg
//	│   ├── hannin_A.ogg   ...
//	│   └── sasayaki_A.ogg ...
//	├── ambient/   … 環境ボイス系。{name}.ogg
//	│   ├── chushajo.ogg, keisatsu.ogg, hall_staff.ogg, ...
//	└── uneasy/    … 不穏系。{name}.ogg
//	    ├── kuromaku.ogg
//	    └── uneasy_bessgenba.ogg
type CrosstalkLibrary struct {
	root string

	// jamming は 名前 → 色 → ファイルパス
	jamming map[string]map[string]string
	// ambient / uneasy は 名前 → ファイルパス
	ambient map[string]string
	uneasy  map[string]string
}

// LoadCrosstalkLibrary は混線音声アセットの一覧を読み込む。
// アセットが未制作でもサーバーは起動できるよう、ディレクトリが無い場合は
// 空のライブラリを返す (再生時にスキップされる)。
func LoadCrosstalkLibrary(root string) *CrosstalkLibrary {
	lib := &CrosstalkLibrary{
		root:    root,
		jamming: make(map[string]map[string]string),
		ambient: make(map[string]string),
		uneasy:  make(map[string]string),
	}

	// 邪魔者系: {name}_{色}.ogg を name ごとにまとめる
	for _, path := range listOggFiles(filepath.Join(root, crosstalkJamming)) {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		idx := strings.LastIndex(base, "_")
		if idx < 0 {
			continue
		}
		name, color := base[:idx], base[idx+1:]
		if !isValidColor(color) {
			continue
		}
		if lib.jamming[name] == nil {
			lib.jamming[name] = make(map[string]string)
		}
		lib.jamming[name][color] = path
	}

	for _, path := range listOggFiles(filepath.Join(root, crosstalkAmbient)) {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		lib.ambient[base] = path
	}
	for _, path := range listOggFiles(filepath.Join(root, crosstalkUneasy)) {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		lib.uneasy[base] = path
	}

	log.Printf("[crosstalk] loaded assets: jamming=%d ambient=%d uneasy=%d",
		len(lib.jamming), len(lib.ambient), len(lib.uneasy))
	return lib
}

// listOggFiles はディレクトリ内の .ogg ファイルを列挙する。
func listOggFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".ogg") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	return paths
}

// PickJamming は邪魔者系のアセットを選ぶ。
//
// 指定する色は「現在の正解以外」から選ぶ。偶然正解と一致して
// 「邪魔者を信じたら成功した」となるのを防ぐ (§5.1)。
func (l *CrosstalkLibrary) PickJamming(rng *rand.Rand, answerColor string) (string, bool) {
	names := sortedKeys(l.jamming)
	if len(names) == 0 {
		return "", false
	}

	rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	for _, name := range names {
		candidates := make([]string, 0, len(allColors))
		for color, path := range l.jamming[name] {
			if color != answerColor {
				candidates = append(candidates, path)
			}
		}
		if len(candidates) > 0 {
			return candidates[rng.Intn(len(candidates))], true
		}
	}
	return "", false
}

// PickAmbient は環境ボイス系のアセットを選ぶ。
func (l *CrosstalkLibrary) PickAmbient(rng *rand.Rand) (string, bool) {
	return pickRandomValue(l.ambient, rng)
}

// PickUneasy は不穏系のアセットを選ぶ。
func (l *CrosstalkLibrary) PickUneasy(rng *rand.Rand) (string, bool) {
	return pickRandomValue(l.uneasy, rng)
}

// EventFile は「別現場の通信」のアセットを返す (イベント駆動再生用)。
func (l *CrosstalkLibrary) EventFile() (string, bool) {
	path, ok := l.uneasy[crosstalkEventFile]
	return path, ok
}

func pickRandomValue(m map[string]string, rng *rand.Rand) (string, bool) {
	keys := sortedKeysString(m)
	if len(keys) == 0 {
		return "", false
	}
	return m[keys[rng.Intn(len(keys))]], true
}

// CrosstalkScheduler は混線の再生タイミングを管理する。
//
// 発生タイミングはゲーム中のランダム。系統別の再生回数は難易度テンプレートで
// 指定する (控えめ運用でゲームのテンポを優先)。ナビゲーターの発話と重ならないよう
// サーバー側でスケジュールする (§5.1)。
type CrosstalkScheduler struct {
	lib     *CrosstalkLibrary
	bridges *BridgeRegistry

	rng   *rand.Rand
	rngMu sync.Mutex

	mu sync.Mutex
	// cancels は device_id → スケジュール停止関数
	cancels map[string]context.CancelFunc
	// speaking はナビゲーターが発話中のセッション (device_id)。
	// 発話中は混線を再生しない。
	speaking map[string]bool
}

func NewCrosstalkScheduler(lib *CrosstalkLibrary, bridges *BridgeRegistry, rng *rand.Rand) *CrosstalkScheduler {
	return &CrosstalkScheduler{
		lib:      lib,
		bridges:  bridges,
		rng:      rng,
		cancels:  make(map[string]context.CancelFunc),
		speaking: make(map[string]bool),
	}
}

// Start はセッションの混線スケジュールを開始する。
func (s *CrosstalkScheduler) Start(ctx context.Context, session *GameSession, sender *AudioSender) {
	if s.lib == nil {
		return
	}

	s.Stop(session.DeviceID)

	scheduleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	s.cancels[session.DeviceID] = cancel
	s.mu.Unlock()

	// 系統ごとに、難易度テンプレートで指定された回数だけ再生予定を組む
	plan := session.Built.Crosstalk
	countdown := time.Duration(session.Built.CountdownMS) * time.Millisecond

	go s.runSchedule(scheduleCtx, session, sender, crosstalkJamming, plan.Jamming, countdown)
	go s.runSchedule(scheduleCtx, session, sender, crosstalkAmbient, plan.Ambient, countdown)
	go s.runSchedule(scheduleCtx, session, sender, crosstalkUneasy, plan.Uneasy, countdown)
}

// Stop はセッションの混線スケジュールを停止する。
func (s *CrosstalkScheduler) Stop(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cancel, ok := s.cancels[deviceID]; ok {
		cancel()
		delete(s.cancels, deviceID)
	}
	delete(s.speaking, deviceID)
}

// SetSpeaking はナビゲーターの発話中フラグを更新する。
// 発話と混線が重ならないようにするために使う (§5.1)。
func (s *CrosstalkScheduler) SetSpeaking(deviceID string, speaking bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.speaking[deviceID] = speaking
}

// NotifyExplosion は他チームのCoreの爆発を契機に「別現場の通信」を流す
// (§5.1 イベント駆動の混線)。
//
// 会場に響いた本物の破裂音と通信内容が一致し、強い臨場感が生まれる。
// この再生は難易度テンプレートの回数枠の外 (該当イベント発生時のみ)。
func (s *CrosstalkScheduler) NotifyExplosion(ctx context.Context, explodedDeviceID string, targets []*GameSession) {
	if s.lib == nil {
		return
	}
	path, ok := s.lib.EventFile()
	if !ok {
		return
	}

	for _, target := range targets {
		if s.isSpeaking(target.DeviceID) {
			continue
		}
		log.Printf("[crosstalk] event-driven: device %s exploded -> notifying %s",
			explodedDeviceID, target.DeviceID)
		s.play(NewAudioSender(s.bridges, target.BridgeID), path)
	}
}

// runSchedule は1系統分の再生予定を実行する。
func (s *CrosstalkScheduler) runSchedule(ctx context.Context, session *GameSession, sender *AudioSender, kind string, count int, countdown time.Duration) {
	if count <= 0 {
		return
	}

	// カウントダウン中に count 回、区間を分けてランダムなタイミングで流す。
	// 開始直後と終盤は避け、中盤に散らす。
	segment := countdown / time.Duration(count+1)

	for i := 1; i <= count; i++ {
		s.rngMu.Lock()
		jitter := time.Duration(s.rng.Int63n(int64(segment)))
		s.rngMu.Unlock()

		wait := segment*time.Duration(i) + jitter - segment/2
		if wait < 0 {
			wait = 0
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// ナビゲーターの発話中は少し待って再試行する
		for attempt := 0; attempt < 10 && s.isSpeaking(session.DeviceID); attempt++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		path, ok := s.pick(session, kind)
		if !ok {
			continue
		}
		log.Printf("[crosstalk] playing %s for device %s: %s", kind, session.DeviceID, filepath.Base(path))
		s.play(sender, path)
	}
}

// pick は系統に応じたアセットを選ぶ。
func (s *CrosstalkScheduler) pick(session *GameSession, kind string) (string, bool) {
	s.rngMu.Lock()
	defer s.rngMu.Unlock()

	switch kind {
	case crosstalkJamming:
		// 邪魔者の色指定は現在の正解以外から選ぶ
		session.mu.Lock()
		stageIndex := session.StageIndex
		session.mu.Unlock()

		answer := ""
		if session.Built != nil && stageIndex >= 0 && stageIndex < len(session.Built.Stages) {
			answer = session.Built.Stages[stageIndex].Cut
		}
		return s.lib.PickJamming(s.rng, answer)

	case crosstalkAmbient:
		return s.lib.PickAmbient(s.rng)

	case crosstalkUneasy:
		return s.lib.PickUneasy(s.rng)

	default:
		return "", false
	}
}

// play は音声アセットを読み込んで無線へ送出する。
func (s *CrosstalkScheduler) play(sender *AudioSender, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[crosstalk] read asset error: %v", err)
		return
	}
	sender.Send(oneshot(data))
}

func (s *CrosstalkScheduler) isSpeaking(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speaking[deviceID]
}

// sortedKeys は map[string]map[string]string のキーをソートして返す。
func sortedKeys(m map[string]map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// sortedKeysString は map[string]string のキーをソートして返す。
func sortedKeysString(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// AssetSummary はアセットの読み込み状況を返す (Web画面用)。
func (l *CrosstalkLibrary) AssetSummary() map[string]int {
	if l == nil {
		return map[string]int{}
	}
	return map[string]int{
		crosstalkJamming: len(l.jamming),
		crosstalkAmbient: len(l.ambient),
		crosstalkUneasy:  len(l.uneasy),
	}
}

// AssetPathHint は未制作アセットの配置先をログに出すためのヒント文を返す。
func (l *CrosstalkLibrary) AssetPathHint() string {
	return fmt.Sprintf("place crosstalk assets under %s/{jamming,ambient,uneasy}/", l.root)
}
