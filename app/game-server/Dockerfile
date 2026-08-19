FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    libopus-dev \
    libopusfile-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2

WORKDIR /build
# context はリポジトリルート (compose.yaml 参照)。
# `.git` は commit id の埋め込み (vcs.revision) に使うので必ず入れる。
COPY .git/ .git/
COPY app/proto/ proto/
COPY app/game-server/ game-server/

WORKDIR /build/game-server/
RUN go generate ./...
# commit id は `go build` が `.git` から自動で埋め込む (vcs.revision)。
# 起動ログの `[boot] commit:` に出て、「どのソースから作られたバイナリが
# 動いているか」が分かる (設定を直したのに反映されない、の切り分けに使う)。
RUN CGO_ENABLED=1 go build -o game-server .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libopus0 \
    libopusfile0 \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/game-server/game-server /usr/local/bin/game-server

# シナリオテンプレート (ステージ定義・難易度テンプレート) は実行時に読むため
# ランタイムイメージへも含める (docs/scenario_design.md §1)
COPY --from=builder /build/game-server/scenarios/ /app/scenarios/

# ナビゲーター設定 (キャラクター・プロンプト) も実行時に読む
COPY --from=builder /build/game-server/navigator/ /app/navigator/

WORKDIR /app
CMD ["game-server", "config.toml"]
