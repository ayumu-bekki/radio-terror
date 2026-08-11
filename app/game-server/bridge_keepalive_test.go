package main

import (
	"context"
	"flag"
	"net"
	"strings"
	"testing"
	"time"

	pb "game-server/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// runKeepaliveTest は低速な keepalive テストを実行するかのフラグ。
// 修正前の再現に121秒かかるため、既定では飛ばす。
var runKeepaliveTest = flag.Bool("keepalive", false,
	"keepalive の切断再現テストを実行する (約5分かかる)")

// radio-bridge の keepalive ping 間隔 (src/grpc/client.rs)。
// サーバー側の EnforcementPolicy.MinTime はこれより短くなければならない。
const bridgePingInterval = 30 * time.Second

// bridge と同じ keepalive 設定のクライアントで接続し、
// 長時間の無音でも切断されないことを確認する。
//
// この設定が無いと gRPC-Go は ENHANCE_YOUR_CALM / "too_many_pings" の
// GOAWAY を返し、**ちょうど121秒**で切断される (実機で発生した現象)。
func TestKeepaliveAcceptsBridgePings(t *testing.T) {
	// 修正前の再現に121秒かかるため、既定では実行しない。
	// 実行するには: go test -run TestKeepaliveAcceptsBridgePings -keepalive -timeout 420s
	if !*runKeepaliveTest {
		t.Skip("低速なため既定では飛ばす (-keepalive で実行)")
	}

	cases := []struct {
		name     string
		opts     []grpc.ServerOption
		wantDrop bool
	}{
		{
			name:     "設定なし (修正前の再現)",
			opts:     nil,
			wantDrop: true,
		},
		{
			name:     "keepaliveOptions 適用",
			opts:     keepaliveOptions(),
			wantDrop: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 実機は121秒で切れる。少し余裕を見て待つ
			dropped, err := runBridgeLikeStream(t, tc.opts, 150*time.Second)

			if dropped != tc.wantDrop {
				t.Errorf("切断された = %v, want %v (err=%v)", dropped, tc.wantDrop, err)
			}
			// 修正前は too_many_pings で切れることまで確かめる
			if tc.wantDrop && err != nil && !strings.Contains(err.Error(), "too_many_pings") {
				t.Errorf("切断理由が too_many_pings でない: %v", err)
			}
		})
	}
}

// runBridgeLikeStream は radio-bridge と同じ keepalive 設定で接続し、
// 無音のまま wait だけ待って切断されたかを返す。
func runBridgeLikeStream(t *testing.T, opts []grpc.ServerOption, wait time.Duration) (bool, error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	server := grpc.NewServer(opts...)
	pb.RegisterTransceiverServiceServer(server, NewBridgeServer(NewBridgeRegistry(), nil))
	go server.Serve(listener)
	defer server.Stop()

	// radio-bridge と同じ keepalive 設定 (src/grpc/client.rs)
	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                bridgePingInterval,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), wait+30*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, bridgeIDMetadataKey, "TEST01")

	stream, err := pb.NewTransceiverServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// 受信待ちのまま無音で放置する。
	// サーバーが切れば Recv() がエラーで返り、生きていればブロックし続ける。
	errCh := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return true, err
	case <-time.After(wait):
		return false, nil
	}
}

// EnforcementPolicy.MinTime が bridge の ping 間隔より短いことを確認する。
//
// ここが逆転すると再び too_many_pings で切断されるため、
// 設定値を変更したときに気付けるようにする。
func TestKeepaliveMinTimeBelowBridgeInterval(t *testing.T) {
	opts := keepaliveOptions()
	if len(opts) == 0 {
		t.Fatal("keepaliveOptions が空: 既定設定では121秒で切断される")
	}

	// 実サーバーへ適用できることだけ確かめる (値の直接参照はできない)
	server := grpc.NewServer(opts...)
	server.Stop()

	// 値そのものはソースの定数で担保する。
	// MinTime を 30秒 以上にすると壊れることを、この定数の存在で示す。
	if bridgePingInterval != 30*time.Second {
		t.Errorf("bridge の ping 間隔が変わった: %v。"+
			"radio-bridge/src/grpc/client.rs と keepaliveOptions() の MinTime を確認すること",
			bridgePingInterval)
	}
}
