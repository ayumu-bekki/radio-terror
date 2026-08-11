package main

import "errors"

var (
	// errDeviceNotConnected は対象 device_id の WS 接続が無い場合に返る。
	errDeviceNotConnected = errors.New("device not connected")

	// errDeviceNotReady は対象 Core が Ready 状態でない場合に返る
	// (docs/bridge_connection_design.md §5 の検証)。
	errDeviceNotReady = errors.New("device not ready")

	// errDeviceBusy は対象 Core が既に他 bridge にバインドされ Playing 中の場合に返る。
	errDeviceBusy = errors.New("device busy")

	// errUnknownDifficulty は申告された難易度に対応するテンプレートが無い場合に返る。
	errUnknownDifficulty = errors.New("unknown difficulty")

	// errNoActiveSession は対象 device_id に進行中セッションが無い場合に返る。
	errNoActiveSession = errors.New("no active session")

	// errNotPlaying は対象セッションが Playing でない場合に返る
	// (強制破裂は進行中にのみ意味を持つ)。
	errNotPlaying = errors.New("session is not playing")
)
