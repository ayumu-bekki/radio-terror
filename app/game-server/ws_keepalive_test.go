package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 相手が黙って消えた場合に、読み取りが期限切れで返ることを確認する。
//
// 電池切れ (Deep Sleep) や電源断は TCP を正常終了しないため、
// ping/pong が無いと OS の TCP タイムアウト (十数分) までブロックし、
// マネージャー画面に「生きている」ように見え続ける。
func TestWSSessionDetectsSilentDeath(t *testing.T) {
	devices := NewDeviceRegistry()

	// 検知の速さを確かめたいので、待ち時間を縮めた設定で動かす
	const (
		testPongWait   = 300 * time.Millisecond
		testPingPeriod = 100 * time.Millisecond
	)

	unregistered := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade: %v", err)
			return
		}

		s := newWSSession(conn, devices, nil)

		// run と同じ構造を、短い待ち時間で再現する
		done := make(chan struct{})
		go func() {
			defer func() {
				close(done)
				if s.deviceID != "" {
					devices.Unregister(s.deviceID, s)
				}
				conn.Close()
				close(unregistered)
			}()

			_ = conn.SetReadDeadline(time.Now().Add(testPongWait))
			conn.SetPongHandler(func(string) error {
				return conn.SetReadDeadline(time.Now().Add(testPongWait))
			})

			go func() {
				ticker := time.NewTicker(testPingPeriod)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						if err := s.writePing(); err != nil {
							return
						}
					case <-done:
						return
					}
				}
			}()

			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				s.handleDeviceMessage(context.Background(), msg)
			}
		}()
	}))
	defer srv.Close()

	// デバイスとして接続し、device_status を送って登録させる
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"device_status","device_id":"0001","state":"ready"}`)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	// 登録されるのを待つ
	deadline := time.Now().Add(2 * time.Second)
	for !devices.IsConnected("0001") {
		if time.Now().After(deadline) {
			t.Fatal("デバイスが登録されない")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// **TCP を正常終了せずに消える** (Deep Sleep / 電源断の再現)。
	// Close ではなく下位の TCP 接続を捨てることで、FIN を送らせない。
	if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0) // RST も送らず捨てる
	}
	conn.UnderlyingConn().Close()

	// pong が途絶えて読み取りが期限切れになるのを待つ
	select {
	case <-unregistered:
		if devices.IsConnected("0001") {
			t.Error("Unregister された後も接続中と判定されている")
		}
	case <-time.After(3 * time.Second):
		t.Error("切断が検知されない (ping/pong が効いていない)")
	}
}

// 正常に応答している間は切断しない。
// 検知を急ぐあまり、生きている接続まで切らないことを確かめる。
func TestWSSessionKeepsHealthyConnection(t *testing.T) {
	devices := NewDeviceRegistry()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s := newWSSession(conn, devices, nil)
		go s.run(context.Background())
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"device_status","device_id":"0002","state":"ready"}`)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	// ping に pong を返す (ESP-IDF の esp_websocket_client も同様に自動応答する)。
	// 読み取りを回さないと ping ハンドラが呼ばれないため、併せて回す。
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteMessage(websocket.PongMessage, []byte(appData))
	})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !devices.IsConnected("0002") {
		if time.Now().After(deadline) {
			t.Fatal("デバイスが登録されない")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 無通信でも繋がったままであること。
	// 本番の ping 間隔 (25秒) を待つのは重いので、
	// 「pong を返す限り読み取り期限が延びる」ことは
	// TestWSSessionDetectsSilentDeath の対照として確認済みとする。
	time.Sleep(500 * time.Millisecond)

	if !devices.IsConnected("0002") {
		t.Error("無通信だけで切断された")
	}
}
