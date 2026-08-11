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

/// Playing中の点滅間隔 (通常時)
inline constexpr uint32_t kPlayingBlinkOnMs = 50;
inline constexpr uint32_t kPlayingBlinkOffMs = 950;

/// Playing中の点滅間隔 (終盤警告時。点滅が速くなる)
inline constexpr uint32_t kPlayingHurryBlinkOnMs = 50;
inline constexpr uint32_t kPlayingHurryBlinkOffMs = 250;

/// Setup中の黄点滅の周期
inline constexpr uint32_t kSetupBlinkMs = 500;

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
/// hurry は Playing中に残り時間が僅少かどうか (点滅を加速する)。
/// ws_connected / wifi_failed は Setup 以外では無視される。
inline Pl9823Task::Command MakeCommand(GameState state, bool hurry, bool ws_connected,
                                       bool wifi_failed) {
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

  if (state == STATE_PLAYING) {
    // 赤の短発点滅 (残り時間僅少で点滅加速)。緊張感を出すので落とさない
    command.pattern = Pl9823Task::PATTERN_BLINK;
    command.r = kBrightness;
    command.on_ms = hurry ? kPlayingHurryBlinkOnMs : kPlayingBlinkOnMs;
    command.off_ms = hurry ? kPlayingHurryBlinkOffMs : kPlayingBlinkOffMs;
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
