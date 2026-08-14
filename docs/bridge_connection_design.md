# radio-bridge 接続設計 (複数台対応・ID付与・動的チームバインド)

radio-bridge を複数プロセス運用するための接続方式と、
チーム(無線)⇔ core-system デバイスの対応付けの設計。

- 作成日: 2026-08-08 / 更新日: 2026-08-10
- ステータス: **実装済み**。実装由来の決定は §2 決定事項 #6-9
- 関連: `game_session_design.md`(Core側セッション設計) /
  `operation_flow.md`(体験運用フロー)
- 用語(プレイヤー / ナビゲーター / マネージャー / Core)は
  `game_session_design.md` §1.1 の用語集に従う

## 1. 背景と課題

**(この節は設計時点の課題認識。現在は §2 の決定どおり実装済み)**

設計当時は game-server が gRPC クライアントとして、config の単一 `host:port` に
ダイヤルして radio-bridge(gRPCサーバー)と1本の双方向ストリームを張っていた。

- radio-bridge を複数プロセス(Audioデバイス複数)動かしたい
- radio-bridge は自身の ID を持っておらず、サーバーがどの無線かを識別できない
- どの無線がどの Core(危険物体験装置)を担当するかの対応付けが必要

## 2. 決定事項

| # | 論点 | 決定 |
|---|---|---|
| 1 | 接続方向 | **反転する**。radio-bridge が gRPC クライアントになり game-server にダイヤルする。サーバーは周辺機器のアドレスを一切管理せず、bridge 増設時のサーバー側 config 変更も不要。core-system の WS 接続と同じ「周辺機器 → サーバーへのダイヤルイン」パターンに統一 |
| 2 | ID の付与 | radio-bridge は起動時に環境変数(例: `RADIO_BRIDGE_ID`)で ID を受け取る |
| 3 | ID の伝達 | 接続時に **gRPC メタデータ**(`bridge-id` ヘッダー)で送る。proto 変更不要。将来交換する情報が増えたら Hello メッセージ方式に移行する |
| 4 | 返信路 | サーバーはアドレス・ポートを記録**しない**。確立済みの双方向ストリーム自体が返信路であり、`map[bridge_id] → ストリーム(送信チャネル)` のレジストリを保持して返信はそのストリームに書く |
| 5 | チーム対応 | **動的バインド**。マネージャーがトランシーバー経由で Core の ID と難易度を音声で申告し、サーバーがその無線(bridge)と Core を紐付けてセッションを開始する(§5) |
| 6 | test-client の試験方式(実装時) | モックサーバーは作らず、**game-server へ直接ダイヤルインして1つの bridge として振る舞う**方式にした。実機と同じ経路を通るため、モックと本番の挙動差が生まれない(§6) |
| 7 | 必須設定が未設定の場合(実装時) | **起動時にエラーで停止**する。ID なしの接続はサーバーに拒否され、デバイス未設定は無音になるため、いずれも実行中に気づくより起動時点で落とす方が運用で分かりやすい(§6.1) |
| 8 | オーディオIFの指定方法(実装時) | bridge ID と同様に**環境変数(`RADIO_BRIDGE_INPUT_DEVICE` / `RADIO_BRIDGE_OUTPUT_DEVICE`)を優先**する。オーディオIFはチーム(周波数)ごとに変わるプロセス固有の設定であり、TOMLに固定すると同じ config.toml を共有できず、プロセスごとに設定ファイルを分ける必要が生じるため(§6.1) |
| 9 | PTT用GPIOピンの指定方法(実装時) | 同じ理由で**環境変数(`RADIO_BRIDGE_PTT_PIN`)を優先**する。PTT配線(PCB)はbridgeを動かすRaspberry Piごとに異なるため(§6.1) |
| 10 | コールサイン抽出の廃止(実装時) | 文字起こしで送信者・受信者のコールサインを抽出していたが、**無線側で名乗りを強制する運用をやめた**ため `message` のみにした。開始申告・秘密ワードの判定は元から `message` だけを見ており(「4桁ID + 難易度 + 開始の意図」の一致)、判定ロジックへの影響はない。抽出結果はログ表示にしか使われていなかった |
| 11 | keepalive の受け入れ設定(実機確認時) | **接続が約2分ごとに切れていた**。radio-bridge は無音区間が長いため30秒ごとに keepalive ping を送るが、gRPC-Go の既定 `EnforcementPolicy` は `MinTime=5分` / `PermitWithoutStream=false` で、これに反する ping が一定数たまると `ENHANCE_YOUR_CALM` / `too_many_pings` の GOAWAY を返す。30秒間隔では**ちょうど121秒**で切断される(テストで再現・修正後は切れないことを確認済み)。サーバー側に `KeepaliveEnforcementPolicy{MinTime: 10s, PermitWithoutStream: true}` を設定して解決した。**`MinTime` は bridge の ping 間隔 (30秒) より短くなければならない** — 逆転すると再発する |
| 12 | ストリーム待ち時間を5秒へ(実機確認時) | ナビゲーターの音声が途中で途切れた。TTS を分割生成して順次送るが、bridge 側は現ストリームの後続チャンクを **2秒**しか待たず、間に合わなかったチャンクを `audio discarded: stream closed` で破棄していた。TTS は通常2〜3秒かかるため、わずかな遅れでも千切れる設定だった。`STREAM_TIMEOUT_MS` を **5秒**へ伸ばした。長くすると遅延中ずっと PTT を握って無線を塞ぐため、大半のケースを救えて塞ぎすぎない範囲に留める。**遅延そのものの主因は TTS プロンプトの表情タグ**で、そちらは廃止済み (`navigator_design.md` §4.1) |

## 3. 接続シーケンス

```
radio-bridge (Rust)                     game-server (Go)
      │  gRPC Connect (metadata: bridge-id=BR01)
      ├───────────────────────────────────▶│
      │                                    │ レジストリ登録: BR01 → stream
      │◀──────── 双方向ストリーム確立 ─────▶│
      │  AudioChunk (マイク音声) ─────────▶│ transcribe → reason → dispatch
      │◀───────── AudioChunk (TTS音声)     │ 宛先bridgeのstreamへ送信
      │                                    │
      │  (切断)                            │ レジストリから BR01 を削除
      │  再接続ループでリトライ ──────────▶│ 再登録
```

- **再接続**: bridge 側が再接続ループを持つ(旧 `bridge_client.go` の
  リトライロジックを Rust / emulator 側へ移植済み)。接続失敗・切断のいずれでも
  `reconnect_interval_secs` 待機してから再試行し続ける。
- **同一IDの二重接続**: bridge プロセスの再起動を想定し、後着を採用して
  旧ストリームを閉じる(ログ警告を出す)。
- **メタデータに bridge-id が無い接続**: 拒否(エラーで閉じる)。
- **keepalive**: トランシーバーは**無音区間が長い**ため、bridge は30秒ごとに
  HTTP/2 の keepalive ping を送って接続を維持する
  (`radio-bridge/src/grpc/client.rs` の `http2_keep_alive_interval`)。
  **サーバー側はこの ping を受け入れる設定が必須**
  (`bridge_server.go` の `keepaliveOptions`)。gRPC-Go の既定では
  短い間隔の ping が違反として数えられ、**約2分で GOAWAY 切断される**
  (§2 決定11)。設定の対応関係は次のとおり。

  | 側 | 設定 | 値 |
  |---|---|---|
  | bridge | ping 間隔 | 30秒 |
  | server | `EnforcementPolicy.MinTime` | **10秒**(ping 間隔より短いこと) |
  | server | `EnforcementPolicy.PermitWithoutStream` | **true**(無音中の ping を許可) |

  片方だけ変更すると切断が再発する。

## 4. game-server 内部の変更

- `TransceiverService` の gRPC サーバー実装を追加(tonic 側にあった役割の移転。
  proto 自体は変更なし)。
- ブリッジレジストリ: `map[bridgeID] → 送信チャネル`。接続時登録・切断時削除。
- 現行の単一 `sendCh` を廃止し、`outgoing` / `dispatcher` は**宛先 bridge_id を
  指定して送る**形に変更する。宛先解決はチームバインド表(§5)を参照。
- 受信処理(transcribe → reason → dispatch)は現行の `handleAudio` の流れを維持し、
  「どの bridge から来た音声か」を文脈に加える。
- 送出音声はTTSに加えて**効果音アセット**(成功・失敗)と**混線音声**
  (`operation_flow.md` §5.1)を扱う。混線音声は**事前生成アセット**で、
  サーバーは再生時にファイル選択のみ行う。混線はランダムスケジュールに加え、
  **イベント駆動**(他チームのCoreの `exploded` を契機に「別現場の通信」を
  Playing中の他bridgeへ流す)にも対応する。いずれもナビゲーターの発話と
  重ならないよう制御する。
- マネージャー音声コマンドとして、開始申告(§5)に加え
  **秘密ワード付き強制リセット**(`operation_flow.md` §7)の意図抽出を行い、
  一致時は対象 Core へ `session_abort` を送る。

## 5. 音声によるチームバインドとセッション開始

**マネージャー**がトランシーバー経由で Core の ID と難易度を申告する。
この一声がバインド(無線 ⇔ Core の紐付け)・難易度指定・セッション開始指示を兼ねる
(運用の全体像は `operation_flow.md` 参照):

> マネージャー「こちらマネージャー。CoreID 3701 難易度ノーマルで開始してください。どうぞ。」
>
> ナビゲーター「こちらナビゲーター。CoreID 3701 難易度ノーマルで開始します。」

- **無線構成の前提**: チームごとに特定小電力トランシーバーの周波数を分離し、
  bridge はチーム周波数ごとに1プロセス+専用オーディオインターフェースで
  入出力する。マネージャーは対象チームの周波数で申告するため、申告は
  対象チームの bridge のみが受信する(`operation_flow.md` §8)。
- **device_id の体系**: **数字4桁**とする。音声申告・文字起こしで扱いやすく、
  意図抽出時は「4桁の数字」をデバイスID候補として検出すればよい。
- **難易度の語彙**: イージー / ノーマル / ハード の3段階。
- **バインド・開始フロー**:
  1. マネージャーが無線で Core の ID と難易度を申告
  2. サーバーが transcribe → reason で「開始申告」の意図・4桁ID・難易度を抽出
  3. 検証: 該当 device_id の Core が WS 接続中かつ Ready 状態か
  4. OK なら `map[bridge_id] ⇔ device_id` を紐付け、難易度に対応するシナリオ
     テンプレートからセッションJSONを組み立てて `session_start` を送信し、
     TTS の無線応答で確認を返す
  5. **開始直後、ナビゲーターから初回の声掛けを TTS でプッシュする**
     (プレイヤーの発話待ちにせず行動を促す)。以降、その Core のゲームイベント
     (stage_cleared 等)はこの bridge への無線演出に接続される
- **競合**: 申告されたデバイスが既に他 bridge にバインドされ **Playing 中**の場合は
  拒否(無線で「使用中」の旨を応答)。それ以外は上書き(後勝ち)。
- **解除**: 明示的な解除は設けない。新しい申告による上書きで運用する。
  bridge・デバイスの切断ではバインドを消さない(再接続で継続)。

## 6. 影響範囲

| コンポーネント | 変更 |
|---|---|
| radio-bridge (Rust) | gRPC サーバー → クライアント化。`RADIO_BRIDGE_ID` 環境変数、bridge-id メタデータ付与、再接続ループ追加 |
| game-server (Go) | gRPC サーバー実装追加、ブリッジレジストリ、宛先指定送信、音声バインドの意図抽出・検証 |
| radio-bridge-emulator (Go) | radio-bridge と同様にクライアント化(ID は環境変数) |
| radio-bridge-test-client (Go) | **game-server へ直接ダイヤルインし、1つの bridge として振る舞う**方式に変更(モックサーバーは作らない)。実機の radio-bridge と同じ経路で試験できる |
| proto | 変更なし(メタデータ方式のため) |

### 6.1 設定項目

接続方向の反転に伴い、各コンポーネントの設定は次のように変わる。

| コンポーネント | 変更前 | 変更後 |
|---|---|---|
| game-server | `[radio_bridge] host` / `port`(ダイヤル先) | `[radio_bridge] listen_addr`(待ち受け) |
| radio-bridge / emulator | `[server] listen_addr`(待ち受け) | `[server] server_addr`(ダイヤル先) + `bridge_id` + `reconnect_interval_secs` |

- game-server 側は bridge のアドレスを設定しないため、**bridge 増設時の
  サーバー設定変更は不要**。

#### プロセスごとに変わる設定は環境変数で指定する

radio-bridge は**チーム(周波数)ごとに1プロセス+専用オーディオインターフェース
+専用PTT用GPIOピン**で動かす(`operation_flow.md` §8)。プロセスごとに異なる値は
環境変数で与え、`config.toml` は全プロセスで共有できるようにする(bind mount
したまま `environment` だけ変えてサービスを増やせる)。

| 環境変数 | 対応するTOML | 内容 |
|---|---|---|
| `RADIO_BRIDGE_ID` | `[server] bridge_id` | この bridge の ID |
| `RADIO_BRIDGE_INPUT_DEVICE` | `[audio] input_device` | マイク側のオーディオIF |
| `RADIO_BRIDGE_OUTPUT_DEVICE` | `[audio] output_device` | スピーカー側のオーディオIF |
| `RADIO_BRIDGE_PTT_PIN` | `[gpio] ptt_pin` | PTT制御用GPIOピン(BCM番号) |

- **環境変数が設定されていればTOMLより優先**する。空文字の環境変数は
  「未設定」として無視し、TOMLの値を残す。
- TOML側の値は環境変数が無い場合の**フォールバック**として機能する
  (単体プロセスでの開発時はTOMLだけで完結する)。
- **いずれの経路でも未設定の場合は起動時にエラーで停止**する。
  ID 未設定は接続拒否、デバイス未設定は無音、ピン未設定はGPIO初期化失敗という
  分かりにくい失敗になるため、起動時点で原因を明示して落とす。
  `RADIO_BRIDGE_PTT_PIN` はTOMLの `[gpio] ptt_pin` と同じく数値(0-255)で
  指定する。数値としてパースできない場合も起動時エラーになる。
- 解決後の値は起動ログに出力し、環境変数とTOMLのどちらが効いたかを
  運用中に判別できるようにする。

## 7. 実装ステップ(完了)

1. ✅ game-server: `TransceiverService` サーバー実装+ブリッジレジストリ
2. ✅ radio-bridge: クライアント化+ID メタデータ+再接続ループ
3. ✅ radio-bridge-emulator: 同様の変更
4. ✅ game-server: `outgoing` / `dispatcher` の宛先指定化
   (単一 `sendCh` を廃止し、宛先 bridge を束縛した `AudioSender` を渡す形にした)
5. ✅ 音声バインド(意図抽出・検証・確認応答)
6. ✅ radio-bridge-test-client の試験方式見直し(§6 のとおり bridge として振る舞う)

**ビルド経路**: radio-bridge (Rust) は ALSA / rppal が Linux 依存のため、
開発機(macOS)ではビルドできない。`radio-bridge/Dockerfile` が唯一のビルド経路で、
**Docker でのビルドは確認済み**。

残るのは実機(Raspberry Pi + 特小無線)での**動作**確認。
音声デバイス名・PTTのGPIO・遅延の実測が必要になる。

<!-- EOF -->
