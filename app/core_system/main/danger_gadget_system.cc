// Danger Gadget
// (C)2026 bekki.jp

// Include ----------------------
#include "danger_gadget_system.h"

#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <driver/gpio.h>

#include <string>
#include <cJSON.h>

#include "gpio_input_watch_task.h"
#include "i2c_util.h"
#include "logger.h"
#include "wifi_util.h"
#include "ws_client.h"

namespace DangerGadget {

namespace {
/// PL9823デイジーチェーン制御GPIO・接続数
constexpr gpio_num_t kPl9823Gpio = GPIO_NUM_4;
constexpr uint32_t kPl9823LedNum = 1;
}  // namespace

DangerGadgetSystem::DangerGadgetSystem()
    : pl9823_task_(kPl9823Gpio, kPl9823LedNum) {}

DangerGadgetSystem::~DangerGadgetSystem() = default;

void DangerGadgetSystem::Start() {
  ESP_LOGI(TAG, "Start");

  // WiFi接続
  WifiUtil::ConnectStaAndWait(CONFIG_DANGER_GADGET_WIFI_SSID, CONFIG_DANGER_GADGET_WIFI_PASSWORD);

  // I2C初期化
  i2c_master_bus_handle_t bus_handle =
      I2CUtil::InitializeMaster(static_cast<i2c_port_t>(CONFIG_DANGER_GADGET_GPIO_I2C_PORT_NO), static_cast<gpio_num_t>(CONFIG_DANGER_GADGET_GPIO_I2C_SDA), static_cast<gpio_num_t>(CONFIG_DANGER_GADGET_GPIO_I2C_SCL));

  // Input設定 (全ピン内部プルアップ有効のInputとして利用)
  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      mcp23017_.SetInputOutput(group, gpio_no, true);
    }
  }

  // MCP23017設定・初期化 (IOCON.MIRROR有効化によりGroup Bの変化もINTAで通知される)
  mcp23017_.Setup(bus_handle, CONFIG_DANGER_GADGET_MCP23017_I2C_ADDRESS);

  // HT16K33設定・初期化 (発振器ON, 表示ON, 輝度最大, 表示クリア)
  ht16k33_.Setup(bus_handle, CONFIG_DANGER_GADGET_HT16K33_I2C_ADDRESS);

  // 初期状態を取得
  CheckMCP23017Input();

  // MCP23017のINTAピン(GPIO19)をESP32側で監視し、変化検知時に読み直す
  GpioInputWatchTask gpio_watcher;
  gpio_watcher.AddMonitor(
      GpioInputWatchTask::GpioInfo(static_cast<gpio_num_t>(CONFIG_DANGER_GADGET_GPIO_MCP23017_INTA), std::bind(&DangerGadgetSystem::CheckMCP23017Input, this), nullptr),
      GpioInputWatchTask::PULL_UP_REGISTOR_ENABLE);
  gpio_watcher.Start();

  // [テスト実装] 30秒からのカウントダウンタスクを起動
  StartCountdownTask();

  // PL9823制御タスク起動 (赤色100ms点滅でデモ)
  pl9823_task_.Start();
  pl9823_task_.SendCommand(
      {Pl9823Task::PATTERN_BLINK, 255, 0, 0, 20, 1480});

  // WebSocketクライアント初期化
  WSClient ws_client;
  ws_client.Connect();

  while (true) {
    if (ws_client.IsConnected()) {
      cJSON* root = cJSON_CreateObject();
      cJSON_AddStringToObject(root, "type", "sample");
      cJSON_AddNumberToObject(root, "uptime_sec",
                              static_cast<double>(xTaskGetTickCount()) *
                                  portTICK_PERIOD_MS / 1000.0);

      char* json_text = cJSON_PrintUnformatted(root);
      ws_client.Send(json_text);
      ESP_LOGI(TAG, "WebSocket Send: %s", json_text);

      cJSON_free(json_text);
      cJSON_Delete(root);
    }

    TickType_t lastWakeTime = xTaskGetTickCount();
    vTaskDelayUntil(&lastWakeTime, 5000 / portTICK_PERIOD_MS);
  }
}

namespace {
constexpr gpio_num_t kSolenoidGpio = GPIO_NUM_23;

/// [テスト実装] カウントダウン開始秒数 (0.1秒単位、30.0秒)
constexpr int kCountdownStartTenths = 300;
}  // namespace

void CountdownTaskEntry(void* arg) {
  auto* self = static_cast<DangerGadgetSystem*>(arg);

  int remaining_tenths = kCountdownStartTenths;
  while (true) {
    const int seconds = remaining_tenths / 10;
    const int tenths = remaining_tenths % 10;

    // 整数部(3桁)+小数点1桁を右詰めで表示 (一の位を除く先頭の不要なゼロは消灯)
    const int int_digits[3] = {seconds / 100 % 10, seconds / 10 % 10, seconds % 10};
    self->ht16k33_.Clear();
    bool leading = true;
    for (int i = 0; i < 3; ++i) {
      if (leading && int_digits[i] == 0 && i < 2) {
        continue;
      }
      leading = false;
      self->ht16k33_.WriteDigitNum(i, int_digits[i], i == 2);
    }
    self->ht16k33_.WriteDigitNum(3, tenths);
    self->ht16k33_.WriteDisplay();

    if (remaining_tenths <= 0) {
      // ソレノイド(GPIO23)を200msだけHIGHにする
      gpio_set_level(kSolenoidGpio, true);
      vTaskDelay(200 / portTICK_PERIOD_MS);
      gpio_set_level(kSolenoidGpio, false);
      break;
    }

    vTaskDelay(100 / portTICK_PERIOD_MS);
    --remaining_tenths;
  }

  vTaskDelete(nullptr);
}

/// [テスト実装] 30秒からカウントダウンしHT16K33に100ms毎に表示、0でソレノイドを駆動するタスクを生成する
void DangerGadgetSystem::StartCountdownTask() {
  gpio_config_t io_conf = {
      .pin_bit_mask = 1ULL << kSolenoidGpio,
      .mode = GPIO_MODE_OUTPUT,
      .pull_up_en = GPIO_PULLUP_DISABLE,
      .pull_down_en = GPIO_PULLDOWN_DISABLE,
      .intr_type = GPIO_INTR_DISABLE,
  };
  gpio_config(&io_conf);

  xTaskCreate(CountdownTaskEntry, "countdown", 2048, this, tskIDLE_PRIORITY + 1, nullptr);
}

/// MCP23017の全ピンを読み直し、前回値と差分があったピンのみログ出力する
void DangerGadgetSystem::CheckMCP23017Input() {
  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    // グループ単位でI2Cを読み直す
    mcp23017_.RefreshInputGroup(group);

    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      const bool level = mcp23017_.GetCachedInputGpio(group, gpio_no);
      if (level != last_level_[group][gpio_no]) {
        last_level_[group][gpio_no] = level;
        ESP_LOGI(TAG, "MCP23017 Changed Group:%d Gpio:%d Level:%d", group,
                 gpio_no, level);
      }
    }
  }
}

} // namespace DangerGadget

// EOF

