# crosstalk-gen

混線音声アセット (`docs/operation_flow.md` §5.1) を Gemini TTS で一括生成する。

接続先は **Gemini Enterprise Agent Platform** (旧 Vertex AI)。認証は ADC のため、
設定はすべて `crosstalk.toml` の1ファイルにあり、**秘密情報を一切含まない**。
出力は Ogg Opus で、`game-server` がそのまま読める形式・ファイル名で書き出される。

## 使い方

```bash
cd app/crosstalk-gen

# まず中身を確認する (APIを呼ばない)
go run . -list
go run . -dry-run

# 認証 (どちらか一度だけ)
gcloud auth application-default login
export GOOGLE_APPLICATION_CREDENTIALS=../secrets/geap-sa.json

# 生成
go run . -out ../game-server/assets/crosstalk
```

認証の準備は `docs/gemini_enterprise_setup.md` を参照。

`-out` の既定値が `../game-server/assets/crosstalk` なので、通常は省略できる。

## 設定を変えて試す

`crosstalk.toml` を編集して再実行する。`skip_existing = true` の間は
既存ファイルを飛ばすので、**作り直したいものだけ `-only` で指定する**のが速い。

```bash
# 1つだけ声を変えて試す
go run . -only sasayaki_C -force

# 邪魔者系だけ全部作り直す
go run . -category jamming -force

# 全部作り直す
go run . -force
```

## オプション

| フラグ | 既定値 | 説明 |
|---|---|---|
| `-config` | `crosstalk.toml` | 設定ファイル |
| `-out` | `../game-server/assets/crosstalk` | 出力先 |
| `-only` | (なし) | 名前で絞る (カンマ区切り)。存在しない名前はエラーになる |
| `-category` | (なし) | `jamming` / `ambient` / `uneasy` で絞る |
| `-dry-run` | false | APIを呼ばず、組み立てたプロンプトを表示 |
| `-list` | false | 対象一覧 (ファイル名・voice・model) を表示 |
| `-force` | false | 既存ファイルがあっても再生成 |

## 設定ファイルの構造

### `[defaults]`

省略時に使われる `model` / `voice` と、出力・実行の設定。

| キー | 説明 |
|---|---|
| `model` / `voice` | 各発話で省略したときの既定値 |
| `opus_bitrate` | Opus のビットレート。16000 で無線らしい帯域感になる |
| `keep_wav` | true にすると 24kHz WAV も残す (聴き比べ用) |
| `concurrency` | 並列生成数 |
| `max_retries` | 失敗時のリトライ回数 |
| `project` / `location` | 接続先 (必須) |
| `skip_existing` | 既存 `.ogg` を飛ばす。`-force` で無視できる |
| `scene` | **全パターン共通**の状況設定 |

### 発話の定義

`[[jamming]]` / `[[ambient]]` / `[[uneasy]]` の配列で定義する。

```toml
[[ambient]]
name    = "chushajo"      # → ambient/chushajo.ogg
role    = "駐車場誘導員"   # ログ表示用
context = "20代の男性誘導員。事務的で早い"   # Sample Context
text    = "こちら第1駐車場、ただいま満車になりました。…"
voice   = "Puck"          # 省略すると defaults.voice
model   = "..."           # 省略すると defaults.model
```

**`model` と `voice` は発話ごとに指定できる**。特定の1つだけ別モデルで
試したいときは、その `[[...]]` に `model` を足すだけでよい。

### Scene と Sample Context の役割分担

`scene` は全パターン共通の状況設定、`context` は個々の話者・口調。
**両方に同じ内容を書くと指示が競合して効きが弱くなる**ため分けている。

組み立てられるプロンプトは `-dry-run` で確認できる。

### 非言語タグ

`text` の中に `[breathless]` のようなタグを書くと、文中のその位置から
話し方が変わる。`context` が「誰が・どんな声か」を決めるのに対し、
タグは**文中のどこで**声を張るか・間を置くかを決める。役割が違うので併用する。

```toml
text = "[breathless] 今すぐ${color}を切らないとまずい![shouting] はやく!"
```

タグは英語で書く(日本語のト書きは読み上げられる事故がある)。
**「`[...]` は演技の指示です。声に出して読まず…」という一文は
ツールがプロンプトに自動で付ける**ので、TOML 側に書く必要はない。

括弧の閉じ忘れ・全角の `［］`・タグだけで本文が空になる定義は
`go test` で検出される (読み上げ事故になるため)。

### `${color}` の展開 (jamming のみ)

`[[jamming]]` の `text` に `${color}` を書くと `[colors]` の5色に展開され、
`{name}_{記号}.ogg` が生成される (1定義 → 5ファイル)。

```toml
[colors]
A = "赤"
B = "黄"
# ...

[[jamming]]
name = "aserase"
text = "今すぐ${color}を切らないとまずい!はやく!"
# → aserase_A.ogg 〜 aserase_E.ogg
```

### `ambient_base`

全 `[[ambient]]` の `context` に前置きされる共通文。
話者の一行だけを各定義に書けばよくなる。

## 接続先の設定

`crosstalk.toml` の `[defaults]` に書く。

```toml
[defaults]
project  = "radio-terror"
location = "us-central1"
```

環境変数 `GOOGLE_CLOUD_PROJECT` / `GOOGLE_CLOUD_LOCATION` でも指定できるが、
**設定ファイルの値が優先される**。どちらも無ければ起動時にエラーになる。

認証は ADC (Application Default Credentials)。APIキーは使わない。

```bash
# ローカル
gcloud auth application-default login

# サービスアカウント (Raspberry Pi など常設マシン)
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/sa.json
```

**モデルの提供リージョンに注意。** TTS モデルは提供リージョンが
限られることがある。`is not found` が出たら `location` を変えて試す。

## レート制限に注意

Enterprise はレート制限が緩いが、事故で枠を使い切らないよう
以下の安全策を持つ。

| 設定 | 既定値 | 役割 |
|---|---|---|
| `concurrency` | 1 | 並列に投げると一気に枠を消費し 429 を誘発する |
| `request_interval_ms` | 4000 | 各リクエストの最小間隔。RPM 制限に合わせる |
| `max_retries` | 3 | 400 対策の最小限 |
| `max_requests` | 40 | **1回の実行で投げる上限**。事故時の安全弁 |

実行前に見込みが表示され、終了時に実際の消費数が出る。

```
レート制御: 間隔 4000ms / 並列 1 / リトライ上限 3回 / リクエスト上限 40回
最短所要: 約2m0s
...
APIリクエスト消費: 32回
```

**認証エラー・モデル名の誤りはリトライせず即停止する。** 回復しない
エラーで枠を溶かさないため。

### 枠を使い切ったら

時間を置く。`skip_existing = true` なので、**再実行すれば成功済みのファイルは
飛ばして続きから**生成される。少しずつ進めるのが安全。

```bash
go run . -category ambient   # 13件ずつなど小分けに
```

### 散発的な 400 について

TTS API は同一リクエストでも `400 INVALID_ARGUMENT` を返すことがある。
プロンプトの内容・長さ・改行とは**無関係**で、投げ直せば通る
(実測: 同一プロンプト15回中3回成功)。`max_retries` で吸収するが、
連続して失敗する場合は時間を置く。

## 注意点

**ファイル名がずれてもサーバーはエラーを出さない。** アセット未制作でも起動できる
設計のため、規約から外れたファイルは無言でスキップされる。配置後は
サーバー起動ログで件数を確認する。

```
[crosstalk] loaded assets: jamming=3 ambient=13 uneasy=2
```

**`uneasy_bessgenba` は名前固定。** `crosstalk.go` の `crosstalkEventFile` が
名前で参照し、他チームの `exploded` を契機にイベント駆動再生される。

**`[[jamming]]` の `name` に `_` は使えない。** サーバーが最後の `_` で
色サフィックスを切り出すため、名前に `_` があると解釈がずれる (起動時に検証される)。

## 生成後

後処理 (ノイズ付与など) は行わない方針。
そのまま `game-server` が読める形式で出力される。
