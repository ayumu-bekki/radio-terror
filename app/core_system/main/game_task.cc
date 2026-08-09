// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "game_task.h"

#include <esp_random.h>
#include <esp_sleep.h>
#include <esp_task_wdt.h>
#include <freertos/FreeRTOS.h>
#include <freertos/task.h>

#include <cstring>

#include "hardware_config.h"
#include "logger.h"

namespace CoreSystem {

namespace {

/// kLED A-E の MCP23017 上の配置 ({group, gpio_no})
constexpr uint8_t kLedPins[kColorNum][2] = {
    {Mcp23017Pin::kGroupA, Mcp23017Pin::kLedA},
    {Mcp23017Pin::kGroupA, Mcp23017Pin::kLedB},
    {Mcp23017Pin::kGroupB, Mcp23017Pin::kLedC},
    {Mcp23017Pin::kGroupB, Mcp23017Pin::kLedD},
    {Mcp23017Pin::kGroupB, Mcp23017Pin::kLedE},
};

/// フルカラーLEDの色 (§4.1)
constexpr uint8_t kFullColorBrightness = 128;

/// push_seq の入力フィードバック表示時間
constexpr int32_t kPushSeqFeedbackMs = 200;

/// Playing中のフルカラーLED点滅間隔 (通常時 / 終盤警告時)
constexpr uint32_t kPlayingBlinkOnMs = 50;
constexpr uint32_t kPlayingBlinkOffMs = 950;
constexpr uint32_t kPlayingHurryBlinkOnMs = 50;
constexpr uint32_t kPlayingHurryBlinkOffMs = 250;

}  // namespace

GameTask::GameTask(MCP23017* mcp23017, HT16K33* ht16k33, Pl9823Task* pl9823_task,
                   const std::string& device_id)
    : Task(std::string(TASK_NAME).c_str(), PRIORITY, CORE_ID),
      mcp23017_(mcp23017),
      ht16k33_(ht16k33),
      pl9823_task_(pl9823_task),
      buzzer_(Esp32Pin::kBuzzer),
      device_id_(device_id) {
  constexpr int32_t kQueueSize = 32;
  if (!message_queue_.Create(kQueueSize)) {
    ESP_LOGE(TAG, "GameTask: creating queue failed");
  }
}

GameTask::~GameTask() { message_queue_.Destroy(); }

void GameTask::Initialize() {
  ESP_LOGI(TAG, "Start GameTask device_id=%s", device_id_.c_str());

  buzzer_.Setup();

  // ソレノイドGPIOを出力・LOWで初期化する (§8.5: 駆動は Detonating のみ)
  gpio_config_t solenoid_conf = {
      .pin_bit_mask = 1ULL << Esp32Pin::kSolenoid,
      .mode = GPIO_MODE_OUTPUT,
      .pull_up_en = GPIO_PULLUP_DISABLE,
      .pull_down_en = GPIO_PULLDOWN_DISABLE,
      .intr_type = GPIO_INTR_DISABLE,
  };
  gpio_config(&solenoid_conf);
  gpio_set_level(Esp32Pin::kSolenoid, 0);

  // ハング時はリセット→Setup起動で安全側に倒す (§8.5)
  esp_task_wdt_add(nullptr);

  last_tick_ = xTaskGetTickCount();

  EnterSetup();
}

void GameTask::Update() {
  // キュー待ちを100msタイムアウトにして、全ての時間処理をこのtickで行う (§8.2)
  GameEvent event;
  if (message_queue_.ReceiveWait(&event, kTickMs)) {
    HandleEvent(event);
  }

  // イベントが連続すると ReceiveWait がタイムアウトせず tick が飢えるため、
  // 実際の経過時間を見て tick を進める (カウントダウンの停止を防ぐ)。
  const TickType_t now = xTaskGetTickCount();
  int32_t elapsed_ms = static_cast<int32_t>((now - last_tick_) * portTICK_PERIOD_MS);
  while (kTickMs <= elapsed_ms) {
    Tick();
    last_tick_ += pdMS_TO_TICKS(kTickMs);
    elapsed_ms -= kTickMs;
  }

  esp_task_wdt_reset();
}

bool GameTask::PostEvent(const GameEvent& event) { return message_queue_.Send(event); }

void GameTask::NotifyI2cError() {
  i2c_error_ = true;
  UpdateFullColorLed();
}

// --- 状態遷移 -------------------------------------------------------------

void GameTask::EnterSetup() {
  state_ = STATE_SETUP;
  session_ = SessionConfig();
  stage_index_ = 0;
  remaining_ms_ = 0;
  lines_stable_ms_ = 0;
  detonate_remaining_ms_ = 0;
  solenoid_fired_ = false;
  ResetStageProgress();

  buzzer_.Off();
  ht16k33_->Clear();
  ht16k33_->WriteDisplay();
  UpdateFullColorLed();

  // 切断中の線に対応するkLEDを点灯して復旧ガイドにする (§4.1)
  ApplyLedOutputs();

  ESP_LOGI(TAG, "state -> Setup");
}

void GameTask::EnterReady() {
  state_ = STATE_READY;
  buzzer_.Off();
  ht16k33_->Clear();
  ht16k33_->WriteDisplay();
  ClearLedOverrides();
  UpdateFullColorLed();
  ApplyLedOutputs();

  ESP_LOGI(TAG, "state -> Ready");
  SendDeviceStatus();
}

void GameTask::EnterPlaying() {
  state_ = STATE_PLAYING;
  stage_index_ = 0;
  remaining_ms_ = session_.countdown_ms;
  ResetStageProgress();
  buzzer_.Off();
  UpdateFullColorLed();

  ESP_LOGI(TAG, "state -> Playing session_id=%s stages=%d", session_.session_id.c_str(),
           static_cast<int>(session_.stages.size()));
}

void GameTask::EnterDetonating(const char* reason, const char* detail, ColorId line) {
  if (state_ == STATE_DETONATING || state_ == STATE_EXPLODED) {
    return;
  }

  state_ = STATE_DETONATING;
  detonate_remaining_ms_ = session_.detonate_delay_ms;
  solenoid_fired_ = false;
  remaining_ms_ = 0;

  ClearLedOverrides();
  for (int i = 0; i < kColorNum; ++i) {
    led_pattern_on_[i] = false;
  }
  ApplyLedOutputs();

  buzzer_.On();
  UpdateFullColorLed();
  TickDisplay();

  ESP_LOGI(TAG, "state -> Detonating reason=%s", reason);

  // 無線演出と破裂音を同期させるため、ソレノイド駆動前に送信する (§4)
  SendExploded(reason, detail, line);
}

void GameTask::EnterExploded() {
  state_ = STATE_EXPLODED;
  buzzer_.Off();
  UpdateFullColorLed();
  ESP_LOGI(TAG, "state -> Exploded");
  SendDeviceStatus();
}

void GameTask::EnterDefused() {
  state_ = STATE_DEFUSED;

  ClearLedOverrides();
  for (int i = 0; i < kColorNum; ++i) {
    led_pattern_on_[i] = false;
  }
  ApplyLedOutputs();

  buzzer_.Beep(3);
  UpdateFullColorLed();
  // 残り時間はスコアなので停止表示のまま維持する (§4.1)
  TickDisplay();

  ESP_LOGI(TAG, "state -> Defused remaining_ms=%d", static_cast<int>(remaining_ms_));
  SendDefused();
}

// --- イベント処理 ---------------------------------------------------------

void GameTask::HandleEvent(const GameEvent& event) {
  switch (event.type) {
    case EVENT_LINE_CHANGED:
      HandleLineChanged(event.color, event.level);
      break;
    case EVENT_PUSH_CHANGED:
      HandlePushChanged(event.color, event.level);
      break;
    case EVENT_ROTARY_CHANGED:
      HandleRotaryChanged(event.rotary);
      break;
    case EVENT_WS_MESSAGE:
      if (event.payload) {
        HandleWsMessage(*event.payload);
        delete event.payload;
      }
      break;
    case EVENT_WS_CONNECTED:
      ws_connected_ = true;
      SendDeviceStatus();
      break;
    case EVENT_WS_DISCONNECTED:
      ws_connected_ = false;
      // ゲーム進行はデバイス内で完結するため Playing は継続する (§7.3)。
      // Ready の場合のみ session_start を受理できなくなるので Setup へ戻す (§4)。
      if (state_ == STATE_READY) {
        EnterSetup();
      }
      break;
    case EVENT_LOW_BATTERY:
      // 低電圧フラグ付きの device_status を送ってから Deep Sleep へ入る (§8.5)
      low_battery_ = true;
      SendDeviceStatus();
      ESP_LOGW(TAG, "low battery: entering deep sleep");
      vTaskDelay(pdMS_TO_TICKS(500));
      esp_deep_sleep_start();
      break;
  }
}

void GameTask::HandleLineChanged(ColorId color, bool connected) {
  if (color < COLOR_A || kColorNum <= color) {
    return;
  }
  if (line_connected_[color] == connected) {
    return;
  }
  line_connected_[color] = connected;

  ESP_LOGI(TAG, "line %c %s", ColorToChar(color), connected ? "connected" : "cut");

  if (state_ == STATE_SETUP) {
    // 復旧ガイド表示を更新する。Ready遷移の安定判定は Tick 側で行う
    lines_stable_ms_ = 0;
    ApplyLedOutputs();
    return;
  }

  if (state_ == STATE_READY) {
    if (!connected) {
      // Ready中に線が外れたら Setup へ戻る (§4)
      EnterSetup();
    }
    return;
  }

  if (state_ == STATE_PLAYING && !connected) {
    OnLineCut(color);
  }
}

void GameTask::HandlePushChanged(ColorId color, bool pressed) {
  if (color < COLOR_A || kColorNum <= color) {
    return;
  }
  push_pressed_[color] = pressed;

  if (state_ != STATE_PLAYING || !pressed) {
    return;
  }

  const StageConfig& stage = session_.stages[stage_index_];

  if (whack_active_ && !whack_completed_) {
    if (color == whack_current_mole_) {
      // ヒット: 即消灯して gap 後に次のモグラを出す
      ++whack_hits_;
      whack_current_mole_ = COLOR_NONE;
      whack_in_gap_ = true;
      whack_timer_ms_ = stage.precondition.whack.gap_ms;
      SetLedOverride(color, false);

      if (stage.precondition.whack.count <= whack_hits_) {
        whack_completed_ = true;
        whack_active_ = false;
        // 完了した瞬間にヒントパターン表示へ切り替わる (§5.1)
        ClearLedOverrides();
        SendWhackCompleted();
      }
    } else {
      // 点灯中に別のkPushを押したらミス。同じモグラが出直すだけ (§5.1)
      whack_current_mole_ = COLOR_NONE;
      whack_in_gap_ = true;
      whack_timer_ms_ = stage.precondition.whack.gap_ms;
      ClearLedOverrides();
    }
    return;
  }

  if (stage.precondition.has_push_seq && !push_seq_completed_) {
    HandlePushSeqInput(color);
  }
}

void GameTask::HandleRotaryChanged(int8_t position) {
  if (position < 0 || kRotaryPositionNum <= position) {
    return;
  }
  if (rotary_position_ == position) {
    return;
  }
  rotary_position_ = position;
  // 位置が変わったら停止時間の計測をやり直す (通過はセーフ。§5)
  forbidden_hold_ms_ = 0;

  ESP_LOGI(TAG, "rotary -> %d", static_cast<int>(position));
}

void GameTask::HandleWsMessage(const std::string& payload) {
  cJSON* root = cJSON_Parse(payload.c_str());
  if (!root) {
    ESP_LOGW(TAG, "WS message parse error");
    return;
  }

  const cJSON* type_item = cJSON_GetObjectItemCaseSensitive(root, "type");
  if (!cJSON_IsString(type_item)) {
    cJSON_Delete(root);
    return;
  }

  // 複数デバイス運用のため、自分宛でないメッセージは無視する (§2)
  const cJSON* device_item = cJSON_GetObjectItemCaseSensitive(root, "device_id");
  if (cJSON_IsString(device_item) && device_id_ != device_item->valuestring) {
    cJSON_Delete(root);
    return;
  }

  const std::string type = type_item->valuestring;
  const int32_t delta_ms =
      static_cast<int32_t>(cJSON_GetObjectItemCaseSensitive(root, "delta_ms")
                               ? cJSON_GetObjectItemCaseSensitive(root, "delta_ms")->valuedouble
                               : 0);
  cJSON_Delete(root);

  if (type == "session_start") {
    HandleSessionStart(payload);
  } else if (type == "session_abort") {
    HandleSessionAbort();
  } else if (type == "force_detonate") {
    HandleForceDetonate();
  } else if (type == "time_adjust") {
    HandleTimeAdjust(delta_ms);
  }
}

// --- サーバーコマンド (§7.1) ----------------------------------------------

void GameTask::HandleSessionStart(const std::string& payload) {
  if (state_ != STATE_READY) {
    // Ready状態でない場合は切断済みライン一覧を添えて拒否する (§7.2)
    std::string cut_lines;
    for (int i = 0; i < kColorNum; ++i) {
      if (!line_connected_[i]) {
        if (!cut_lines.empty()) {
          cut_lines += ",";
        }
        cut_lines += ColorToChar(static_cast<ColorId>(i));
      }
    }
    SendSessionRejected("not_ready", cut_lines);
    return;
  }

  SessionConfig config;
  const ParseResult result = ParseSessionJson(payload, &config);
  if (!result.ok) {
    SendSessionRejected(result.reason.c_str(), result.detail);
    return;
  }

  session_ = std::move(config);
  SendSessionAccepted();
  EnterPlaying();
}

void GameTask::HandleSessionAbort() {
  // Detonating中でもソレノイド駆動前なら中止して安全側へ倒す (§8.5)
  if (state_ == STATE_DETONATING && solenoid_fired_) {
    ESP_LOGI(TAG, "session_abort ignored: solenoid pulse already started");
    return;
  }

  ESP_LOGI(TAG, "session_abort");
  EnterSetup();
}

void GameTask::HandleForceDetonate() {
  if (state_ != STATE_PLAYING) {
    return;
  }
  EnterDetonating("forced", nullptr, COLOR_NONE);
}

void GameTask::HandleTimeAdjust(int32_t delta_ms) {
  if (state_ != STATE_PLAYING) {
    return;
  }

  remaining_ms_ += delta_ms;
  if (remaining_ms_ > kCountdownMaxMs) {
    remaining_ms_ = kCountdownMaxMs;
  }
  ESP_LOGI(TAG, "time_adjust %d -> remaining=%d", static_cast<int>(delta_ms),
           static_cast<int>(remaining_ms_));

  // 減算で0以下になった場合もタイムアウト扱いにする (§5)
  if (remaining_ms_ <= 0) {
    remaining_ms_ = 0;
    EnterDetonating("timeout", nullptr, COLOR_NONE);
  }
}

// --- 100ms tick (§8.2) ----------------------------------------------------

void GameTask::Tick() {
  buzzer_.Tick();

  switch (state_) {
    case STATE_SETUP:
      // 全線結線が一定時間安定し、サーバーと接続できたら Ready へ (§4)
      if (AreAllLinesConnected() && ws_connected_) {
        lines_stable_ms_ += kTickMs;
        if (kReadyStableMs <= lines_stable_ms_) {
          EnterReady();
        }
      } else {
        lines_stable_ms_ = 0;
      }
      break;

    case STATE_PLAYING:
      TickCountdown();
      if (state_ != STATE_PLAYING) {
        // カウントダウンでタイムアウトした場合はここで抜ける
        break;
      }
      TickLeds();
      TickWhack();
      TickForbiddenRotary();
      TickDisplay();
      break;

    case STATE_DETONATING:
      if (!solenoid_fired_) {
        detonate_remaining_ms_ -= kTickMs;
        if (detonate_remaining_ms_ <= 0) {
          FireSolenoid();
          EnterExploded();
        }
      }
      break;

    case STATE_READY:
    case STATE_EXPLODED:
    case STATE_DEFUSED:
      break;
  }

  // device_status の定期送信 (§7.2)
  status_timer_ms_ += kTickMs;
  if (kStatusIntervalMs <= status_timer_ms_) {
    status_timer_ms_ = 0;
    SendDeviceStatus();
  }
}

void GameTask::TickCountdown() {
  remaining_ms_ -= kTickMs;
  if (remaining_ms_ <= 0) {
    remaining_ms_ = 0;
    EnterDetonating("timeout", nullptr, COLOR_NONE);
    return;
  }

  // 終盤で点滅を加速させる (§4.1)
  UpdateFullColorLed();

  if (0 < timer_digit_grace_ms_) {
    timer_digit_grace_ms_ -= kTickMs;
  }

  if (0 < push_seq_feedback_ms_) {
    push_seq_feedback_ms_ -= kTickMs;
    if (push_seq_feedback_ms_ <= 0) {
      push_seq_feedback_color_ = COLOR_NONE;
      push_seq_feedback_error_ = false;
      ClearLedOverrides();
    }
  }
}

void GameTask::TickLeds() {
  const StageConfig& stage = session_.stages[stage_index_];

  for (int i = 0; i < kColorNum; ++i) {
    const LedPattern& pattern = stage.leds[i];
    if (pattern.steps.empty()) {
      led_pattern_on_[i] = false;
      continue;
    }

    if (static_cast<int32_t>(pattern.steps.size()) <= led_step_index_[i]) {
      led_step_index_[i] = 0;
      led_step_elapsed_[i] = 0;
    }

    const LedStep& step = pattern.steps[led_step_index_[i]];
    led_pattern_on_[i] = step.on;

    ++led_step_elapsed_[i];
    if (step.ticks <= led_step_elapsed_[i]) {
      led_step_elapsed_[i] = 0;
      led_step_index_[i] = (led_step_index_[i] + 1) %
                           static_cast<int32_t>(pattern.steps.size());
    }
  }

  ApplyLedOutputs();
}

void GameTask::TickWhack() {
  if (!whack_active_ || whack_completed_) {
    return;
  }

  const StageConfig& stage = session_.stages[stage_index_];
  const WhackSpec& whack = stage.precondition.whack;

  whack_timer_ms_ -= kTickMs;
  if (0 < whack_timer_ms_) {
    return;
  }

  if (whack_in_gap_ || whack_current_mole_ == COLOR_NONE) {
    // 次のモグラを出現させる
    whack_in_gap_ = false;
    whack_current_mole_ = PickNextMole();
    whack_timer_ms_ = whack.mole_on_ms;
    ClearLedOverrides();
    SetLedOverride(whack_current_mole_, true);
  } else {
    // 点灯時間内に押せなかったミス。ペナルティなしで同じ枠を出し直す (§5.1)
    whack_current_mole_ = COLOR_NONE;
    whack_in_gap_ = true;
    whack_timer_ms_ = whack.gap_ms;
    ClearLedOverrides();
  }
}

void GameTask::TickForbiddenRotary() {
  const StageConfig& stage = session_.stages[stage_index_];
  if (!stage.forbidden_rotary.enabled) {
    forbidden_hold_ms_ = 0;
    return;
  }

  if (!stage.forbidden_rotary.positions[rotary_position_]) {
    forbidden_hold_ms_ = 0;
    return;
  }

  // 通過は違反にせず、一定時間止まったら違反確定とする (§5)
  forbidden_hold_ms_ += kTickMs;
  if (forbidden_hold_ms_ < kForbiddenRotaryHoldMs) {
    return;
  }
  forbidden_hold_ms_ = 0;

  ESP_LOGI(TAG, "forbidden rotary violation at %d", static_cast<int>(rotary_position_));

  if (stage.forbidden_rotary.on_violation.action == ACTION_EXPLODE) {
    EnterDetonating("wrong_cut", "forbidden_rotary", COLOR_NONE);
    return;
  }

  ApplyPenalty(stage.forbidden_rotary.on_violation.penalty_ms);
  SendWrongAction("forbidden_rotary", COLOR_NONE,
                  stage.forbidden_rotary.on_violation.penalty_ms);
}

void GameTask::TickDisplay() {
  // 整数部(3桁)+小数点1桁を右詰めで表示する (先頭の不要なゼロは消灯)
  const int32_t tenths_total = remaining_ms_ / 100;
  const int seconds = static_cast<int>(tenths_total / 10);
  const int tenths = static_cast<int>(tenths_total % 10);

  const int int_digits[3] = {seconds / 100 % 10, seconds / 10 % 10, seconds % 10};
  ht16k33_->Clear();
  bool leading = true;
  for (int i = 0; i < 3; ++i) {
    if (leading && int_digits[i] == 0 && i < 2) {
      continue;
    }
    leading = false;
    ht16k33_->WriteDigitNum(i, int_digits[i], i == 2);
  }
  ht16k33_->WriteDigitNum(3, tenths);
  ht16k33_->WriteDisplay();
}

// --- ゲームルール (§5) ----------------------------------------------------

void GameTask::OnLineCut(ColorId color) {
  const StageConfig& stage = session_.stages[stage_index_];

  const bool is_correct_line = (color == stage.cut);
  const bool precondition_met = IsPreconditionMet(stage);

  if (is_correct_line && precondition_met) {
    ESP_LOGI(TAG, "stage %d cleared", static_cast<int>(stage_index_));
    SendStageCleared();
    AdvanceStage();
    return;
  }

  // 誤配線と事前条件未達を detail で区別する (§7.2)
  const char* detail = is_correct_line ? "precondition_unmet" : "wrong_line";

  if (stage.on_wrong_cut.action == ACTION_EXPLODE) {
    EnterDetonating("wrong_cut", detail, color);
    return;
  }

  // penalty の場合
  ApplyPenalty(stage.on_wrong_cut.penalty_ms);
  SendWrongAction(detail, color, stage.on_wrong_cut.penalty_ms);
  if (state_ != STATE_PLAYING) {
    // ペナルティで残り時間が0になった場合
    return;
  }

  if (is_correct_line) {
    // 事前条件未達でも正解線は物理的に戻せないため、クリア扱いで次へ進む (§5)
    ESP_LOGI(TAG, "stage %d cleared (precondition unmet, penalty applied)",
             static_cast<int>(stage_index_));
    AdvanceStage();
  }
}

bool GameTask::IsPreconditionMet(const StageConfig& stage) const {
  const Precondition& precondition = stage.precondition;

  if (precondition.has_rotary && rotary_position_ != precondition.rotary) {
    return false;
  }

  if (precondition.has_push) {
    for (int i = 0; i < kColorNum; ++i) {
      if (precondition.push_required[i] && !push_pressed_[i]) {
        return false;
      }
    }
  }

  if (precondition.has_whack && !whack_completed_) {
    return false;
  }

  if (precondition.has_push_seq && !push_seq_completed_) {
    return false;
  }

  if (precondition.has_timer_digit && !IsTimerDigitMet(precondition.timer_digit)) {
    return false;
  }

  if (precondition.leds_all_off) {
    for (int i = 0; i < kColorNum; ++i) {
      if (led_pattern_on_[i]) {
        return false;
      }
    }
  }

  return true;
}

bool GameTask::IsTimerDigitMet(const TimerDigitSpec& spec) const {
  const int32_t seconds = remaining_ms_ / 1000;
  const int32_t digit =
      (spec.digit == TIMER_DIGIT_ONES) ? (seconds % 10) : (seconds / 10 % 10);

  const int32_t target =
      (spec.match == TIMER_MATCH_ROTARY) ? rotary_position_ : spec.value;

  if (digit == target) {
    return true;
  }

  // 判定窓は「対象桁が一致する期間 + 直後1秒の猶予」(§5)
  return 0 < timer_digit_grace_ms_;
}

void GameTask::ApplyPenalty(int32_t penalty_ms) {
  remaining_ms_ -= penalty_ms;
  buzzer_.Beep(1);

  ESP_LOGI(TAG, "penalty %d ms -> remaining=%d", static_cast<int>(penalty_ms),
           static_cast<int>(remaining_ms_));

  if (remaining_ms_ <= 0) {
    remaining_ms_ = 0;
    EnterDetonating("timeout", nullptr, COLOR_NONE);
  }
}

void GameTask::AdvanceStage() {
  ++stage_index_;
  if (static_cast<int32_t>(session_.stages.size()) <= stage_index_) {
    EnterDefused();
    return;
  }

  ResetStageProgress();
}

void GameTask::ResetStageProgress() {
  for (int i = 0; i < kColorNum; ++i) {
    led_step_index_[i] = 0;
    led_step_elapsed_[i] = 0;
    led_pattern_on_[i] = false;
  }

  whack_active_ = false;
  whack_completed_ = false;
  whack_hits_ = 0;
  whack_current_mole_ = COLOR_NONE;
  whack_timer_ms_ = 0;
  whack_in_gap_ = false;

  push_seq_completed_ = false;
  push_seq_index_ = 0;
  push_seq_feedback_ms_ = 0;
  push_seq_feedback_color_ = COLOR_NONE;
  push_seq_feedback_error_ = false;

  forbidden_hold_ms_ = 0;
  timer_digit_grace_ms_ = 0;

  ClearLedOverrides();

  if (state_ != STATE_PLAYING || session_.stages.empty()) {
    return;
  }

  const StageConfig& stage = session_.stages[stage_index_];
  if (stage.precondition.has_whack) {
    StartWhack();
  }
}

// --- whack (§5.1) ---------------------------------------------------------

void GameTask::StartWhack() {
  whack_active_ = true;
  whack_completed_ = false;
  whack_hits_ = 0;
  whack_in_gap_ = true;
  // 最初のモグラは gap 経過後に出現させる
  whack_timer_ms_ = session_.stages[stage_index_].precondition.whack.gap_ms;
  whack_current_mole_ = COLOR_NONE;
  ClearLedOverrides();
}

ColorId GameTask::PickNextMole() const {
  const StageConfig& stage = session_.stages[stage_index_];
  const WhackSpec& whack = stage.precondition.whack;

  // last_mole_matches_cut: 最後の1匹だけ cut と同色に固定する (§5.1)
  if (whack.last_mole_matches_cut && whack_hits_ == whack.count - 1) {
    return stage.cut;
  }

  // 出題順はデバイス側でランダム抽選し、直前と同じLEDは避ける
  for (int attempt = 0; attempt < 16; ++attempt) {
    const ColorId candidate = static_cast<ColorId>(esp_random() % kColorNum);
    if (candidate != whack_current_mole_) {
      return candidate;
    }
  }
  return static_cast<ColorId>(esp_random() % kColorNum);
}

// --- push_seq (§5) --------------------------------------------------------

void GameTask::HandlePushSeqInput(ColorId color) {
  const StageConfig& stage = session_.stages[stage_index_];
  const PushSeqSpec& push_seq = stage.precondition.push_seq;

  if (static_cast<int32_t>(push_seq.entries.size()) <= push_seq_index_) {
    return;
  }

  const PushSeqEntry& entry = push_seq.entries[push_seq_index_];
  const bool color_ok = (entry.push == color);
  const bool rotary_ok = (entry.rotary < 0) || (entry.rotary == rotary_position_);

  if (color_ok && rotary_ok) {
    ++push_seq_index_;

    // 正入力のたびに対応色LEDを短点滅させ、サーバーへ進捗を通知する
    push_seq_feedback_color_ = color;
    push_seq_feedback_error_ = false;
    push_seq_feedback_ms_ = kPushSeqFeedbackMs;
    ClearLedOverrides();
    SetLedOverride(color, true);

    SendPushProgress(push_seq_index_);

    if (static_cast<int32_t>(push_seq.entries.size()) <= push_seq_index_) {
      push_seq_completed_ = true;
      ESP_LOGI(TAG, "push_seq completed");
    }
    return;
  }

  // ミス時は全LEDを短く点滅させる
  push_seq_feedback_error_ = true;
  push_seq_feedback_ms_ = kPushSeqFeedbackMs;
  led_override_active_ = true;
  for (int i = 0; i < kColorNum; ++i) {
    led_override_on_[i] = true;
  }
  ApplyLedOutputs();

  switch (push_seq.on_wrong_press.action) {
    case ACTION_EXPLODE:
      EnterDetonating("wrong_cut", "push_seq", COLOR_NONE);
      return;
    case ACTION_PENALTY:
      ApplyPenalty(push_seq.on_wrong_press.penalty_ms);
      SendWrongAction("push_seq", COLOR_NONE, push_seq.on_wrong_press.penalty_ms);
      push_seq_index_ = 0;
      return;
    case ACTION_RETRY:
    default:
      // 列の先頭からやり直し (既定)
      push_seq_index_ = 0;
      SendWrongAction("push_seq", COLOR_NONE, 0);
      return;
  }
}

// --- 出力 -----------------------------------------------------------------

void GameTask::ApplyLedOutputs() {
  bool desired[kColorNum] = {false, false, false, false, false};

  if (state_ == STATE_SETUP) {
    // 切断中の線に対応するLEDを点灯して復旧ガイドにする (§4.1)
    for (int i = 0; i < kColorNum; ++i) {
      desired[i] = !line_connected_[i];
    }
  } else if (state_ == STATE_PLAYING) {
    for (int i = 0; i < kColorNum; ++i) {
      desired[i] = led_override_active_ ? led_override_on_[i] : led_pattern_on_[i];
    }
  }
  // Ready / Detonating / Exploded / Defused は全消灯 (§4.1)

  for (int i = 0; i < kColorNum; ++i) {
    if (led_written_[i] == desired[i]) {
      continue;
    }
    led_written_[i] = desired[i];
    mcp23017_->SetOutputGpio(kLedPins[i][0], kLedPins[i][1], desired[i]);
  }
}

void GameTask::SetLedOverride(ColorId color, bool on) {
  if (color < COLOR_A || kColorNum <= color) {
    return;
  }
  led_override_active_ = true;
  led_override_on_[color] = on;
  ApplyLedOutputs();
}

void GameTask::ClearLedOverrides() {
  led_override_active_ = false;
  for (int i = 0; i < kColorNum; ++i) {
    led_override_on_[i] = false;
  }

  // whack進行中はモグラ表示がkLEDを専有し続ける (§5.1)
  if (whack_active_ && !whack_completed_ && whack_current_mole_ != COLOR_NONE) {
    led_override_active_ = true;
    led_override_on_[whack_current_mole_] = true;
  }

  ApplyLedOutputs();
}

void GameTask::UpdateFullColorLed() {
  Pl9823Task::Command command;

  // I2Cエラーが復帰不能な場合は状態にかかわらず紫点滅で示す (§8.5)
  if (i2c_error_) {
    command.pattern = Pl9823Task::PATTERN_BLINK;
    command.r = kFullColorBrightness;
    command.g = 0;
    command.b = kFullColorBrightness;
    command.on_ms = 300;
    command.off_ms = 300;
    pl9823_task_->SendCommand(command);
    return;
  }

  switch (state_) {
    case STATE_SETUP:
      // 黄点滅
      command.pattern = Pl9823Task::PATTERN_BLINK;
      command.r = kFullColorBrightness;
      command.g = kFullColorBrightness;
      command.b = 0;
      command.on_ms = 500;
      command.off_ms = 500;
      break;

    case STATE_READY:
      // 青点灯
      command.pattern = Pl9823Task::PATTERN_SOLID;
      command.b = kFullColorBrightness;
      break;

    case STATE_PLAYING: {
      // 赤の短発点滅 (残り時間僅少で点滅加速)
      const bool hurry = (remaining_ms_ <= kHurryThresholdMs);
      command.pattern = Pl9823Task::PATTERN_BLINK;
      command.r = kFullColorBrightness;
      command.on_ms = hurry ? kPlayingHurryBlinkOnMs : kPlayingBlinkOnMs;
      command.off_ms = hurry ? kPlayingHurryBlinkOffMs : kPlayingBlinkOffMs;
      break;
    }

    case STATE_DETONATING:
    case STATE_EXPLODED:
      // 赤点灯
      command.pattern = Pl9823Task::PATTERN_SOLID;
      command.r = kFullColorBrightness;
      break;

    case STATE_DEFUSED:
      // 緑点灯
      command.pattern = Pl9823Task::PATTERN_SOLID;
      command.g = kFullColorBrightness;
      break;
  }

  pl9823_task_->SendCommand(command);
}

void GameTask::FireSolenoid() {
  // 駆動は Detonating 状態からのみ。二重駆動もフラグで防ぐ (§8.5)
  if (state_ != STATE_DETONATING || solenoid_fired_) {
    return;
  }
  solenoid_fired_ = true;

  ESP_LOGI(TAG, "solenoid fire");
  gpio_set_level(Esp32Pin::kSolenoid, 1);
  vTaskDelay(pdMS_TO_TICKS(kSolenoidPulseMs));
  gpio_set_level(Esp32Pin::kSolenoid, 0);
}

// --- 送信 (§7.2) ----------------------------------------------------------

void GameTask::SendJson(const char* type, const std::function<void(cJSON*)>& fill) {
  if (!send_func_) {
    return;
  }

  cJSON* root = cJSON_CreateObject();
  cJSON_AddStringToObject(root, "type", type);
  cJSON_AddStringToObject(root, "device_id", device_id_.c_str());
  if (fill) {
    fill(root);
  }

  char* json_text = cJSON_PrintUnformatted(root);
  if (json_text) {
    send_func_(json_text);
    cJSON_free(json_text);
  }
  cJSON_Delete(root);
}

void GameTask::SendDeviceStatus() {
  SendJson("device_status", [this](cJSON* root) {
    cJSON_AddStringToObject(root, "state", StateName());
    cJSON_AddStringToObject(root, "session_id", session_.session_id.c_str());
    cJSON_AddNumberToObject(root, "stage_index", stage_index_);
    cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms_);
    cJSON_AddNumberToObject(root, "battery", battery_voltage_);
    cJSON_AddBoolToObject(root, "low_battery", low_battery_);
    cJSON_AddBoolToObject(root, "i2c_error", i2c_error_);

    // 各配線の結線状態 (Setup中の復旧進捗をマネージャーが確認するのに使う)
    cJSON* lines = cJSON_CreateObject();
    for (int i = 0; i < kColorNum; ++i) {
      const char key[2] = {ColorToChar(static_cast<ColorId>(i)), '\0'};
      cJSON_AddBoolToObject(lines, key, line_connected_[i]);
    }
    cJSON_AddItemToObject(root, "lines", lines);
  });
}

void GameTask::SendSessionAccepted() {
  SendJson("session_accepted", [this](cJSON* root) {
    cJSON_AddStringToObject(root, "session_id", session_.session_id.c_str());
  });
}

void GameTask::SendSessionRejected(const char* reason, const std::string& detail) {
  ESP_LOGW(TAG, "session_rejected: %s (%s)", reason, detail.c_str());
  SendJson("session_rejected", [reason, &detail](cJSON* root) {
    cJSON_AddStringToObject(root, "reason", reason);
    if (!detail.empty()) {
      cJSON_AddStringToObject(root, "detail", detail.c_str());
    }
  });
}

void GameTask::SendStageCleared() {
  SendJson("stage_cleared", [this](cJSON* root) {
    cJSON_AddNumberToObject(root, "stage_index", stage_index_);
    cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms_);
  });
}

void GameTask::SendWhackCompleted() {
  SendJson("whack_completed", [this](cJSON* root) {
    cJSON_AddNumberToObject(root, "stage_index", stage_index_);
    cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms_);
  });
}

void GameTask::SendPushProgress(int32_t seq_index) {
  SendJson("push_progress", [this, seq_index](cJSON* root) {
    cJSON_AddNumberToObject(root, "stage_index", stage_index_);
    cJSON_AddNumberToObject(root, "seq_index", seq_index);
    cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms_);
  });
}

void GameTask::SendWrongAction(const char* detail, ColorId line, int32_t penalty_ms) {
  SendJson("wrong_action", [this, detail, line, penalty_ms](cJSON* root) {
    cJSON_AddStringToObject(root, "detail", detail);
    if (line != COLOR_NONE) {
      const char value[2] = {ColorToChar(line), '\0'};
      cJSON_AddStringToObject(root, "line", value);
    }
    cJSON_AddNumberToObject(root, "penalty_ms", penalty_ms);
    cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms_);
  });
}

void GameTask::SendExploded(const char* reason, const char* detail, ColorId line) {
  SendJson("exploded", [reason, detail, line](cJSON* root) {
    cJSON_AddStringToObject(root, "reason", reason);
    if (detail) {
      cJSON_AddStringToObject(root, "detail", detail);
    }
    if (line != COLOR_NONE) {
      const char value[2] = {ColorToChar(line), '\0'};
      cJSON_AddStringToObject(root, "line", value);
    }
  });
}

void GameTask::SendDefused() {
  SendJson("defused", [this](cJSON* root) {
    cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms_);
  });
}

const char* GameTask::StateName() const {
  switch (state_) {
    case STATE_SETUP: return "setup";
    case STATE_READY: return "ready";
    case STATE_PLAYING: return "playing";
    case STATE_DETONATING: return "detonating";
    case STATE_EXPLODED: return "exploded";
    case STATE_DEFUSED: return "defused";
    default: return "unknown";
  }
}

bool GameTask::AreAllLinesConnected() const {
  for (int i = 0; i < kColorNum; ++i) {
    if (!line_connected_[i]) {
      return false;
    }
  }
  return true;
}

}  // namespace CoreSystem

// EOF
