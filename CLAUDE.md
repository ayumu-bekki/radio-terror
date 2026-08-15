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

# keepalive の切断再現テスト (約5分。既定では飛ばされる)
go test -run TestKeepaliveAcceptsBridgePings -keepalive -timeout 420s -v
```

`gofmt -w <file>` は編集後に必ず実行する(`gofmt -l *.go` が何も出さない状態を保つ)。

### core-system (ESP-IDF / C++)

```bash
cd app/core-system
source /opt/esp-idf/export.sh   # または get_idf
idf.py build                    # ESP-IDF v6.0
idf.py menuconfig               # Wi-Fi・接続先・CoreID(数字4桁)・バッテリー監視の設定
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

### 正解の一貫性

Core向けJSONとナビゲーター知識は**同一の抽選結果から機械的に生成**する。
食い違うとプレイヤーが理不尽に失敗するため、`scenario_validate.go` が
「`answer` が `cut` の色に言及しているか」を組み立てのたびに検証する。
**別々に人手で書く運用にしてはいけない**。

### 切断線は必ず1本以上余らせる

配線は5本だが1セッション最大4ステージ(`maxStagesPerSession`)。全色を使い切ると
抽選の余地が消え、切る色が構造的に決まるステージ(203 暗号電文=語の頭文字→対照表の色)が
組み立て不能になる。203の候補語は全5色分を揃えてある。

### 表情タグは許可リストの6語だけ

ナビゲーターの発話には `[calm]` のような表情タグが付く(TTS が演技として解釈)。
使える語は `tts_prompt.go` の `allowedTTSTags` と
`navigator/prompt.toml` の両方に書く。**片方だけ増やしても効かない**
(サーバーが落とすか、生成AIが使わないか)。整合はテストで担保している。

一覧外のタグは落とす。演技として解釈されず「リリーブド」と読み上げられるため。
会話ログへ記録するときは `stripTTSTags` で全タグを除去する(画面表示用)。

### TTS は必ずストリーミングで受け取る

`GenerateContentStream` を使う。一括の `GenerateContent` に戻すと
**応答待ちが跳ねる** — 同一プロンプト15回で最大56.51秒、27%が10秒超。
ストリーミングでは同じ条件で全て 2.2〜3.8秒に収まった。

遅延はモデルの推論ではなく**一括応答の待ち受け側**にあった。
入力内容・並行数・出力量のいずれとも無関係なことは実測で確認済み
(`tts_latency_probe_test.go`。`-ttsprobe` で再実行できる)。

### Opus の granule position は常に 48kHz

音声は 24kHz で生成・エンコードしているが、Ogg の granule position は
**入力レートによらず 48kHz で数える**(RFC 7845 §4)。
`encodePCMToOggOpus` で `opusGranuleRate / sampleRate` を掛けているのはこのため。

ここを入力レートのまま書くと、**granule から尺を求める側が実尺の半分と誤認する**。
再生自体はデコード後のサンプル数で行われるため**表面化しにくい**
(実際、radio-bridge の音声長上限チェックが2倍ゆるく効いていた)。
尺を使う箇所は `audio_duration.go`(サーバー)と `queue.rs`(bridge)。

### ナビゲーター知識に固定値を書かない

ステージTOMLの `[navigator]`(briefing / answer / procedure / hint_*)に
数値や色を直接書くと、抽選値と衝突して**指示が自己矛盾する**。
101 では procedure の中間経路を `4`/`2` と固定で書いていたところ、
その値が危険位置に抽選され「4まで回せ」と「4は危険」を同時に言う状態になった
(危険位置自体はその後チュートリアルから廃止したが、原則は変わらない)。
手順に出る値は `[random]` の変数にして、衝突しうる値を `exclude` で外す。
回帰は `TestTutorialProcedureUsesDrawnPath` が押さえている。

**正解は「今は言うな」とセットで渡す**。プロンプトには常に正解が入るので、
禁止指示が離れた位置にあると生成AIが引きずられる(L3 で正解の色名を
直言した実例あり)。`navigator_prompt.go` は L4 未満で正解行の直後に
警告を併記する。

**`answer` は「読み上げる文」ではなく「照合の材料」として書く**。
なぞられると成立してしまう文(「プレイヤーが白色を報告してきたら復唱して〜」)は、
L1-L2 でそのまま出るとナビゲーターから色名が漏れる。
「プレイヤーの報告が白色と一致するかを照合に使う」という**判定の指示**で書く。

**L4 でも色名を言えないステージがある**。205 ブループリントは色を言うと
シートを読む工程が消えるため、L4 でも端子番号しか言えない。L4 は `answer` を
「そのまま伝えてください」と埋め込むので、`answer` 側に但し書きを書き、
`navigator_hint.go` の L4 ブロックが「但し書きが優先・但し書き自体は読み上げない」
と補う。回帰は `navigator_hint_test.go` が押さえている。

### 紙資料とサーバー設定の連動

`config.toml` の `[mission_sheet]`(記号の数・数字の合計・端子対応)は
**印刷物の実測値**。刷り直したら設定も更新する。印刷内容の仕様は
`docs/printed_materials.md` に集約している。

### Core ファームの時間管理

GameTask は**実経過時間の差分で tick を進める**。キュー待ちのタイムアウトだけに
頼ると入力イベント連続時に tick が飢え、カウントダウンが止まる(実装中に踏んだ)。

### MCP23017 の INTA はレベルで見る(エッジで拾わない)

MCP23017 の割り込みは**GPIO を読み出すまでクリアされない**ため、その間 INTA は
LOW に張り付く。`GpioInputWatchTask` の `on_up_` は「LOW が3サンプル続いた
**瞬間**」の1回しか呼ばれない(以降カウンタが飽和)ので、エッジで拾うと
**読み出す前に次の変化が起きた時点で入力検知が永久に止まる**
(ロータリーを速く回すと検知ログが消える不具合として実機で発生)。

`gpio_watcher_.SetPollHandler()` で5msごとに `ScanIfPending()` を呼び、
INTA のレベルを毎周期確認する。取りこぼしても次の周期で拾い直せる。

**ロータリーの確定は差分の有無で条件分岐しない**。過渡状態(全OFF)を読んだ
スキャンが差分を消費すると、その後安定しても確定処理に入れなくなる。

### 切った線の「結線」通知はプレイ中に受け付けない

ニッパーで切る瞬間、切断面が接触し直して **`切断 → 結線 → 切断` が数十ms間隔で
届く**。2度目の切断を処理すると、ステージ遷移後には「誤った線の切断」として
評価され、`on_wrong_cut: explode` のステージで**正しく切ったのに即爆発**する
(101 の実プレイで発生。40ms / 20ms 間隔)。

`GpioInputWatchTask` のデバウンス (5ms × 3 = 15ms) より長い間隔で届くので
**フィルタ時間を延ばしても防げない**。`HandleLineChanged` が `STATE_PLAYING` 中の
結線通知を破棄し、`OnLineCut` が1本につき1回だけ呼ばれるようにしてある。
プレイ中に切れた線が結線へ戻ることは物理的に起こらないので成立する
(運用でも繋ぎ直さないことを確認済み)。Setup 中の復帰は復旧ガイドに必要なため対象外。

### 配線テーブルは hardware_config.h にしかない

色や位置で引く対応表は**すべて `hardware_config.h`** に置く。
`kLineGpiosByColor` / `kLedPinsByColor` / `kPushGpiosByColor` /
`kRotaryGpiosByPosition` の4つ。基板を変えるときに見る場所を1つに保つため、
他のファイルへ同種の表を作らない。

LEDの出力ピンはこの5本がすべてなので、順序を問わない初期化ループも
`kLedPinsByColor` を回す(同じ配線を別の並びで二重に書かない)。

### ロータリーの位置対応

`kRotary1`→位置0 〜 `kRotary6`→位置5。GPIO番号は**降順**(GPA7→GPA2)で
直感に反するため、`kRotaryGpiosByPosition` は**配列の添字が
そのまま位置番号**になる並びで定義している。ここがずれると全ステージの
ロータリー条件が1つずれ、「動くが正解しない」形で現れる。
対応表は `docs/game_session_design.md` §3。

### ソレノイド(風船破裂)

駆動は単一関数に集約し、**Detonating 状態からのみ**呼べる。二重駆動はフラグで防ぐ。
この安全ガードを緩めないこと。

### Management Console は表示判断をテンプレートに書かない

画面は `html/template` のサーバー側描画。色分け・時刻書式・進行表示といった
**表示の判断は `manager_view.go` のビューモデルに寄せ**、テンプレートは
受け取った値を並べるだけにする。テンプレートへロジックを持ち込むと
Go のテストで検証できなくなる(`manager_view_test.go` が判断部分を押さえている)。

ステージ名・ナビゲーターの発話は**外部TOMLと生成AI由来**なので、
表示前のエスケープは `html/template` に任せる。`template.HTML` へ
キャストして自前で組み立てると、この保護が外れる。

テンプレートの拡張子は **`.gohtml`**(`.html` ではない)。`{{define}}` で始まり
単体では表示できないため、素のHTMLと取り違えないようにしている。
VS Code での色付けは `.vscode/settings.json` の `files.associations` で対応済み。

## 未検証・既知の制約

- **生成AI周り**は接続とTTS生成まで確認済み(Gemini Enterprise Agent Platform)。
  ナビゲーターの**実際の発話品質**は実機・実運用での確認が残っている。
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
