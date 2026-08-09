package main

import (
	"context"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WSServer は core-system デバイスの WebSocket 接続を受け付ける。
// マネージャー向け Web 画面も同じ HTTP サーバーで提供する。
type WSServer struct {
	devices    *DeviceRegistry
	game       *GameCoordinator
	managerWeb *ManagerWeb
	ctx        context.Context
}

func NewWSServer(devices *DeviceRegistry, game *GameCoordinator, managerWeb *ManagerWeb) *WSServer {
	return &WSServer{
		devices:    devices,
		game:       game,
		managerWeb: managerWeb,
	}
}

// ServeHTTP は http.Handler として WebSocket アップグレードを処理する。
func (s *WSServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] upgrade error: %v", err)
		return
	}
	session := newWSSession(conn, s.devices, s.game)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[WS] session panic: %v", rec)
			}
		}()
		session.run(s.ctx)
	}()
}

// Run は HTTP サーバーを起動し、ctx がキャンセルされたら停止する。
func (s *WSServer) Run(ctx context.Context, addr string) error {
	s.ctx = ctx
	mux := http.NewServeMux()
	mux.Handle("/ws", s)

	// マネージャー向け簡易 Web 画面 (docs/game_session_design.md §9)
	if s.managerWeb != nil {
		s.managerWeb.Register(mux)
		log.Printf("[manager-web] available at http://localhost%s/manager", addr)
	}

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	log.Printf("[WS] listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
