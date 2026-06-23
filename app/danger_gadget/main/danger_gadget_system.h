#ifndef DANGER_GADGET_SYSTEM_H_ 
#define DANGER_GADGET_SYSTEM_H_ 
// Danger Gadget
// (C)2026 bekki.jp

// Include ----------------------
#include "mcp23017.h"
#include "gpio_input_watch_task.h"

#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <esp_event_base.h>
#include <memory>

#include "i2c_util.h"
#include "logger.h"

namespace DangerGadget {

/// IrrigationController
class DangerGadgetSystem final
    : public std::enable_shared_from_this<DangerGadgetSystem> {
 public:
  DangerGadgetSystem();
  ~DangerGadgetSystem();

  void Start();

 private:
  void CheckMCP23017Input();

 private:
  MCP23017 mcp23017_;
  bool last_level_[MCP23017::GPIO_GROUP_NUM][MCP23017::GPIO_NUM];

};

}  // namespace DangerGadget

#endif  // DANGER_GADGET_SYSTEM_H_
// EOF

