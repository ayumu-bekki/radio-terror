package main

import (
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestManagerWeb は HTTP 層の検証用に ManagerWeb を組む。
// 生成AI・TTS は経由しないため、それらの依存は不要。
func newTestManagerWeb(t *testing.T) (*ManagerWeb, *http.ServeMux, SessionStore) {
	t.Helper()

	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	bridges := NewBridgeRegistry()
	game := NewGameCoordinator(devices, bridges, nil, store, rand.New(rand.NewSource(1)))
	logs := NewSessionLogStore(store)

	web := NewManagerWeb(devices, bridges, game, logs, nil, &APIHealth{}, store)
	mux := http.NewServeMux()
	web.Register(mux)
	return web, mux, store
}

// saveTestSession は終了済みセッションを1件永続化する。
func saveTestSession(t *testing.T, store SessionStore, sessionID, deviceID, state string, startedAt time.Time) {
	t.Helper()

	session := &GameSession{
		SessionID:  sessionID,
		DeviceID:   deviceID,
		BridgeID:   "bridge-1",
		Difficulty: difficultyNormal,
		Character:  NavigatorCharacter{Name: "テストナビ"},
		State:      state,
		StageIndex: 1,
		Score:      42000,
		StartedAt:  startedAt,
		Built: &BuiltSession{
			SessionID: sessionID,
			Stages: []*BuiltStage{
				{
					TemplateID: "102",
					Name:       "シグナル",
					Cut:        "A",
					Core:       map[string]any{"cut": "A"},
					Navigator:  map[string]string{"answer": "赤を切る", "briefing": "3つのボタン"},
				},
				{
					TemplateID: "203",
					Name:       "暗号電文",
					Cut:        "C",
					Core:       map[string]any{"cut": "C"},
					Navigator:  map[string]string{"answer": "緑を切る"},
				},
			},
		},
	}
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

// get はページを取得して本文を返す。
func get(t *testing.T, mux *http.ServeMux, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

// 3ページと共通CSSが配信されることを確認する。
func TestManagerPagesServed(t *testing.T) {
	_, mux, store := newTestManagerWeb(t)
	saveTestSession(t, store, "s-1", "0001", deviceStateDefused, time.Now())

	cases := []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/manager", "text/html; charset=utf-8", "マネージャー画面"},
		{"/manager/history", "text/html; charset=utf-8", "過去のセッション"},
		{"/manager/history/s-1", "text/html; charset=utf-8", "ステージ構成と正解"},
		{"/manager/manager.css", "text/css; charset=utf-8", ".summary"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != tc.contentType {
			t.Errorf("%s: Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s: body does not contain %q", tc.path, tc.contains)
		}
	}
}

// セッションIDの無い /manager/history/ は一覧へ寄せる。
func TestManagerHistoryTrailingSlashRedirects(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager/history/", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/manager/history" {
		t.Errorf("Location = %q, want /manager/history", got)
	}
}

// 詳細ページが概要・全ステージ・ログを描画することを確認する。
func TestManagerSessionPageRenders(t *testing.T) {
	web, mux, store := newTestManagerWeb(t)

	// 2ステージ目で爆発 = 到達しなかったステージがある状態
	saveTestSession(t, store, "s-1", "0001", deviceStateExploded, time.Now())
	web.logs.Append("s-1", ConversationEntry{Sender: senderPlayer, Message: "配線を確認した"})
	web.logs.AppendEvent("s-1", EventStageCleared, "ステージ1クリア", 0, 100000)

	status, body := get(t, mux, "/manager/history/s-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	for _, want := range []string{
		"Core 0001",     // 概要
		"爆発",            // 結果
		"シグナル",          // ステージ1
		"暗号電文",          // ステージ2 (到達していない)
		"赤を切る",          // ステージ1の正解
		"緑を切る",          // 到達しなかったステージの正解も出す
		"配線を確認した",       // 発話ログ
		"ステージ1クリア",      // イベントログ
		`class="ev-ok"`, // イベントの色分け
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}

	// 終了済みなので自動更新しない
	if strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Error("終了済みセッションで meta refresh が出ている")
	}
}

// 進行中セッションの詳細だけ自動更新する。
func TestManagerSessionPageLiveRefresh(t *testing.T) {
	_, mux, store := newTestManagerWeb(t)
	saveTestSession(t, store, "s-live", "0001", deviceStatePlaying, time.Now())

	_, body := get(t, mux, "/manager/history/s-live")
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("進行中セッションで meta refresh が出ていない")
	}
}

// 存在しないセッションは 404。
func TestManagerSessionPageNotFound(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	status, _ := get(t, mux, "/manager/history/missing")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

// 履歴一覧の絞り込みがクエリで効くことを確認する。
func TestManagerHistoryFilter(t *testing.T) {
	_, mux, store := newTestManagerWeb(t)

	// 日付の境界をまたがないよう、その日の正午を基準にする。
	// time.Now() を直接使うと深夜0時付近で「同じ日」の前提が崩れる。
	now := time.Now()
	noon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)

	saveTestSession(t, store, "s-ok", "0001", deviceStateDefused, noon)
	saveTestSession(t, store, "s-ng", "0002", deviceStateExploded, noon.Add(-time.Minute))
	// 前日のセッション (日付絞り込み用)
	yesterday := noon.AddDate(0, 0, -1)
	saveTestSession(t, store, "s-old", "0001", deviceStateDefused, yesterday)

	cases := []struct {
		name  string
		query string
		want  []string
		deny  []string
	}{
		{
			name:  "絞り込みなし",
			query: "",
			want:  []string{"s-ok", "s-ng", "s-old"},
		},
		{
			name:  "結果=解除成功",
			query: "?result=ok",
			want:  []string{"s-ok", "s-old"},
			deny:  []string{"s-ng"},
		},
		{
			name:  "結果=爆発",
			query: "?result=ng",
			want:  []string{"s-ng"},
			deny:  []string{"s-ok", "s-old"},
		},
		{
			name:  "Core指定",
			query: "?device=0002",
			want:  []string{"s-ng"},
			deny:  []string{"s-ok", "s-old"},
		},
		{
			name:  "日付指定",
			query: "?date=" + yesterday.Format("2006-01-02"),
			want:  []string{"s-old"},
			deny:  []string{"s-ok", "s-ng"},
		},
		{
			name:  "複合 (結果かつCore)",
			query: "?result=ok&device=0001",
			want:  []string{"s-ok", "s-old"},
			deny:  []string{"s-ng"},
		},
		{
			name:  "該当なし",
			query: "?result=ng&device=0001",
			deny:  []string{"s-ok", "s-ng", "s-old"},
			want:  []string{"該当する履歴はありません"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := get(t, mux, "/manager/history"+tc.query)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("body does not contain %q", want)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(body, "/manager/history/"+deny) {
					t.Errorf("body unexpectedly contains %q", deny)
				}
			}
		})
	}
}

// 絞り込んでも選択肢は全件から作る (絞った結果で選択肢が消えないこと)。
func TestManagerHistoryFilterOptionsStable(t *testing.T) {
	_, mux, store := newTestManagerWeb(t)

	now := time.Now()
	saveTestSession(t, store, "s-ok", "0001", deviceStateDefused, now)
	saveTestSession(t, store, "s-ng", "0002", deviceStateExploded, now)

	// 0001 だけに絞っても、選択肢には 0002 が残る
	_, body := get(t, mux, "/manager/history?device=0001")
	if !strings.Contains(body, `<option value="0002"`) {
		t.Error("絞り込み後に選択肢 0002 が消えている")
	}
	// 選択中の項目には selected が付く
	if !strings.Contains(body, `<option value="0001" selected>`) {
		t.Error("選択中の Core に selected が付いていない")
	}
}

// ダッシュボードは進行中の監視に絞る。履歴そのものは載せず、一覧への導線だけ置く。
func TestManagerDashboardHasNoHistory(t *testing.T) {
	_, mux, store := newTestManagerWeb(t)

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		saveTestSession(t, store,
			"s-"+string(rune('a'+i)), "000"+string(rune('1'+i)),
			deviceStateDefused, base.Add(time.Duration(i)*time.Minute))
	}

	_, body := get(t, mux, "/manager")

	// 履歴の行 (TXTリンク) は出さない
	if strings.Contains(body, "/manager/api/transcript?session_id=") {
		t.Error("ダッシュボードに履歴の行が出ている")
	}
	// 導線だけは残す
	if !strings.Contains(body, `href="/manager/history"`) {
		t.Error("履歴一覧への導線がない")
	}
}

// ダッシュボードの見出しは デバイス → 無線接続状況 → セッション の順。
// 当日の運営が上から順に確認する流れに合わせている。
func TestManagerDashboardSectionOrder(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)
	_, body := get(t, mux, "/manager")

	want := []string{"デバイス", "無線接続状況", "セッション", "交信ログ"}
	pos := make([]int, len(want))
	for i, heading := range want {
		pos[i] = strings.Index(body, "<h2>"+heading)
		if pos[i] < 0 {
			t.Fatalf("見出しが無い: %s", heading)
		}
	}
	for i := 1; i < len(pos); i++ {
		if pos[i-1] > pos[i] {
			t.Errorf("見出しの順序が違う: %s が %s より後ろにある", want[i-1], want[i])
		}
	}

	// 外部API / アセットは出さない
	if strings.Contains(body, "外部API") {
		t.Error("外部API/アセットのセクションが残っている")
	}
}

// partial=live は差し替え用の部分だけを返す (ページ全体を返さない)。
func TestManagerDashboardPartial(t *testing.T) {
	_, mux, store := newTestManagerWeb(t)
	saveTestSession(t, store, "s-1", "0001", deviceStateDefused, time.Now())

	_, full := get(t, mux, "/manager")
	if !strings.Contains(full, "<!doctype html>") {
		t.Fatal("通常のリクエストでページ全体が返っていない")
	}

	_, partial := get(t, mux, "/manager?partial=live")
	if strings.Contains(partial, "<!doctype html>") {
		t.Error("partial=live でページ全体が返っている")
	}
	// 進行中の部分は含む
	if !strings.Contains(partial, "デバイス") {
		t.Error("partial=live にデバイス表が含まれていない")
	}
	// 履歴は含まない (変化が遅いので差し替え対象外)
	if strings.Contains(partial, "直近の履歴") {
		t.Error("partial=live に履歴が含まれている")
	}
}

// テンプレートが値をエスケープすることを確認する。
// ステージ名やナビゲーターの発話は外部ファイル・生成AI由来なので、
// HTMLとして解釈されると画面が壊れる。
func TestManagerEscapesContent(t *testing.T) {
	web, mux, store := newTestManagerWeb(t)

	saveTestSession(t, store, "s-1", "0001", deviceStateDefused, time.Now())
	web.logs.Append("s-1", ConversationEntry{
		Sender:  senderPlayer,
		Message: `<script>alert('xss')</script>`,
	})

	_, body := get(t, mux, "/manager/history/s-1")
	if strings.Contains(body, "<script>alert(") {
		t.Error("発話がエスケープされずに埋め込まれている")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("エスケープされた形が見当たらない")
	}
}

// 交信ログのテキスト書き出し。
func TestManagerTranscript(t *testing.T) {
	web, mux, store := newTestManagerWeb(t)

	saveTestSession(t, store, "s-1", "0001", deviceStateDefused, time.Now())
	web.logs.Append("s-1", ConversationEntry{
		Sender: senderPlayer, Receiver: "テストナビ", Message: "配線を確認した",
	})
	web.logs.AppendEvent("s-1", EventDefused, "解除成功", 1, 42000)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/manager/api/transcript?session_id=s-1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "プレイヤー → テストナビ: 配線を確認した") {
		t.Errorf("発話行が見当たらない:\n%s", body)
	}
	if !strings.Contains(body, "[装置] 解除成功") {
		t.Errorf("イベント行が見当たらない:\n%s", body)
	}
}

// 強制破裂のWeb API。誤操作の影響が大きいため、メソッドと引数を厳しく検証する。
func TestManagerDetonateEndpoint(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	// GET は拒否する (誤ってURLを開いても発動しない)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager/api/detonate?device_id=0001", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rec.Code)
	}

	// device_id なしは 400
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/manager/api/detonate", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("device_idなし: status = %d, want 400", rec.Code)
	}

	// 進行中セッションが無ければ 409 (状態を変えずに拒否する)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/manager/api/detonate?device_id=0001", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("セッションなし: status = %d, want 409", rec.Code)
	}
}

// 切断済みの Core を「最後に見えていた状態」のまま表示しない。
//
// Deep Sleep (電池切れ) や電源断で WS が切れても DeviceStatus は残るため、
// 接続状態を併せて見ないと ready のまま生きているように見える。
func TestManagerDashboardShowsDisconnectedDevice(t *testing.T) {
	web, mux, _ := newTestManagerWeb(t)

	// ready 状態の報告を受けた後、接続だけが切れた状況を作る
	web.devices.UpdateStatus(&deviceMessage{
		Type: msgDeviceStatus, DeviceID: "0001", State: deviceStateReady,
		Battery: 4.0,
	})

	// 接続はしていない (Register していない) = 切断済み
	_, body := get(t, mux, "/manager")

	if !strings.Contains(body, "切断") {
		t.Error("切断中の表示が出ていない")
	}
	// 最後の状態をそのまま出さない
	if strings.Contains(body, `<span class="state ready">`) {
		t.Error("切断済みなのに ready と表示されている")
	}
	if !strings.Contains(body, `class="offline"`) {
		t.Error("切断中の行が区別されていない")
	}
}

// 接続中の Core は通常どおり状態を表示する。
func TestManagerDashboardShowsConnectedDevice(t *testing.T) {
	web, mux, _ := newTestManagerWeb(t)

	web.devices.Register("0001", &fakeDeviceConn{})
	web.devices.UpdateStatus(&deviceMessage{
		Type: msgDeviceStatus, DeviceID: "0001", State: deviceStateReady,
		Battery: 4.0,
	})

	_, body := get(t, mux, "/manager")

	if !strings.Contains(body, `<span class="state ready">`) {
		t.Error("接続中の Core の状態が出ていない")
	}
	if strings.Contains(body, `class="offline"`) {
		t.Error("接続中なのに切断扱いされている")
	}
}
