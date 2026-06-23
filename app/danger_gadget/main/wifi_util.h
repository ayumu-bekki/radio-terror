#ifndef WIFI_UTIL_H_
#define WIFI_UTIL_H_
// (C)2024 bekki.jp
// WiFi Utilities

namespace WifiUtil {

/// WiFi(STA)に接続し、接続完了まで待機する
void ConnectStaAndWait(const char* ssid, const char* password);

}  // namespace WifiUtil

#endif  // WIFI_UTIL_H_

// EOF
