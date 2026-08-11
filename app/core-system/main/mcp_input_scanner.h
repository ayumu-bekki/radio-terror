#ifndef MCP_INPUT_SCANNER_H_
#define MCP_INPUT_SCANNER_H_
// Core System
// (C)2026 bekki.jp
// MCP23017 のピン変化を GameEvent へ翻訳する (docs/game_session_design.md §5.2)

// Include ----------------------
#include <functional>

#include "game_task.h"
#include "hardware_config.h"
#include "mcp23017.h"

namespace CoreSystem {

/// MCP23017 の入力を走査し、変化を GameEvent として通知する。
///
/// kPush (Group B) と ロータリー (Group A) の読み取り状態をここに閉じ込める。
/// 前回値の保持とロータリーの確定判定を持つため、この2つを扱う唯一の場所にする。
class McpInputScanner final {
 public:
  using EventSink = std::function<void(const GameEvent&)>;

  McpInputScanner(MCP23017* mcp23017, EventSink sink)
      : mcp23017_(mcp23017), sink_(std::move(sink)) {}

  /// 全ピンを読み直し、前回値と差分があったピンを通知する。
  /// INTA割り込みのたびに呼ぶほか、起動時の初期状態取得にも使う。
  void Scan() {
    bool rotary_changed = false;

    for (uint8_t group = 0; group < MCP23017::GPIO_GROUP_NUM; ++group) {
      // グループ単位でI2Cを読み直す
      mcp23017_->RefreshInputGroup(group);

      for (uint8_t gpio_no = 0; gpio_no < MCP23017::GPIO_NUM; ++gpio_no) {
        const bool level = mcp23017_->GetCachedInputGpio(group, gpio_no);
        if (has_last_level_ && level == last_level_[group][gpio_no]) {
          continue;
        }
        last_level_[group][gpio_no] = level;

        if (group == Mcp23017Pin::kGroupB) {
          NotifyPushIfMatched(gpio_no, level);
        }

        // ロータリーは接点が複数変化するため、走査後にまとめて確定させる
        if (group == Mcp23017Pin::kGroupA && Mcp23017Pin::IsRotaryGpio(gpio_no)) {
          rotary_changed = true;
        }
      }
    }

    has_last_level_ = true;

    if (rotary_changed) {
      UpdateRotaryPosition();
    }
  }

 private:
  /// kPush の変化を通知する。プルアップ入力のため LOW = 押下。
  void NotifyPushIfMatched(uint8_t gpio_no, bool level) {
    const ColorId color = Mcp23017Pin::PushColorForGpio(gpio_no);
    if (color == COLOR_NONE) {
      return;
    }

    GameEvent event;
    event.type = EVENT_PUSH_CHANGED;
    event.color = color;
    event.level = !level;
    sink_(event);
  }

  /// ロータリーは「ちょうど1つがLOW」のときだけ値を確定する。
  /// 接点間の全OFF・複数ON等の過渡状態では最後に確定した値を保持する (§5.2)。
  void UpdateRotaryPosition() {
    int8_t low_position = -1;
    int low_count = 0;

    for (int position = 0; position < kRotaryPositionNum; ++position) {
      if (!mcp23017_->GetCachedInputGpio(
              Mcp23017Pin::kGroupA, Mcp23017Pin::kRotaryGpiosByPosition[position])) {
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
    sink_(event);
  }

  MCP23017* mcp23017_;
  EventSink sink_;

  bool last_level_[MCP23017::GPIO_GROUP_NUM][MCP23017::GPIO_NUM] = {};
  bool has_last_level_ = false;

  /// 直近に確定したロータリー位置 (過渡状態では最後の確定値を保持する)
  /// -1 は未確定。起動直後は位置が読めていないため 0 で初期化しない
  /// (0 で初期化すると「実際に位置0にある」ケースで初回通知が飛ばない)
  int8_t rotary_position_ = -1;
};

}  // namespace CoreSystem

#endif  // MCP_INPUT_SCANNER_H_
// EOF
