#ifndef IDLE_DEMO_H_
#define IDLE_DEMO_H_
// Core System
// (C)2026 bekki.jp
// 待機中 (Ready) のデモ表示 (docs/game_session_design.md §4.1b)

// Include ----------------------
#include <cstdint>

#include "hardware_config.h"
#include "ht16k33.h"
#include "mcp23017.h"

namespace CoreSystem {

/// 待機中 (Ready) に kLED と7セグを流して、通りがかりの人の目を引く。
///
/// ゲーム状態には触れない。GameTask が Ready の間だけ 100ms tick から Step() を呼び、
/// Ready を出るときに Stop() を呼ぶ。GameTask と同じスレッドで動くので排他は要らない。
/// フルカラーLEDとブザーは触らない (青点灯は StatusIndicator のまま)。
class IdleDemo final {
 public:
  IdleDemo(MCP23017* mcp23017, HT16K33* ht16k33)
      : mcp23017_(mcp23017), ht16k33_(ht16k33) {}

  /// 先頭のコマから流し始める。最初のコマは次の Step() で出る
  void Start() {
    step_ = 0;
    flash_ticks_ = 0;
    active_ = true;
  }

  /// 1コマ進める。**状態を問わず** tick ごとに呼ぶ (閃光は Ready を出たあとも続くため)
  void Step() {
    if (!active_) {
      return;
    }
    if (0 < flash_ticks_) {
      --flash_ticks_;
      if (flash_ticks_ == 0) {
        Stop();
      }
      return;
    }
    DrawFrame();
    ++step_;
  }

  /// 待機を終える合図。kLED と7セグを**全点灯**し、kFlashTicks 後に消灯する。
  /// 流れていないときは何もしない。消灯は Step() が行う (待たない)。
  void Flash() {
    if (!active_) {
      return;
    }
    for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
      mcp23017_->SetOutputGpio(pin[0], pin[1], true, true);
    }
    for (uint8_t d = 0; d < HT16K33::DIGIT_NUM; ++d) {
      ht16k33_->WriteDigitRaw(d, kSevenSegAllOn);
    }
    ht16k33_->WriteDisplay();
    flash_ticks_ = kFlashTicks;
  }

  /// 流れていれば kLED と7セグを消灯して止める。流れていなければ何もしない
  /// (他の状態の表示を消さないため)。LedController のキャッシュと食い違わないよう強制書き込み
  void Stop() {
    if (!active_) {
      return;
    }
    active_ = false;
    flash_ticks_ = 0;
    for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
      mcp23017_->SetOutputGpio(pin[0], pin[1], false, true);
    }
    ht16k33_->Clear();
    ht16k33_->WriteDisplay();
  }

 private:
  /// 閃光の長さ (tick 数。100ms x 10 = 約1000ms)
  static constexpr uint32_t kFlashTicks = 10;

  /// 閃光で7セグへ出す文字 ("8." の全点灯。起動演出と同じ)
  static constexpr uint8_t kSevenSegAllOn = 0xFF;

  /// kLED の往復 (A→E→A) の1周期。端は重ねず A B C D E D C B の8コマ
  static constexpr uint32_t kLedCycle = 2 * (kColorNum - 1);

  /// 7セグのセグメントビット (bit0-6 = a-g)
  static constexpr uint8_t kSegA = 1 << 0;
  static constexpr uint8_t kSegB = 1 << 1;
  static constexpr uint8_t kSegC = 1 << 2;
  static constexpr uint8_t kSegD = 1 << 3;
  static constexpr uint8_t kSegE = 1 << 4;
  static constexpr uint8_t kSegF = 1 << 5;

  struct SegPos {
    uint8_t digit;
    uint8_t mask;
  };

  /// 4桁の外周を時計回りに1周する順路 (上段を左→右、右辺を下り、下段を右→左、左辺を上る)
  static constexpr SegPos kLoop[] = {
      {0, kSegA}, {1, kSegA}, {2, kSegA}, {3, kSegA}, {3, kSegB}, {3, kSegC},
      {3, kSegD}, {2, kSegD}, {1, kSegD}, {0, kSegD}, {0, kSegE}, {0, kSegF},
  };
  static constexpr uint32_t kLoopLen = sizeof(kLoop) / sizeof(kLoop[0]);

  /// 7セグで流れるヘビの長さ (先頭を含む)
  static constexpr uint32_t kSnakeLen = 3;

  /// kLED の位置 (0-4)。往復の途中から位置を引く
  static uint32_t LedPosition(uint32_t step) {
    const uint32_t phase = step % kLedCycle;
    if (phase < kColorNum) {
      return phase;
    }
    return kLedCycle - phase;
  }

  void DrawFrame() {
    // kLED: 先頭と、ひとつ前の位置を点ける (尾を引く)
    const uint32_t head = LedPosition(step_);
    const uint32_t tail = LedPosition(step_ + kLedCycle - 1);
    for (uint32_t i = 0; i < kColorNum; ++i) {
      const auto& pin = Mcp23017Pin::kLedPinsByColor[i];
      mcp23017_->SetOutputGpio(pin[0], pin[1], i == head || i == tail);
    }

    // 7セグ: 外周をヘビが回る
    uint8_t digits[HT16K33::DIGIT_NUM] = {0, 0, 0, 0};
    for (uint32_t k = 0; k < kSnakeLen; ++k) {
      const SegPos& pos = kLoop[(step_ + kLoopLen - k) % kLoopLen];
      digits[pos.digit] |= pos.mask;
    }
    ht16k33_->Clear();
    for (uint8_t d = 0; d < HT16K33::DIGIT_NUM; ++d) {
      ht16k33_->WriteDigitRaw(d, digits[d]);
    }
    ht16k33_->WriteDisplay();
  }

  MCP23017* mcp23017_;
  HT16K33* ht16k33_;
  bool active_ = false;
  uint32_t step_ = 0;
  uint32_t flash_ticks_ = 0;  // 0 でなければ閃光中
};

}  // namespace CoreSystem

#endif  // IDLE_DEMO_H_
// EOF
