// ESP32 IO Expander MCP23017 I2C Driver + WebSocket Sample
// (C)2024 bekki.jp

// Include ----------------------
#include <freertos/FreeRTOS.h>
#include <freertos/task.h>

#include <cstring>

#include <cJSON.h>
#include <esp_websocket_client.h>

#include "gpio_input_watch_task.h"
#include "i2c_util.h"
#include "logger.h"
#include "mcp23017.h"
#include "wifi_util.h"

namespace {

MCP23017 g_mcp23017;
bool g_last_level[MCP23017::GPIO_GROUP_NUM][MCP23017::GPIO_NUM] = {};

/// MCP23017の全ピンを読み直し、前回値と差分があったピンのみログ出力する
void CheckMCP23017Input() {
  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    // グループ単位でI2Cを1回だけ読み直す (ピンごとに読み直すと8倍遅くなる)
    g_mcp23017.RefreshInputGroup(group);

    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      const bool level = g_mcp23017.GetCachedInputGpio(group, gpio_no);
      if (level != g_last_level[group][gpio_no]) {
        g_last_level[group][gpio_no] = level;
        ESP_LOGI(TAG, "MCP23017 Changed Group:%d Gpio:%d Level:%d", group,
                 gpio_no, level);
      }
    }
  }
}

/// WebSocketイベントハンドラ (接続/切断/送受信をログ出力する)
void OnWebSocketEvent(void* handler_args, esp_event_base_t base,
                      int32_t event_id, void* event_data) {
  auto* data = static_cast<esp_websocket_event_data_t*>(event_data);

  switch (static_cast<esp_websocket_event_id_t>(event_id)) {
    case WEBSOCKET_EVENT_CONNECTED:
      ESP_LOGI(TAG, "WebSocket Connected");
      break;
    case WEBSOCKET_EVENT_DISCONNECTED:
      ESP_LOGI(TAG, "WebSocket Disconnected");
      break;
    case WEBSOCKET_EVENT_DATA:
      if (0 < data->data_len) {
        ESP_LOGI(TAG, "WebSocket Received: %.*s", data->data_len,
                 data->data_ptr);
      }
      break;
    case WEBSOCKET_EVENT_ERROR:
      ESP_LOGW(TAG, "WebSocket Error");
      break;
    default:
      break;
  }
}

/// 適当なJSONを組み立てて送信する
void SendSampleJson(esp_websocket_client_handle_t client, int counter) {
  cJSON* root = cJSON_CreateObject();
  cJSON_AddStringToObject(root, "type", "sample");
  cJSON_AddNumberToObject(root, "counter", counter);
  cJSON_AddNumberToObject(root, "uptime_sec",
                          static_cast<double>(xTaskGetTickCount()) *
                              portTICK_PERIOD_MS / 1000.0);

  char* json_text = cJSON_PrintUnformatted(root);
  ESP_LOGI(TAG, "WebSocket Send: %s", json_text);
  esp_websocket_client_send_text(client, json_text, strlen(json_text),
                                 portMAX_DELAY);

  cJSON_free(json_text);
  cJSON_Delete(root);
}
}  // namespace

extern "C" void app_main() {
  ESP_LOGI(TAG, "Start");

  // WiFi接続
  WifiUtil::ConnectStaAndWait(CONFIG_DANGER_GADGET_WIFI_SSID, CONFIG_DANGER_GADGET_WIFI_PASSWORD);

  // I2C初期化
  i2c_master_bus_handle_t bus_handle =
      I2CUtil::InitializeMaster(static_cast<i2c_port_t>(CONFIG_DANGER_GADGET_GPIO_I2C_PORT_NO), static_cast<gpio_num_t>(CONFIG_DANGER_GADGET_GPIO_I2C_SDA), static_cast<gpio_num_t>(CONFIG_DANGER_GADGET_GPIO_I2C_SCL));

  // Input設定 (全ピン内部プルアップ有効のInputとして利用)
  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      g_mcp23017.SetInputOutput(group, gpio_no, true);
    }
  }

  // MCP23017設定・初期化 (IOCON.MIRROR有効化によりGroup Bの変化もINTAで通知される)
  g_mcp23017.Setup(bus_handle, CONFIG_DANGER_GADGET_MCP23017_I2C_ADDRESS);

  // 初期状態を取得
  CheckMCP23017Input();

  // MCP23017のINTAピン(GPIO19)をESP32側で監視し、変化検知時に読み直す
  GpioInputWatchTask gpio_watcher;
  gpio_watcher.AddMonitor(
      GpioInputWatchTask::GpioInfo(static_cast<gpio_num_t>(CONFIG_DANGER_GADGET_GPIO_MCP23017_INTA), &CheckMCP23017Input,
                                   nullptr),
      GpioInputWatchTask::PULL_UP_REGISTOR_ENABLE);
  gpio_watcher.Start();

  // WebSocketクライアント初期化
  esp_websocket_client_config_t websocket_config = {};
  websocket_config.uri = CONFIG_DANGER_GADGET_WEBSOCKET_URI;

  esp_websocket_client_handle_t websocket_client =
      esp_websocket_client_init(&websocket_config);
  esp_websocket_register_events(websocket_client, WEBSOCKET_EVENT_ANY,
                                &OnWebSocketEvent, nullptr);
  esp_websocket_client_start(websocket_client);

  int counter = 0;
  while (true) {
    if (esp_websocket_client_is_connected(websocket_client)) {
      SendSampleJson(websocket_client, counter);
      ++counter;
    }

    TickType_t lastWakeTime = xTaskGetTickCount();
    vTaskDelayUntil(&lastWakeTime, 5000 / portTICK_PERIOD_MS);
  }
}

// EOF
