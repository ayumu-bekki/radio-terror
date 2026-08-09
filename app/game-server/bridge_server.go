package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"

	pb "game-server/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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
					Status:      out.Status,
					StreamId:    out.StreamID,
				}); err != nil {
					sendErrCh <- fmt.Errorf("stream.Send: %w", err)
					return
				}
				log.Printf("[bridge %s] sent audio: %d bytes (status=%s stream_id=%s)",
					bridgeID, len(out.Data), out.Status, out.StreamID)
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

// Run は gRPC サーバーを起動し、ctx がキャンセルされたら停止する。
func (s *BridgeServer) Run(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}

	server := grpc.NewServer()
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
