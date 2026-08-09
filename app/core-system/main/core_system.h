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

class System final
    : public std::enable_shared_from_this<System> {
 public:
  System();
  ~System();

  void Start();

 private:
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
