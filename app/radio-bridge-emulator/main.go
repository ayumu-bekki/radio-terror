package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gordonklaus/portaudio"
)

func main() {
	configPath := "config.toml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("LoadConfig: %v", err)
	}

	if err := portaudio.Initialize(); err != nil {
		log.Fatalf("portaudio init: %v", err)
	}
	defer portaudio.Terminate()

	rec := newRecorder(cfg.Audio)
	go rec.run()

	pl := newPlayer()
	go pl.run()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("shutting down...")
		cancel()
	}()

	// wl-game-server へダイヤルインする (接続方向の反転)
	log.Printf("radio-bridge-emulator starting as %s -> %s",
		cfg.Server.BridgeID, cfg.Server.ServerAddr)
	newBridgeClient(cfg.Server, rec, pl).run(ctx)
}
