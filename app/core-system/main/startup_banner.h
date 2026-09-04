#ifndef STARTUP_BANNER_H_
#define STARTUP_BANNER_H_
// Core System
// (C)2026 bekki.jp
// 起動ログ (ADR P-7 と同じ狙いを core-system 側でも行う)

// Include ----------------------
// CONFIG_* を参照するため他より先に読む
#include <sdkconfig.h>

#include <esp_app_desc.h>
#include <esp_mac.h>
#include <esp_system.h>

#if CONFIG_CORE_SYSTEM_BATTERY_MONITOR
// 閾値を表示するためだけに読む。core_system.h と同じく条件付きにして、
// 無効ビルドで ADC ドライバを引き込まないようにする
#include "battery_monitor_task.h"
#endif

#include "logger.h"
// CMake が git describe から生成する。**リポジトリの位置を一意に指す**ため、
// 「直したのに反映されない」の切り分けはこれで足りる (ADR P-7 と同じ)
#include "version.h"

namespace CoreSystem {

/// 起動時に「どのファームが、どの設定で動いているか」を出す。
///
/// game-server は同じ狙いの起動ログを持っている (ADR P-7)。Core は
/// **menuconfig で作った sdkconfig がファームに焼き込まれる**ため、
/// 現物を見ても設定が分からない。複数台を並べて運用するので、
/// 「この個体はどのCoreIDか」「電池監視を切ったビルドはどれか」を
/// シリアルログだけで判別できるようにする。
///
/// **接続先とバッテリー監視・ブザーの有効無効を必ず出す。** どれも
/// 間違えても起動は成功してしまい、症状が出るのは体験の最中になる
/// (USB給電で電池監視を有効のまま焼くと起動直後に Deep Sleep へ入る。
/// Kconfig.projbuild の help 参照)。
///
/// WiFiパスワードは出さない。ログは画面共有や記録に残るため。
namespace StartupBanner {

/// 未設定の文字列を「空」と分かる形にする。
/// SSID を設定し忘れたビルドは、接続失敗のログより先にここで気付ける。
inline const char* DescribeOrUnset(const char* value) {
  if (value == nullptr || value[0] == '\0') {
    return "(unset)";
  }
  return value;
}

/// 機能の有効無効。#if で切り替わるので値ではなく引数で受ける。
inline const char* DescribeEnabled(bool enabled) {
  if (enabled) {
    return "enabled";
  }
  return "disabled";
}

/// 起動ログを出力する。app_main の最初に呼ぶ。
///
/// ヘッダー行を挟むのは、ESP-IDF 自身のブートログ (パーティション表・
/// ヒープ・各ドライバの初期化) に埋もれるため。区切りが無いと
/// **どこからがアプリの情報か**を目で追えない。
inline void Log() {
  const esp_app_desc_t* app_desc = esp_app_get_description();

  // MACアドレスは個体を一意に指す。CoreIDの設定ミスで同じIDの個体が
  // 2台できたとき、どちらのログかをこれで切り分ける
  uint8_t mac[6] = {};
  esp_read_mac(mac, ESP_MAC_WIFI_STA);

  ESP_LOGI(TAG, "======== RADIO TERROR core-system boot ========");
  ESP_LOGI(TAG, "[boot] firmware: %s / commit: %s", app_desc->project_name,
           GIT_VERSION);
  ESP_LOGI(TAG, "[boot] built: %s %s", app_desc->date, app_desc->time);
  ESP_LOGI(TAG, "[boot] idf: %s / chip: %s / mac: %02x:%02x:%02x:%02x:%02x:%02x",
           app_desc->idf_ver, CONFIG_IDF_TARGET, mac[0], mac[1], mac[2], mac[3],
           mac[4], mac[5]);
  ESP_LOGI(TAG, "[boot] core id: %s", CONFIG_CORE_SYSTEM_DEVICE_ID);
  ESP_LOGI(TAG, "[boot] wifi ssid: %s",
           DescribeOrUnset(CONFIG_CORE_SYSTEM_WIFI_SSID));
  ESP_LOGI(TAG, "[boot] server uri: %s",
           DescribeOrUnset(CONFIG_CORE_SYSTEM_WEBSOCKET_URI));

  // 電池監視は閾値まで出す。「電池が減ってきたのか設定が違うのか」を
  // 現場で判断するのに、有効無効だけでは足りない
#if CONFIG_CORE_SYSTEM_BATTERY_MONITOR
  ESP_LOGI(TAG, "[boot] battery monitor: %s (deep sleep below %.1fV)",
           DescribeEnabled(true), BatteryMonitorTask::kLowVoltageThreshold);
#else
  ESP_LOGI(TAG, "[boot] battery monitor: %s", DescribeEnabled(false));
#endif

#if CONFIG_CORE_SYSTEM_BUZZER
  ESP_LOGI(TAG, "[boot] buzzer: %s", DescribeEnabled(true));
#else
  ESP_LOGI(TAG, "[boot] buzzer: %s", DescribeEnabled(false));
#endif

  ESP_LOGI(TAG, "==============================================");
}

}  // namespace StartupBanner

}  // namespace CoreSystem

#endif  // STARTUP_BANNER_H_
// EOF
