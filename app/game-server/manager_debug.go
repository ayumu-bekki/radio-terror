package main

// デバッグ用のセッション開始ページ (`/manager/debug`)。
//
// Core・難易度・オペレーター・無線・ステージを指定して開始する**開発用**の口。
// 当日の運営では使わないため、他のページからはリンクしない (ADR M-6)。
//
// 指定できるのは**ステージ構成だけ**で、残り時間・ヒント・混線・入力量は
// 難易度テンプレートから引く。開始そのものは音声申告と同じ
// `StartSessionWith` を通す — 別経路を作ると本番と挙動がずれる。

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
)

// --- デバッグ用のセッション開始 ---

// debugPageData はデバッグ開始ページのテンプレートへ渡す値。
type debugPageData struct {
	Devices      []debugDeviceView
	Bridges      []string
	Difficulties []debugChoice
	Characters   []debugChoice

	// StageGroups は難易度ごとに束ねたステージ。
	// 18個を平坦に並べると目的のものを探しにくいため区切って出す。
	StageGroups []debugStageGroup

	// MaxStages は選択できるステージ数の上限 (画面の注意書きに出す)
	MaxStages int
	// Error は直前の開始が失敗した理由 (成功時・初回表示は空)
	Error string
}

// debugChoice は選択肢1つ (値と表示名)。
type debugChoice struct {
	Value string
	Label string
}

// debugStageGroup は難易度ごとのステージ群。
//
// **番号帯 (100/200/300番台) ではなく `difficulty` タグで束ねる。**
// 現在は一致しているが、正本はタグの方 (ADR S-8)。番号から判定すると
// ずれたときに表示だけが実態と食い違う。
type debugStageGroup struct {
	// Difficulty は難易度タグ (easy / normal / hard)
	Difficulty string
	// Label は見出し (「イージー (easy)」)
	Label  string
	Stages []debugStageChoice
}

// debugStageChoice はステージ1つ分の選択肢。
type debugStageChoice struct {
	Value string
	Label string
	// EasyOnly は「イージーでしか選出されない」ステージ (101)。
	// 他の難易度と組むと**本番では起こらない組み合わせ**になるので印を付ける
	EasyOnly bool
}

// debugDeviceView は Core の選択肢。接続していない Core も出す
// (状態を見て選べた方がデバッグしやすい)。
type debugDeviceView struct {
	DeviceID  string
	Connected bool
	State     string
}

// handleDebugPage はデバッグ用のセッション開始フォームを描画する。
//
// **デバッグ専用**。Core・難易度・オペレーター・ステージを明示指定して
// 開始できるようにし、狙ったステージを引き当てるまで抽選を回す手間を無くす。
func (w *ManagerWeb) handleDebugPage(rw http.ResponseWriter, r *http.Request) {
	data := debugPageData{
		MaxStages: maxStagesPerSession,
		Error:     r.URL.Query().Get("error"),
	}

	for _, status := range w.devices.AllStatus() {
		data.Devices = append(data.Devices, debugDeviceView{
			DeviceID:  status.DeviceID,
			Connected: w.devices.IsConnected(status.DeviceID),
			State:     status.State,
		})
	}
	sort.Slice(data.Devices, func(i, j int) bool {
		return data.Devices[i].DeviceID < data.Devices[j].DeviceID
	})

	data.Bridges = w.bridges.IDs()
	sort.Strings(data.Bridges)

	// 難易度は3つ固定 (ロードも同じ3つを決め打ちで読む)。
	// 表示名はテンプレートの Name を使う
	for _, name := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		data.Difficulties = append(data.Difficulties, debugChoice{
			Value: name,
			Label: w.difficultyLabel(name),
		})
	}

	if w.navigator != nil {
		for _, character := range w.navigator.Characters {
			data.Characters = append(data.Characters, debugChoice{
				Value: character.ID,
				Label: fmt.Sprintf("%s (%s)", character.Name, character.ID),
			})
		}
	}

	data.StageGroups = w.buildStageGroups()

	w.render(rw, managerDebugTmpl, "page", data)
}

// buildStageGroups は難易度ごとに束ねたステージ一覧を作る。
//
// 並びは easy → normal → hard、各群の中はID順 (StageIDs が昇順)。
// **どの難易度にも属さないタグが付いていたら最後にまとめて出す** —
// 黙って捨てると、タグを打ち間違えたステージが画面から消えて気付けない。
func (w *ManagerWeb) buildStageGroups() []debugStageGroup {
	if w.library == nil {
		return nil
	}

	byTag := make(map[string][]debugStageChoice)
	order := make([]string, 0, 4)
	seen := make(map[string]bool)

	for _, id := range w.library.StageIDs() {
		stage, err := w.library.Stage(id)
		if err != nil {
			continue
		}
		tag := stage.Difficulty
		if !seen[tag] {
			seen[tag] = true
			order = append(order, tag)
		}
		byTag[tag] = append(byTag[tag], debugStageChoice{
			Value:    id,
			Label:    fmt.Sprintf("%s %s", id, stage.Name),
			EasyOnly: stage.EasyOnly,
		})
	}

	// 既知の難易度を先に、想定外のタグはその後ろへ
	known := []string{difficultyEasy, difficultyNormal, difficultyHard}
	tags := make([]string, 0, len(order))
	for _, name := range known {
		if seen[name] {
			tags = append(tags, name)
		}
	}
	for _, name := range order {
		if name != difficultyEasy && name != difficultyNormal && name != difficultyHard {
			tags = append(tags, name)
		}
	}

	groups := make([]debugStageGroup, 0, len(tags))
	for _, tag := range tags {
		groups = append(groups, debugStageGroup{
			Difficulty: tag,
			Label:      w.difficultyLabel(tag),
			Stages:     byTag[tag],
		})
	}
	return groups
}

// difficultyLabel は難易度タグの見出しを作る (「イージー (easy)」)。
// テンプレートに名前が無ければタグをそのまま使う。
func (w *ManagerWeb) difficultyLabel(tag string) string {
	if w.library != nil {
		if tmpl, err := w.library.Difficulty(tag); err == nil && tmpl.Name != "" {
			return fmt.Sprintf("%s (%s)", tmpl.Name, tag)
		}
	}
	return tag
}

// handleDebugStart はデバッグ開始フォームの送信を受けてセッションを開始する。
//
// 開始そのものは音声申告と同じ StartSessionWith を通す。ステージの整合
// (切断線の重複など) は既存の検証に任せ、ここでは数だけ先に見る。
func (w *ManagerWeb) handleDebugStart(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "invalid form", http.StatusBadRequest)
		return
	}

	deviceID := r.FormValue("device_id")
	if deviceID == "" {
		http.Error(rw, "device_id is required", http.StatusBadRequest)
		return
	}
	difficulty := r.FormValue("difficulty")
	if difficulty == "" {
		http.Error(rw, "difficulty is required", http.StatusBadRequest)
		return
	}

	stageIDs := r.Form["stage_id"]
	if len(stageIDs) > maxStagesPerSession {
		http.Error(rw,
			fmt.Sprintf("ステージは最大%d個までです (指定: %d個)", maxStagesPerSession, len(stageIDs)),
			http.StatusBadRequest)
		return
	}

	// 無線は必須。ナビゲーターの発話が流れない状態で開始しても
	// 本番と同じ体験にならず、デバッグとして意味を成さない。
	bridgeID := r.FormValue("bridge_id")
	if bridgeID == "" {
		http.Error(rw, "bridge_id is required", http.StatusBadRequest)
		return
	}
	// 未接続の無線を選んでも送出は false が返るだけで開始できてしまうため、
	// ここで弾く (選択肢は接続中のものだけだが、その後に切れることがある)
	if !w.bridges.IsConnected(bridgeID) {
		http.Redirect(rw, r,
			"/manager/debug?error="+url.QueryEscape("無線 "+bridgeID+" が接続されていません"),
			http.StatusSeeOther)
		return
	}

	log.Printf("[manager-web] debug start: device=%s difficulty=%s bridge=%s character=%s stages=%v",
		deviceID, difficulty, bridgeID, r.FormValue("character_id"), stageIDs)

	sender := NewAudioSender(w.bridges, bridgeID)
	err := w.game.StartSessionWith(r.Context(), sender, deviceID, difficulty, StartOptions{
		StageIDs:    stageIDs,
		CharacterID: r.FormValue("character_id"),
	})
	if err != nil {
		// 失敗理由をフォームへ戻して見せる。組み立ての失敗
		// (切断線の重複など) はステージの選び直しで解決するため
		http.Redirect(rw, r, "/manager/debug?error="+url.QueryEscape(err.Error()),
			http.StatusSeeOther)
		return
	}

	// 開始できたらダッシュボードへ。進行表にそのまま出る
	http.Redirect(rw, r, "/manager", http.StatusSeeOther)
}
