#ifndef PL9823_TASK_H_
#define PL9823_TASK_H_
// Danger Gadget
// (C)2026 bekki.jp
// PL9823 (デイジーチェーン接続フルカラーLED) 制御タスク

// Include ----------------------
#include <driver/gpio.h>
#include <freertos/FreeRTOS.h>
#include <led_strip.h>

#include <algorithm>

#include "logger.h"
#include "message_queue.h"
#include "task.h"

/// PL9823をRMTペリフェラル経由(led_strip、WS2812タイミング流用)でデイジーチェーン制御するタスク
/// 注意:PL9823専用のタイミングモデルはled_stripに存在しないため、GRB順・閾値判定式で互換性のある
///     LED_MODEL_WS2812を流用する。
class Pl9823Task final : public Task {
 public:
  static constexpr std::string_view TASK_NAME = "Pl9823Task";
  static constexpr int32_t PRIORITY = Task::PRIORITY_LOW;
  static constexpr int32_t CORE_ID = PRO_CPU_NUM;

  /// 点灯パターン
  enum PatternType {
    PATTERN_OFF,
    PATTERN_SOLID,
    PATTERN_BLINK,
  };

  /// 外部スレッドから送るコマンド (全LED共通の色・パターンを指定する)
  struct Command {
    PatternType pattern = PATTERN_OFF;
    uint8_t r = 0;
    uint8_t g = 0;
    uint8_t b = 0;
    uint32_t on_ms = 500;  // PATTERN_BLINK時の点灯時間
    uint32_t off_ms = 0;   // PATTERN_BLINK時の消灯時間 (0の場合はon_msと同じ値を使う)
  };

 public:
  Pl9823Task(gpio_num_t gpio_no, uint32_t led_num)
      : Task(std::string(TASK_NAME).c_str(), PRIORITY, CORE_ID),
        gpio_no_(gpio_no),
        led_num_(led_num),
        led_strip_(nullptr),
        message_queue_(),
        command_(),
        blink_on_(false) {
    constexpr int32_t MESSAGE_QUEUE_SIZE = 4;
    if (!message_queue_.Create(MESSAGE_QUEUE_SIZE)) {
      ESP_LOGE(TAG, "Pl9823Task: Creating queue failed");
    }
  }

  ~Pl9823Task() {
    if (led_strip_) {
      led_strip_del(led_strip_);
      led_strip_ = nullptr;
    }
    message_queue_.Destroy();
  }

  void Initialize() override {
    ESP_LOGI(TAG, "Start Pl9823Task");

    led_strip_config_t strip_config = {};
    strip_config.strip_gpio_num = gpio_no_;
    strip_config.max_leds = led_num_;
    strip_config.led_model = LED_MODEL_WS2812;
    // PL9823は送信順がRGB (WS2812標準のGRBとは異なる) のためRGBを指定
    strip_config.color_component_format = LED_STRIP_COLOR_COMPONENT_FMT_RGB;
    strip_config.flags.invert_out = false;

    led_strip_rmt_config_t rmt_config = {};
    rmt_config.resolution_hz = 10 * 1000 * 1000;  // 10MHz
    rmt_config.flags.with_dma = false;

    const esp_err_t err =
        led_strip_new_rmt_device(&strip_config, &rmt_config, &led_strip_);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "led_strip_new_rmt_device failed err:%s",
               esp_err_to_name(err));
      return;
    }
    led_strip_clear(led_strip_);
  }

  void Update() override {
    Command received_command;
    constexpr int32_t QUEUE_RECEIVE_LIMIT_BASE =
        1000 / portTICK_PERIOD_MS;  // コマンド待ちなしの場合の最大待機tick
    int32_t wait_ms = QUEUE_RECEIVE_LIMIT_BASE;
    if (command_.pattern == PATTERN_BLINK) {
      wait_ms = std::max<int32_t>(
          blink_on_ ? command_.on_ms : GetEffectiveOffMs(), 1);
    }

    if (message_queue_.ReceiveWait(&received_command, wait_ms)) {
      command_ = received_command;
      blink_on_ = true;
      ApplyCurrentState();
      return;
    }

    // タイムアウト(コマンドが無かった場合)は点滅タスクの時間経過処理を行う
    if (command_.pattern == PATTERN_BLINK) {
      blink_on_ = !blink_on_;
      ApplyCurrentState();
    }
  }

  /// 外部スレッドからパターン・色・点滅間隔を指定する
  bool SendCommand(const Command& command) {
    return message_queue_.Send(command);
  }

 private:
  /// off_msが未指定(0)の場合はon_msと同じ値を消灯時間として使う
  uint32_t GetEffectiveOffMs() const {
    return command_.off_ms != 0 ? command_.off_ms : command_.on_ms;
  }

  /// 現在のコマンド・点滅位相に応じてLEDへ反映する
  void ApplyCurrentState() {
    if (!led_strip_) {
      return;
    }

    const bool light_on =
        (command_.pattern == PATTERN_SOLID) ||
        (command_.pattern == PATTERN_BLINK && blink_on_);

    if (light_on) {
      for (uint32_t i = 0; i < led_num_; ++i) {
        led_strip_set_pixel(led_strip_, i, command_.r, command_.g,
                            command_.b);
      }
    } else {
      led_strip_clear(led_strip_);
    }
    led_strip_refresh(led_strip_);
  }

 private:
  gpio_num_t gpio_no_;
  uint32_t led_num_;
  led_strip_handle_t led_strip_;
  MessageQueue<Command> message_queue_;
  Command command_;
  bool blink_on_;
};

#endif  // PL9823_TASK_H_

// EOF
