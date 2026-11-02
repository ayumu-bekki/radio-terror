#ifndef IDLE_DEMO_TASK_H_
#define IDLE_DEMO_TASK_H_
// Core System
// (C)2026 bekki.jp
// 待機中 (Ready) のデモ表示 (docs/game_session_design.md §4.1b)

// Include ----------------------
#include <freertos/FreeRTOS.h>
#include <freertos/semphr.h>
#include <freertos/task.h>

#include <string>
#include <string_view>

#include "hardware_config.h"
#include "ht16k33.h"
#include "logger.h"
#include "mcp23017.h"
#include "task.h"

namespace CoreSystem {

/// 待機中 (Ready) に kLED と7セグを流して、通りがかりの人の目を引く。
///
/// ゲーム状態には触れない。GameTask が状態遷移のたびに SetEnabled() を呼ぶだけで、
/// 滑らかに動かしたいので 100ms tick には乗せず専用タスクで回す。
/// フルカラーLEDとブザーは触らない (青点灯は StatusIndicator のまま)。
///
/// **表示の所有権はミューテックスで渡す。** 無効にした時点で自分の表示を消し、
/// SetEnabled(false) から戻った後は一切書かない。GameTask が直後にクリアしても、
/// 演出の1コマが後から上書きして残ることはない。
class IdleDemoTask final : public Task {
 public:
  static constexpr std::string_view TASK_NAME = "IdleDemoTask";
  static constexpr int32_t PRIORITY = Task::PRIORITY_LOW + 1;
  static constexpr int32_t CORE_ID = APP_CPU_NUM;

  IdleDemoTask(MCP23017* mcp23017, HT16K33* ht16k33)
      : Task(std::string(TASK_NAME).c_str(), PRIORITY, CORE_ID),
        mcp23017_(mcp23017),
        ht16k33_(ht16k33),
        mutex_(xSemaphoreCreateMutex()) {}

  ~IdleDemoTask() override {
    Stop();
    if (mutex_ != nullptr) {
      vSemaphoreDelete(mutex_);
    }
  }

  /// デモの有効/無効を切り替える。同じ値の再設定は何もしない。
  ///
  /// 有効にすると先頭のコマから流れる。無効にすると kLED と7セグを消灯する。
  void SetEnabled(bool enabled) {
    if (mutex_ == nullptr) {
      return;
    }
    xSemaphoreTake(mutex_, portMAX_DELAY);
    if (enabled != enabled_) {
      enabled_ = enabled;
      step_ = 0;
      if (!enabled) {
        ClearOutputs();
      }
    }
    xSemaphoreGive(mutex_);
  }

  void Update() override {
    if (mutex_ != nullptr) {
      xSemaphoreTake(mutex_, portMAX_DELAY);
      if (enabled_) {
        DrawFrame();
        ++step_;
      }
      xSemaphoreGive(mutex_);
    }
    vTaskDelay(pdMS_TO_TICKS(kStepMs));
  }

 private:
  /// 1コマの長さ。kLED も7セグもこの間隔で1歩ずつ進める
  static constexpr uint32_t kStepMs = 80;

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

  /// ミューテックスを持った状態で呼ぶ
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

  /// ミューテックスを持った状態で呼ぶ。LedController のキャッシュと食い違わないよう強制書き込み
  void ClearOutputs() {
    for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
      mcp23017_->SetOutputGpio(pin[0], pin[1], false, true);
    }
    ht16k33_->Clear();
    ht16k33_->WriteDisplay();
  }

  MCP23017* mcp23017_;
  HT16K33* ht16k33_;
  SemaphoreHandle_t mutex_;
  bool enabled_ = false;
  uint32_t step_ = 0;
};

}  // namespace CoreSystem

#endif  // IDLE_DEMO_TASK_H_
// EOF
