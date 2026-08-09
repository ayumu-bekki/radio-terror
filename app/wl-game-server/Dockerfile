FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    libopus-dev \
    libopusfile-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2

WORKDIR /build
COPY proto/ proto/
COPY wl-game-server/ wl-game-server/

WORKDIR /build/wl-game-server/
RUN go generate ./...
RUN CGO_ENABLED=1 go build -o wl-game-server .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libopus0 \
    libopusfile0 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/wl-game-server/wl-game-server /usr/local/bin/wl-game-server

# シナリオテンプレート (ステージ定義・難易度テンプレート) は実行時に読むため
# ランタイムイメージへも含める (docs/scenario_design.md §1)
COPY --from=builder /build/wl-game-server/scenarios/ /app/scenarios/

# ナビゲーター設定 (キャラクター・プロンプト) も実行時に読む
COPY --from=builder /build/wl-game-server/navigator/ /app/navigator/

WORKDIR /app
CMD ["wl-game-server", "config.toml"]
