#ifndef HARDWARE_CONFIG_H_
#define HARDWARE_CONFIG_H_
// Core System
// (C)2026 bekki.jp
// 基板配線・I2Cアドレスの一元管理

#include <driver/gpio.h>
#include <driver/i2c_master.h>

#include "game_session.h"  // ColorId / kColorNum / kRotaryPositionNum

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

/// kLED A-E の配置を ColorId 順に並べたもの ({group, gpio_no})。
///
/// MCP23017 の出力ピンはこの5本がすべてなので、
/// **色で引く用途と、出力設定・一括消灯のループの両方をこの表で兼ねる**。
/// 順序を問わない処理もこれを回せばよく、同じ配線を二重に書かずに済む。
constexpr uint8_t kLedPinsByColor[][2] = {
    {kGroupA, kLedA},
    {kGroupA, kLedB},
    {kGroupB, kLedC},
    {kGroupB, kLedD},
    {kGroupB, kLedE},
};

/// kPush A-E を ColorId 順に並べたもの (全て Group B)。
/// 添字が ColorId になるので、押されたピンから色を引ける。
constexpr uint8_t kPushGpiosByColor[kColorNum] = {
    kPushA, kPushB, kPushC, kPushD, kPushE,
};

/// ロータリー6接点を**位置0-5の順**に並べたもの (全て Group A)。
///
/// **配列の添字がそのまま位置番号**になる並びで定義している。
/// GPIO番号は降順 (kRotary1=GPA7 → kRotary6=GPA2) で直感に反するため、
/// ここを基板の並び順で書くと全ステージのロータリー条件が1つずれ、
/// 「動くが正解しない」形で現れる。対応表は
/// docs/game_session_design.md §3。
constexpr uint8_t kRotaryGpiosByPosition[kRotaryPositionNum] = {
    kRotary1, kRotary2, kRotary3, kRotary4, kRotary5, kRotary6,
};

/// Group B の GPIO番号から kPush の色を引く。該当しなければ COLOR_NONE。
/// 配線から色を求める処理をここに置き、呼び出し側の探索ループを無くす。
constexpr ColorId PushColorForGpio(uint8_t gpio_no) {
  for (int i = 0; i < kColorNum; ++i) {
    if (kPushGpiosByColor[i] == gpio_no) {
      return static_cast<ColorId>(i);
    }
  }
  return COLOR_NONE;
}

/// Group A の GPIO番号がロータリー接点かを返す。
constexpr bool IsRotaryGpio(uint8_t gpio_no) {
  for (const uint8_t rotary_gpio : kRotaryGpiosByPosition) {
    if (rotary_gpio == gpio_no) {
      return true;
    }
  }
  return false;
}
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

/// kLine A-E を ColorId 順に並べたもの。
/// GPIO番号は連番でないため、色から引くには必ずこの表を通す。
constexpr gpio_num_t kLineGpiosByColor[kColorNum] = {
    kLineA, kLineB, kLineC, kLineD, kLineE,
};

/// PL9823 (フルカラーLED) のデイジーチェーン接続数
constexpr uint32_t kFullColorLedNum = 1;
}  // namespace Esp32Pin

/// I2Cクライアントアドレス
namespace I2cAddress {
constexpr uint8_t kMcp23017 = 0x20;
constexpr uint8_t kHt16k33 = 0x70;
}  // namespace I2cAddress

}  // namespace CoreSystem

#endif  // HARDWARE_CONFIG_H_
// EOF
