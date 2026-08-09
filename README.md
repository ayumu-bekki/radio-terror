RADIO TERROR

トランシーバー(特小無線)で生成AIナビゲーターと交信しながら、
風船入りの模擬爆弾(Core)を解体するハンズオン。

## ドキュメント

- [ゲームセッション設計 (Core)](docs/game_session_design.md)
- [radio-bridge 接続設計](docs/bridge_connection_design.md)
- [体験運用フロー](docs/operation_flow.md)
- [謎解きステージ集](docs/puzzle_stage_ideas.md)
- [ナビゲーター設計](docs/navigator_design.md)
- [シナリオテンプレート設計](docs/scenario_design.md)
- [紙資料(別紙)制作仕様](docs/printed_materials.md)
- **[マネージャー運用マニュアル](docs/manager_manual.md)** — 当日の運営手順(現場で見る)

設計は確定済み(2026-08-08)。実装は設計に沿って完了している(2026-08-09)。

## 構成

```
[core_system (複数台)] ──WebSocket /ws──▶ [wl-game-server] ◀── gRPC ── [radio-bridge (複数)]
                                              │
                                          [Valkey]  ナビゲーター(生成AI)・混線演出
```

周辺機器(Core・radio-bridge)はいずれも **サーバーへダイヤルインする**。
サーバーは周辺機器のアドレスを一切管理しない。

| コンポーネント | 言語 | 役割 |
|---|---|---|
| `app/core_system` | C++ (ESP-IDF) | Core本体。セッションJSONを受け取り単体でゲームを完遂する |
| `app/wl-game-server` | Go | セッション組み立て・ナビゲーター・混線・永続化・マネージャー画面 |

**編集して再起動すれば反映される設定**(再ビルド不要):

| 場所 | 内容 |
|---|---|
| `app/wl-game-server/scenarios/` | ステージ定義・難易度テンプレート |
| `app/wl-game-server/navigator/` | ナビゲーターのキャラクター・プロンプト |
| `app/wl-game-server/config.toml` | モデル・接続先・紙資料の物理定数など |
| `app/radio-bridge` | Rust | 特小無線とのPTT制御・音声入出力(Raspberry Pi) |
| `app/radio-bridge-emulator` | Go | radio-bridge の代替(PCのマイク・スピーカーを使う) |
| `app/radio-bridge-test-client` | Go | 動作確認用CLI(1つのbridgeとして振る舞う) |

## 動かす

```bash
cd app
cp wl-game-server/config.sample.toml wl-game-server/config.toml  # api_key を設定する
docker compose up
```

- マネージャー向け画面: <http://localhost:8080/manager>
- Core (core_system) の接続先とCoreIDは `idf.py menuconfig` の
  "Core System Configuration" で設定する(CoreIDは数字4桁)。

radio-bridge はチーム(周波数)ごとに1プロセス動かす。プロセスごとに変わる設定は
環境変数で指定し、`config.toml` は共有する:

| 環境変数 | 内容 |
|---|---|
| `RADIO_BRIDGE_ID` | この bridge の ID |
| `RADIO_BRIDGE_INPUT_DEVICE` | マイク側のオーディオIF (例 `hw:2,0`) |
| `RADIO_BRIDGE_OUTPUT_DEVICE` | スピーカー側のオーディオIF (例 `plughw:2,0`) |

デバイス名は実機で `aplay -l` / `arecord -l` で確認する。
チームを増やす場合は `compose.yaml` の `environment` だけ変えた
サービスを追加する。

## 制作物(コンテンツ)

コードとは別に、以下の制作が必要:

- 紙資料: モールス対照表・記号/数字シート・回路図シート・CoreID銘板。
  **印刷する表の中身と制作要件は [`docs/printed_materials.md`](docs/printed_materials.md)**
- 混線音声・効果音アセット(`app/wl-game-server/assets/README.md` に
  ファイル名規約とセリフ一覧)
