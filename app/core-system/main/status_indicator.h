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

/// 色 (0-255の比率) に明るさを掛けて実際の出力値にする。
///
/// 例: 紫 {170, 0, 255} に明るさ 8 を掛けると (5, 0, 8)。
/// 255 で割るので、**どの色でも「その明るさ」に揃う**。
inline constexpr uint8_t Scale(uint8_t component, uint8_t brightness) {
  return static_cast<uint8_t>(component * brightness / 255);
}


/// Playing中の点滅 (§4.1)。
///
/// **短く光って長く消える**「パッ … パッ」のリズムから始め、
/// 残り時間に応じて段階的に速くする。点灯時間は一定 (50ms) にして
/// 消灯時間だけを詰めるため、加速が素直に伝わる。
///
/// 序盤から速く点滅させると「はじめから爆発しそう」に見えてしまうので、
/// 残り1分を切るまでは落ち着かせておく。
inline constexpr uint32_t kPlayingBlinkOnMs = 50;

/// 点滅の1段階。
struct BlinkStage {
  /// **この残り時間より多い**間、下の off_ms を使う
  int32_t above_ms;
  /// 消灯時間
  uint32_t off_ms;
};

/// 残り時間ごとの点滅間隔 (残り時間の多い順)。
///
/// 点灯を 50ms へ縮めた分、消灯を 50ms 伸ばして**周期は据え置き**にしてある。
/// 加速のリズム (2.0→1.5→1.0→0.5秒) を変えずに、閃光を鋭くするため。
///
/// | 残り | 点灯 | 消灯 | 周期 |
/// |---|---|---|---|
/// | 60秒超 | 50ms | 1950ms | 2.0秒 |
/// | 60秒以下 | 50ms | 1450ms | 1.5秒 |
/// | 30秒以下 | 50ms | 950ms | 1.0秒 |
/// | 10秒以下 | 50ms | 450ms | 0.5秒 |
inline constexpr BlinkStage kPlayingBlinkStages[] = {
    {60000, 1950},
    {30000, 1450},
    {10000, 950},
    {0, 450},
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

/// フルカラーLEDの色 (0-255の比率。明るさは Scale() で別に掛ける)
struct Color {
  uint8_t r;
  uint8_t g;
  uint8_t b;
};

/// 色と明るさをコマンドへ書き込む。**色の指定はすべてここを通す。**
inline void ApplyColor(Pl9823Task::Command& command, const Color& color,
                       uint8_t brightness) {
  command.r = Scale(color.r, brightness);
  command.g = Scale(color.g, brightness);
  command.b = Scale(color.b, brightness);
}

/// 状態ごとの表示 (§4.1)。**色と明るさを1組で持つ。**
///
/// 見え方は色によって大きく変わる (青は低階調でも目立ち、紫は視感度が低い)。
/// 明るさを共通の定数で束ねると、片方を直したときにもう片方が巻き添えになる。
/// **状態ごとに独立して調整できる**よう、ここで1つずつ定義する。
struct Appearance {
  Color color;
  uint8_t brightness;
};

/// 配線の復旧待ち (黄点滅)。長く点きっぱなしなので暗く。
inline constexpr Appearance kLookSetup = {{255, 255, 0}, 8};

/// 開始待ち (青点灯)。常時点灯かつ青は目立つので暗く。
inline constexpr Appearance kLookReady = {{0, 0, 255}, 8};

/// 開始申告後の待機 (青点滅)。Ready と揃える。
inline constexpr Appearance kLookPending = {{0, 0, 255}, 8};

/// カウントダウン中 (赤点滅)。
/// **50ms しか光らない短い閃光**なので、点きっぱなしの状態より強くする。
inline constexpr Appearance kLookPlaying = {{255, 0, 0}, 192};

/// 破裂前後の警告 (赤点灯)。見せ場なので落とさない。
inline constexpr Appearance kLookDanger = {{255, 0, 0}, 128};

/// 解除成功 (緑点灯)。達成感の見せ場なので落とさない。
inline constexpr Appearance kLookDefused = {{0, 255, 0}, 128};

/// Appearance をそのままコマンドへ書き込む。
inline void ApplyLook(Pl9823Task::Command& command, const Appearance& look) {
  ApplyColor(command, look.color, look.brightness);
}

/// 初期化中を示す紫 (点灯)。電源投入直後から光る。
///
/// **サーバーへ接続できるまで点灯し続ける** (§4.0)。Setup の黄点滅は
/// 「配線を復旧してほしい」という現場への合図なので、まだ受け付けられない
/// 段階で出すと運営を惑わせる。
///
/// 紫は視感度が低く、同じ明るさでも他の色より暗く見える。
/// 見えにくければ**ここの明るさだけ**を上げる。
inline constexpr Appearance kLookBootInitializing = {{170, 0, 255}, 8};

/// 初期化失敗を示す紫 (点滅)。
///
/// **区別は点滅の有無で付ける**。低い明るさでは色の微差が出力値に残らない
/// (170/255 も 190/255 も、明るさ8では同じ値に丸まる) ため、
/// 色を変えても見分けられない。初期化中と同じ見た目にして点滅で区別する。
inline constexpr Appearance kLookBootFailed = kLookBootInitializing;

/// 起動インジケータの点滅周期
inline constexpr uint32_t kBootBlinkMs = 300;

/// 破裂の瞬間の閃光 (白点灯)。**最高光度**で焚く。
///
/// ソレノイド駆動と同時に光らせ、風船が割れる瞬間を視覚でも打ち込む。
/// 赤点灯 (kLookDanger) の延長では「破裂した」ことが伝わらないため、
/// **状態表示から外れた色**として白を使う。
inline constexpr Appearance kLookBurst = {{255, 255, 255}, 255};

/// 閃光の点灯時間。長いと「白点灯の状態」に見えてしまう
inline constexpr uint32_t kBurstFlashMs = 100;

/// 破裂の閃光コマンドを組み立てる。
///
/// 状態から導けない**一瞬の演出**なので MakeCommand とは分けてある。
/// 消灯は呼び出し側 (FireSolenoid) が Exploded 遷移で上書きして行う。
inline Pl9823Task::Command MakeBurstFlashCommand() {
  Pl9823Task::Command command;
  command.pattern = Pl9823Task::PATTERN_SOLID;
  ApplyLook(command, kLookBurst);
  return command;
}

/// Setup中にサーバーを待っている間の表示 (§4.0)。
///
/// wifi_failed が真なら紫点滅 (接続に失敗した)、偽なら紫点灯 (接続待ち)。
/// 同じ色の点灯では成功/失敗が見分けられないため、失敗は点滅で示す。
inline Pl9823Task::Command MakeWaitingCommand(bool wifi_failed) {
  const Appearance& look = wifi_failed ? kLookBootFailed : kLookBootInitializing;

  Pl9823Task::Command command;
  command.pattern = wifi_failed ? Pl9823Task::PATTERN_BLINK : Pl9823Task::PATTERN_SOLID;
  ApplyLook(command, look);
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
    ApplyLook(command, kLookSetup);
    command.on_ms = kSetupBlinkMs;
    command.off_ms = kSetupBlinkMs;
    return command;
  }

  if (state == STATE_READY) {
    // 青点灯。開始を待つ間ずっと点いているので暗くする
    command.pattern = Pl9823Task::PATTERN_SOLID;
    ApplyLook(command, kLookReady);
    return command;
  }

  if (state == STATE_PENDING) {
    // 青点滅。開始申告が通り、ナビゲーターの応答を待っている段階 (§4.2)。
    //
    // Ready (青点灯) の延長として「青が動いている = もうすぐ始まる」と読ませる。
    // Setup の黄点滅とは色で、Ready とは点滅の有無で区別できる。
    // まだ始まっていないので明るさは Ready と揃えて落としておく。
    command.pattern = Pl9823Task::PATTERN_BLINK;
    ApplyLook(command, kLookPending);
    command.on_ms = kPendingBlinkMs;
    command.off_ms = kPendingBlinkMs;
    return command;
  }

  if (state == STATE_PLAYING) {
    // 赤の短発点滅。残り時間が減るほど間隔が詰まる (§4.1)。
    // 緊張感を出す場面なので明るさは落とさない (短い閃光ぶん強めにする)
    command.pattern = Pl9823Task::PATTERN_BLINK;
    ApplyLook(command, kLookPlaying);
    command.on_ms = kPlayingBlinkOnMs;
    command.off_ms = PlayingBlinkOffMs(remaining_ms);
    return command;
  }

  if (state == STATE_DETONATING || state == STATE_EXPLODED) {
    // 赤点灯。破裂前後の警告なので落とさない
    command.pattern = Pl9823Task::PATTERN_SOLID;
    ApplyLook(command, kLookDanger);
    return command;
  }

  if (state == STATE_DEFUSED) {
    // 緑点灯。解除成功の見せ場なので落とさない
    command.pattern = Pl9823Task::PATTERN_SOLID;
    ApplyLook(command, kLookDefused);
    return command;
  }

  return command;
}

}  // namespace StatusIndicator

}  // namespace CoreSystem

#endif  // STATUS_INDICATOR_H_
// EOF
