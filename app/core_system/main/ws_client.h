#ifndef WS_CLIENT_H_
#define WS_CLIENT_H_
// Danger Gadget
// (C)2026 bekki.jp

// Include ----------------------
#include <esp_websocket_client.h>

#include <string>

namespace DangerGadget {

class WSClient final {
 public:
  WSClient();
  ~WSClient();

  void Connect();
  bool IsConnected();
  void Send(const std::string& data);
  void OnEvent(esp_event_base_t base,
                      int32_t event_id, void* event_data);

 private:
  esp_websocket_client_handle_t websocket_client_;
};

}  // namespace DangerGadget

#endif  // WS_CLIENT_H_
// EOF

