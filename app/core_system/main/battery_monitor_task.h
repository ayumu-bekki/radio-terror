#ifndef BATTERY_MONITOR_TASK_H_
#define BATTERY_MONITOR_TASK_H_
// Core System
// (C)2026 bekki.jp
// バッテリー電圧監視と低電圧保護 (docs/game_session_design.md §8.5)
// 30秒に1回測定し、3.4V未満が2回連続したら GameTask へ通知して Deep Sleep させる。

// Include ----------------------
#include <esp_adc/adc_oneshot.h>
#include <freertos/FreeRTOS.h>

#include <functional>

#include "hardware_config.h"
#include "logger.h"
#include "task.h"

namespace CoreSystem {

class BatteryMonitorTask final : public Task {
 public:
  static constexpr std::string_view TASK_NAME = "BatteryMonitorTask";
  static constexpr int32_t PRIORITY = Task::PRIORITY_LOW;
  static constexpr int32_t CORE_ID = PRO_CPU_NUM;

  /// 測定間隔
  static constexpr int32_t kIntervalMs = 30000;

  /// Deep Sleepへ入る電圧閾値 (過放電保護)
  static constexpr float kLowVoltageThreshold = 3.4f;

  /// 閾値を下回った測定がこの回数連続したら低電圧確定
  /// (ソレノイド駆動などの瞬間的な電圧降下で誤判定しないため)
  static constexpr int32_t kLowVoltageConfirmCount = 2;

  /// 1回の測定で平均するサンプル数
  static constexpr int32_t kSampleNum = 16;

  /// 220kΩ×2の分圧のため、実電圧は測定値の2倍
  static constexpr float kVoltageDividerRatio = 2.0f;

 public:
  /// voltage_handler: 測定のたびに実電圧を通知する (device_status 用)
  /// low_battery_handler: 低電圧が確定したときに1回だけ通知する
  BatteryMonitorTask(std::function<void(float)> voltage_handler,
                     std::function<void()> low_battery_handler)
      : Task(std::string(TASK_NAME).c_str(), PRIORITY, CORE_ID),
        voltage_handler_(std::move(voltage_handler)),
        low_battery_handler_(std::move(low_battery_handler)),
        adc_handle_(nullptr),
        low_voltage_count_(0),
        notified_(false) {}

  ~BatteryMonitorTask() override {
    if (adc_handle_) {
      adc_oneshot_del_unit(adc_handle_);
      adc_handle_ = nullptr;
    }
  }

  void Initialize() override {
    ESP_LOGI(TAG, "Start BatteryMonitorTask");

    adc_oneshot_unit_init_cfg_t init_config = {};
    init_config.unit_id = ADC_UNIT_1;
    esp_err_t err = adc_oneshot_new_unit(&init_config, &adc_handle_);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "adc_oneshot_new_unit failed err:%s", esp_err_to_name(err));
      adc_handle_ = nullptr;
      return;
    }

    adc_oneshot_chan_cfg_t channel_config = {};
    // 分圧後でも最大3.3V付近まで測るため最大減衰を選ぶ
    channel_config.atten = ADC_ATTEN_DB_12;
    channel_config.bitwidth = ADC_BITWIDTH_DEFAULT;
    err = adc_oneshot_config_channel(adc_handle_, kChannel, &channel_config);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "adc_oneshot_config_channel failed err:%s", esp_err_to_name(err));
    }
  }

  void Update() override {
    vTaskDelay(pdMS_TO_TICKS(kIntervalMs));

    if (!adc_handle_) {
      return;
    }

    const float voltage = MeasureVoltage();
    if (voltage <= 0.0f) {
      return;
    }

    if (voltage_handler_) {
      voltage_handler_(voltage);
    }

    if (kLowVoltageThreshold <= voltage) {
      low_voltage_count_ = 0;
      return;
    }

    ++low_voltage_count_;
    ESP_LOGW(TAG, "low battery detected: %.2fV (count=%d)", voltage,
             static_cast<int>(low_voltage_count_));

    if (low_voltage_count_ < kLowVoltageConfirmCount || notified_) {
      return;
    }

    notified_ = true;
    if (low_battery_handler_) {
      low_battery_handler_();
    }
  }

 private:
  /// 複数サンプルの平均から実電圧を求める。測定に失敗した場合は0を返す
  float MeasureVoltage() {
    int32_t total_mv = 0;
    int32_t valid_samples = 0;

    for (int32_t i = 0; i < kSampleNum; ++i) {
      int raw = 0;
      if (adc_oneshot_read(adc_handle_, kChannel, &raw) != ESP_OK) {
        continue;
      }
      // 12bit・12dB減衰のフルスケールを約3.3Vとして概算する
      total_mv += static_cast<int32_t>(static_cast<float>(raw) * 3300.0f / 4095.0f);
      ++valid_samples;
    }

    if (valid_samples == 0) {
      return 0.0f;
    }

    const float measured_v = static_cast<float>(total_mv) / valid_samples / 1000.0f;
    return measured_v * kVoltageDividerRatio;
  }

 private:
  /// kBatteryAdc (GPIO32) は ADC1_CH4
  static constexpr adc_channel_t kChannel = ADC_CHANNEL_4;

  std::function<void(float)> voltage_handler_;
  std::function<void()> low_battery_handler_;
  adc_oneshot_unit_handle_t adc_handle_;
  int32_t low_voltage_count_;
  bool notified_;
};

}  // namespace CoreSystem

#endif  // BATTERY_MONITOR_TASK_H_
// EOF
