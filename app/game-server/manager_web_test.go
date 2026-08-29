package main

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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

	web := NewManagerWeb(devices, bridges, game, logs, nil, &APIHealth{}, store, nil, nil)
	mux := http.NewServeMux()
	web.Register(mux)
	return web, mux, store
}

// saveTestSession は終了済みセッションを1件、履歴として永続化する。
// (`history:{id}` に書く。進行中セッション用の `session:{id}` とは別物 — session_store.go 参照)
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
					TemplateID: "202",
					Name:       "暗号電文",
					Cut:        "C",
					Core:       map[string]any{"cut": "C"},
					Navigator:  map[string]string{"answer": "緑を切る"},
				},
			},
		},
	}
	if err := store.SaveHistory(context.Background(), session); err != nil {
		t.Fatalf("SaveHistory: %v", err)
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
		{"/manager", "text/html; charset=utf-8", "Management Console"},
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

// TestManagerPageShowsRotary はマネージャー画面にロータリー位置が
// 表示されることを確かめる。
//
// 表示はテンプレートの列とビューモデルの両方が揃って初めて出る。
// 列を足し忘れる/colspan がずれるといった崩れをここで捕まえる。
func TestManagerPageShowsRotary(t *testing.T) {
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	bridges := NewBridgeRegistry()
	game := NewGameCoordinator(devices, bridges, nil, store, rand.New(rand.NewSource(1)))
	logs := NewSessionLogStore(store)

	web := NewManagerWeb(devices, bridges, game, logs, nil, &APIHealth{}, store, nil, nil)
	mux := http.NewServeMux()
	web.Register(mux)

	pos := 4
	devices.UpdateStatus(&deviceMessage{
		Type: "device_status", DeviceID: "0001",
		State: deviceStatePlaying, Rotary: &pos,
	})
	// ロータリー未報告のデバイス (旧ファーム想定)
	devices.UpdateStatus(&deviceMessage{
		Type: "device_status", DeviceID: "0002",
		State: deviceStateReady,
	})

	code, body := get(t, mux, "/manager")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	if !strings.Contains(body, "<th>ダイヤル</th>") {
		t.Error("ダイヤル列の見出しがない")
	}

	// 表の列数が揃っていること (th と各行の td)
	headers := strings.Count(body[strings.Index(body, "<h2>デバイス</h2>"):], "<th>")
	if headers < 7 {
		t.Errorf("デバイス表の列が %d しかない (ダイヤル追加後は7列)", headers)
	}

	// 値が実際に描画されていること。
	// 0001 の行に位置 4、0002 の行に未報告の「—」が出る。
	row0001 := deviceRow(t, body, "0001")
	if !strings.Contains(row0001, ">4<") {
		t.Errorf("0001 の行にロータリー位置 4 がない: %s", row0001)
	}
	row0002 := deviceRow(t, body, "0002")
	if !strings.Contains(row0002, "—") {
		t.Errorf("0002 の行に未報告の表示がない: %s", row0002)
	}
}

// deviceRow はデバイス表から device_id を含む <tr> を1行取り出す。
func deviceRow(t *testing.T, body, deviceID string) string {
	t.Helper()

	idx := strings.Index(body, ">"+deviceID+"<")
	if idx < 0 {
		t.Fatalf("デバイス %s の行が見つからない", deviceID)
	}
	start := strings.LastIndex(body[:idx], "<tr")
	end := strings.Index(body[idx:], "</tr>")
	if start < 0 || end < 0 {
		t.Fatalf("デバイス %s の行を切り出せない", deviceID)
	}
	return body[start : idx+end]
}

// TestManagerPageBridgeTable は無線接続状況が表として描画されることを確かめる。
//
// かつては "BR01 → Core 3701" のような1行テキストだったが、
// デバイス表・セッション表と体裁を揃えて表にした。
// 列の追加漏れや colspan のずれをここで捕まえる。
func TestManagerPageBridgeTable(t *testing.T) {
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	bridges := NewBridgeRegistry()
	game := NewGameCoordinator(devices, bridges, nil, store, rand.New(rand.NewSource(1)))
	logs := NewSessionLogStore(store)

	web := NewManagerWeb(devices, bridges, game, logs, nil, &APIHealth{}, store, nil, nil)
	mux := http.NewServeMux()
	web.Register(mux)

	bridges.Register("BR01")
	bridges.Register("BR02")

	code, body := get(t, mux, "/manager")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	// 無線の節を切り出して検証する (他の表と混ざらないように)
	start := strings.Index(body, "<h2>無線接続状況</h2>")
	if start < 0 {
		t.Fatal("無線接続状況の見出しがない")
	}
	end := strings.Index(body[start:], "</table>")
	if end < 0 {
		t.Fatal("無線接続状況が表になっていない")
	}
	section := body[start : start+end]

	for _, want := range []string{"<th>無線</th>", "<th>CoreID</th>", "<th>状態</th>"} {
		if !strings.Contains(section, want) {
			t.Errorf("列 %s がない", want)
		}
	}
	// 未バインドは CoreID を「—」で埋め、行を薄くする
	if !strings.Contains(section, "未バインド") {
		t.Error("未バインドの表示がない")
	}
	if !strings.Contains(section, `class="offline"`) {
		t.Error("未バインド行が薄く表示されていない")
	}
	if strings.Contains(section, "→ Core") {
		t.Error("旧形式の1行テキストが残っている")
	}
}

// TestLineChipColorsDefined は配線色のCSSクラスが全色分あることを確かめる。
//
// 1色でも欠けるとチップが透明になり、その配線だけ見えなくなる。
// 色体系は core-system の hardware_config.h と対応 (A=赤 B=黄 C=緑 D=青 E=白)。
func TestLineChipColorsDefined(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	code, css := get(t, mux, "/manager/manager.css")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	for _, class := range []string{".line-a", ".line-b", ".line-c", ".line-d", ".line-e"} {
		if !strings.Contains(css, class) {
			t.Errorf("CSS に %s の定義がない", class)
		}
	}
	// 切断済みの表現 (薄く+取消線)
	if !strings.Contains(css, ".line.cut") {
		t.Error("CSS に .line.cut の定義がない")
	}
}

// TestFinishedSessionKeepsResetButton は**終了したセッションでも
// Management Console からリセットできる**ことを確かめる。
//
// リセットボタンは進行表の行から描画されるため、終了時にバインドごと
// セッションを消すと**行が消えてリセットできなくなる**
// (実運用で発生: 解除後にセッションが消え、次のゲームを始められなかった)。
// 終了しても表には残し、マネージャーが Setup へ戻せる状態を保つ
// (docs/operation_flow.md §6 / §9 決定17)。
func TestFinishedSessionKeepsResetButton(t *testing.T) {
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	bridges := NewBridgeRegistry()
	game := NewGameCoordinator(devices, bridges, nil, store, rand.New(rand.NewSource(1)))
	web := NewManagerWeb(devices, bridges, game, NewSessionLogStore(store), nil, &APIHealth{}, store, nil, nil)
	mux := http.NewServeMux()
	web.Register(mux)

	devices.Register("3701", &fakeDeviceConn{})

	// 解除して終了したセッション
	session := &GameSession{
		SessionID: "s-1", DeviceID: "3701", BridgeID: "BR01",
		State: deviceStateDefused, Finished: true, Score: 54700,
		StartedAt: time.Now(),
	}
	game.binder.Bind("BR01", "3701", session)

	code, body := get(t, mux, "/manager")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	if strings.Contains(body, "進行中のセッションはありません") {
		t.Fatal("終了セッションが進行表から消えている — リセットできない")
	}
	if !strings.Contains(body, "abort('3701')") {
		t.Error("リセットボタンが描画されていない")
	}
	// 結果が読み取れること
	if !strings.Contains(body, "defused") {
		t.Error("解除済みの状態が表示されていない")
	}

	// 実際にリセットが通ること (バインドが残っていること)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/manager/api/abort?device_id=3701", nil))
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusAccepted {
		t.Errorf("リセット API の status = %d", rec.Code)
	}
}

// TestDebugPageRenders はデバッグ開始ページが描画できることを確認する。
// library / navigator が nil でも落ちないこと (選択肢が空になるだけ)。
func TestDebugPageRenders(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager/debug", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"デバッグ開始", "/manager/api/debug-start"} {
		if !strings.Contains(body, want) {
			t.Errorf("body に %q が無い", want)
		}
	}
}

// TestDebugStartRejectsTooManyStages は上限を超えるステージ指定を弾くことを確認する。
// **セッション開始まで到達しない**ことが要点 (到達すると外部APIを呼ぶ)。
func TestDebugStartRejectsTooManyStages(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	form := url.Values{}
	form.Set("device_id", "3701")
	form.Set("difficulty", difficultyNormal)
	for _, id := range []string{"101", "202", "203", "301", "302"} {
		form.Add("stage_id", id)
	}

	req := httptest.NewRequest(http.MethodPost, "/manager/api/debug-start",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "最大") {
		t.Errorf("理由が本文に無い: %q", rec.Body.String())
	}
}

// TestDebugStartRequiresBridge は無線の指定が無い場合に開始しないことを確認する。
// 発話が流れない状態で開始しても本番と同じ挙動にならないため。
func TestDebugStartRequiresBridge(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	form := url.Values{}
	form.Set("device_id", "3701")
	form.Set("difficulty", difficultyNormal)

	req := httptest.NewRequest(http.MethodPost, "/manager/api/debug-start",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bridge_id") {
		t.Errorf("理由が本文に無い: %q", rec.Body.String())
	}
}

// TestDebugStartRejectsDisconnectedBridge は未接続の無線を弾くことを確認する。
func TestDebugStartRejectsDisconnectedBridge(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	form := url.Values{}
	form.Set("device_id", "3701")
	form.Set("difficulty", difficultyNormal)
	form.Set("bridge_id", "bridge-not-connected")

	req := httptest.NewRequest(http.MethodPost, "/manager/api/debug-start",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// フォームへ理由を持たせて戻す
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Errorf("戻り先に理由が無い: %q", loc)
	}
}

// TestDebugStartRequiresPost は GET を拒否することを確認する
// (既存の abort / detonate と同じ扱い)。
func TestDebugStartRequiresPost(t *testing.T) {
	_, mux, _ := newTestManagerWeb(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager/api/debug-start", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestHealthPanelRenders は外部APIの状況がダッシュボードに出ることを確認する。
//
// **この表示は一度壊れていた** — テンプレートに #health 要素が無いまま
// JS だけが書き込もうとしており、サーバー障害時に何も出なかった。
// 要素の存在と partial=live への追従を固定する。
func TestHealthPanelRenders(t *testing.T) {
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	bridges := NewBridgeRegistry()
	game := NewGameCoordinator(devices, bridges, nil, store, nil)
	health := &APIHealth{}
	lib := LoadCrosstalkLibrary("assets/crosstalk")

	web := NewManagerWeb(devices, bridges, game, NewSessionLogStore(store), lib,
		health, store, nil, nil)
	mux := http.NewServeMux()
	web.Register(mux)

	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		return rec.Body.String()
	}

	// 正常時
	body := get()
	if !strings.Contains(body, `id="health"`) {
		t.Fatal(`#health 要素が無い — catch 節の書き込み先が存在しない`)
	}
	if !strings.Contains(body, "正常") {
		t.Error("正常表示が無い")
	}
	t.Logf("混線アセット件数が出ているか: %v", strings.Contains(body, "邪魔"))

	// エラー発生後
	health.NoteError(errors.New("429 RESOURCE_EXHAUSTED: quota exceeded"))
	body = get()
	if !strings.Contains(body, "エラー 1件") {
		t.Error("エラー件数が出ていない")
	}
	if !strings.Contains(body, "quota exceeded") {
		t.Error("エラー内容が出ていない")
	}

	// partial=live にも含まれること (2秒ごとの差し替えで消えない)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager?partial=live", nil))
	if !strings.Contains(rec.Body.String(), `id="health"`) {
		t.Error("partial=live に #health が無い — 差し替えで消える")
	}
}

// TestStageGroupRender はステージ選択が難易度ごとに区切られることを確認する。
//
// 束ねる基準は**番号帯ではなく difficulty タグ** (正本はタグ側。ADR S-8)。
func TestStageGroupRender(t *testing.T) {
	lib, err := LoadScenarioLibrary("scenarios")
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	devices := NewDeviceRegistry()
	bridges := NewBridgeRegistry()
	game := NewGameCoordinator(devices, bridges, nil, store, nil)
	web := NewManagerWeb(devices, bridges, game, NewSessionLogStore(store), nil,
		&APIHealth{}, store, lib, nil)
	mux := http.NewServeMux()
	web.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manager/debug", nil))
	body := rec.Body.String()

	// legend の並び順
	re := regexp.MustCompile(`<legend>([^<]+)</legend>`)
	var legends []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		legends = append(legends, strings.TrimSpace(m[1]))
	}
	t.Logf("見出しの並び: %v", legends)

	// 各群のステージ数
	for _, g := range web.buildStageGroups() {
		ids := make([]string, 0, len(g.Stages))
		for _, st := range g.Stages {
			ids = append(ids, st.Value)
		}
		t.Logf("%-16s %d件: %v", g.Label, len(g.Stages), ids)
	}

	if !strings.Contains(body, "イージー専用") {
		t.Error("101 の easy_only 印が無い")
	}
	if strings.Count(body, `name="stage_id"`) != 18 {
		t.Errorf("チェックボックス数 = %d, want 18", strings.Count(body, `name="stage_id"`))
	}
}

// 想定外の difficulty タグが付いたステージが画面から消えないことを確認する。
// 黙って捨てるとタグの打ち間違いに気付けない。
func TestStageGroupKeepsUnknownTag(t *testing.T) {
	lib, err := LoadScenarioLibrary("scenarios")
	if err != nil {
		t.Fatal(err)
	}
	// タグを打ち間違えたステージを注入する
	lib.stages["999"] = &StageTemplate{ID: "999", Name: "打ち間違い", Difficulty: "nomal"}

	store := NewMemoryStore()
	web := NewManagerWeb(NewDeviceRegistry(), NewBridgeRegistry(),
		NewGameCoordinator(NewDeviceRegistry(), NewBridgeRegistry(), nil, store, nil),
		NewSessionLogStore(store), nil, &APIHealth{}, store, lib, nil)

	groups := web.buildStageGroups()
	var last debugStageGroup
	found := false
	for _, g := range groups {
		if g.Difficulty == "nomal" {
			found, last = true, g
		}
	}
	if !found {
		t.Fatal("未知タグの群が消えている — 打ち間違いに気付けない")
	}
	t.Logf("未知タグ群: %q (%d件)", last.Label, len(last.Stages))
	if groups[len(groups)-1].Difficulty != "nomal" {
		t.Error("未知タグは末尾に置くべき")
	}
}
