#ifndef BUZZER_H_
#define BUZZER_H_
// Core System
// (C)2026 bekki.jp
// アクティブブザー制御 (GPIO ON/OFFのみ。音程制御なし)
// 鳴動パターンはGameTaskの100ms tickから Tick() を呼んで進める。

// Include ----------------------
#include <driver/gpio.h>
#include <sdkconfig.h>

#include <cstdint>

namespace CoreSystem {

class Buzzer final {
 public:
  explicit Buzzer(gpio_num_t gpio_no)
      : gpio_no_(gpio_no), remaining_ticks_(0), beep_count_(0), is_on_(false),
        continuous_(false) {}

  /// GPIOを出力に設定して消音状態にする。
  /// CONFIG_CORE_SYSTEM_BUZZER=n でも出力(LOW)には設定する
  /// (入力のまま浮かせると外部回路によっては鳴りうるため)
  void Setup() {
    gpio_config_t io_conf = {
        .pin_bit_mask = 1ULL << gpio_no_,
        .mode = GPIO_MODE_OUTPUT,
        .pull_up_en = GPIO_PULLUP_DISABLE,
        .pull_down_en = GPIO_PULLDOWN_DISABLE,
        .intr_type = GPIO_INTR_DISABLE,
    };
    gpio_config(&io_conf);
    Off();
  }

  /// 短音を count 回鳴らす (ペナルティ通知・解除成功の演出用)
  void Beep(int32_t count, int32_t on_ticks = 2, int32_t off_ticks = 2) {
    continuous_ = false;
    beep_count_ = count;
    on_ticks_ = on_ticks;
    off_ticks_ = off_ticks;
    remaining_ticks_ = on_ticks_;
    SetLevel(true);
  }

  /// 連続鳴動 (Detonating中の警告音)
  void On() {
    continuous_ = true;
    beep_count_ = 0;
    SetLevel(true);
  }

  /// 消音してパターンを停止する
  void Off() {
    continuous_ = false;
    beep_count_ = 0;
    remaining_ticks_ = 0;
    SetLevel(false);
  }

  /// GameTaskの100ms tickから呼び、短音パターンを進める
  void Tick() {
    if (continuous_ || beep_count_ <= 0) {
      return;
    }

    if (0 < remaining_ticks_) {
      --remaining_ticks_;
    }
    if (0 < remaining_ticks_) {
      return;
    }

    if (is_on_) {
      // 点音の終わり: 残り回数を1つ消化して消音区間へ
      SetLevel(false);
      --beep_count_;
      remaining_ticks_ = (0 < beep_count_) ? off_ticks_ : 0;
    } else {
      SetLevel(true);
      remaining_ticks_ = on_ticks_;
    }
  }

 private:
  /// 実際に音を出す唯一の箇所。無効時はここだけを止める。
  ///
  /// 呼び出し側 (GameTask) は11箇所あり、各所を #if で囲むと足し忘れが起きる。
  /// is_on_ は更新したままにして、パターン進行 (Tick) の挙動を
  /// 有効時と完全に同じに保つ (無効化で状態機械の振る舞いを変えない)。
  void SetLevel(bool on) {
    is_on_ = on;
#if CONFIG_CORE_SYSTEM_BUZZER
    gpio_set_level(gpio_no_, on ? 1 : 0);
#else
    gpio_set_level(gpio_no_, 0);
#endif
  }

 private:
  gpio_num_t gpio_no_;
  int32_t remaining_ticks_;
  int32_t beep_count_;
  int32_t on_ticks_ = 2;
  int32_t off_ticks_ = 2;
  bool is_on_;
  bool continuous_;
};

}  // namespace CoreSystem

#endif  // BUZZER_H_
// EOF
