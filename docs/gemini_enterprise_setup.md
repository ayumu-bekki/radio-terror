# Gemini Enterprise Agent Platform セットアップ手順

game-server と crosstalk-gen が使う生成AIの接続先を、
**Google AI Studio (Gemini API) から Gemini Enterprise Agent Platform へ
切り替える**手順。

> **名称について**: Gemini Enterprise Agent Platform は旧称 **Vertex AI**。
> SDK の定数は `genai.BackendEnterprise` を使う。
>
> ただし **API エンドポイント (`aiplatform.googleapis.com`) と
> IAM ロール名 (`Vertex AI User`) には旧称が残っている**。
> Cloud Console でそう表示されるため、本ドキュメントでもそのまま記載している。

## なぜ切り替えるか

Gemini API は無料枠 / Tier 1 のレート制限が厳しく、
混線音声30ファイルの一括生成や本番中の連続TTSで枠を使い切る。
Gemini Enterprise Agent Platform はレート制限が緩く、本番運用に向く。

代わりに**従量課金**になり、認証が Google Cloud の方式になる。

| | Gemini API | Gemini Enterprise Agent Platform |
|---|---|---|
| 認証 | APIキー1つ | Google Cloud の認証情報 |
| レート制限 | 厳しい | 緩い |
| 課金 | 無料枠あり | 従量課金 |
| 設定 | (対応しない) | `project` / `location` |

**game-server / crosstalk-gen とも Gemini Enterprise Agent Platform 専用**。
Gemini API (APIキー認証) には対応しない。

---

## 1. 認証方式の選択

Gemini Enterprise Agent Platform に **APIキーは存在しない**。Google Cloud の認証基盤を使う。
実行環境に応じて2つの方式がある。

| 方式 | 用途 | 有効期限 |
|---|---|---|
| **サービスアカウントキー** | 本番・常設マシン (Raspberry Pi 含む) | なし |
| ユーザーADC | 手元での動作確認 | あり (再ログインが必要) |

**GCE 上で動かす場合は両方とも不要**(メタデータサーバーから自動取得される)。
このプロジェクトは GCE 外での運用を想定しているため、以下が必要になる。

### `gcloud` コマンドは実行環境に不要

`gcloud` は**認証情報を発行するための道具**であって、
アプリの実行には要らない。game-server は Go の SDK が直接 HTTPS で
Gemini Enterprise Agent Platform を呼ぶだけで、`gcloud` を起動しない。

つまり **Raspberry Pi に Google Cloud SDK を入れる必要はない**。
JSONファイルを1つ置くだけでよい。

---

## 2. サービスアカウントキーの発行

**Cloud Console の画面だけで完結する** (`gcloud` 不要)。

### 2-1. API を有効化

<https://console.cloud.google.com/apis/library/aiplatform.googleapis.com>

プロジェクト `radio-terror` を選び、「有効にする」。

### 2-2. サービスアカウントを作る

<https://console.cloud.google.com/iam-admin/serviceaccounts>

「サービスアカウントを作成」で以下を入力する。

| 項目 | 値の例 |
|---|---|
| 名前 | `radio-terror-ai` |
| 説明 | RADIO TERROR の生成AI呼び出し用 |

**ロールは `Vertex AI User` (`roles/aiplatform.user`) だけ**を付ける。
これで `generateContent` (TTS・文字起こし・推論) が呼べる。
オーナーや編集者を付けてはいけない — 鍵が漏れた場合の被害が大きくなる。

### 2-3. 鍵 (JSON) をダウンロード

作ったサービスアカウントを開き、「キー」タブ →
「鍵を追加」→「新しい鍵を作成」→ **JSON** を選ぶ。

ダウンロードされるファイルはこういう内容になっている。

```json
{
  "type": "service_account",
  "project_id": "radio-terror",
  "private_key": "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----\n",
  "client_email": "radio-terror-ai@radio-terror.iam.gserviceaccount.com",
  ...
}
```

### 鍵の取り扱い

**`private_key` を持っている者は誰でもこのプロジェクトの Gemini Enterprise Agent Platform を
呼べる (= 課金が発生する)。** Gemini APIキーと同じ性質のもの。

- **絶対にコミットしない** (`app/secrets/` は gitignore 済み)
- Slack やチャットに貼らない
- 漏れた可能性があれば Console から即座に鍵を削除して再発行する

---

## 3. 鍵の配置

リポジトリ内の `app/secrets/` に置く。このディレクトリは
`.gitignore` 済みで、`README.md` 以外はコミットされない。

```bash
mkdir -p app/secrets
cp ~/Downloads/radio-terror-xxxxx.json app/secrets/geap-sa.json
chmod 600 app/secrets/geap-sa.json
```

Raspberry Pi へは scp などでコピーする。

```bash
scp app/secrets/geap-sa.json pi@raspberrypi.local:~/radio-terror/app/secrets/
```

---

## 4. 設定の切り替え

### game-server (`app/game-server/config.toml`)

**接続先は Gemini Enterprise Agent Platform のみ**。APIキーの項目は無い。

```toml
[gemini]
project  = "radio-terror"
location = "us-central1"

transcribe_model = "gemini-3.1-flash-lite"
reasoning_model  = "gemini-3.5-flash-lite"
tts_model        = "gemini-3.1-flash-tts-preview"
```

**`service_tier` は設定しない** (項目自体を削除済み)。Vertex/Enterprise は
どの値でも 400 を返すため、指定すると全API呼び出しが失敗する。
公式ドキュメントが「使える」と書いているのは Gemini Developer API の話で、
このプロジェクトの接続先ではない (`docs/adr.md` G-5)。

### crosstalk-gen (`app/crosstalk-gen/crosstalk.toml`)

接続先だけを書く (game-server と同じ)。

```toml
[defaults]
project  = "radio-terror"
location = "us-central1"
```

レート枠に余裕があるので、
`concurrency` / `request_interval_ms` を緩めてよい。

### 環境変数で鍵の場所を教える

SDK は `GOOGLE_APPLICATION_CREDENTIALS` を自動的に読む。
**設定ファイルに鍵のパスを書く必要はない。**

```bash
export GOOGLE_APPLICATION_CREDENTIALS=/absolute/path/to/geap-sa.json
```

### Docker Compose

`compose.yaml` の `game-server` に以下を追加する。

```yaml
services:
  game-server:
    environment:
      GOOGLE_APPLICATION_CREDENTIALS: /secrets/geap-sa.json
    volumes:
      - type: bind
        source: ./secrets/geap-sa.json
        target: /secrets/geap-sa.json
        read_only: true
```

`read_only: true` を付けること。コンテナ側から書き換えられないようにする。

---

## 5. 動作確認

まず crosstalk-gen で1件だけ生成して疎通を見る。
game-server より切り分けが簡単で、失敗しても影響がない。

```bash
cd app/crosstalk-gen
export GOOGLE_APPLICATION_CREDENTIALS=../secrets/geap-sa.json
go run . -only kuromaku -force
```

成功すると以下のように出る。

```
接続先: Gemini Enterprise Agent Platform (project=radio-terror location=us-central1)
レート制御: 間隔 4000ms / 並列 1 / リトライ上限 3回 / リクエスト上限 40回
[ok]    uneasy/kuromaku.ogg  4.2s  8.5KB  (2.1s)
APIリクエスト消費: 1回
```

通ったら全件生成する。

```bash
go run . -force
```

その後 game-server を起動する。

```bash
cd ../ && docker compose up game-server
```

---

## 6. トラブルシューティング

| 症状 | 原因と対処 |
|---|---|
| `could not find default credentials` | `GOOGLE_APPLICATION_CREDENTIALS` が未設定、またはパスが違う。**絶対パス**で指定する |
| `PERMISSION_DENIED` | サービスアカウントに `Vertex AI User` が付いていない |
| `aiplatform.googleapis.com is not enabled` | 手順 2-1 の API 有効化が済んでいない |
| `models/... is not found` | **そのリージョンでモデルが提供されていない**。`location` を変える (下記) |
| `[gemini] project が未設定です` | `config.toml` に `project`/`location` が無い。環境変数 `GOOGLE_CLOUD_PROJECT` でも可 |
| 起動直後の1回だけ応答が異常に遅い (6〜10秒) | **既知の現象で対処済み**。下記「起動直後だけ応答が遅い」参照 |

### 起動直後だけ応答が遅い (コールドスタート)

**2026-09-06 実測 (Raspberry Pi)。** プロセス起動後の1回目のリクエストだけ
Transcribe/TTS が数秒遅く、2回目以降は正常 (2〜3秒) に戻る。

まず疑うべきは DNS。特にルーターのIPv6 (AAAA) 転送が遅い環境で、
名前解決自体に数秒かかることがある (`docker compose exec game-server
getent ahosts aiplatform.googleapis.com` で計測できる)。これは
`compose.yaml` の `dns: [8.8.8.8, 1.1.1.1]` で対処済み。

DNSを直しても残る遅延は、`genai.Client` の内部初期化 (認証トークン
取得やコネクション確立) が初回リクエストにだけ乗るためと見ている。
**起動時に3経路 (Transcribe/Reasoning/TTS) をダミー呼び出しで温める
ウォームアップを実装済み** (`docs/adr.md` T-13)。起動ログの
`[boot] warmup complete: <秒数>` で所要時間を確認できる。数秒以上
かかっている場合は、ウォームアップ自体がまだコールドスタートの
影響を受けている (通常は2秒未満で終わる)。

### モデルの提供リージョン

**プレビュー版モデルは提供リージョンが限られる。**
現在使っている `gemini-3.1-flash-tts-preview` などは、
Gemini API と Gemini Enterprise Agent Platform で提供状況が異なることがある。

**実測結果 (2026-08-10、本プロジェクトのモデル構成)**

| location | TTS<br>`3.1-flash-tts-preview` | 推論/文字起こし<br>`3.5-flash-lite` / `3.1-flash-lite` |
|---|---|---|
| **`global`** | **OK** | **OK** |
| `us-central1` | OK | 404 |
| `asia-northeast1` | 404 | 404 |

**3モデルすべてが使えるのは `global` だけ**。`location = "global"` を使う。

東京 (`asia-northeast1`) はレイテンシの点では有利だが、
**プレビュー版モデルが1つも提供されていない**。会場のレイテンシを
優先したい場合は、安定版モデルへの変更とセットで再検証が必要。

モデル構成を変えたら、この表を実測で取り直すこと。

---

## 7. 課金について

Gemini Enterprise Agent Platform は従量課金。無料枠は限定的。

- **TTS が最もコストに効く**。ナビゲーターは本番中ずっと喋る
- **Priority ティアは使えない** (Vertex/Enterprise が `service_tier` を
  受け付けないため実装ごと削除済み。`docs/adr.md` G-5)。
  課金は標準ティアのみで、優先度による割増は発生しない
- 混線音声30ファイルは一度作れば終わり (再生成しない限り課金されない)
- 予期せぬ高額請求を防ぐため、**予算アラートの設定を推奨**する
  (<https://console.cloud.google.com/billing/budgets>)

crosstalk-gen には `max_requests` (1回の実行の上限) があり、
事故で大量リクエストが飛ぶのを防いでいる。

---

## 決定記録

| # | 決定 | 理由 |
|---|---|---|
| 1 | 接続先 (project/location) を設定ファイルで指定する | 環境ごと(開発・本番・会場)にプロジェクトを使い分けられる。環境変数へのフォールバックも用意した |
| 2 | 接続先は設定ファイルが環境変数より優先 | 設定ファイルを見れば接続先が確定する。環境変数は Docker などで上書きしたいとき用 |
| 3 | 認証はサービスアカウントキー (JSON) | GCE 外での運用のため。ユーザーADCは有効期限があり無人運用に向かない |
| 4 | 実行環境に `gcloud` を入れない | 鍵の発行は Cloud Console で完結する。Raspberry Pi に Google Cloud SDK を入れる必要はない |
| 5 | ロールは `Vertex AI User` のみ | 鍵が漏れた場合の被害を最小化する。オーナー・編集者は付けない |
| 6 | 鍵のパスは設定ファイルに書かず環境変数で渡す | SDK が `GOOGLE_APPLICATION_CREDENTIALS` を自動で読む。設定ファイルに秘密情報の所在を残さない |
| 7 | 鍵は `app/secrets/` に置き gitignore する | `config.toml` と同じ扱い。コミット事故を防ぐ |
| 8 | SDK 定数は `genai.BackendEnterprise` を使う | 正式名称に合わせる。SDK は内部で `BackendVertexAI` に変換するため挙動は同一 |
| 9 | Gemini API (APIキー認証) には対応しない | バックエンド切り替えの分岐を持たずシンプルに保つ。レート制限の厳しい Gemini API を本番で使う理由がない |
| 10 | `api_key` 設定項目を廃止 | ADC 認証のみになり不要。設定ファイルから秘密情報が1つ減った |
| 11 | `location = "global"` を使う | 実測で**3モデルすべてが使えるのは global のみ**。`us-central1` は TTS のみ、`asia-northeast1` は全て404。モデル構成を変えたら再検証する |
| 12 | ~~3経路すべてを Priority ティアで呼ぶ~~ **覆った** | 実測 (2026-08-20) で Vertex/Enterprise は `service_tier` を**どの値でも受け付けない**と判明 (400)。決定13〜15もろとも無効。2026-09-06 に実装ごと削除した |
| 13 | ~~`service_tier` は設定ファイルで切り替え可能にする~~ **覆った** | 切り替え先が無い。むしろ `service_tier` と `tts_service_tier` の**2つの逃がし口を作ったことが事故を生んだ** — 共通側を空に戻したとき TTS 側の `"priority"` が残り、TTS だけ全滅した |
| 14 | ~~不正な `service_tier` は起動時に落とす~~ **覆った** | 綴りが正しくても 400 になるため、検証しても意味がない |
| 15 | ~~未設定の `service_tier` は空文字で表す~~ **覆った** | 項目自体が無くなった。ただし `genai.ServiceTierUnspecified` の実体が文字列 `"unspecified"` で `omitempty` に落ちない性質は、**再導入時に踏み直す罠**として `docs/adr.md` G-5 に残してある |
| 16 | Interactions API へは移行しない (2026-08 時点) | **Go SDK に存在しない** (v1.68.0 の `genai.Client` に `Interactions` フィールドが無く、`client.Interactions.Create` はコンパイルが通らない)。かつ `v1beta2/interactions` は APIキー認証の Gemini Developer API 系で決定9と衝突する。`generateContent` は「レガシーだが完全にサポート」で廃止期限の告知も無い。追跡先は go-genai issue #658 (Open/P1) |

<!-- EOF -->
