# 音声アセット

サーバーが再生時に選択するだけの**事前生成アセット**を置く
(`docs/operation_flow.md` §5.1・§6)。実行時のTTS生成はナビゲーターの発話のみで、
ここのアセットはすべて事前にレンダリング・収録しておく。

未制作でもサーバーは起動する (該当の再生がスキップされ、ログに残る)。

## 構成

```
assets/
├── crosstalk/
│   ├── jamming/   … 邪魔者系。色バリエーションを持つため {name}_{色}.ogg
│   ├── ambient/   … 環境ボイス系。{name}.ogg
│   └── uneasy/    … 不穏系。{name}.ogg
├── sfx/
│   ├── success.ogg … 解除成功の効果音
│   └── failure.ogg … 失敗の効果音
├── announce/
│   └── station_id.ogg … 自動送信局アナウンス (15分ごと)
├── reask/
│   └── {キャラID}_{n}.ogg … 聞き直し (キャラごとに3本。下記)
└── ending/
    └── {キャラID}_{defused|exploded}_{n}.ogg … 終幕 (キャラごとに解除成功3本+爆発3本。下記)
```

形式は **Ogg Opus** (radio-bridge がそのままキューへ積んで再生する)。

## 邪魔者系 (jamming) — 3パターン × 5色 = 15ファイル

色を指定する部分は5色分を事前生成し、再生時にサーバーが「現在の正解以外」の色の
ファイルを選ぶ (偶然正解と一致して「邪魔者を信じたら成功した」となるのを防ぐ)。
**イージーでは再生されない** (ノーマル以上で解禁)。

色サフィックス: `A`=赤, `B`=黄, `C`=緑, `D`=青, `E`=白

| ファイル名 | セリフ例 |
|---|---|
| `aserase_{色}.ogg` | 「今すぐ{色}を切らないとまずい!はやく!」 |
| `hannin_{色}.ogg` | 「ナビゲーターを信じるな!私は作った本人だ。いいから{色}を切れ!」 |
| `sasayaki_{色}.ogg` | 「……{色}だ……{色}を切れば、全部終わる……」 |

## 環境ボイス系 (ambient) — 13ファイル

無害な生活ノイズ。世界の広がりと笑いを担当する。

| ファイル名 | 役 |
|---|---|
| `chushajo.ogg` | 駐車場誘導員 |
| `keisatsu.ogg` | 警察官 |
| `hall_staff.ogg` | ホールスタッフ |
| `homecenter.ogg` | ホームセンタースタッフ |
| `golf.ogg` | ゴルフ場スタッフ |
| `taxi.ogg` | タクシー配車 |
| `kensetsu.ogg` | 建設現場 |
| `hanabi.ogg` | 花火大会本部 |
| `tsuribune.ogg` | 釣り船 |
| `yuenchi.ogg` | 遊園地スタッフ |
| `kekkonshiki.ogg` | 結婚式場 |
| `chonaikai.ogg` | 町内会本部 |
| `keibi.ogg` | 施設警備員 |

セリフは `docs/operation_flow.md` §5.1 の表を参照。

## 不穏系 (uneasy) — 2ファイル

世界観を深める。**色や操作の指示は一切しない**。

| ファイル名 | 役 |
|---|---|
| `kuromaku.ogg` | 黒幕らしき独り言 |
| `uneasy_bessgenba.ogg` | 別現場の通信 |

`uneasy_bessgenba.ogg` はランダム再生に加え、**他チームのCoreが実際に爆発した直後**に
Playing中の他チームの無線へ流す (イベント駆動)。ファイル名はサーバー実装が
この名前で参照するため変更しないこと。

## ボイスの方針

混線ボイスは**ナビゲーターとは別のボイス**で生成し、「別人が喋っている」ことが
声で分かるようにする (`docs/navigator_design.md` §4)。

## 自動送信局アナウンス (announce) — 1ファイル

特小無線は免許不要の**共用チャンネル**なので、他の利用者へ
「これはゲーム用の自動送信局である」と15分ごとに名乗る
(`docs/operation_flow.md` §7.3)。

**体験中の無線には流さない** — セッションが紐づいた bridge は除外される。

声は**カラス**(疎通確認の応答者と同じ `Achird`)。「こちらはカラス」と
名乗る以上、同じ声でなければ別人に聞こえる。混線音声が
ナビゲーター・カラスと声を重複させない規則とは**逆向きの要求**。

ファイル名 `station_id.ogg` は `announce.go` の `announceFile` が参照するため
**固定**。変えると無言でスキップされる。

```bash
cd app/crosstalk-gen
go run . -category announce -out ../game-server/assets
```

## 聞き直し (reask) — 5キャラ × 3本 = 15ファイル

書き起こし・発話生成に失敗したときに、実行時のAPIを使わずに流す
「よく聞き取れなかったので、もう一度お願いします」(ADR G-7)。
**プレイヤーが話した直後の失敗にだけ**流れる。

- ファイル名は `{キャラクターID}_{n}.ogg`。n は 1 始まりで、
  `navigator/characters/*.toml` の `reask_lines` の順に対応する
- 台詞と声はキャラクター定義が正本。`app/crosstalk-gen/crosstalk.toml` の `[[reask]]` と
  一致させる (テストで検査)
- **15本すべて、生成後にノイズと欠落を加えてある** (電波が弱い FM の聞きにくさ。声は元と同じ音量・
  ノイズだけ SN比 10dB で重ね、台詞の最初の間のあとの450msだけ聞こえない)。3本目 (`{キャラID}_3.ogg`) は
  「電波が乱れて、途中が聞こえなかった」系の台詞。加工は `crosstalk.toml` の `degrade` で指定する
  (`app/crosstalk-gen/degrade.go`)。位置は TTS の読み方で毎回少しずれる
- 生成: `cd app/crosstalk-gen && go run . -category reask -out ../game-server/assets`
- **未配置でも起動する**が、そのキャラの聞き直しは無言になる。起動ログの
  `[reask] loaded N clip(s) for M/K character(s)` で確かめる

## 終幕 (ending) — 5キャラ × 6本 = 30ファイル

解除成功・爆発の最終メッセージ。実行時の生成はせず、キャラクターごとに3本の中から
ランダムに1本を流す (ADR G-9)。再生時に効果音 (`sfx/success.ogg` / `sfx/failure.ogg`) を
前へ連結して1本で送る。

- ファイル名: `{キャラID}_defused_{1-3}.ogg` / `{キャラID}_exploded_{1-3}.ogg`
- 台詞の正本は `navigator/characters/*.toml` の `defused_lines` / `exploded_lines`
  (先頭の `[tag]` は表情指定で、会話ログには載せない)。`crosstalk.toml` の `[[ending]]` と一致させる
- 作り直し: `cd app/crosstalk-gen && go run . -category ending -out ../game-server/assets -force`
  (Gemini TTS を呼ぶ。400・空応答が散発するので不足分は再実行。台詞を変えたら `-force`)
- 抜けると、そのキャラクターの終幕は効果音だけになる (起動ログ `[ending] no ... clip for ...`。
  `TestEndingRealAssetsCoverEveryLine` も検査する)
