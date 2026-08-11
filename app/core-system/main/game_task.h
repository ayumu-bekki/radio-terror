#ifndef GAME_TASK_H_
#define GAME_TASK_H_
// Core System
// (C)2026 bekki.jp
// ゲームセッションの状態機械 (docs/game_session_design.md §4・§5・§8)
// 全イベント (入力変化・WS受信・100ms tick) をこのタスクへ集約する
// シングルコンシューマー構成。

// Include ----------------------
#include <driver/gpio.h>

#include <cJSON.h>

#include <functional>
#include <string>

#include "buzzer.h"
#include "countdown_display.h"
#include "event_sender.h"
#include "game_session.h"
#include "ht16k33.h"
#include "led_controller.h"
#include "mcp23017.h"
#include "message_queue.h"
#include "pl9823_task.h"
#include "push_seq_input.h"
#include "status_indicator.h"
#include "task.h"
#include "timer_digit_rule.h"
#include "whack_game.h"

namespace CoreSystem {

/// GameTaskのキューへ積むイベント種別
enum GameEventType : uint8_t {
  EVENT_LINE_CHANGED,    ///< kLineの結線状態が変化した (切断/復旧)
  EVENT_PUSH_CHANGED,    ///< kPushの押下状態が変化した
  EVENT_ROTARY_CHANGED,  ///< ロータリー位置が確定した
  EVENT_WS_MESSAGE,      ///< WebSocket受信 (JSON文字列。GameTask内でパースする)
  EVENT_WS_CONNECTED,    ///< WebSocket接続確立
  EVENT_WS_DISCONNECTED, ///< WebSocket切断
  EVENT_LOW_BATTERY,     ///< バッテリー低電圧を検知した (Deep Sleepへ)
};

/// キューへ積むイベント。可変長のJSON文字列はヒープ確保して所有権をGameTaskへ渡す
struct GameEvent {
  GameEventType type = EVENT_LINE_CHANGED;

  /// EVENT_LINE_CHANGED / EVENT_PUSH_CHANGED の対象色
  ColorId color = COLOR_NONE;
  /// EVENT_LINE_CHANGED: true=結線, EVENT_PUSH_CHANGED: true=押下
  bool level = false;
  /// EVENT_ROTARY_CHANGED の確定位置 (0-5)
  int8_t rotary = -1;
  /// EVENT_WS_MESSAGE のペイロード (GameTaskが delete する)
  std::string* payload = nullptr;
};

class GameTask final : public Task {
 public:
  static constexpr std::string_view TASK_NAME = "GameTask";
  static constexpr int32_t PRIORITY = Task::PRIORITY_NORMAL;
  static constexpr int32_t CORE_ID = APP_CPU_NUM;

  /// device_status の定期送信間隔 (§7.2)
  static constexpr int32_t kStatusIntervalMs = 5000;

  /// Setup → Ready 遷移に必要な全線結線の安定時間 (§4)
  static constexpr int32_t kReadyStableMs = 1000;

  /// forbidden_rotary の違反確定までの停止時間 (§5。実機調整前提)
  static constexpr int32_t kForbiddenRotaryHoldMs = 300;

  /// ソレノイドのパルス幅 (§8.3)
  static constexpr int32_t kSolenoidPulseMs = 200;

  /// 終盤警告として点滅を加速させる残り時間の閾値 (§4.1。実機調整前提)
  static constexpr int32_t kHurryThresholdMs = 30000;

 public:
  GameTask(MCP23017* mcp23017, HT16K33* ht16k33, Pl9823Task* pl9823_task,
           const std::string& device_id);
  ~GameTask() override;

  void Initialize() override;
  void Update() override;

  /// 外部タスク (GPIO監視・WSイベント) からイベントを積む
  bool PostEvent(const GameEvent& event);

  /// WS送信関数を設定する (WSClient::Send のラッパを渡す)
  void SetSendFunc(SendFunc send_func) { sender_.SetSendFunc(std::move(send_func)); }

  /// バッテリー測定値 (実電圧) を更新する。device_status に載せる (§8.5)
  void UpdateBatteryVoltage(float voltage) { battery_voltage_ = voltage; }

  /// WiFi接続に失敗したことを伝える。
  /// サーバー待ちの表示を「紫点灯」から「紫点滅」へ変える (§4.0)。
  void SetWifiFailed(bool failed) { wifi_failed_ = failed; }

 private:
  // --- 状態遷移 ---
  void EnterSetup();
  void EnterReady();
  void EnterPlaying();
  void EnterDetonating(const char* reason, const char* detail, ColorId line);
  void EnterExploded();
  void EnterDefused();

  // --- イベント処理 ---
  void HandleEvent(const GameEvent& event);
  void HandleLineChanged(ColorId color, bool connected);
  void HandlePushChanged(ColorId color, bool pressed);
  void HandleRotaryChanged(int8_t position);
  void HandleWsMessage(const std::string& payload);

  // --- サーバーコマンド (§7.1) ---
  void HandleSessionStart(const std::string& payload);
  void HandleSessionAbort();
  void HandleForceDetonate();

  // --- 100ms tick 処理 (§8.2) ---
  void Tick();
  void TickCountdown();
  void TickLeds();
  void TickWhack();
  void TickForbiddenRotary();
  void TickDisplay();

  // --- ゲームルール (§5) ---
  void OnLineCut(ColorId color);
  bool IsPreconditionMet(const StageConfig& stage) const;

  /// timer_digit の猶予タイマーを進める (§5)
  void UpdateTimerDigitGrace();
  void ApplyPenalty(int32_t penalty_ms);
  void AdvanceStage();
  void ResetStageProgress();

  // --- push_seq (§5) ---
  void HandlePushSeqInput(ColorId color);

  // --- 出力 ---
  /// 現在の状態に応じて kLED の表示を反映する (§4.1)
  void ApplyLedOutputs();
  /// 上書き表示を解除する。whack 進行中はモグラ表示を復元する (§5.1)
  void ClearLedOverrides();
  void UpdateFullColorLed();
  void FireSolenoid();

  // --- 送信 (§7.2) ---
  /// 送信時点の状態をまとめる (EventSender へ渡す)
  StatusSnapshot MakeStatusSnapshot() const;
  void SendDeviceStatus();

  /// 全kLineが結線されているか
  bool AreAllLinesConnected() const;

 private:
  MCP23017* mcp23017_;
  HT16K33* ht16k33_;
  Pl9823Task* pl9823_task_;
  Buzzer buzzer_;
  LedController leds_;
  std::string device_id_;
  EventSender sender_;

  MessageQueue<GameEvent> message_queue_;

  GameState state_ = STATE_SETUP;
  SessionConfig session_;
  int32_t stage_index_ = 0;
  int32_t remaining_ms_ = 0;

  /// kLineの結線状態 (true=結線)
  bool line_connected_[kColorNum] = {true, true, true, true, true};
  /// kPushの押下状態 (true=押下)
  bool push_pressed_[kColorNum] = {false, false, false, false, false};
  /// 確定済みのロータリー位置 (§5.2)
  int8_t rotary_position_ = 0;

  /// モグラ叩きの進行 (§5.1)
  WhackGame whack_;

  /// ボタン列入力の進行 (§5)
  PushSeqInput push_seq_;

  // --- forbidden_rotary 進行状態 ---
  int32_t forbidden_hold_ms_ = 0;

  // --- timer_digit 判定窓 (§5: 一致期間+直後1秒の猶予) ---
  TimerDigitRule timer_digit_;

  // --- Setup/Ready 遷移 ---
  int32_t lines_stable_ms_ = 0;
  bool ws_connected_ = false;

  /// WiFi接続に失敗したか。サーバー待ちの表示を点滅にする (§4.0)
  bool wifi_failed_ = false;

  // --- Detonating ---
  int32_t detonate_remaining_ms_ = 0;
  /// ソレノイドを駆動済みか (二重駆動防止。§8.5)
  bool solenoid_fired_ = false;

  /// 最後に Tick() を進めた時刻。イベント連続時も実時間で刻むために保持する
  TickType_t last_tick_ = 0;

  // --- 定期送信・バッテリー ---
  int32_t status_timer_ms_ = 0;
  float battery_voltage_ = 0.0f;
  bool low_battery_ = false;
};

}  // namespace CoreSystem

#endif  // GAME_TASK_H_
// EOF
