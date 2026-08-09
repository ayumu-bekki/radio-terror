package main

import (
	"errors"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server ServerConfig `toml:"server"`
	Audio  AudioConfig  `toml:"audio"`
}

// ServerConfig は game-server への接続設定。
//
// 接続方向は反転済み (docs/bridge_connection_design.md §2 決定1) のため、
// エミュレータは listen せずサーバーへダイヤルインする。
type ServerConfig struct {
	// ServerAddr は接続先 game-server の gRPC エンドポイント
	ServerAddr string `toml:"server_addr"`
	// BridgeID は自分の ID。環境変数 RADIO_BRIDGE_ID があればそちらを優先する
	BridgeID string `toml:"bridge_id"`
	// ReconnectIntervalSecs は切断時の再接続待機間隔(秒)
	ReconnectIntervalSecs int `toml:"reconnect_interval_secs"`
}

type AudioConfig struct {
	InputThresholdRMS     uint16 `toml:"input_threshold_rms"`
	InputSilenceMs        uint64 `toml:"input_silence_ms"`
	InputMinRecordingMs   uint64 `toml:"input_min_recording_ms"`
	InputMaxRecordingSecs uint64 `toml:"input_max_recording_secs"`
}

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}

	// bridge ID は環境変数を優先する (§2 決定2)。
	// 同じ config.toml のままプロセスごとに ID を変えて起動できる。
	if id := os.Getenv("RADIO_BRIDGE_ID"); id != "" {
		cfg.Server.BridgeID = id
	}
	if cfg.Server.BridgeID == "" {
		return nil, errors.New("bridge_id is required (set RADIO_BRIDGE_ID or [server].bridge_id)")
	}
	return &cfg, nil
}
