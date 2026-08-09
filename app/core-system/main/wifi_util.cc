// (C)2024 bekki.jp
// WiFi Utilities

// Include ----------------------
#include "wifi_util.h"

#include <cstring>

#include <esp_event.h>
#include <esp_wifi.h>
#include <freertos/FreeRTOS.h>
#include <freertos/event_groups.h>
#include <nvs_flash.h>

#include "logger.h"

namespace WifiUtil {
namespace {

constexpr int WIFI_CONNECTED_BIT = BIT0;

EventGroupHandle_t g_wifi_event_group = nullptr;

void OnWifiEvent(void* arg, esp_event_base_t event_base, int32_t event_id,
                 void* event_data) {
  if (event_base == WIFI_EVENT && event_id == WIFI_EVENT_STA_START) {
    esp_wifi_connect();
  } else if (event_base == WIFI_EVENT &&
             event_id == WIFI_EVENT_STA_DISCONNECTED) {
    ESP_LOGI(TAG, "WiFi Disconnected. Retry to connect");
    esp_wifi_connect();
  } else if (event_base == IP_EVENT && event_id == IP_EVENT_STA_GOT_IP) {
    xEventGroupSetBits(g_wifi_event_group, WIFI_CONNECTED_BIT);
  }
}

}  // namespace

void ConnectStaAndWait(const char* ssid, const char* password) {
  ESP_ERROR_CHECK(nvs_flash_init());
  ESP_ERROR_CHECK(esp_netif_init());
  ESP_ERROR_CHECK(esp_event_loop_create_default());
  esp_netif_create_default_wifi_sta();

  g_wifi_event_group = xEventGroupCreate();

  wifi_init_config_t init_config = WIFI_INIT_CONFIG_DEFAULT();
  ESP_ERROR_CHECK(esp_wifi_init(&init_config));

  ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID,
                                             &OnWifiEvent, nullptr));
  ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP,
                                             &OnWifiEvent, nullptr));

  wifi_config_t wifi_config = {};
  std::strncpy(reinterpret_cast<char*>(wifi_config.sta.ssid), ssid,
              sizeof(wifi_config.sta.ssid) - 1);
  std::strncpy(reinterpret_cast<char*>(wifi_config.sta.password), password,
              sizeof(wifi_config.sta.password) - 1);

  ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
  ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wifi_config));
  ESP_ERROR_CHECK(esp_wifi_start());

  ESP_LOGI(TAG, "Connecting to WiFi SSID:%s ...", ssid);
  xEventGroupWaitBits(g_wifi_event_group, WIFI_CONNECTED_BIT, pdFALSE,
                      pdTRUE, portMAX_DELAY);
  ESP_LOGI(TAG, "WiFi Connected");
}

}  // namespace WifiUtil

// EOF
