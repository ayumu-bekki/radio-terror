package main

import (
	"log"
	"sync"
)

// BridgeRegistry は bridge_id → 送信チャネル のマッピングを保持する。
//
// docs/bridge_connection_design.md §2 決定4:
// サーバーは radio-bridge のアドレス・ポートを一切記録しない。確立済みの双方向
// ストリーム自体が返信路であり、返信はこのレジストリが保持する送信チャネルへ書く。
type BridgeRegistry struct {
	mu      sync.RWMutex
	bridges map[string]chan outgoingAudio
}

func NewBridgeRegistry() *BridgeRegistry {
	return &BridgeRegistry{
		bridges: make(map[string]chan outgoingAudio),
	}
}

// Register は bridge_id に対する送信チャネルを登録し、そのチャネルを返す。
//
// 同一IDの二重接続は bridge プロセスの再起動を想定して後着を採用し、旧ストリームの
// チャネルを閉じて送信ループを終了させる (§3)。
func (r *BridgeRegistry) Register(bridgeID string) chan outgoingAudio {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.bridges[bridgeID]; ok {
		log.Printf("[bridge] WARN duplicate connection for %s: closing previous stream", bridgeID)
		close(old)
	}

	ch := make(chan outgoingAudio, 16)
	r.bridges[bridgeID] = ch
	log.Printf("[bridge] registered: %s", bridgeID)
	return ch
}

// Unregister は指定チャネルが現行のものである場合に限り登録を解除する。
// 後着接続に置き換わった後で旧接続が切断された場合に、新しい登録を消さないための判定。
func (r *BridgeRegistry) Unregister(bridgeID string, ch chan outgoingAudio) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.bridges[bridgeID]
	if !ok || current != ch {
		return
	}
	delete(r.bridges, bridgeID)
	close(ch)
	log.Printf("[bridge] unregistered: %s", bridgeID)
}

// Send は宛先 bridge_id のストリームへ音声を送る。
// 宛先が未接続の場合は false を返す (呼び出し側でログのみ出して継続する)。
func (r *BridgeRegistry) Send(bridgeID string, audio outgoingAudio) bool {
	r.mu.RLock()
	ch, ok := r.bridges[bridgeID]
	r.mu.RUnlock()

	if !ok {
		log.Printf("[bridge] send to unknown bridge: %s", bridgeID)
		return false
	}

	// 切断直後にチャネルが閉じられている可能性があるため、送信の panic を回収する
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[bridge] send to closed bridge: %s", bridgeID)
		}
	}()

	select {
	case ch <- audio:
		return true
	default:
		log.Printf("[bridge] send buffer full, dropping audio for %s", bridgeID)
		return false
	}
}

// IDs は現在接続中の bridge_id 一覧を返す (混線のイベント駆動配信・Web画面用)。
func (r *BridgeRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.bridges))
	for id := range r.bridges {
		ids = append(ids, id)
	}
	return ids
}

// IsConnected は指定 bridge_id が接続中かを返す。
func (r *BridgeRegistry) IsConnected(bridgeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.bridges[bridgeID]
	return ok
}
