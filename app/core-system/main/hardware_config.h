#ifndef HARDWARE_CONFIG_H_
#define HARDWARE_CONFIG_H_
// Core System
// (C)2026 bekki.jp
// 基板配線・I2Cアドレスの一元管理

#include <driver/gpio.h>
#include <driver/i2c_master.h>

namespace CoreSystem {

/// MCP23017 GPIO割り当て (Group A / Group B)
namespace Mcp23017Pin {
constexpr uint8_t kGroupA = 0;
constexpr uint8_t kGroupB = 1;

constexpr uint8_t kLedA = 0;       // GPA0 OUT
constexpr uint8_t kLedB = 1;       // GPA1 OUT
constexpr uint8_t kRotary6 = 2;    // GPA2 IN
constexpr uint8_t kRotary5 = 3;    // GPA3 IN
constexpr uint8_t kRotary4 = 4;    // GPA4 IN
constexpr uint8_t kRotary3 = 5;    // GPA5 IN
constexpr uint8_t kRotary2 = 6;    // GPA6 IN
constexpr uint8_t kRotary1 = 7;    // GPA7 IN

constexpr uint8_t kPushA = 0;      // GPB0 IN
constexpr uint8_t kPushB = 1;      // GPB1 IN
constexpr uint8_t kPushC = 2;      // GPB2 IN
constexpr uint8_t kPushD = 3;      // GPB3 IN
constexpr uint8_t kPushE = 4;      // GPB4 IN
constexpr uint8_t kLedE = 5;       // GPB5 OUT
constexpr uint8_t kLedD = 6;       // GPB6 OUT
constexpr uint8_t kLedC = 7;       // GPB7 OUT

/// OUTPUTピン一覧 ({group, gpio_no})
constexpr uint8_t kOutputPins[][2] = {
    {kGroupA, kLedA},
    {kGroupA, kLedB},
    {kGroupB, kLedE},
    {kGroupB, kLedD},
    {kGroupB, kLedC},
};
}  // namespace Mcp23017Pin

/// ESP32 GPIO割り当て
namespace Esp32Pin {
constexpr i2c_port_t kI2cPortNo = I2C_NUM_0;
constexpr gpio_num_t kBatteryAdc = GPIO_NUM_32;    // ADC IN
constexpr gpio_num_t kSolenoid = GPIO_NUM_33;      // OUT
constexpr gpio_num_t kLineE = GPIO_NUM_15;         // IN
constexpr gpio_num_t kLineD = GPIO_NUM_4;          // IN
constexpr gpio_num_t kLineA = GPIO_NUM_18;         // IN
constexpr gpio_num_t kBuzzer = GPIO_NUM_19;        // OUT
constexpr gpio_num_t kI2cSda = GPIO_NUM_21;        // OUT (I2C SDA)
constexpr gpio_num_t kI2cScl = GPIO_NUM_22;        // OUT (I2C SCL)
constexpr gpio_num_t kMcp23017Interrupt = GPIO_NUM_23;  // IN
constexpr gpio_num_t kFullColorLed = GPIO_NUM_27;  // OUT (PL9823)
constexpr gpio_num_t kLineC = GPIO_NUM_16;         // IN
constexpr gpio_num_t kLineB = GPIO_NUM_17;         // IN
}  // namespace Esp32Pin

/// I2Cクライアントアドレス
namespace I2cAddress {
constexpr uint8_t kMcp23017 = 0x20;
constexpr uint8_t kHt16k33 = 0x70;
}  // namespace I2cAddress

}  // namespace CoreSystem

#endif  // HARDWARE_CONFIG_H_
// EOF
