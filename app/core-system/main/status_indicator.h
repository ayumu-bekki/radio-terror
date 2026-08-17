#ifndef STATUS_INDICATOR_H_
#define STATUS_INDICATOR_H_
// Core System
// (C)2026 bekki.jp
// 状態からフルカラーLEDの表示を決める (docs/game_session_design.md §4.1)

// Include ----------------------
#include "game_session.h"
#include "pl9823_task.h"

namespace CoreSystem {

/// フルカラーLED (kFullColorLed) の表示決定。
///
/// §4.1 の状態別表示そのもの。状態を書き換えず、状態から色・点滅を導くだけなので
/// 状態機械から切り離してある。表示仕様を変えるときはここだけを見ればよい。
namespace StatusIndicator {

/// フルカラーLEDの明るさ (§4.1)
///
/// 常時点いている状態 (Setup の復旧待ち・Ready の待機) は会場で眩しいため
/// 落とす。**ゲームの見せ場は落とさない** — Playing の緊張感、破裂前後の警告、
/// 解除成功の達成感は明るさも含めて演出のうち。
inline constexpr uint8_t kBrightness = 128;

/// 待機中 (Setup / Ready) の明るさ。通常の20%。
inline constexpr uint8_t kDimBrightness = kBrightness / 5;

/// Dim は色を待機中の明るさへ落とす。
/// 起動インジケータの紫のように、色ごとに強さが違う値を一律で絞る。
inline constexpr uint8_t Dim(uint8_t value) {
  return static_cast<uint8_t>(value / 5);
}

/// Playing中の点滅 (§4.1)。
///
/// **短く光って長く消える**「パッ … パッ」のリズムから始め、
/// 残り時間に応じて段階的に速くする。点灯時間は一定 (100ms) にして
/// 消灯時間だけを詰めるため、加速が素直に伝わる。
///
/// 序盤から速く点滅させると「はじめから爆発しそう」に見えてしまうので、
/// 残り1分を切るまでは落ち着かせておく。
inline constexpr uint32_t kPlayingBlinkOnMs = 100;

/// 点滅の1段階。
struct BlinkStage {
  /// **この残り時間より多い**間、下の off_ms を使う
  int32_t above_ms;
  /// 消灯時間
  uint32_t off_ms;
};

/// 残り時間ごとの点滅間隔 (残り時間の多い順)。
///
/// | 残り | 点灯 | 消灯 | 周期 |
/// |---|---|---|---|
/// | 60秒超 | 100ms | 1900ms | 2.0秒 |
/// | 60秒以下 | 100ms | 1400ms | 1.5秒 |
/// | 30秒以下 | 100ms | 900ms | 1.0秒 |
/// | 10秒以下 | 100ms | 400ms | 0.5秒 |
inline constexpr BlinkStage kPlayingBlinkStages[] = {
    {60000, 1900},
    {30000, 1400},
    {10000, 900},
    {0, 400},
};

/// 残り時間に対応する消灯時間を返す。
///
/// 表の上から順に「この残り時間より多いか」を見て、最初に該当した段階を使う。
/// どれにも該当しない (残り0秒付近) 場合は**最後の段階**、つまり最も速い
/// 点滅になる。ここで先頭の段階へ戻すと、時間切れ寸前に急に遅くなってしまう。
inline constexpr uint32_t PlayingBlinkOffMs(int32_t remaining_ms) {
  constexpr size_t kStageNum = sizeof(kPlayingBlinkStages) / sizeof(BlinkStage);

  for (const BlinkStage& stage : kPlayingBlinkStages) {
    if (stage.above_ms < remaining_ms) {
      return stage.off_ms;
    }
  }
  return kPlayingBlinkStages[kStageNum - 1].off_ms;
}

/// Setup中の黄点滅の周期
inline constexpr uint32_t kSetupBlinkMs = 500;

/// Pending中の青点滅の周期 (§4.2)
///
/// Setup の黄点滅より**速く**する。どちらも「待っている」状態だが、
/// Pending は数秒で終わるので、動きの速さで「もうすぐ始まる」を伝える。
inline constexpr uint32_t kPendingBlinkMs = 250;

/// 起動インジケータの色 (フルカラーLED)
struct BootColor {
  uint8_t r;
  uint8_t g;
  uint8_t b;
};

/// 初期化中を示す紫。電源投入直後から点灯する。
///
/// **サーバーへ接続できるまで点灯し続ける** (§4.0)。Setup の黄点滅は
/// 「配線を復旧してほしい」という現場への合図なので、まだ受け付けられない
/// 段階で出すと運営を惑わせる。
inline constexpr BootColor kBootColorInitializing = {80, 0, 120};

/// 初期化失敗を示す紫。点滅で「初期化中」と区別する
/// (同じ色の点灯では成功/失敗が見分けられないため)。
inline constexpr BootColor kBootColorFailed = {120, 0, 160};

/// 起動インジケータの点滅周期
inline constexpr uint32_t kBootBlinkMs = 300;

/// Setup中にサーバーを待っている間の表示 (§4.0)。
///
/// wifi_failed が真なら紫点滅 (接続に失敗した)、偽なら紫点灯 (接続待ち)。
/// 同じ色の点灯では成功/失敗が見分けられないため、失敗は点滅で示す。
inline Pl9823Task::Command MakeWaitingCommand(bool wifi_failed) {
  const BootColor& color = wifi_failed ? kBootColorFailed : kBootColorInitializing;

  // サーバー待ちは常時点いているため明るさを落とす
  Pl9823Task::Command command;
  command.pattern = wifi_failed ? Pl9823Task::PATTERN_BLINK : Pl9823Task::PATTERN_SOLID;
  command.r = Dim(color.r);
  command.g = Dim(color.g);
  command.b = Dim(color.b);
  command.on_ms = kBootBlinkMs;
  command.off_ms = kBootBlinkMs;
  return command;
}

/// 状態に対応するフルカラーLEDのコマンドを組み立てる。
///
/// remaining_ms は Playing中の残り時間 (点滅の速さを決める)。
/// ws_connected / wifi_failed は Setup 以外では無視される。
inline Pl9823Task::Command MakeCommand(GameState state, int32_t remaining_ms,
                                       bool ws_connected, bool wifi_failed) {
  Pl9823Task::Command command;

  if (state == STATE_SETUP) {
    if (!ws_connected) {
      // サーバーへ接続できるまでは起動インジケータを引き継ぐ (§4.0)。
      // この段階では Ready へ遷移できないため、復旧の合図 (黄) を出さない。
      return MakeWaitingCommand(wifi_failed);
    }
    // 黄点滅 (配線の復旧待ち)。長く点きっぱなしになるので暗くする
    command.pattern = Pl9823Task::PATTERN_BLINK;
    command.r = kDimBrightness;
    command.g = kDimBrightness;
    command.b = 0;
    command.on_ms = kSetupBlinkMs;
    command.off_ms = kSetupBlinkMs;
    return command;
  }

  if (state == STATE_READY) {
    // 青点灯。開始を待つ間ずっと点いているので暗くする
    command.pattern = Pl9823Task::PATTERN_SOLID;
    command.b = kDimBrightness;
    return command;
  }

  if (state == STATE_PENDING) {
    // 青点滅。開始申告が通り、ナビゲーターの応答を待っている段階 (§4.2)。
    //
    // Ready (青点灯) の延長として「青が動いている = もうすぐ始まる」と読ませる。
    // Setup の黄点滅とは色で、Ready とは点滅の有無で区別できる。
    // まだ始まっていないので明るさは Ready と揃えて落としておく。
    command.pattern = Pl9823Task::PATTERN_BLINK;
    command.b = kDimBrightness;
    command.on_ms = kPendingBlinkMs;
    command.off_ms = kPendingBlinkMs;
    return command;
  }

  if (state == STATE_PLAYING) {
    // 赤の短発点滅。残り時間が減るほど間隔が詰まる (§4.1)。
    // 緊張感を出す場面なので明るさは落とさない
    command.pattern = Pl9823Task::PATTERN_BLINK;
    command.r = kBrightness;
    command.on_ms = kPlayingBlinkOnMs;
    command.off_ms = PlayingBlinkOffMs(remaining_ms);
    return command;
  }

  if (state == STATE_DETONATING || state == STATE_EXPLODED) {
    // 赤点灯。破裂前後の警告なので落とさない
    command.pattern = Pl9823Task::PATTERN_SOLID;
    command.r = kBrightness;
    return command;
  }

  if (state == STATE_DEFUSED) {
    // 緑点灯。解除成功の見せ場なので落とさない
    command.pattern = Pl9823Task::PATTERN_SOLID;
    command.g = kBrightness;
    return command;
  }

  return command;
}

}  // namespace StatusIndicator

}  // namespace CoreSystem

#endif  // STATUS_INDICATOR_H_
// EOF
