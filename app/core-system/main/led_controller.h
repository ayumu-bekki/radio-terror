#ifndef LED_CONTROLLER_H_
#define LED_CONTROLLER_H_
// Core System
// (C)2026 bekki.jp
// kLED A-E の表示制御 (docs/game_session_design.md §4.1・§6.1)
//
// 表示は3層で決まり、上の層が優先される:
//   1. 上書き表示  … 色合わせの点灯・push_seq のフィードバック
//   2. パターン再生 … ステージ定義の blink / morse
//   3. 消灯
// Setup中だけは例外で、切断中の線を点灯して復旧ガイドにする。

// Include ----------------------
#include <cstdint>

#include "game_session.h"
#include "hardware_config.h"
#include "mcp23017.h"

namespace CoreSystem {

/// kLED A-E の表示制御。GameTask が所有し、100ms tick から進める。
class LedController final {
 public:
  explicit LedController(MCP23017* mcp23017) : mcp23017_(mcp23017) {}

  /// ステージ切り替え時にパターン再生位置と上書き表示をリセットする
  void Reset() {
    for (int i = 0; i < kColorNum; ++i) {
      step_index_[i] = 0;
      step_elapsed_[i] = 0;
      pattern_on_[i] = false;
      override_on_[i] = false;
    }
    override_active_ = false;
  }

  /// プリコンパイル済みLEDパターンを1tick分進める (§6.1)
  void TickPatterns(const StageConfig& stage) {
    for (int i = 0; i < kColorNum; ++i) {
      const LedPattern& pattern = stage.leds[i];
      if (pattern.steps.empty()) {
        pattern_on_[i] = false;
        continue;
      }

      if (static_cast<int32_t>(pattern.steps.size()) <= step_index_[i]) {
        step_index_[i] = 0;
        step_elapsed_[i] = 0;
      }

      const LedStep& step = pattern.steps[step_index_[i]];
      pattern_on_[i] = step.on;

      ++step_elapsed_[i];
      if (step.ticks <= step_elapsed_[i]) {
        step_elapsed_[i] = 0;
        step_index_[i] =
            (step_index_[i] + 1) % static_cast<int32_t>(pattern.steps.size());
      }
    }
  }

  /// 指定色だけを上書き点灯する (色合わせの点灯・push_seq の入力反応)
  void SetOverride(ColorId color, bool on) {
    if (color < COLOR_A || kColorNum <= color) {
      return;
    }
    override_active_ = true;
    override_on_[color] = on;
  }

  /// 全色を上書き点灯する (push_seq のミス通知)
  void SetOverrideAll(bool on) {
    override_active_ = true;
    for (int i = 0; i < kColorNum; ++i) {
      override_on_[i] = on;
    }
  }

  /// 上書き表示を解除してパターン再生へ戻す
  void ClearOverride() {
    override_active_ = false;
    for (int i = 0; i < kColorNum; ++i) {
      override_on_[i] = false;
    }
  }

  /// 全kLEDが消灯しているか (precondition.leds_all_off の判定。§5)
  bool AreAllPatternsOff() const {
    for (int i = 0; i < kColorNum; ++i) {
      if (pattern_on_[i]) {
        return false;
      }
    }
    return true;
  }

  /// Setup中の復旧ガイドを表示する (§4.1)。
  ///
  /// **結線済みの線に対応するLEDを点灯**する。復旧が進むほど点灯が増え、
  /// 全点灯で準備完了になるため、進捗が一目で分かる。
  void ApplySetupGuide(const bool line_connected[kColorNum]) {
    bool desired[kColorNum];
    for (int i = 0; i < kColorNum; ++i) {
      desired[i] = line_connected[i];
    }
    Write(desired);
  }

  /// Playing中の表示を反映する (上書き表示があればそれを優先)
  void ApplyPlaying() {
    bool desired[kColorNum];
    for (int i = 0; i < kColorNum; ++i) {
      desired[i] = override_active_ ? override_on_[i] : pattern_on_[i];
    }
    Write(desired);
  }

  /// 全消灯する (Ready / Detonating / Exploded / Defused。§4.1)
  void ApplyAllOff() {
    bool desired[kColorNum] = {false, false, false, false, false};
    Write(desired);
  }

 private:
  /// kLED A-E の配置は hardware_config.h の kLedPinsByColor を使う
  static constexpr auto& kLedPins = Mcp23017Pin::kLedPinsByColor;

  /// 変化があった色だけI2Cへ書き込む (毎tickの全書き込みを避ける。§6.1)
  void Write(const bool desired[kColorNum]) {
    for (int i = 0; i < kColorNum; ++i) {
      if (written_[i] == desired[i]) {
        continue;
      }
      written_[i] = desired[i];
      mcp23017_->SetOutputGpio(kLedPins[i][0], kLedPins[i][1], desired[i]);
    }
  }

 private:
  MCP23017* mcp23017_;

  /// パターン再生位置 (ステップindexと、そのステップ内の経過tick)
  int32_t step_index_[kColorNum] = {0, 0, 0, 0, 0};
  int32_t step_elapsed_[kColorNum] = {0, 0, 0, 0, 0};
  /// パターン再生結果としての点灯状態
  bool pattern_on_[kColorNum] = {false, false, false, false, false};

  /// 上書き表示 (パターンより優先)
  bool override_active_ = false;
  bool override_on_[kColorNum] = {false, false, false, false, false};

  /// 実際にMCP23017へ書いた値 (変化時のみ書き込むため保持する)
  bool written_[kColorNum] = {false, false, false, false, false};
};

}  // namespace CoreSystem

#endif  // LED_CONTROLLER_H_
// EOF
