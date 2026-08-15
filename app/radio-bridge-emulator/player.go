package main

import (
	"log"
	"sync"
)

// player は受信した AudioChunk をシリアルに再生するキュー付きプレイヤー。
//
// radio-bridge (Rust) の Controller / AudioQueue を macOS 上で擬似再現する。
// GPIO/PTT は無いが、キューの扱いは同等にする。
//
// **1 エントリ = 1 再生サイクル (ワンショット)**。到着順に 1 つずつ再生する。
// かつては stream_id ごとに複数チャンクを束ねて連続再生する仕組みを持っていたが、
// サーバーが音声を分割せず 1 パケットで送るようになったため廃止した
// (docs/bridge_connection_design.md §2 決定14)。
type player struct {
	mu   sync.Mutex
	cond *sync.Cond

	// 再生待ちの音声 (到着順)。
	entries [][]byte
}

func newPlayer() *player {
	p := &player{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// push は音声をキューへ積む。
func (p *player) push(data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.entries = append(p.entries, data)
	p.cond.Signal()
}

// run は再生ループ。goroutine で 1 本だけ起動する。
func (p *player) run() {
	for {
		p.mu.Lock()
		for len(p.entries) == 0 {
			p.cond.Wait()
		}
		data := p.entries[0]
		p.entries = p.entries[1:]
		p.mu.Unlock()

		log.Printf("[play] playing %d bytes", len(data))
		if err := playOggOpus(data); err != nil {
			log.Printf("[play] error: %v", err)
		}
	}
}
