// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "ws_client.h"

#include "logger.h"

namespace {

void OnWebSocketEvent(void* handler_args, esp_event_base_t base,
                      int32_t event_id, void* event_data) {
  if (handler_args) {
    static_cast<CoreSystem::WSClient*>(handler_args)->OnEvent(base, event_id, event_data);
  }
}

}


namespace CoreSystem {

WSClient::WSClient() = default;

WSClient::~WSClient() = default;

void WSClient::Connect() {
  esp_websocket_client_config_t websocket_config = {};
  websocket_config.uri = CONFIG_CORE_SYSTEM_WEBSOCKET_URI;

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
    receive_buffer_.clear();
    if (connection_handler_) {
      connection_handler_(true);
    }
  } else if (event_id == WEBSOCKET_EVENT_DISCONNECTED) {
    ESP_LOGI(TAG, "WebSocket Disconnected");
    receive_buffer_.clear();
    if (connection_handler_) {
      connection_handler_(false);
    }
  } else if (event_id == WEBSOCKET_EVENT_ERROR) {
    ESP_LOGW(TAG, "WebSocket Error");
  } else if (event_id == WEBSOCKET_EVENT_DATA) {
    auto* data = static_cast<esp_websocket_event_data_t*>(event_data);

    // テキストフレーム(opcode 0x1)と継続フレーム(0x0)のみ扱う。
    // Ping/Pong等の制御フレームはここでは無視する。
    if (data->op_code != 0x01 && data->op_code != 0x00) {
      return;
    }

    if (0 < data->data_len) {
      receive_buffer_.append(data->data_ptr, data->data_len);
    }

    // 分割受信の途中はメッセージが揃うまで待つ
    if (data->payload_offset + data->data_len < data->payload_len) {
      return;
    }

    if (!receive_buffer_.empty()) {
      ESP_LOGI(TAG, "WebSocket Received: %s", receive_buffer_.c_str());
      if (message_handler_) {
        message_handler_(receive_buffer_);
      }
    }
    receive_buffer_.clear();
  }
}


} // namespace CoreSystem

// EOF

