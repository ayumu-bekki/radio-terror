#ifndef WS_CLIENT_H_
#define WS_CLIENT_H_
// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include <esp_websocket_client.h>

#include <functional>
#include <string>

namespace CoreSystem {

class WSClient final {
 public:
  /// 受信テキスト・接続状態変化の通知先。イベントハンドラを軽く保つため、
  /// 受信文字列はそのまま渡してパースは呼び出し側 (GameTask) で行う。
  using MessageHandler = std::function<void(const std::string&)>;
  using ConnectionHandler = std::function<void(bool connected)>;

 public:
  WSClient();
  ~WSClient();

  void Connect();
  bool IsConnected();
  void Send(const std::string& data);
  void OnEvent(esp_event_base_t base,
                      int32_t event_id, void* event_data);

  void SetMessageHandler(MessageHandler handler) {
    message_handler_ = std::move(handler);
  }
  void SetConnectionHandler(ConnectionHandler handler) {
    connection_handler_ = std::move(handler);
  }

 private:
  esp_websocket_client_handle_t websocket_client_;
  MessageHandler message_handler_;
  ConnectionHandler connection_handler_;

  /// 分割受信 (WEBSOCKET_EVENT_DATA が複数回に分かれる) の組み立てバッファ
  std::string receive_buffer_;
};

}  // namespace CoreSystem

#endif  // WS_CLIENT_H_
// EOF

