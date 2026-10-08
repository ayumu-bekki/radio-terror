# USBオーディオのカードID固定(radio-bridge ホスト設定)

radio-bridge は bridge ごとに USB オーディオを1本ずつ使う。
同型品を複数挿すと、ALSA のカード番号(`hw:0,0` / `hw:2,0` など)が**挿す順・再起動・抜き差しで値が変わってしまう**。
そこで udev ルールで **USBシリアルごとにカードIDを固定**し、
`compose.yaml` ではカード番号ではなくカードIDで指定する。

## 対応表

| bridge | カードID | compose.yaml の指定 | PTT GPIO (BCM) |
|---|---|---|---|
| BR01 | `SC_BR01` | 入力 `hw:SC_BR01,0` / 出力 `plughw:SC_BR01,0` | 26 |
| BR02 | `SC_BR02` | 入力 `hw:SC_BR02,0` / 出力 `plughw:SC_BR02,0` | 16 |

- 設定箇所は `app/compose.yaml` の `RADIO_BRIDGE_INPUT_DEVICE` / `RADIO_BRIDGE_OUTPUT_DEVICE`。
  環境変数が `radio-bridge/config.toml` の `input_device` / `output_device` より優先される。
- `config.toml` 側の値は環境変数が無いときのフォールバック(現在は `hw:0,0` / `plughw:0,0`)。
- カードIDは **英数字とアンダースコアのみ、15文字以内**。

## 手順

### 1. シリアルを調べる

```bash
lsusb -v 2>/dev/null | grep -E "Bus|iSerial|iProduct"
```

個体ごとに違う値が出ればよい(今回は2本とも異なっていた)。
**同じ値が出る個体では、この方法は使えない**(別の識別子、たとえば挿すUSBポートで区別する)。

### 2. udev ルールを作る

```bash
sudo nano /etc/udev/rules.d/90-usb-audio-names.rules
```

```
SUBSYSTEM=="sound", KERNEL=="card[0-9]*", SUBSYSTEMS=="usb", ATTRS{serial}=="シリアルA", ATTR{id}="SC_BR01"
SUBSYSTEM=="sound", KERNEL=="card[0-9]*", SUBSYSTEMS=="usb", ATTRS{serial}=="シリアルB", ATTR{id}="SC_BR02"
```

`シリアルA` / `シリアルB` は手順1で得た各個体の値に置き換える。

### 3. 反映する

```bash
sudo udevadm control --reload
```

その後 **USB を抜き差しする**。既存のカードは再接続しないと反映されない。

### 4. 確認

```bash
cat /proc/asound/cards
```

`[SC_BR01 ]` `[SC_BR02 ]` が出れば完了。`aplay -l` / `arecord -l` でも同じ名前で見える。

### 5. プログラムからの指定

```
hw:SC_BR01,0        # 録音(入力)
plughw:SC_BR01,0    # 再生(出力)
```

## 3本目以降を足すとき

シリアルを調べて、ルールに `SC_BR03` の行を追加するだけ。
あわせて `compose.yaml` に `radio-bridge-br03` を足す(`RADIO_BRIDGE_ID`・カードID・PTT GPIO を bridge ごとに変える)。

## デバイスが無いとき・抜けたとき

radio-bridge は入力デバイスを**5秒ごとに開き直し**、開けている間だけ game-server へ接続する
(`bridge_connection_design.md` 決定15)。

- 起動時に無い: ログに `audio input unavailable ... retry_secs=5` と
  `audio input not available, waiting before connecting` が出る。挿すと自動で接続する。
- 動作中に抜けた: `alsa capture error` のあと切断される。マネージャー画面からは
  その bridge が未接続になる。挿し直せば自動で復帰する(再起動不要)。
- 再生側は都度 open するので、入力が戻れば再生も戻る。
- **`compose.yaml` で `/dev/snd` を bind mount している**。`privileged: true` の `/dev` は
  起動時のスナップショットで、抜き差し後の新しい `/dev/snd/*` がコンテナに現れず、
  ホストでは見えているのにコンテナの ALSA が `Cannot get card index for SC_BR01` /
  `No such device (19)` を返し続ける(実機で確認)。外すと再接続が検知できなくなる。

## トラブルシュート

| 症状 | 原因と対処 |
|---|---|
| `Device or resource busy` | PipeWire がデバイスを掴んでいる。WirePlumber で対象デバイスに `device.disabled = true` を設定して外す |
| 抜き差ししてもカードIDが変わらない | ルールのシリアルが `lsusb` の値と一致しているか確認。`udevadm control --reload` を忘れていないか確認 |
| 起動したが音が出ない・録音されない | `docker compose logs radio-bridge` で ALSA のエラーを見る。`aplay -l` のカード名と compose の指定が合っているか確認 |
| 別の bridge の音が出る | カードIDとシリアルの対応が逆。ルールのシリアルを見直す |

## 決定記録

- **2026-10-08 カード番号ではなくカードIDで指定する**:
  同型のUSBオーディオを2本以上使うため、カード番号は挿し順で入れ替わる。
  USBシリアルは個体ごとに異なる(2本で確認)ので、これをキーに `ATTR{id}` を固定した。
  カード番号の指定(`hw:0,0` / `hw:2,0`)は、ホストを替えるたびに実機確認が要り、取り違えに気づきにくい。
