# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## プロジェクト概要

RADIO TERROR — トランシーバー(特小無線)で生成AIナビゲーターと交信しながら、
風船入りの模擬爆弾(Core)を解体するハンズオン。

**ドキュメントが設計の正本**。`docs/` に設計と決定の経緯が記録されており、
各ファイル末尾の「決定記録」に理由込みで残っている。

- **現在有効な決定は `docs/adr.md`** に集約してある。実装を変更する前にここを読む
- **経緯・実測値・却下案・覆った決定**は各 doc の決定記録に残す

実装を変更したら **該当doc・決定記録・`docs/adr.md` の3つを揃える**
(この運用をユーザーは重視している)。

## コマンド

### game-server (Go)

```bash
cd app/game-server
go build ./... && go vet ./... && go test ./...

# 単体テスト
go test -run TestBuildAllDifficulties -v
go test -run "TestHintLevel" -v      # 前方一致で複数実行

# keepalive の切断再現テスト (約5分。既定では飛ばされる)
go test -run TestKeepaliveAcceptsBridgePings -keepalive -timeout 420s -v

# 全ステージのナビゲーター交信を文字のみでシミュレーション (実APIを呼ぶ。約2分)
# 色漏れ・発話長・表情タグ・必須情報を検査する。docs/navigator_design.md §6
go test -run TestSimulateAllStages -simulate -timeout 60m -v
go test -run TestSimulateAllStages -simulate -sim-stages 201,305 -v

# **キャラクターを変えて回す**。口調で出る問題が違うため4人とも
# (敬語のアオサギは字数超過、命令形を使わないツグミは口調写し、
# ヒバリは最も字数が厳しく詰め込みすぎが最初に現れる)
go test -run TestSimulateAllStages -simulate -sim-character heron -v
go test -run TestSimulateAllStages -simulate -sim-character thrush -v
go test -run TestSimulateAllStages -simulate -sim-character lark -v
```

`gofmt -w <file>` は編集後に必ず実行する(`gofmt -l *.go` が何も出さない状態を保つ)。

### core-system (ESP-IDF / C++)

```bash
cd app/core-system
source /opt/esp-idf/export.sh   # または get_idf
idf.py build                    # ESP-IDF v6.0
idf.py menuconfig               # Wi-Fi・接続先・CoreID(数字4桁)・バッテリー監視・ブザーの設定
```

- **必ずプロジェクトルート (`app/core-system`) で実行する**。`main/` で実行すると
  そこに `build/` を作って失敗する。
- clangd が `-mlongcalls` や `std::string` について大量のエラーを出すが、
  xtensa ツールチェーンを見ていないだけの**偽陽性**。`idf.py build` が唯一の判断基準。
- ソースを追加したら `main/CMakeLists.txt` の `SRCS` にも追加する
  (`.h` だけのヘッダオンリーなら不要)。

**書き方の決まり**(ユーザーの好み。既存コードもこれで揃えてある):

- **`switch` は使わない。`if` で書く。** 列挙型の分岐も `if (x == A) { ... }` を並べ、
  最後に既定値を返す形にする。
- **`if` の本体は必ず中括弧で囲み、改行する。** `if (x) return;` のような
  一行の書き方はしない。`else if` の連鎖は可。

**ファイルの分かれ方**: 状態を持つのは `GameTask` に寄せ、周辺は
**ゲーム状態に触れない部品**にしてある。新しい処理を足すときもこの線を保つ。

| ファイル | 役割 |
|---|---|
| `core_system.h/.cc` | 組み立てと配線のみ。状態を持たない |
| `boot_animation.h` | 起動演出・起動インジケータ |
| `mcp_input_scanner.h` | MCP23017 のピン変化 → GameEvent |
| `game_task.h/.cc` | 状態機械とゲームルール(中核) |
| `event_sender.h` | サーバーへの送信。cJSON の組み立て |
| `status_indicator.h` | 状態 → フルカラーLEDの色・点滅 |
| `countdown_display.h` | 残り時間 → 7セグ表示 |
| `timer_digit_rule.h` | timer_digit の桁一致判定と猶予窓 |
| `game_session.h/.cc` | セッションJSONのパース・検証 |
| `led_pattern.h/.cc` | LED表示パターンの構築(点滅・モールス) |

**Tick系は GameTask に置いたまま**にしてある。残り時間・進行状態を書き換えるため、
切り出すと参照の引き回しが増えて逆に読みにくくなる。

### radio-bridge (Rust)

**macOS ではビルドできない**。`alsa` / `rppal` が Linux 依存で、`cargo build` は
自コードに到達する前に `rppal` で失敗する。**Docker でのビルドが唯一の検証経路**
(ビルド確認済み)。

```bash
docker compose build radio-bridge
```

macOS で手早く見たい場合、`src/config.rs` は `serde`/`toml` にしか依存しないため、
一時クレートへコピーすれば型チェックとテストを実行できる。
`client.rs` / `main.rs` は構文チェックのみ可能。

### 全体起動

```bash
cd app
cp game-server/config.sample.toml game-server/config.toml  # project/location を設定
docker compose up
```

起動ログの先頭3行で、バイナリと設定を切り分けられる:

```
[boot] commit: 1fcd431ca78a-dirty / go1.25.13
[boot] config: config.toml
[boot] manager secret words: 2 word(s): で…(3), 電…(2)
```

**設定を直したのに反映されない**ときは、まずこの3行を見る。commit が想定と
違えばコンテナが古い。秘密ワードの語数が想定と違えば設定かバイナリが古い
(`"でんぱ,電波"` を1語と数えるのは、カンマ区切り対応前のバイナリ)。

commit id は `go build` が `.git` から自動で埋め込む。そのため
**compose の build context はリポジトリルート**にしてある(`app/` だと
`.git` が見えず commit が空になる)。重い成果物は `/.dockerignore` で除外済み。

Management Console: <http://localhost:8080/manager>

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
| Management Console | `manager_web.go`(ハンドラ) + `manager_view.go`(表示整形) + `manager_*.gohtml` |

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

**規範は `docs/adr.md` に移した。** 実装を変更する前に読むこと。
経緯・実測値・却下案は各 doc の決定記録に残してある。

とくに事故に直結するものだけ、ここに再掲する:

| 論点 | 要点 | ADR |
|---|---|---|
| 正解の一貫性 | Core向けJSONとナビゲーター知識は同一の抽選結果から機械的に生成する。別々に人手で書かない | S-1 |
| 切断線を余らせる | 5本の配線に対し1セッション最大4ステージ。全色を使い切ると203が組み立て不能 | S-2 |
| 正解色を伏せる | L4未満は `redactCutColor` でプロンプトから伏せる。「書いてあるが言うな」は守られない | N-1 |
| 出し惜しみしない | 装置に現れない情報(ボタン列・危険位置)は伏せると手詰まりになる | N-4 |
| まず報告させる | 課題の第一声は「ランプはどうなっている?」。`hint_l1` は「報告させる」と動作で書く | N-10 |
| 60文字は目安 | 締めると危険の警告・数字といった情報が先に削られる。切り詰めない・止めない | N-22 |
| TTS はストリーミング | `GenerateContent` に戻すと応答待ちが跳ねる(最大56.51秒) | T-1 |
| 表情タグは6語 | `allowedTTSTags` と `prompt.toml` の両方に書く。片方だけでは効かない | T-5 |
| ノートに場面を書かない | TTS が場面を演じて相手の発話まで作る | T-6 |
| granule は48kHz | 入力レートで書くと尺が半分に誤認される (RFC 7845 §4) | T-7 |
| 結線通知を破棄 | プレイ中の `切断→結線→切断` バウンスで正しく切っても即爆発する | C-3 |
| INTA はレベルで見る | エッジで拾うと入力検知が永久に止まる | C-2 |
| 配線テーブルは1箇所 | 対応表は `hardware_config.h` だけ。他に同種の表を作らない | C-4 |
| ソレノイド | Detonating 状態からのみ駆動。安全ガードを緩めない | C-6 |
| 秘密ワードの表記ゆれ | 漢字はかなへ戻せない。`"でんぱ,電波"` と全て並べる | P-6 |
| 操作に反応しない | ナビは装置を見ていない。押下通知で発話しない、叩いている最中は黙る | N-26 |
| ミスには音を出す | 無線越しは音が唯一のフィードバック。ブザー+軽いペナルティ | C-6b |
| ミスは誤押下だけ | モグラは押せずに消えてもミスにしない。無操作でブザーが鳴り続け勝手に爆発する | C-11 |
| 爆発は死亡扱い | 安否確認 → 現地へ向かうで締める。「次は」「お疲れさま」は使わない | N-25 |
| リセットは消す | `AbortSession` は Valkey からも消す。binder だけだと再起動で復活する | P-4b |
| 検証は複数回 | 生成AIはばらつく。色漏れ・字数は1回で「直った」と判断しない | V-1 |

## 未検証・既知の制約

- **生成AI周り**は接続とTTS生成まで確認済み(Gemini Enterprise Agent Platform)。
  発話内容は**全24ステージを文字のみでシミュレーション済み**
  (色漏れ・発話長・表情タグ・必須情報。`docs/navigator_design.md` §6)。
  残るのは**音声込みの品質**(声質・間)と**音声認識との組み合わせ**で、
  これは実機でしか確認できない。
  Gemini API (APIキー認証) には対応しない (`docs/gemini_enterprise_setup.md`)。
- **紙資料**は未制作。`config.toml` の `[mission_sheet]` は印刷物の実測値なので、
  刷ったら値を突き合わせる (`docs/printed_materials.md`)。
- **音声アセットは全て配置済み**(混線30ファイル + 効果音2ファイル)。
  未配置でも起動はするが**無言でスキップされる**ため、起動ログの
  `[crosstalk] loaded assets:` で件数を確認する。
- `config.toml` / `config-mac-docker.toml` は **キルスイッチの秘密ワードを含むため
  gitignore 済み**。スキーマを変えたら `*.sample.toml` も揃える。
- `app/secrets/` のサービスアカウントキーも gitignore 済み。

## ドキュメント

| ファイル | 内容 |
|---|---|
| `docs/adr.md` | **現在有効な決定事項の一覧**(実装を変更する前に読む) |
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
