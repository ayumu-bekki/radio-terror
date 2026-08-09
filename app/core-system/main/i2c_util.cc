// ESP32 Epoch Clock
// (C)2021 bekki.jp
// Utilities

// Include ----------------------
#include "i2c_util.h"

namespace I2CUtil {

i2c_master_bus_handle_t InitializeMaster(const i2c_port_t port,
                                         const gpio_num_t sdaPin,
                                         const gpio_num_t sclPin) {
  i2c_master_bus_config_t bus_config = {};
  bus_config.i2c_port = port;
  bus_config.sda_io_num = sdaPin;
  bus_config.scl_io_num = sclPin;
  bus_config.clk_source = I2C_CLK_SRC_DEFAULT;
  bus_config.glitch_ignore_cnt = 7;
  bus_config.flags.enable_internal_pullup = true;

  i2c_master_bus_handle_t bus_handle = nullptr;
  i2c_new_master_bus(&bus_config, &bus_handle);

  return bus_handle;
}

}  // namespace I2CUtil

// EOF
