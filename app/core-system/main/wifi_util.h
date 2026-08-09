#ifndef WIFI_UTIL_H_
#define WIFI_UTIL_H_
// (C)2024 bekki.jp
// WiFi Utilities

#include <cstdint>

namespace WifiUtil {

/// WiFi(STA)に接続し、IP取得まで待機する。
///
/// timeout_ms 以内に接続できなければ false を返す。会場のWi-Fiが
/// 用意できていない場合でも起動を止めないため、無限待ちにはしない。
/// Core はセッション受信後、Wi-Fiが切れても単体でゲームを完遂できる
/// (docs/game_session_design.md) ため、接続失敗でも起動は続行する。
bool ConnectStaAndWait(const char* ssid, const char* password,
                       uint32_t timeout_ms);

}  // namespace WifiUtil

#endif  // WIFI_UTIL_H_

// EOF
