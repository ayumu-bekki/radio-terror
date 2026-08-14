package main

import (
	"context"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultWSListenAddr     = ":8080"
	defaultBridgeListenAddr = ":50051"
	defaultScenarioDir      = "scenarios"
	defaultAssetDir         = "assets"
	defaultNavigatorDir     = "navigator"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("shutting down...")
		cancel()
	}()

	processor, err := NewGeminiProcessor(ctx, cfg.Gemini)
	if err != nil {
		log.Fatalf("NewGeminiProcessor: %v", err)
	}
	defer processor.Close()

	ttsClient, err := NewTTSClient(ctx, cfg.Gemini)
	if err != nil {
		log.Fatalf("NewTTSClient: %v", err)
	}

	// --- シナリオテンプレート ---
	scenarioDir := cfg.Scenario.Dir
	if scenarioDir == "" {
		scenarioDir = defaultScenarioDir
	}
	library, err := LoadScenarioLibrary(scenarioDir)
	if err != nil {
		log.Fatalf("LoadScenarioLibrary: %v", err)
	}
	log.Printf("[scenario] loaded %d stages from %s", library.StageCount(), scenarioDir)

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	builder := NewScenarioBuilder(library, cfg.MissionSheet, rng)

	// --- ナビゲーター設定 (キャラクター・プロンプト) ---
	navigatorDir := cfg.Navigator.Dir
	if navigatorDir == "" {
		navigatorDir = defaultNavigatorDir
	}
	navigatorCfg, err := LoadNavigatorConfig(navigatorDir)
	if err != nil {
		log.Fatalf("LoadNavigatorConfig: %v", err)
	}
	log.Printf("[navigator] loaded %d characters from %s: %v",
		len(navigatorCfg.Characters), navigatorDir, navigatorCfg.Names())

	// --- 永続化 (Valkey。接続できない場合はメモリへフォールバック) ---
	var store SessionStore
	if cfg.Valkey.Addr != "" {
		valkey, err := NewValkeyStore(ctx, cfg.Valkey.Addr)
		if err != nil {
			log.Printf("[store] valkey unavailable (%v): falling back to in-memory store", err)
			store = NewMemoryStore()
		} else {
			log.Printf("[store] valkey connected: %s", cfg.Valkey.Addr)
			store = valkey
		}
	} else {
		log.Printf("[store] valkey not configured: using in-memory store")
		store = NewMemoryStore()
	}

	// --- レジストリ ---
	bridges := NewBridgeRegistry()
	devices := NewDeviceRegistry()
	sessionLogs := NewSessionLogStore(store)

	game := NewGameCoordinator(devices, bridges, builder, store, rng)
	game.SetSessionLogStore(sessionLogs)
	game.SetNavigatorConfig(navigatorCfg)

	// 保存済みセッションを復元する。進行状態は Core からの device_status で再同期する
	// (docs/scenario_design.md §6)。
	if restored, err := store.LoadSessions(ctx); err != nil {
		log.Printf("[store] load sessions: %v", err)
	} else if len(restored) > 0 {
		game.Restore(restored)
		for _, session := range restored {
			if entries, err := store.LoadLog(ctx, session.SessionID); err == nil {
				sessionLogs.Restore(session.SessionID, entries)
			}
		}
		log.Printf("[store] restored %d sessions", len(restored))
	}

	// --- 混線・効果音アセット ---
	assetDir := cfg.Assets.Dir
	if assetDir == "" {
		assetDir = defaultAssetDir
	}
	crosstalkLib := LoadCrosstalkLibrary(assetDir + "/crosstalk")
	crosstalk := NewCrosstalkScheduler(crosstalkLib, bridges, rng)
	game.SetCrosstalkScheduler(crosstalk)

	// --- ナビゲーター ---
	navigator := NewGeminiNavigator(processor, ttsClient, sessionLogs, navigatorCfg, assetDir+"/sfx")
	navigator.SetCrosstalkScheduler(crosstalk)
	game.SetNavigatorSpeaker(navigator)

	// --- 音声パイプライン ---
	pipeline := NewAudioPipeline(processor, bridges)
	pipeline.SetManagerCommandHandler(NewManagerCommandHandler(game, cfg.Manager.SecretWord))
	pipeline.SetGameCoordinator(game, navigator, sessionLogs)

	// セッション開始前でも無線が返事をするようにする (会場設営時の疎通確認)
	testResponder := NewTestResponder(processor, ttsClient)
	pipeline.SetTestResponder(testResponder)
	game.SetTestResponder(testResponder)

	// --- WebSocket サーバー (core-system デバイス) + マネージャー向け Web 画面 ---
	wsAddr := cfg.WebSocket.ListenAddr
	if wsAddr == "" {
		wsAddr = defaultWSListenAddr
	}
	health := &APIHealth{}
	processor.SetHealth(health)
	// TTS の失敗は発話が丸ごと無音になる形で表れるため、画面で検知できるようにする
	ttsClient.SetHealth(health)

	managerWeb := NewManagerWeb(devices, bridges, game, sessionLogs, crosstalkLib, health, store)

	wsServer := NewWSServer(devices, game, managerWeb)
	go func() {
		if err := wsServer.Run(ctx, wsAddr); err != nil {
			log.Printf("WSServer.Run: %v", err)
		}
	}()

	// --- gRPC サーバー (radio-bridge からのダイヤルインを受ける) ---
	bridgeAddr := cfg.RadioBridge.ListenAddr
	if bridgeAddr == "" {
		bridgeAddr = defaultBridgeListenAddr
	}
	bridgeServer := NewBridgeServer(bridges, pipeline)

	if err := bridgeServer.Run(ctx, bridgeAddr); err != nil && ctx.Err() == nil {
		log.Fatalf("BridgeServer.Run: %v", err)
	}
}
