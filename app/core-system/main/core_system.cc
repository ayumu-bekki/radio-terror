// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "core_system.h"

#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <driver/gpio.h>

#include <string>

#include "hardware_config.h"
#include "i2c_util.h"
#include "logger.h"
#include "wifi_util.h"

namespace CoreSystem {

System::System()
    : pl9823_task_(Esp32Pin::kFullColorLed, Esp32Pin::kFullColorLedNum),
      boot_animation_(&mcp23017_, &ht16k33_, &pl9823_task_),
      game_task_(&mcp23017_, &ht16k33_, &pl9823_task_,
                 CONFIG_CORE_SYSTEM_DEVICE_ID),
      input_scanner_(&mcp23017_,
                     [this](const GameEvent& event) { game_task_.PostEvent(event); }) {}

System::~System() = default;

void System::Start() {
  ESP_LOGI(TAG, "Start device_id=%s", CONFIG_CORE_SYSTEM_DEVICE_ID);

  // 起動インジケータを最優先で点ける。
  // 電源投入直後から「生きている」ことが目視で分かるようにする。
  // 紫点灯 = 初期化中、紫点滅 = 初期化失敗 (§4.0)。
  //
  // この紫は GameTask 起動後も引き継がれる。Setup かつサーバー未接続の間は
  // StatusIndicator が同じ紫を出すため、接続できるまで点灯したままになる。
  pl9823_task_.Start();
  boot_animation_.SetIndicator(kBootColorInitializing, Pl9823Task::PATTERN_SOLID);

  if (!InitializeDevices()) {
    boot_animation_.SetIndicator(kBootColorFailed, Pl9823Task::PATTERN_BLINK);
    return;
  }

  // 起動演出 (約2.8秒)。デバイス初期化が終わった直後に流す。
  // WiFi接続を待たないので、電源投入から早く始まる。
  boot_animation_.Play();

  if (!ConnectWifi()) {
    boot_animation_.SetIndicator(kBootColorFailed, Pl9823Task::PATTERN_BLINK);
    // 起動は続行する。WS接続は ws_client_ 側の再接続に委ねる。
    //
    // GameTask 起動後もサーバー待ちの表示を点滅のまま保つ。
    // 起動前に伝えるので、EnterSetup() の初回描画から反映される。
    game_task_.SetWifiFailed(true);
  }

  WireEventRoutes();
  StartTasks();

  // WebSocketクライアント接続 (再接続は esp_websocket_client が行う)
  ws_client_.Connect();

  // 以降の進行は各タスクが担うため、このタスクは待機するだけにする
  while (true) {
    vTaskDelay(pdMS_TO_TICKS(1000));
  }
}

/// I2C・MCP23017・HT16K33 を初期化する。
/// LED演出に必要なので WiFi より先に行う (§4.0)。
bool System::InitializeDevices() {
  i2c_master_bus_handle_t bus_handle =
      I2CUtil::InitializeMaster(Esp32Pin::kI2cPortNo, Esp32Pin::kI2cSda, Esp32Pin::kI2cScl);
  if (bus_handle == nullptr) {
    ESP_LOGE(TAG, "I2C initialization failed");
    return false;
  }

  // Input/Output設定 (LED以外は全ピン内部プルアップ有効のInputとして利用)
  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      mcp23017_.SetInputOutput(group, gpio_no, true);
    }
  }
  for (const auto& led_pin : Mcp23017Pin::kLedPinsByColor) {
    mcp23017_.SetInputOutput(led_pin[0], led_pin[1], false);
  }

  // MCP23017設定・初期化 (IOCON.MIRROR有効化によりGroup Bの変化もINTAで通知される)
  mcp23017_.Setup(bus_handle, I2cAddress::kMcp23017);

  // kLED A-Eを消灯状態から開始する
  for (const auto& led_pin : Mcp23017Pin::kLedPinsByColor) {
    mcp23017_.SetOutputGpio(led_pin[0], led_pin[1], false, true);
  }

  // HT16K33設定・初期化 (発振器ON, 表示ON, 輝度最大, 表示クリア)
  ht16k33_.Setup(bus_handle, I2cAddress::kHt16k33);

  return true;
}

/// WiFi接続。会場のWiFiが未設営でも起動を止めない (タイムアウトあり)。
/// Core はセッション受信後、WiFiが切れても単体でゲームを完遂できる。
bool System::ConnectWifi() {
  if (WifiUtil::ConnectStaAndWait(CONFIG_CORE_SYSTEM_WIFI_SSID,
                                  CONFIG_CORE_SYSTEM_WIFI_PASSWORD,
                                  kWifiConnectTimeoutMs)) {
    return true;
  }
  ESP_LOGE(TAG, "WiFi connection failed");
  return false;
}

/// WebSocketの受信・接続状態をGameTaskのイベントとして流す (§8.1)
void System::WireEventRoutes() {
  ws_client_.SetMessageHandler([this](const std::string& message) {
    GameEvent event;
    event.type = EVENT_WS_MESSAGE;
    // 所有権をGameTaskへ渡す。キューが満杯で積めなかった場合はここで解放する
    event.payload = new std::string(message);
    if (!game_task_.PostEvent(event)) {
      ESP_LOGW(TAG, "GameTask queue full: dropping WS message");
      delete event.payload;
    }
  });
  ws_client_.SetConnectionHandler([this](bool connected) {
    GameEvent event;
    event.type = connected ? EVENT_WS_CONNECTED : EVENT_WS_DISCONNECTED;
    game_task_.PostEvent(event);
  });

  // GameTaskからの送信をWSClientへ繋ぐ
  game_task_.SetSendFunc([this](const std::string& data) {
    if (ws_client_.IsConnected()) {
      ws_client_.Send(data);
    }
  });
}

/// 入力監視・ゲーム・バッテリー監視の各タスクを起動する。
void System::StartTasks() {
  // 入力の初期状態を取得してからGameTaskを起動する
  input_scanner_.Scan();

  game_task_.Start();

  // kLine A-E と MCP23017のINTAピンを監視する
  SetupLineWatchers();

  // 監視は「変化」しか通知しないため、起動時に切れている線は
  // イベントが来ない。プルアップ設定後の実レベルを読んで初期状態を渡す
  // (これが無いと、線が切れたまま Ready になる。§4)
  ReportInitialLineStates();
  // MCP23017 の INTA。**エッジではなくレベルで見る** (§5.2)。
  //
  // MCP23017 の割り込みは GPIO を読み出すまでクリアされず、その間 INTA は
  // LOW に張り付く。一方 GpioInfo の on_up_ は「LOW が3サンプル続いた瞬間」の
  // 1回しか呼ばれないため、読み出す前に次の入力変化が起きると
  // **INTA が LOW のまま次の通知も来ず、入力検知が永久に止まる**
  // (ロータリーを速く回すと検知ログが出なくなる不具合として実機で発生した)。
  //
  // AddMonitor は INTA ピンのプルアップ設定のために残し、走査自体は
  // 毎周期のレベル確認 (ScanIfPending) で行う。取りこぼしても次の周期で拾える。
  gpio_watcher_.AddMonitor(
      GpioInputWatchTask::GpioInfo(Esp32Pin::kMcp23017Interrupt, nullptr,
                                   nullptr),
      GpioInputWatchTask::PULL_UP_REGISTOR_ENABLE);
  gpio_watcher_.SetPollHandler([this]() { input_scanner_.ScanIfPending(); });
  gpio_watcher_.Start();

  // バッテリー電圧監視 (§8.5)。
  // USB給電など電池を繋がない構成では menuconfig で無効にできる
  // (分圧回路の入力が不定になり、低電圧と誤判定するため)。
#if CONFIG_CORE_SYSTEM_BATTERY_MONITOR
  battery_monitor_task_ = std::make_unique<BatteryMonitorTask>(
      [this](float voltage) { game_task_.UpdateBatteryVoltage(voltage); },
      [this]() {
        GameEvent event;
        event.type = EVENT_LOW_BATTERY;
        game_task_.PostEvent(event);
      });
  battery_monitor_task_->Start();
#else
  ESP_LOGW(TAG, "battery monitor disabled (CONFIG_CORE_SYSTEM_BATTERY_MONITOR=n)");
#endif
}

/// 起動時の kLine A-E の状態を GameTask へ通知する。
///
/// GpioInputWatchTask は**レベルの変化**を通知する汎用部品なので、
/// 起動時点で既に切れている線は通知されない。プルアップ有効化後の
/// 実レベルを直接読んで、初期状態を1回だけ流し込む。
///
/// SetupLineWatchers() の後に呼ぶこと (AddMonitor がプルアップを設定するため、
/// それより前に読むと未設定のレベルを拾う)。
void System::ReportInitialLineStates() {
  // 内部プルアップを有効にした直後はピンの電圧が安定していない。
  // ライン容量があると立ち上がりが遅く、そのまま読むと誤った値を拾う。
  // GpioInputWatchTask のデバウンス (5ms x 3回) と同等の余裕を取る。
  vTaskDelay(pdMS_TO_TICKS(kLineSettleMs));

  for (int i = 0; i < kColorNum; ++i) {
    // 複数回読んで一致した場合のみ確定する (単発のノイズを拾わない)
    bool connected = false;
    if (!ReadLineStable(Esp32Pin::kLineGpiosByColor[i], &connected)) {
      // 安定しなかった線は通知しない。切断されていれば GPIO 監視が
      // 変化を捉えるまで待つ (誤った初期値を入れるより無通知の方が安全)
      ESP_LOGW(TAG, "line %c: unstable at boot, skipped", 'A' + i);
      continue;
    }

    ESP_LOGI(TAG, "line %c initial: %s", 'A' + i, connected ? "connected" : "cut");

    GameEvent event;
    event.type = EVENT_LINE_CHANGED;
    event.color = static_cast<ColorId>(i);
    event.level = connected;
    game_task_.PostEvent(event);
  }
}

/// GPIO を複数回読み、全て一致した場合だけ確定する。
/// プルアップ入力なので LOW=結線, HIGH=切断 (§5.2)。
bool System::ReadLineStable(gpio_num_t gpio_no, bool* connected) {
  const bool first = gpio_get_level(gpio_no) == 0;

  for (int i = 1; i < kLineReadSamples; ++i) {
    vTaskDelay(pdMS_TO_TICKS(kLineReadIntervalMs));
    if ((gpio_get_level(gpio_no) == 0) != first) {
      return false;
    }
  }

  *connected = first;
  return true;
}

/// kLine A-E のGPIO監視を登録する。
/// プルアップ有効で「GND接続の線が切れる → HIGH」で切断を検知する (§5.2)。
/// GpioInputWatchTask は 5ms x 3回 のエッジカウンタでデバウンスする。
void System::SetupLineWatchers() {
  for (int i = 0; i < kColorNum; ++i) {
    const ColorId color = static_cast<ColorId>(i);

    // on_up は「LOWで安定した = 結線」、on_down は「HIGHで安定した = 切断」
    auto post_line_event = [this, color](bool connected) {
      GameEvent event;
      event.type = EVENT_LINE_CHANGED;
      event.color = color;
      event.level = connected;
      game_task_.PostEvent(event);
    };

    gpio_watcher_.AddMonitor(
        GpioInputWatchTask::GpioInfo(
            Esp32Pin::kLineGpiosByColor[i],
            [post_line_event]() { post_line_event(true); },
            [post_line_event]() { post_line_event(false); }),
        GpioInputWatchTask::PULL_UP_REGISTOR_ENABLE);
  }
}

} // namespace CoreSystem

// EOF
