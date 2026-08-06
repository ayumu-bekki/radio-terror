#ifndef CORE_SYSTEM_H_
#define CORE_SYSTEM_H_
// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "mcp23017.h"
#include "ht16k33.h"
#include "gpio_input_watch_task.h"
#include "pl9823_task.h"

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
  void CheckMCP23017Input();
  void StartCountdownTask();

 private:
  MCP23017 mcp23017_;
  bool last_level_[MCP23017::GPIO_GROUP_NUM][MCP23017::GPIO_NUM];
  HT16K33 ht16k33_;
  Pl9823Task pl9823_task_;

  friend void CountdownTaskEntry(void* arg);
};

}  // namespace CoreSystem

#endif  // CORE_SYSTEM_H_
// EOF

