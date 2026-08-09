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

namespace {
/// PL9823デイジーチェーン制御GPIO・接続数
constexpr gpio_num_t kPl9823Gpio = Esp32Pin::kFullColorLed;
constexpr uint32_t kPl9823LedNum = 1;

/// kLine A-E のESP32 GPIO割り当て (ColorId順)
constexpr gpio_num_t kLineGpios[kColorNum] = {
    Esp32Pin::kLineA, Esp32Pin::kLineB, Esp32Pin::kLineC,
    Esp32Pin::kLineD, Esp32Pin::kLineE,
};

/// kPush A-E のMCP23017 GPIO番号 (全てGroup B)
constexpr uint8_t kPushGpios[kColorNum] = {
    Mcp23017Pin::kPushA, Mcp23017Pin::kPushB, Mcp23017Pin::kPushC,
    Mcp23017Pin::kPushD, Mcp23017Pin::kPushE,
};

/// ロータリー6接点のMCP23017 GPIO番号 (位置0-5の順。全てGroup A)
/// パネル表記0-5に対して、基板上は kRotary1-6 が対応する
constexpr uint8_t kRotaryGpios[kRotaryPositionNum] = {
    Mcp23017Pin::kRotary1, Mcp23017Pin::kRotary2, Mcp23017Pin::kRotary3,
    Mcp23017Pin::kRotary4, Mcp23017Pin::kRotary5, Mcp23017Pin::kRotary6,
};

/// 起動演出のタイミング
/// 内訳: スイープ 5x160=800ms + 全点灯 1200ms + 点滅 3x(2x140)=840ms ≒ 2.8秒
constexpr uint32_t kBootSweepStepMs = 160;  // 1色ずつ流す間隔
constexpr uint32_t kBootAllOnMs = 1200;     // 全点灯で見せる時間
constexpr uint32_t kBootFlashMs = 140;      // 点滅の点灯/消灯それぞれの時間
constexpr int kBootFlashCount = 3;

/// 起動演出中に7セグへ出す文字 ("8888" の全点灯)
constexpr uint8_t kSevenSegAllOn = 0xFF;
}  // namespace

System::System()
    : pl9823_task_(kPl9823Gpio, kPl9823LedNum),
      game_task_(&mcp23017_, &ht16k33_, &pl9823_task_,
                 CONFIG_CORE_SYSTEM_DEVICE_ID) {}

System::~System() = default;

void System::Start() {
  ESP_LOGI(TAG, "Start device_id=%s", CONFIG_CORE_SYSTEM_DEVICE_ID);

  // 起動インジケータを最優先で点ける。
  // 電源投入直後から「生きている」ことが目視で分かるようにする。
  // 紫点灯 = 初期化中、紫点滅 = 初期化失敗 (§起動演出)。
  pl9823_task_.Start();
  SetBootIndicator(kBootColorInitializing, Pl9823Task::PATTERN_SOLID);

  // I2C初期化 (LED演出に必要なので WiFi より先に行う)
  i2c_master_bus_handle_t bus_handle =
      I2CUtil::InitializeMaster(Esp32Pin::kI2cPortNo, Esp32Pin::kI2cSda, Esp32Pin::kI2cScl);
  if (bus_handle == nullptr) {
    ESP_LOGE(TAG, "I2C initialization failed");
    SetBootIndicator(kBootColorFailed, Pl9823Task::PATTERN_BLINK);
    return;
  }

  // Input/Output設定 (LED以外は全ピン内部プルアップ有効のInputとして利用)
  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      mcp23017_.SetInputOutput(group, gpio_no, true);
    }
  }
  for (const auto& output_pin : Mcp23017Pin::kOutputPins) {
    mcp23017_.SetInputOutput(output_pin[0], output_pin[1], false);
  }

  // MCP23017設定・初期化 (IOCON.MIRROR有効化によりGroup Bの変化もINTAで通知される)
  mcp23017_.Setup(bus_handle, I2cAddress::kMcp23017);

  // kLED A-Eを消灯状態から開始する
  for (const auto& led_pin : Mcp23017Pin::kOutputPins) {
    mcp23017_.SetOutputGpio(led_pin[0], led_pin[1], false, true);
  }

  // HT16K33設定・初期化 (発振器ON, 表示ON, 輝度最大, 表示クリア)
  ht16k33_.Setup(bus_handle, I2cAddress::kHt16k33);

  // 起動演出 (約3秒)。デバイス初期化が終わった直後に流す。
  // WiFi接続を待たないので、電源投入から早く始まる。
  PlayBootAnimation();

  // WiFi接続。会場のWiFiが未設営でも起動を止めない (タイムアウトあり)。
  // Core はセッション受信後、WiFiが切れても単体でゲームを完遂できる。
  if (!WifiUtil::ConnectStaAndWait(CONFIG_CORE_SYSTEM_WIFI_SSID,
                                   CONFIG_CORE_SYSTEM_WIFI_PASSWORD,
                                   kWifiConnectTimeoutMs)) {
    ESP_LOGE(TAG, "WiFi connection failed");
    SetBootIndicator(kBootColorFailed, Pl9823Task::PATTERN_BLINK);
    // 起動は続行する。WS接続は ws_client_ 側の再接続に委ねる。
  }

  // WebSocketの受信・接続状態をGameTaskのイベントとして流す (§8.1)
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

  // 入力の初期状態を取得してからGameTaskを起動する
  CheckMCP23017Input();

  game_task_.Start();

  // kLine A-E と MCP23017のINTAピンを監視する
  SetupLineWatchers();
  gpio_watcher_.AddMonitor(
      GpioInputWatchTask::GpioInfo(Esp32Pin::kMcp23017Interrupt,
                                   std::bind(&System::CheckMCP23017Input, this), nullptr),
      GpioInputWatchTask::PULL_UP_REGISTOR_ENABLE);
  gpio_watcher_.Start();

  // バッテリー電圧監視 (§8.5)
  battery_monitor_task_ = std::make_unique<BatteryMonitorTask>(
      [this](float voltage) { game_task_.UpdateBatteryVoltage(voltage); },
      [this]() {
        GameEvent event;
        event.type = EVENT_LOW_BATTERY;
        game_task_.PostEvent(event);
      });
  battery_monitor_task_->Start();

  // WebSocketクライアント接続 (再接続は esp_websocket_client が行う)
  ws_client_.Connect();

  // 以降の進行は各タスクが担うため、このタスクは待機するだけにする
  while (true) {
    vTaskDelay(pdMS_TO_TICKS(1000));
  }
}

/// kLine A-E のGPIO監視を登録する。
/// プルアップ有効で「GND接続の線が切れる → HIGH」で切断を検知する (§5.2)。
/// GpioInputWatchTask は 5ms x 3回 のエッジカウンタでデバウンスする。
void System::SetupLineWatchers() {
  for (int i = 0; i < kColorNum; ++i) {
    const ColorId color = static_cast<ColorId>(i);

    // on_up は「LOWで安定した = 結線」、on_down は「HIGHで安定した = 切断」
    auto on_connected = [this, color]() {
      GameEvent event;
      event.type = EVENT_LINE_CHANGED;
      event.color = color;
      event.level = true;
      game_task_.PostEvent(event);
    };
    auto on_cut = [this, color]() {
      GameEvent event;
      event.type = EVENT_LINE_CHANGED;
      event.color = color;
      event.level = false;
      game_task_.PostEvent(event);
    };

    gpio_watcher_.AddMonitor(
        GpioInputWatchTask::GpioInfo(kLineGpios[i], on_connected, on_cut),
        GpioInputWatchTask::PULL_UP_REGISTOR_ENABLE);
  }
}

/// 起動インジケータ (フルカラーLED) を設定する。
/// 紫点灯=初期化中 / 紫点滅=初期化失敗 (§4.0)。
void System::SetBootIndicator(const BootColor& color,
                              Pl9823Task::PatternType pattern) {
  Pl9823Task::Command command;
  command.pattern = pattern;
  command.r = color.r;
  command.g = color.g;
  command.b = color.b;
  command.on_ms = 300;
  command.off_ms = 300;
  pl9823_task_.SendCommand(command);
}

void System::PlayBootAnimation() {
  ESP_LOGI(TAG, "Boot animation");

  const auto sleep_ms = [](uint32_t ms) { vTaskDelay(pdMS_TO_TICKS(ms)); };

  // 1) kLED A-E を左から順に点けていく (点けたら消さないので、
  //    流れながら全点灯へ向かう)。同時に7セグの桁も1つずつ埋める。
  for (uint8_t i = 0; i < kColorNum; ++i) {
    const auto& pin = Mcp23017Pin::kLedPinsByColor[i];
    mcp23017_.SetOutputGpio(pin[0], pin[1], true, true);
    if (i < HT16K33::DIGIT_NUM) {
      ht16k33_.WriteDigitRaw(i, kSevenSegAllOn);
      ht16k33_.WriteDisplay();
    }
    sleep_ms(kBootSweepStepMs);
  }

  // 2) 全点灯でしばらく見せる
  sleep_ms(kBootAllOnMs);

  // 3) 全体を数回点滅させて締める
  for (int n = 0; n < kBootFlashCount; ++n) {
    for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
      mcp23017_.SetOutputGpio(pin[0], pin[1], false, true);
    }
    ht16k33_.Clear();
    ht16k33_.WriteDisplay();
    sleep_ms(kBootFlashMs);

    for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
      mcp23017_.SetOutputGpio(pin[0], pin[1], true, true);
    }
    for (uint8_t d = 0; d < HT16K33::DIGIT_NUM; ++d) {
      ht16k33_.WriteDigitRaw(d, kSevenSegAllOn);
    }
    ht16k33_.WriteDisplay();
    sleep_ms(kBootFlashMs);
  }

  // 4) 消灯して通常状態へ戻す。
  //    GameTask は全消灯を前提に描画を始めるため、必ず消してから抜ける。
  for (const auto& pin : Mcp23017Pin::kLedPinsByColor) {
    mcp23017_.SetOutputGpio(pin[0], pin[1], false, true);
  }
  ht16k33_.Clear();
  ht16k33_.WriteDisplay();
}

/// MCP23017の全ピンを読み直し、前回値と差分があったピンをGameEventとして通知する
void System::CheckMCP23017Input() {
  bool rotary_changed = false;

  for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
    // グループ単位でI2Cを読み直す
    mcp23017_.RefreshInputGroup(group);

    for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
      const bool level = mcp23017_.GetCachedInputGpio(group, gpio_no);
      if (has_last_level_ && level == last_level_[group][gpio_no]) {
        continue;
      }
      last_level_[group][gpio_no] = level;

      if (group == Mcp23017Pin::kGroupB) {
        // kPush: プルアップ入力のため LOW = 押下
        for (int i = 0; i < kColorNum; ++i) {
          if (kPushGpios[i] != gpio_no) {
            continue;
          }
          GameEvent event;
          event.type = EVENT_PUSH_CHANGED;
          event.color = static_cast<ColorId>(i);
          event.level = !level;
          game_task_.PostEvent(event);
        }
      }

      if (group == Mcp23017Pin::kGroupA) {
        for (const uint8_t rotary_gpio : kRotaryGpios) {
          if (rotary_gpio == gpio_no) {
            rotary_changed = true;
          }
        }
      }
    }
  }

  has_last_level_ = true;

  if (rotary_changed) {
    UpdateRotaryPosition();
  }
}

/// ロータリーは「ちょうど1つがLOW」のときだけ値を確定する。
/// 接点間の全OFF・複数ON等の過渡状態では最後に確定した値を保持する (§5.2)。
void System::UpdateRotaryPosition() {
  int8_t low_position = -1;
  int low_count = 0;

  for (int position = 0; position < kRotaryPositionNum; ++position) {
    if (!mcp23017_.GetCachedInputGpio(Mcp23017Pin::kGroupA, kRotaryGpios[position])) {
      low_position = static_cast<int8_t>(position);
      ++low_count;
    }
  }

  if (low_count != 1 || low_position == rotary_position_) {
    return;
  }
  rotary_position_ = low_position;

  GameEvent event;
  event.type = EVENT_ROTARY_CHANGED;
  event.rotary = low_position;
  game_task_.PostEvent(event);
}

} // namespace CoreSystem

// EOF
