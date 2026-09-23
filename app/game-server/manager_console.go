package main

// コンソールモードの画面 (`/manager/console/{device_id}`)。
//
// 実機 (Core)・無線 (bridge) を使わず、キーボード操作だけでステージ進行を
// 確認するデバッグ専用の画面 (ADR M-7)。開始は `handleDebugStart` から
// `startConsoleSession` へ分岐して行う。
//
// **本番の StartSessionWith / HandleDeviceMessage をそのまま通す** —
// 疑似デバイス (`consoleDeviceConn`) を DeviceRegistry へ登録して ready を
// 装うだけで、あとはボタン操作で進行イベント (stage_cleared 等) を注入する。
// ナビゲーターの発話生成ロジックも GeminiNavigator.generateReply を共有し、
// TTS・無線送出・混線・永続化だけを迂回する。

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// consolePageData はコンソール画面のテンプレートへ渡す値。
// トップレベルに DeviceID を持たせるのは、live テンプレートを差し替えても
// page 側のスクリプトから常に対象デバイスを参照できるようにするため。
type consolePageData struct {
	DeviceID     string
	Session      sessionView
	Entries      []entryView
	Finished     bool
	IsFinalStage bool
}

// consoleSessionOrNotFound は device_id からコンソールセッションを取得する。
// 見つからない、またはコンソールモードでない場合は 404 を返して false。
func (w *ManagerWeb) consoleSessionOrNotFound(rw http.ResponseWriter, deviceID string) (*GameSession, bool) {
	if !isConsoleDeviceID(deviceID) {
		http.Error(rw, "not a console session", http.StatusBadRequest)
		return nil, false
	}
	session := w.game.Binder().SessionFor(deviceID)
	if session == nil {
		http.Error(rw, "console session not found", http.StatusNotFound)
		return nil, false
	}
	return session, true
}

// buildConsolePageData はコンソール画面表示用のデータを組み立てる。
func (w *ManagerWeb) buildConsolePageData(ctx context.Context, session *GameSession) consolePageData {
	views := buildSessionViews([]*GameSession{session})
	view := sessionView{}
	if len(views) > 0 {
		view = views[0]
	}

	session.mu.Lock()
	finished := session.Finished
	stageIndex := session.StageIndex
	stageCount := 0
	if session.Built != nil {
		stageCount = len(session.Built.Stages)
	}
	session.mu.Unlock()

	return consolePageData{
		DeviceID:     session.DeviceID,
		Session:      view,
		Entries:      buildEntryViews(w.entriesFor(ctx, session.SessionID)),
		Finished:     finished,
		IsFinalStage: stageCount > 0 && stageIndex == stageCount-1,
	}
}

// handleConsolePage はコンソール画面を描画する (フル/partial=live 両対応)。
func (w *ManagerWeb) handleConsolePage(rw http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimPrefix(r.URL.Path, "/manager/console/")
	if deviceID == "" {
		http.NotFound(rw, r)
		return
	}
	session, ok := w.consoleSessionOrNotFound(rw, deviceID)
	if !ok {
		return
	}

	data := w.buildConsolePageData(r.Context(), session)

	if r.URL.Query().Get("partial") == "live" {
		w.render(rw, managerConsoleTmpl, "live", data)
		return
	}
	w.render(rw, managerConsoleTmpl, "page", data)
}

// handleConsoleMessage はテキスト入力欄からの発言を処理し、
// ナビゲーターのテキスト応答を生成する (TTS・無線送出は行わない)。
func (w *ManagerWeb) handleConsoleMessage(rw http.ResponseWriter, r *http.Request) {
	deviceID, ok := postDeviceID(rw, r)
	if !ok {
		return
	}
	session, ok := w.consoleSessionOrNotFound(rw, deviceID)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "invalid form", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		rw.WriteHeader(http.StatusNoContent)
		return
	}

	// audio_pipeline.go の handlePlayerMessage と同じ「音声認識より後ろ」の
	// 処理: ログ追記 → 質問カウント・無応答計測のリセット → 発話生成。
	if w.logs != nil {
		w.logs.Append(session.SessionID, ConversationEntry{
			Sender:   senderPlayer,
			Receiver: session.Character.Name,
			Message:  text,
		})
	}
	w.game.NoteQuestion(deviceID)
	if watcher := w.game.SilenceWatcher(); watcher != nil {
		watcher.Notice(deviceID)
	}

	if w.navigatorSpeaker != nil {
		if _, err := w.navigatorSpeaker.SpeakText(r.Context(), session, "player_message", ""); err != nil {
			http.Error(rw, fmt.Sprintf("SpeakText: %v", err), http.StatusInternalServerError)
			return
		}
	}
	rw.WriteHeader(http.StatusNoContent)
}

// handleConsoleStageClear は「ステージ成功」ボタン。
// 最終ステージなら defused 相当、そうでなければ stage_cleared 相当を注入する。
func (w *ManagerWeb) handleConsoleStageClear(rw http.ResponseWriter, r *http.Request) {
	deviceID, ok := postDeviceID(rw, r)
	if !ok {
		return
	}
	session, ok := w.consoleSessionOrNotFound(rw, deviceID)
	if !ok {
		return
	}

	session.mu.Lock()
	stageIndex := session.StageIndex
	remainingMS := session.RemainingMS
	stageCount := 0
	if session.Built != nil {
		stageCount = len(session.Built.Stages)
	}
	session.mu.Unlock()

	msgType := msgStageCleared
	if stageCount > 0 && stageIndex == stageCount-1 {
		msgType = msgDefused
	}
	w.game.HandleDeviceMessage(r.Context(), &deviceMessage{
		Type:        msgType,
		DeviceID:    deviceID,
		StageIndex:  stageIndex,
		RemainingMS: remainingMS,
	})
	rw.WriteHeader(http.StatusNoContent)
}

// handleConsoleExplode は「即爆発」ボタン。
// 目的は生成AIの失敗演出テキストを見ることなので、発話しない
// wrong_action (ADR N-26) ではなく exploded を直接注入する。
func (w *ManagerWeb) handleConsoleExplode(rw http.ResponseWriter, r *http.Request) {
	deviceID, ok := postDeviceID(rw, r)
	if !ok {
		return
	}
	session, ok := w.consoleSessionOrNotFound(rw, deviceID)
	if !ok {
		return
	}

	session.mu.Lock()
	stageIndex := session.StageIndex
	remainingMS := session.RemainingMS
	session.mu.Unlock()

	w.game.HandleDeviceMessage(r.Context(), &deviceMessage{
		Type:        msgExploded,
		DeviceID:    deviceID,
		StageIndex:  stageIndex,
		RemainingMS: remainingMS,
		Reason:      "wrong_cut",
		Detail:      "console",
	})
	rw.WriteHeader(http.StatusNoContent)
}

// handleConsoleForget は「片付ける」ボタン。
// 進行中なら安全に中断してから、DeviceRegistry の登録ごと消す。
func (w *ManagerWeb) handleConsoleForget(rw http.ResponseWriter, r *http.Request) {
	deviceID, ok := postDeviceID(rw, r)
	if !ok {
		return
	}
	if !isConsoleDeviceID(deviceID) {
		http.Error(rw, "not a console session", http.StatusBadRequest)
		return
	}

	if err := w.game.AbortSession(r.Context(), nil, deviceID); err != nil {
		// デバイス未接続 (疑似デバイスへの送信は常に成功するため通常は
		// 起きないが、念のため) でもサーバー側の片付けは続行する。
		_ = err
	}
	w.devices.ForgetStatus(deviceID)
	rw.WriteHeader(http.StatusNoContent)
}

// startConsoleSession はコンソールモードでセッションを開始する。
// handleDebugStart から device_id == consoleDeviceSentinel の場合に呼ばれる。
func (w *ManagerWeb) startConsoleSession(rw http.ResponseWriter, r *http.Request, difficulty string, stageIDs []string) {
	deviceID, bridgeID := newConsoleIDs()

	w.devices.Register(deviceID, consoleDeviceConn{})
	w.devices.UpdateStatus(&deviceMessage{
		Type:     msgDeviceStatus,
		DeviceID: deviceID,
		State:    deviceStateReady,
	})

	sender := NewAudioSender(w.bridges, bridgeID)
	err := w.game.StartSessionWith(r.Context(), sender, deviceID, difficulty, StartOptions{
		StageIDs:    stageIDs,
		CharacterID: r.FormValue("character_id"),
		ConsoleMode: true,
	})
	if err != nil {
		w.devices.ForgetStatus(deviceID)
		http.Redirect(rw, r, "/manager/debug?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(rw, r, "/manager/console/"+deviceID, http.StatusSeeOther)
}
