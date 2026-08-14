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

GameTask::GameTask(MCP23017* mcp23017, HT16K33* ht16k33, Pl9823Task* pl9823_task,
                   const std::string& device_id)
    : Task(std::string(TASK_NAME).c_str(), PRIORITY, CORE_ID),
      mcp23017_(mcp23017),
      ht16k33_(ht16k33),
      pl9823_task_(pl9823_task),
      buzzer_(Esp32Pin::kBuzzer),
      leds_(mcp23017),
      device_id_(device_id),
      sender_(device_id) {
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

  // 点滅の段階を今の残り時間で確定させる (前セッションの値を持ち越さない)
  last_blink_off_ms_ = StatusIndicator::PlayingBlinkOffMs(remaining_ms_);
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

  // 以降は全消灯になるため、パターン再生も止める (§4.1)
  leds_.Reset();
  ApplyLedOutputs();

  buzzer_.On();
  UpdateFullColorLed();
  TickDisplay();

  ESP_LOGI(TAG, "state -> Detonating reason=%s", reason);

  // 無線演出と破裂音を同期させるため、ソレノイド駆動前に送信する (§4)
  sender_.SendExploded(reason, detail, line);
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

  // 以降は全消灯になるため、パターン再生も止める (§4.1)
  leds_.Reset();
  ApplyLedOutputs();

  buzzer_.Beep(3);
  UpdateFullColorLed();
  // 残り時間はスコアなので停止表示のまま維持する (§4.1)
  TickDisplay();

  ESP_LOGI(TAG, "state -> Defused remaining_ms=%d", static_cast<int>(remaining_ms_));
  sender_.SendDefused(remaining_ms_);
}

// --- イベント処理 ---------------------------------------------------------

void GameTask::HandleEvent(const GameEvent& event) {
  if (event.type == EVENT_LINE_CHANGED) {
    HandleLineChanged(event.color, event.level);
    return;
  }

  if (event.type == EVENT_PUSH_CHANGED) {
    HandlePushChanged(event.color, event.level);
    return;
  }

  if (event.type == EVENT_ROTARY_CHANGED) {
    HandleRotaryChanged(event.rotary);
    return;
  }

  if (event.type == EVENT_WS_MESSAGE) {
    if (event.payload) {
      HandleWsMessage(*event.payload);
      delete event.payload;
    }
    return;
  }

  if (event.type == EVENT_WS_CONNECTED) {
    ws_connected_ = true;
    // 繋がった時点で起動時のWiFi失敗は解消している (§4.0)。
    // 再接続で復帰した場合に点滅が残らないよう降ろす。
    wifi_failed_ = false;
    // Setup中は紫点灯 → 黄点滅へ切り替わる (§4.0)。
    // 表示は状態変化のたびにしか更新しないため、ここで明示的に反映する。
    UpdateFullColorLed();
    SendDeviceStatus();
    return;
  }

  if (event.type == EVENT_WS_DISCONNECTED) {
    ws_connected_ = false;
    // ゲーム進行はデバイス内で完結するため Playing は継続する (§7.3)。
    // Ready の場合のみ session_start を受理できなくなるので Setup へ戻す (§4)。
    if (state_ == STATE_READY) {
      EnterSetup();  // 中で UpdateFullColorLed() が呼ばれる
      return;
    }
    // Setup中に切断したら黄点滅 → 紫点灯へ戻す (§4.0)
    if (state_ == STATE_SETUP) {
      UpdateFullColorLed();
    }
    return;
  }

  if (event.type == EVENT_LOW_BATTERY) {
    // 低電圧フラグ付きの device_status を送ってから Deep Sleep へ入る (§8.5)
    low_battery_ = true;
    SendDeviceStatus();
    ESP_LOGW(TAG, "low battery: entering deep sleep");

    // 全表示を消してから眠る。Deep Sleep 中も MCP23017・HT16K33 は
    // 自前の電源で点いたままになり、消し忘れると電池を消費し続ける (§8.5)
    TurnOffAllOutputs();

    // 送信と消灯が終わるのを待つ。フルカラーLEDはキュー経由の別タスクなので、
    // ここで待たずに眠ると消灯コマンドが処理されないまま点いたまま残る。
    vTaskDelay(pdMS_TO_TICKS(500));
    esp_deep_sleep_start();
    return;
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

  if (whack_.IsRunning()) {
    if (whack_.HandlePush(color, stage, &leds_)) {
      sender_.SendWhackCompleted(stage_index_, remaining_ms_);
    }
    ApplyLedOutputs();
    return;
  }

  if (stage.precondition.has_push_seq && !push_seq_.IsCompleted()) {
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
  cJSON_Delete(root);

  if (type == "session_start") {
    HandleSessionStart(payload);
  } else if (type == "session_abort") {
    HandleSessionAbort();
  } else if (type == "force_detonate") {
    HandleForceDetonate();
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
    sender_.SendSessionRejected("not_ready", cut_lines);
    return;
  }

  SessionConfig config;
  const ParseResult result = ParseSessionJson(payload, &config);
  if (!result.ok) {
    sender_.SendSessionRejected(result.reason.c_str(), result.detail);
    return;
  }

  session_ = std::move(config);
  sender_.SendSessionAccepted(session_.session_id);
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

// --- 100ms tick (§8.2) ----------------------------------------------------

void GameTask::Tick() {
  buzzer_.Tick();

  // Ready / Exploded / Defused は tick で進む処理を持たない
  if (state_ == STATE_SETUP) {
    // 全線結線が一定時間安定し、サーバーと接続できたら Ready へ (§4)
    if (AreAllLinesConnected() && ws_connected_) {
      lines_stable_ms_ += kTickMs;
      if (kReadyStableMs <= lines_stable_ms_) {
        EnterReady();
      }
    } else {
      lines_stable_ms_ = 0;
    }
  } else if (state_ == STATE_PLAYING) {
    TickCountdown();
    // カウントダウンでタイムアウトした場合は以降の描画を行わない
    if (state_ == STATE_PLAYING) {
      TickLeds();
      TickWhack();
      TickForbiddenRotary();
      TickDisplay();
    }
  } else if (state_ == STATE_DETONATING) {
    if (!solenoid_fired_) {
      detonate_remaining_ms_ -= kTickMs;
      if (detonate_remaining_ms_ <= 0) {
        FireSolenoid();
        EnterExploded();
      }
    }
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

  // 残り時間に応じて点滅を加速させる (§4.1)。
  // **段階が変わったときだけ**送る。毎tick送ると Pl9823Task が
  // コマンドのたびに位相をリセットし、点灯しっぱなしに見えてしまう。
  const uint32_t blink_off_ms = StatusIndicator::PlayingBlinkOffMs(remaining_ms_);
  if (blink_off_ms != last_blink_off_ms_) {
    last_blink_off_ms_ = blink_off_ms;
    UpdateFullColorLed();
  }

  UpdateTimerDigitGrace();

  // push_seq のフィードバック表示が終わったら上書きを解除する
  if (push_seq_.TickFeedback()) {
    ClearLedOverrides();
    ApplyLedOutputs();
  }
}

void GameTask::TickLeds() {
  leds_.TickPatterns(session_.stages[stage_index_]);
  ApplyLedOutputs();
}

void GameTask::TickWhack() {
  whack_.Tick(session_.stages[stage_index_], &leds_);
  ApplyLedOutputs();
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
  sender_.SendWrongAction("forbidden_rotary", COLOR_NONE,
                          stage.forbidden_rotary.on_violation.penalty_ms, remaining_ms_);
}

void GameTask::TickDisplay() {
  CountdownDisplay::Render(ht16k33_, remaining_ms_);
}

// --- ゲームルール (§5) ----------------------------------------------------

void GameTask::OnLineCut(ColorId color) {
  const StageConfig& stage = session_.stages[stage_index_];

  const bool is_correct_line = (color == stage.cut);
  const bool precondition_met = IsPreconditionMet(stage);

  if (is_correct_line && precondition_met) {
    ESP_LOGI(TAG, "stage %d cleared", static_cast<int>(stage_index_));
    sender_.SendStageCleared(stage_index_, remaining_ms_);
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
  sender_.SendWrongAction(detail, color, stage.on_wrong_cut.penalty_ms, remaining_ms_);
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

  if (precondition.has_whack && !whack_.IsCompleted()) {
    return false;
  }

  if (precondition.has_push_seq && !push_seq_.IsCompleted()) {
    return false;
  }

  if (precondition.has_timer_digit &&
      !timer_digit_.IsMet(precondition.timer_digit, remaining_ms_, rotary_position_)) {
    return false;
  }

  if (precondition.leds_all_off && !leds_.AreAllPatternsOff()) {
    return false;
  }

  return true;
}

/// timer_digit の猶予タイマーを進める (§5)。
/// 条件の無いステージ・Playing以外では猶予を捨てる。
void GameTask::UpdateTimerDigitGrace() {
  if (state_ != STATE_PLAYING || session_.stages.empty()) {
    timer_digit_.Reset();
    return;
  }
  const StageConfig& stage = session_.stages[stage_index_];
  if (!stage.precondition.has_timer_digit) {
    timer_digit_.Reset();
    return;
  }

  timer_digit_.Tick(stage.precondition.timer_digit, remaining_ms_, rotary_position_);
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
  leds_.Reset();
  whack_.Reset();
  push_seq_.Reset();

  forbidden_hold_ms_ = 0;
  timer_digit_.Reset();

  ClearLedOverrides();

  if (state_ != STATE_PLAYING || session_.stages.empty()) {
    return;
  }

  const StageConfig& stage = session_.stages[stage_index_];
  if (stage.precondition.has_whack) {
    whack_.Start(stage.precondition.whack, &leds_);
  }
}

// --- push_seq (§5) --------------------------------------------------------

void GameTask::HandlePushSeqInput(ColorId color) {
  const StageConfig& stage = session_.stages[stage_index_];
  const PushSeqSpec& spec = stage.precondition.push_seq;

  const PushSeqResult result =
      push_seq_.HandlePush(color, spec, rotary_position_, &leds_);
  ApplyLedOutputs();

  if (result == PUSH_SEQ_IGNORED) {
    return;
  }

  if (result == PUSH_SEQ_ADVANCED) {
    sender_.SendPushProgress(stage_index_, push_seq_.Index(), remaining_ms_);
    return;
  }

  if (result == PUSH_SEQ_COMPLETED) {
    sender_.SendPushProgress(stage_index_, push_seq_.Index(), remaining_ms_);
    ESP_LOGI(TAG, "push_seq completed");
    return;
  }

  // 以降は PUSH_SEQ_WRONG。ミス時の扱いは on_wrong_press に従う (§5)
  if (spec.on_wrong_press.action == ACTION_EXPLODE) {
    EnterDetonating("wrong_cut", "push_seq", COLOR_NONE);
    return;
  }

  if (spec.on_wrong_press.action == ACTION_PENALTY) {
    ApplyPenalty(spec.on_wrong_press.penalty_ms);
    sender_.SendWrongAction("push_seq", COLOR_NONE, spec.on_wrong_press.penalty_ms,
                            remaining_ms_);
    return;
  }

  // ACTION_RETRY (既定)。列の先頭からやり直し。
  // PushSeqInput 側で index はリセット済み
  sender_.SendWrongAction("push_seq", COLOR_NONE, 0, remaining_ms_);
}

// --- 出力 -----------------------------------------------------------------

void GameTask::ApplyLedOutputs() {
  if (state_ == STATE_SETUP) {
    // 切断中の線に対応するLEDを点灯して復旧ガイドにする (§4.1)
    leds_.ApplySetupGuide(line_connected_);
    return;
  }

  if (state_ == STATE_PLAYING) {
    leds_.ApplyPlaying();
    return;
  }

  // Ready / Detonating / Exploded / Defused は全消灯 (§4.1)
  leds_.ApplyAllOff();
}

void GameTask::ClearLedOverrides() {
  leds_.ClearOverride();

  // whack進行中はモグラ表示がkLEDを専有し続ける (§5.1)
  if (whack_.IsRunning() && whack_.CurrentMole() != COLOR_NONE) {
    leds_.SetOverride(whack_.CurrentMole(), true);
  }

  ApplyLedOutputs();
}

void GameTask::UpdateFullColorLed() {
  // 表示の決定は StatusIndicator に任せる (§4.1)。
  // 点滅の速さは残り時間から段階的に決まる
  pl9823_task_->SendCommand(
      StatusIndicator::MakeCommand(state_, remaining_ms_, ws_connected_, wifi_failed_));
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

/// 全ての表示・音を止める (§8.5)。
///
/// Deep Sleep は ESP32 を眠らせるだけで、MCP23017 (kLED) と HT16K33 (7セグ) は
/// I2C デバイス自身の電源で点灯を保持する。**消さずに眠ると電池を消費し続け**、
/// 過放電保護の意味が薄れる。
void GameTask::TurnOffAllOutputs() {
  buzzer_.Off();

  // kLED: パターン再生を止めてから全消灯する
  leds_.Reset();
  leds_.ClearOverride();
  leds_.ApplyAllOff();

  // 7セグ
  ht16k33_->Clear();
  ht16k33_->WriteDisplay();

  // フルカラーLED
  Pl9823Task::Command command;
  command.pattern = Pl9823Task::PATTERN_OFF;
  pl9823_task_->SendCommand(command);
}

// --- 送信 (§7.2) ----------------------------------------------------------

StatusSnapshot GameTask::MakeStatusSnapshot() const {
  StatusSnapshot status;
  status.state = GameStateName(state_);
  status.session_id = session_.session_id.c_str();
  status.stage_index = stage_index_;
  status.remaining_ms = remaining_ms_;
  status.battery = battery_voltage_;
  status.low_battery = low_battery_;
  status.line_connected = line_connected_;
  return status;
}

void GameTask::SendDeviceStatus() { sender_.SendDeviceStatus(MakeStatusSnapshot()); }

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
