#ifndef CORE_SYSTEM_H_
#define CORE_SYSTEM_H_
// Core System
// (C)2026 bekki.jp

// Include ----------------------
// CONFIG_* を参照するため、条件付き include より先に読む
#include <sdkconfig.h>

#if CONFIG_CORE_SYSTEM_BATTERY_MONITOR
#include "battery_monitor_task.h"
#endif

#include "boot_animation.h"
#include "game_task.h"
#include "gpio_input_watch_task.h"
#include "ht16k33.h"
#include "mcp23017.h"
#include "mcp_input_scanner.h"
#include "pl9823_task.h"
#include "ws_client.h"

#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <esp_event_base.h>
#include <memory>


namespace CoreSystem {

/// WiFi接続のタイムアウト。会場のWiFiが未設営でも起動を止めない。
inline constexpr uint32_t kWifiConnectTimeoutMs = 15000;

/// 起動時の kLine 読み取り (§4)。
///
/// 内部プルアップの有効化直後はピンの電圧が安定しておらず、
/// そのまま読むと誤った値を拾う。落ち着くまで待ってから、
/// 複数回一致した場合だけ確定する。

/// プルアップ設定後、電圧が安定するまでの待ち時間
inline constexpr uint32_t kLineSettleMs = 50;

/// 1本あたりの読み取り回数 (全て一致したら確定)
inline constexpr int kLineReadSamples = 3;

/// 読み取りの間隔
inline constexpr uint32_t kLineReadIntervalMs = 5;

/// 各デバイス・タスクを組み立てて起動する。
///
/// 起動演出は BootAnimation、MCP23017 の入力読み取りは McpInputScanner が担う。
/// この class 自身は組み立てと配線に専念し、状態を持たない。
class System final
    : public std::enable_shared_from_this<System> {
 public:
  System();
  ~System();

  void Start();

 private:
  /// I2C・MCP23017・HT16K33 を初期化する。失敗したら false。
  /// LED演出に必要なため WiFi より先に行う (§4.0)。
  bool InitializeDevices();

  /// WiFiへ接続する。失敗しても起動は続行するので戻り値は表示用。
  bool ConnectWifi();

  /// WSClient ⇔ GameTask のイベント経路を繋ぐ (§8.1)
  void WireEventRoutes();

  /// 入力監視・ゲーム・バッテリー監視の各タスクを起動する
  void StartTasks();

  /// kLine A-E のGPIO監視を GpioInputWatchTask へ登録する
  void SetupLineWatchers();

  /// 起動時の kLine A-E の状態を GameTask へ通知する。
  /// 監視は変化しか通知しないため、初期状態はここで読んで流し込む
  void ReportInitialLineStates();

  /// GPIO を複数回読み、一致した場合だけ結線状態を確定する。
  /// 安定しなければ false を返す (呼び出し側で通知を見送る)
  static bool ReadLineStable(gpio_num_t gpio_no, bool* connected);

 private:
  MCP23017 mcp23017_;
  HT16K33 ht16k33_;
  Pl9823Task pl9823_task_;
  BootAnimation boot_animation_;
  GameTask game_task_;
  McpInputScanner input_scanner_;
  WSClient ws_client_;
  GpioInputWatchTask gpio_watcher_;
#if CONFIG_CORE_SYSTEM_BATTERY_MONITOR
  std::unique_ptr<BatteryMonitorTask> battery_monitor_task_;
#endif
};

}  // namespace CoreSystem

#endif  // CORE_SYSTEM_H_
// EOF
