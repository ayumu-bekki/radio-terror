#ifndef CORE_SYSTEM_H_
#define CORE_SYSTEM_H_
// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "battery_monitor_task.h"
#include "game_task.h"
#include "gpio_input_watch_task.h"
#include "ht16k33.h"
#include "mcp23017.h"
#include "pl9823_task.h"
#include "ws_client.h"

#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <esp_event_base.h>
#include <memory>

#include "i2c_util.h"
#include "logger.h"

namespace CoreSystem {

/// 起動インジケータの色 (フルカラーLED)
struct BootColor {
  uint8_t r;
  uint8_t g;
  uint8_t b;
};

/// 初期化中を示す紫。電源投入直後から点灯する。
inline constexpr BootColor kBootColorInitializing = {80, 0, 120};

/// 初期化失敗を示す紫。点滅で「初期化中」と区別する
/// (同じ色の点灯では成功/失敗が見分けられないため)。
inline constexpr BootColor kBootColorFailed = {120, 0, 160};

/// WiFi接続のタイムアウト。会場のWiFiが未設営でも起動を止めない。
inline constexpr uint32_t kWifiConnectTimeoutMs = 15000;

class System final
    : public std::enable_shared_from_this<System> {
 public:
  System();
  ~System();

  void Start();

 private:
  /// 起動演出 (約3秒)。kLED A-E・7セグ・フルカラーLEDを順に光らせる。
  /// デバイス初期化の直後、WiFi接続を待たずに実行する。
  void PlayBootAnimation();

  /// フルカラーLEDを起動インジケータとして光らせる。
  /// 紫点灯=初期化中 / 紫点滅=初期化失敗。
  void SetBootIndicator(const BootColor& color, Pl9823Task::PatternType pattern);

  /// MCP23017の全ピンを読み直し、変化したピンを GameEvent としてGameTaskへ通知する
  void CheckMCP23017Input();

  /// kLine A-E のGPIO監視を GpioInputWatchTask へ登録する
  void SetupLineWatchers();

  /// ロータリー6接点から「ちょうど1つがLOW」の確定位置を求める (§5.2)
  void UpdateRotaryPosition();

 private:
  MCP23017 mcp23017_;
  bool last_level_[MCP23017::GPIO_GROUP_NUM][MCP23017::GPIO_NUM];
  bool has_last_level_ = false;
  HT16K33 ht16k33_;
  Pl9823Task pl9823_task_;
  GameTask game_task_;
  WSClient ws_client_;
  GpioInputWatchTask gpio_watcher_;
  std::unique_ptr<BatteryMonitorTask> battery_monitor_task_;

  /// 直近に確定したロータリー位置 (過渡状態では最後の確定値を保持する)
  int8_t rotary_position_ = 0;
};

}  // namespace CoreSystem

#endif  // CORE_SYSTEM_H_
// EOF
