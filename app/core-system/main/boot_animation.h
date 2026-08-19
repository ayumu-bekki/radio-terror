#ifndef BOOT_ANIMATION_H_
#define BOOT_ANIMATION_H_
// Core System
// (C)2026 bekki.jp
// 起動演出と起動インジケータ (docs/game_session_design.md §4.0)

// Include ----------------------
#include <freertos/FreeRTOS.h>
#include <freertos/task.h>

#include "hardware_config.h"
#include "ht16k33.h"
#include "logger.h"
#include "mcp23017.h"
#include "pl9823_task.h"
#include "status_indicator.h"

namespace CoreSystem {

/// 起動インジケータの色は表示仕様として status_indicator.h に集約している。
/// Setup中のサーバー未接続でも同じ紫を使うため (§4.0)。
using StatusIndicator::Appearance;
using StatusIndicator::kLookBootFailed;
using StatusIndicator::kLookBootInitializing;

/// 起動演出と起動インジケータを担う。
///
/// ゲーム進行とは独立していて、kLED・7セグ・フルカラーLEDを光らせるだけ。
/// 演出の調整でゲーム側のコードを触らずに済むよう分けている。
class BootAnimation final {
 public:
  BootAnimation(MCP23017* mcp23017, HT16K33* ht16k33, Pl9823Task* pl9823_task)
      : mcp23017_(mcp23017), ht16k33_(ht16k33), pl9823_task_(pl9823_task) {}

  /// 起動インジケータ (フルカラーLED) を設定する。
  /// 紫点灯=初期化中 / 紫点滅=初期化失敗 (§4.0)。
  /// 明るさは StatusIndicator と揃える。GameTask 起動後は同じ紫を
  /// StatusIndicator が出し続けるため、ここで明るさが違うと
  /// 起動の瞬間に明滅して見える。
  void SetIndicator(const Appearance& look, Pl9823Task::PatternType pattern) {
    Pl9823Task::Command command;
    command.pattern = pattern;
    StatusIndicator::ApplyLook(command, look);
    command.on_ms = kIndicatorBlinkMs;
    command.off_ms = kIndicatorBlinkMs;
    pl9823_task_->SendCommand(command);
  }

  /// 起動演出 (約2.8秒)。デバイス初期化の直後、WiFi接続を待たずに流す。
  ///
  /// 終了時は必ず全消灯する。GameTask は全消灯を前提に描画を始めるため。
  void Play() {
    ESP_LOGI(TAG, "Boot animation");

    // 1) kLED A-E を左から順に点けていく (点けたら消さないので、
    //    流れながら全点灯へ向かう)。同時に7セグの桁も1つずつ埋める。
    for (uint8_t i = 0; i < kColorNum; ++i) {
      const auto& pin = Mcp23017Pin::kLedPinsByColor[i];
      mcp23017_->SetOutputGpio(pin[0], pin[1], true, true);
      if (i < HT16K33::DIGIT_NUM) {
        ht16k33_->WriteDigitRaw(i, kSevenSegAllOn);
        ht16k33_->WriteDisplay();
      }
      SleepMs(kSweepStepMs);
    }

    // 2) 全点灯でしばらく見せる
    SleepMs(kAllOnMs);

    // 3) 全体を数回点滅させて締める
    for (int n = 0; n < kFlashCount; ++n) {
      SetAllLeds(false);
      ClearSevenSeg();
      SleepMs(kFlashMs);

      SetAllLeds(true);
      FillSevenSeg();
      SleepMs(kFlashMs);
    }

    // 4) 消灯して通常状態へ戻す
    SetAllLeds(false);
    ClearSevenSeg();
  }

 private:
  /// 演出のタイミング
  /// 内訳: スイープ 5x160=800ms + 全点灯 1200ms + 点滅 3x(2x140)=840ms ≒ 2.8秒
  static constexpr uint32_t kSweepStepMs = 160;  // 1色ずつ流す間隔
  static constexpr uint32_t kAllOnMs = 1200;     // 全点灯で見せる時間
  static constexpr uint32_t kFlashMs = 140;      // 点滅の点灯/消灯それぞれの時間
  static constexpr int kFlashCount = 3;

  /// 起動インジケータの点滅周期
  static constexpr uint32_t kIndicatorBlinkMs = 300;

  /// 演出中に7セグへ出す文字 ("8888" の全点灯)
  static constexpr uint8_t kSevenSegAllOn = 0xFF;

  static void SleepMs(uint32_t ms) { vTaskDelay(pdMS_TO_TICKS(ms)); }

  /// kLED A-E をまとめて点灯/消灯する
  void SetAllLeds(bool on) {
    for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
      mcp23017_->SetOutputGpio(pin[0], pin[1], on, true);
    }
  }

  void FillSevenSeg() {
    for (uint8_t d = 0; d < HT16K33::DIGIT_NUM; ++d) {
      ht16k33_->WriteDigitRaw(d, kSevenSegAllOn);
    }
    ht16k33_->WriteDisplay();
  }

  void ClearSevenSeg() {
    ht16k33_->Clear();
    ht16k33_->WriteDisplay();
  }

  MCP23017* mcp23017_;
  HT16K33* ht16k33_;
  Pl9823Task* pl9823_task_;
};

}  // namespace CoreSystem

#endif  // BOOT_ANIMATION_H_
// EOF
