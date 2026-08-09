# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## プロジェクト概要

RADIO TERROR — トランシーバー(特小無線)で生成AIナビゲーターと交信しながら、
風船入りの模擬爆弾(Core)を解体するハンズオン。

**ドキュメントが設計の正本**。`docs/` の7ファイルに設計と決定の経緯が記録されており、
各ファイル末尾の「決定記録」表に理由込みで残っている。実装を変更したら
該当docと決定記録も更新する(この運用をユーザーは重視している)。

## コマンド

### game-server (Go)

```bash
cd app/game-server
go build ./... && go vet ./... && go test ./...

# 単体テスト
go test -run TestBuildAllDifficulties -v
go test -run "TestHintLevel" -v      # 前方一致で複数実行
```

`gofmt -w <file>` は編集後に必ず実行する。ただし `callsign.go` は元から未フォーマットなので触らない。

### core-system (ESP-IDF / C++)

```bash
cd app/core-system
source /opt/esp-idf/export.sh   # または get_idf
idf.py build                    # ESP-IDF v6.0
idf.py menuconfig               # Wi-Fi・接続先・CoreID(数字4桁)の設定
```

- **必ずプロジェクトルート (`app/core-system`) で実行する**。`main/` で実行すると
  そこに `build/` を作って失敗する。
- clangd が `-mlongcalls` や `std::string` について大量のエラーを出すが、
  xtensa ツールチェーンを見ていないだけの**偽陽性**。`idf.py build` が唯一の判断基準。
- ソースを追加したら `main/CMakeLists.txt` の `SRCS` にも追加する。

### radio-bridge (Rust)

**macOS ではビルドできない**。`alsa` / `rppal` が Linux 依存で、`cargo build` は
自コードに到達する前に `rppal` で失敗する。

```bash
# 実機/Docker でのビルドが唯一の経路
docker compose build radio-bridge
```

macOS で検証する場合、`src/config.rs` は `serde`/`toml` にしか依存しないため、
一時クレートへコピーすれば型チェックとテストを実行できる(実際にこの方法で検証した)。
`client.rs` / `main.rs` は構文チェックのみ可能。

### 全体起動

```bash
cd app
cp game-server/config.sample.toml game-server/config.toml  # project/location を設定
docker compose up
```

マネージャー画面: <http://localhost:8080/manager>

## アーキテクチャ

### 接続方向 — 周辺機器がすべてサーバーへダイヤルインする

```
[core-system 複数台] ──WebSocket /ws──▶ [game-server] ◀── gRPC ── [radio-bridge 複数]
                                             │
                                          [Valkey]
```

**サーバーは周辺機器のアドレスを一切持たない**。確立済みのストリーム自体が返信路で、
`map[bridge_id] → 送信チャネル` のレジストリ経由で返す。bridge を増やしても
サーバー設定の変更は不要。この反転は `docs/bridge_connection_design.md` §2 決定1。

### 責務の分割 — サーバーが決め、デバイスが自律実行する

サーバーがシナリオ(セッションJSON、**正解を含む**)を組み立てて送り、
Core は受信後 **Wi-Fi が切れても単体でゲームを完遂**する。Core は進行イベントを
サーバーへ通知し、それがナビゲーター演出のトリガーになる。

謎解きの内容は完全にサーバー側コンテンツの責務で、**謎の差し替えにファーム書き換えは不要**。

### game-server の主要な流れ

| 経路 | ファイル |
|---|---|
| bridge からの音声受信 | `bridge_server.go` → `audio_pipeline.go` |
| マネージャー音声コマンド判定 | `manager_command.go`(開始申告・秘密ワード付きリセット) |
| セッション組み立て | `scenario_builder.go` + `scenario_expand.go` + `scenario_validate.go` |
| バインド・イベント処理の中心 | `game_coordinator.go` |
| ナビゲーター発話生成 | `navigator_speaker.go` → `navigator_prompt.go` |
| デバイスとのWS | `ws_session.go` + `device_registry.go` |

**`/ws` は1エンドポイントにトランシーバーとデバイスが相乗り**する。
接続種別は最初のメッセージで判定する(`login` → トランシーバー、
`device_status` → core-system)。

### 外部ファイルで差し替えられるもの(再ビルド不要)

編集してサーバーを再起動すれば反映される。compose で bind mount 済み。

| 場所 | 内容 |
|---|---|
| `app/game-server/scenarios/stages/*.toml` | ステージ定義(1ステージ1ファイル) |
| `app/game-server/scenarios/difficulty/*.toml` | 難易度テンプレート |
| `app/game-server/navigator/prompt.toml` | 共通役割定義・出力ルール・発話トリガー指示 |
| `app/game-server/navigator/characters/*.toml` | キャラクター(1人1ファイル) |

保留中のステージは `.toml.disabled` にしておけば置いたまま無効化できる。

## この設計で壊しやすい点

### 正解の一貫性

Core向けJSONとナビゲーター知識は**同一の抽選結果から機械的に生成**する。
食い違うとプレイヤーが理不尽に失敗するため、`scenario_validate.go` が
「`answer` が `cut` の色に言及しているか」を組み立てのたびに検証する。
**別々に人手で書く運用にしてはいけない**。

### 切断線は必ず1本以上余らせる

配線は5本だが1セッション最大4ステージ(`maxStagesPerSession`)。全色を使い切ると
抽選の余地が消え、切る色が構造的に決まるステージ(203 暗号電文=語の頭文字→対照表の色)が
組み立て不能になる。203の候補語は全5色分を揃えてある。

### 紙資料とサーバー設定の連動

`config.toml` の `[mission_sheet]`(記号の数・数字の合計・端子対応)は
**印刷物の実測値**。刷り直したら設定も更新する。印刷内容の仕様は
`docs/printed_materials.md` に集約している。

### Core ファームの時間管理

GameTask は**実経過時間の差分で tick を進める**。キュー待ちのタイムアウトだけに
頼ると入力イベント連続時に tick が飢え、カウントダウンが止まる(実装中に踏んだ)。

### ソレノイド(風船破裂)

駆動は単一関数に集約し、**Detonating 状態からのみ**呼べる。二重駆動はフラグで防ぐ。
この安全ガードを緩めないこと。

## 未検証・既知の制約

- **radio-bridge (Rust)** は型検証が未実施(上記のビルド制約)。実機/Docker での確認が必要。
- **生成AI周り**は Google Cloud の認証が要るため未実行。ナビゲーターの実際の発話品質・
  TTSボイスの妥当性は実機確認が残っている。接続先は Gemini Enterprise Agent Platform
  のみで、Gemini API (APIキー認証) には対応しない (`docs/gemini_enterprise_setup.md`)。
- **ロータリーのGPIO対応**(`kRotary1`→位置0)は推測のまま。ずれていると
  全ステージのロータリー条件が1つずれるが「動くが正解しない」形で現れる。
- `config.toml` / `config-mac-docker.toml` は **キルスイッチの秘密ワードを含むため
  gitignore 済み**。スキーマを変えたら `*.sample.toml` も揃える。
- `app/secrets/` のサービスアカウントキーも gitignore 済み。

## ドキュメント

| ファイル | 内容 |
|---|---|
| `docs/game_session_design.md` | Core の状態機械・ゲームルール・WSプロトコル(最も参照する) |
| `docs/bridge_connection_design.md` | 接続反転・bridge レジストリ・音声バインド |
| `docs/scenario_design.md` | テンプレート形式・組み立てフロー・Valkeyキー設計 |
| `docs/navigator_design.md` | キャラクター・プロンプト構成・ヒントレベル |
| `docs/puzzle_stage_ideas.md` | 全ステージの内容と構成ルール |
| `docs/operation_flow.md` | 運用フロー・混線演出・音声コマンド |
| `docs/printed_materials.md` | 紙資料の印刷内容(実装と一致させる値) |
| `docs/manager_manual.md` | 当日の運営手順書(現場で見る) |
| `docs/gemini_enterprise_setup.md` | Gemini Enterprise Agent Platform(旧 Vertex AI)への切り替え手順 |
| `docs/crosstalk_audio_generation.md` | 混線音声の生成テキストと TTS 設定 |

## 作業の進め方

- 設計相談は**論点を一つずつ**進める。まとめて提示すると検討が浅くなる。
- 決定したら**その場で docs と決定記録表を更新**する。
- ハードウェアの実態(特小無線の運用、PCB側の対策)はユーザーが把握しているので、
  推測せず確認する。
