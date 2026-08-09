package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	pb "radio-bridge-emulator/proto"
)

// bridgeIDMetadataKey は bridge-id を伝えるメタデータキー
// (docs/bridge_connection_design.md §2 決定3)。
const bridgeIDMetadataKey = "bridge-id"

// bridgeClient は game-server へダイヤルインする gRPC クライアント。
//
// 接続方向は設計で反転済み (§2 決定1) のため、エミュレータも radio-bridge と
// 同様にクライアントとして動作する。
type bridgeClient struct {
	serverAddr        string
	bridgeID          string
	reconnectInterval time.Duration

	recorder *recorder
	player   *player

	// oneshotCounter は ONESHOT チャンクに振る内部 stream_id の連番
	oneshotCounter atomic.Uint64
}

func newBridgeClient(cfg ServerConfig, rec *recorder, pl *player) *bridgeClient {
	interval := time.Duration(cfg.ReconnectIntervalSecs) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &bridgeClient{
		serverAddr:        cfg.ServerAddr,
		bridgeID:          cfg.BridgeID,
		reconnectInterval: interval,
		recorder:          rec,
		player:            pl,
	}
}

// run は再接続ループ付きで接続を維持する (§3)。
func (c *bridgeClient) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		if err := c.connectOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[grpc] connection error: %v", err)
		}

		if ctx.Err() != nil {
			return
		}

		log.Printf("[grpc] reconnecting in %s", c.reconnectInterval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.reconnectInterval):
		}
	}
}

// connectOnce は1回分の接続を確立し、双方向ストリームを処理する。
func (c *bridgeClient) connectOnce(ctx context.Context) error {
	conn, err := grpc.NewClient(c.serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("grpc.NewClient: %w", err)
	}
	defer conn.Close()

	// 接続時に bridge-id をメタデータで送る。
	// メタデータが無い接続はサーバー側で拒否される (§3)。
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	streamCtx = metadata.AppendToOutgoingContext(streamCtx, bridgeIDMetadataKey, c.bridgeID)

	client := pb.NewTransceiverServiceClient(conn)
	stream, err := client.Connect(streamCtx)
	if err != nil {
		return fmt.Errorf("client.Connect: %w", err)
	}
	log.Printf("[grpc] connected to %s as %s", c.serverAddr, c.bridgeID)

	// マイク録音 → サーバー送信
	sub := c.recorder.subscribe()
	go func() {
		for {
			select {
			case <-streamCtx.Done():
				return
			case data, ok := <-sub:
				if !ok {
					return
				}
				log.Printf("[grpc] sending audio to server: %d bytes", len(data))
				// マイク受信音声は単発再生扱い
				if err := stream.Send(&pb.AudioChunk{
					OggOpusData: data,
					Status:      pb.StreamStatus_ONESHOT,
				}); err != nil {
					log.Printf("[grpc] send error: %v", err)
					cancel()
					return
				}
			}
		}
	}()

	// サーバー → 受信して再生
	for {
		chunk, err := stream.Recv()
		if err == io.EOF || streamCtx.Err() != nil {
			log.Println("[grpc] stream closed")
			return nil
		}
		if err != nil {
			if st, _ := status.FromError(err); st.Code() == codes.Canceled {
				return nil
			}
			return fmt.Errorf("stream.Recv: %w", err)
		}

		log.Printf("[grpc] received audio from server: %d bytes (status=%s stream_id=%s)",
			len(chunk.OggOpusData), chunk.Status, chunk.StreamId)

		// ONESHOT は stream_id を内部生成する (空文字列をキーにしない)。
		// stream 系 (START/CONTINUE/END) で stream_id が空なら破棄する。
		var streamID string
		switch chunk.Status {
		case pb.StreamStatus_ONESHOT:
			streamID = fmt.Sprintf("__oneshot_%d", c.oneshotCounter.Add(1))
		case pb.StreamStatus_START, pb.StreamStatus_CONTINUE, pb.StreamStatus_END:
			if chunk.StreamId == "" {
				log.Printf("[grpc] audio discarded: stream_id is empty for stream chunk (status=%s)", chunk.Status)
				continue
			}
			streamID = chunk.StreamId
		default:
			// UNKNOWN(0) / 未知値は player.push 内でも破棄されるが、ここで先に弾く。
			log.Printf("[grpc] audio discarded: invalid status (raw=%d)", int32(chunk.Status))
			continue
		}

		c.player.push(chunk.OggOpusData, chunk.Status, streamID)
	}
}
