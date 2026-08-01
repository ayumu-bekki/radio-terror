// Danger Gadget
// (C)2026 bekki.jp

// Include ----------------------
#include "ws_client.h"

#include "logger.h"

namespace {

void OnWebSocketEvent(void* handler_args, esp_event_base_t base,
                      int32_t event_id, void* event_data) {
  if (handler_args) {
    static_cast<DangerGadget::WSClient*>(handler_args)->OnEvent(base, event_id, event_data);
  }
}

}


namespace DangerGadget {

WSClient::WSClient() = default;

WSClient::~WSClient() = default;

void WSClient::Connect() {
  esp_websocket_client_config_t websocket_config = {};
  websocket_config.uri = CONFIG_DANGER_GADGET_WEBSOCKET_URI;

  websocket_client_ = esp_websocket_client_init(&websocket_config);
  esp_websocket_register_events(websocket_client_, WEBSOCKET_EVENT_ANY,
                                &::OnWebSocketEvent, this);
  esp_websocket_client_start(websocket_client_);
}

bool WSClient::IsConnected() {
  return esp_websocket_client_is_connected(websocket_client_);
}

void WSClient::Send(const std::string& data) {
  esp_websocket_client_send_text(websocket_client_, data.c_str(), data.length(),
                                 portMAX_DELAY);
}

void WSClient::OnEvent(esp_event_base_t base,
                    int32_t event_id, void* event_data) {
  if (event_id == WEBSOCKET_EVENT_CONNECTED) {
    ESP_LOGI(TAG, "WebSocket Connected");
  } else if (event_id == WEBSOCKET_EVENT_DISCONNECTED) {
    ESP_LOGI(TAG, "WebSocket Disconnected");
  } else if (event_id == WEBSOCKET_EVENT_ERROR) {
    ESP_LOGW(TAG, "WebSocket Error");
  } else if (event_id == WEBSOCKET_EVENT_DATA) {
    auto* data = static_cast<esp_websocket_event_data_t*>(event_data);
    if (0 < data->data_len) {
      ESP_LOGI(TAG, "WebSocket Received: %.*s", data->data_len,
               data->data_ptr);
    }
  }
}


} // namespace DangerGadget

// EOF

