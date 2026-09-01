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

// orDefault は設定値が空なら既定値を返す。
// 設定ファイルの項目は「書かなければ既定」という扱いで揃えてある。
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// validateStartup は起動時に落とすべき設定の不整合をまとめて検査する。
//
// ここに集めてあるものは**抽選次第でしか再現しない**性質を持つ —
// 対応が足りない色や端子は、その色が正解になったセッションだけが
// 組み立てに失敗する。当日の1回に当たると原因の特定が難しいため、
// 起動時に全件を検査して落とす。
func validateStartup(cfg *Config, library *ScenarioLibrary) {
	// 資料名が空だと `${sheet_morse}` が空文字に展開され、「を使って解読しろ」
	// という意味の通らない発話になる (ADR D-2)。
	if err := cfg.MissionSheet.Documents.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}
	// 端子は X/Y の両系統を検査する (ADR D-6)。
	if err := cfg.MissionSheet.ValidateTerminalMaps(); err != nil {
		log.Fatalf("config: %v", err)
	}
	// 202 暗号電文 の対照表。
	if err := validateCodebookTable(); err != nil {
		log.Fatalf("codebook: %v", err)
	}
	// 209 配電盤照合 の対照表。見え方が重複していると現在位置を特定できない。
	if err := validatePanelTable(); err != nil {
		log.Fatalf("panel: %v", err)
	}
	// 難易度ごとの入力量。未設定だと「押す回数0回」の成立しないステージになる。
	for _, name := range []string{difficultyEasy, difficultyNormal, difficultyHard} {
		diff, err := library.Difficulty(name)
		if err != nil {
			log.Fatalf("difficulty %s: %v", name, err)
		}
		if err := diff.Load.Validate(name); err != nil {
			log.Fatalf("config: %v", err)
		}
	}
}

func main() {
	configPath := "config.toml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("LoadConfig: %v", err)
	}
	logStartupBanner(configPath, cfg)

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
	scenarioDir := orDefault(cfg.Scenario.Dir, defaultScenarioDir)
	library, err := LoadScenarioLibrary(scenarioDir)
	if err != nil {
		log.Fatalf("LoadScenarioLibrary: %v", err)
	}
	log.Printf("[scenario] loaded %d stages from %s", library.StageCount(), scenarioDir)

	validateStartup(cfg, library)

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	builder := NewScenarioBuilder(library, cfg.MissionSheet, rng)

	// --- ナビゲーター設定 (キャラクター・プロンプト) ---
	navigatorDir := orDefault(cfg.Navigator.Dir, defaultNavigatorDir)
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
	assetDir := orDefault(cfg.Assets.Dir, defaultAssetDir)
	crosstalkLib := LoadCrosstalkLibrary(assetDir + "/crosstalk")
	crosstalk := NewCrosstalkScheduler(crosstalkLib, bridges, rng)
	game.SetCrosstalkScheduler(crosstalk)

	// --- 自動送信局アナウンス ---
	// 特小無線は共用チャンネルのため、体験していない無線から定期的に名乗る
	// (docs/operation_flow.md §7.3)。体験中の無線には流さない。
	if cfg.Announce.Disabled {
		log.Printf("[announce] disabled by config")
	} else {
		announce := NewAnnounceScheduler(assetDir, game.Binder(), bridges, cfg.Announce.Interval())
		go announce.Run(ctx)
	}

	// --- ナビゲーター ---
	navigator := NewGeminiNavigator(processor, ttsClient, sessionLogs, navigatorCfg, assetDir+"/sfx")
	navigator.SetCrosstalkScheduler(crosstalk)
	game.SetNavigatorSpeaker(navigator)

	// 応答が途絶えたらナビゲーターから声を掛ける (docs/navigator_design.md §3.5)
	game.SetSilenceWatcher(NewSilenceWatcher(
		navigator, bridges, crosstalk,
		time.Duration(navigatorCfg.Prompt.SilenceMinMS)*time.Millisecond,
		time.Duration(navigatorCfg.Prompt.SilenceMaxMS)*time.Millisecond,
		// **専用の乱数源を渡す。** 既存の rng は組み立て・混線と共有しており、
		// それぞれが自前のミューテックスで守っているだけなので、
		// 別のゴルーチンから引くと競合する。
		rand.New(rand.NewSource(time.Now().UnixNano())),
	))
	log.Printf("[silence] navigator checks in after %d-%dms of no reply",
		navigatorCfg.Prompt.SilenceMinMS, navigatorCfg.Prompt.SilenceMaxMS)

	// --- 音声パイプライン ---
	pipeline := NewAudioPipeline(processor, bridges)
	pipeline.SetManagerCommandHandler(NewManagerCommandHandler(game, cfg.Manager.SecretWord))
	pipeline.SetGameCoordinator(game, navigator, sessionLogs)

	// セッション開始前でも無線が返事をするようにする (会場設営時の疎通確認)
	testResponder := NewTestResponder(processor, ttsClient)
	pipeline.SetTestResponder(testResponder)
	game.SetTestResponder(testResponder)

	// --- WebSocket サーバー (core-system デバイス) + マネージャー向け Web 画面 ---
	wsAddr := orDefault(cfg.WebSocket.ListenAddr, defaultWSListenAddr)
	health := &APIHealth{}
	processor.SetHealth(health)
	// TTS の失敗は発話が丸ごと無音になる形で表れるため、画面で検知できるようにする
	ttsClient.SetHealth(health)

	managerWeb := NewManagerWeb(devices, bridges, game, sessionLogs, crosstalkLib, health, store,
		library, navigatorCfg)

	wsServer := NewWSServer(devices, game, managerWeb)
	go func() {
		if err := wsServer.Run(ctx, wsAddr); err != nil {
			log.Printf("WSServer.Run: %v", err)
		}
	}()

	// --- gRPC サーバー (radio-bridge からのダイヤルインを受ける) ---
	bridgeAddr := orDefault(cfg.RadioBridge.ListenAddr, defaultBridgeListenAddr)
	bridgeServer := NewBridgeServer(bridges, pipeline)

	if err := bridgeServer.Run(ctx, bridgeAddr); err != nil && ctx.Err() == nil {
		log.Fatalf("BridgeServer.Run: %v", err)
	}
}
