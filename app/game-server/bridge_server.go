package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	pb "game-server/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// bridgeIDMetadataKey は radio-bridge が接続時に送る ID のメタデータキー
// (docs/bridge_connection_design.md §2 決定3)。
const bridgeIDMetadataKey = "bridge-id"

// AudioHandler は bridge から受信した音声の処理先。
// どの bridge から来た音声かを文脈に加えるため bridgeID を伴う。
type AudioHandler interface {
	HandleAudio(ctx context.Context, bridgeID string, data []byte)
}

// BridgeServer は radio-bridge からのダイヤルインを受ける gRPC サーバー。
//
// 接続方向は設計で反転済み (§2 決定1): radio-bridge がクライアントとして
// game-server にダイヤルする。サーバーは周辺機器のアドレスを一切管理しない。
type BridgeServer struct {
	pb.UnimplementedTransceiverServiceServer

	registry *BridgeRegistry
	handler  AudioHandler
}

func NewBridgeServer(registry *BridgeRegistry, handler AudioHandler) *BridgeServer {
	return &BridgeServer{registry: registry, handler: handler}
}

// Connect は radio-bridge との双方向ストリームを処理する。
func (s *BridgeServer) Connect(stream pb.TransceiverService_ConnectServer) error {
	bridgeID, err := bridgeIDFromContext(stream.Context())
	if err != nil {
		return err
	}

	sendCh := s.registry.Register(bridgeID)
	defer s.registry.Unregister(bridgeID, sendCh)

	ctx := stream.Context()

	// 送信ループ: レジストリのチャネルに積まれた音声をこのストリームへ書く
	sendErrCh := make(chan error, 1)
	go func() {
		for {
			select {
			case out, ok := <-sendCh:
				if !ok {
					// 後着接続に置き換えられた (レジストリがチャネルを閉じた)
					sendErrCh <- nil
					return
				}
				if err := stream.Send(&pb.AudioChunk{
					OggOpusData: out.Data,
				}); err != nil {
					sendErrCh <- fmt.Errorf("stream.Send: %w", err)
					return
				}
				log.Printf("[bridge %s] sent audio: %d bytes", bridgeID, len(out.Data))
			case <-ctx.Done():
				sendErrCh <- nil
				return
			}
		}
	}()

	// 受信チャンクはシリアルに処理する (並列 Transcribe/TTS によるキュー溢れを防ぐ)
	audioCh := make(chan []byte, 1)
	go func() {
		for {
			select {
			case data := <-audioCh:
				s.handler.HandleAudio(ctx, bridgeID, data)
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case err := <-sendErrCh:
			return err
		default:
		}

		chunk, err := stream.Recv()
		if err == io.EOF {
			log.Printf("[bridge %s] stream closed by client", bridgeID)
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream.Recv: %w", err)
		}

		log.Printf("[bridge %s] received audio chunk: %d bytes", bridgeID, len(chunk.OggOpusData))
		select {
		case audioCh <- chunk.OggOpusData:
		case <-ctx.Done():
			return nil
		}
	}
}

// keepaliveOptions は radio-bridge の keepalive ping を受け入れる設定を返す。
//
// **これが無いと接続が約2分で切れる** (docs/bridge_connection_design.md §2 決定6)。
// radio-bridge は無音区間が長い運用のため30秒ごとに ping を送るが、
// gRPC-Go の既定 EnforcementPolicy は
//
//	MinTime             = 5分   (これより短い間隔の ping は違反)
//	PermitWithoutStream = false (ストリームが無い間の ping は違反)
//
// であり、違反 ping が一定数たまると ENHANCE_YOUR_CALM / "too_many_pings" の
// GOAWAY で切断される。30秒間隔では**ちょうど121秒**で切れる (実測・再現済み)。
func keepaliveOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			// bridge の ping 間隔 (30秒) を下回る値にする。
			// ここが 30秒 より大きいと違反として数えられ、再び切断が始まる。
			MinTime: 10 * time.Second,
			// 無音区間 (音声ストリームが流れていない間) の ping を許可する。
			// トランシーバーは沈黙が普通なので、これが false だと常に違反になる。
			PermitWithoutStream: true,
		}),
	}
}

// Run は gRPC サーバーを起動し、ctx がキャンセルされたら停止する。
func (s *BridgeServer) Run(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}

	server := grpc.NewServer(keepaliveOptions()...)
	pb.RegisterTransceiverServiceServer(server, s)

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	log.Printf("[bridge] gRPC listening on %s", addr)
	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("server.Serve: %w", err)
	}
	return nil
}

// bridgeIDFromContext は gRPC メタデータから bridge-id を取り出す。
// メタデータに bridge-id が無い接続は拒否する (§3)。
func bridgeIDFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get(bridgeIDMetadataKey)
	if len(values) == 0 || values[0] == "" {
		return "", status.Errorf(codes.Unauthenticated, "missing %s metadata", bridgeIDMetadataKey)
	}
	return values[0], nil
}
