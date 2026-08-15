#ifndef MCP_INPUT_SCANNER_H_
#define MCP_INPUT_SCANNER_H_
// Core System
// (C)2026 bekki.jp
// MCP23017 のピン変化を GameEvent へ翻訳する (docs/game_session_design.md §5.2)

// Include ----------------------
#include <driver/gpio.h>

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

  /// interrupt_gpio は MCP23017 の INTA を受けている GPIO。
  /// ScanIfPending() がこのレベルを見て取りこぼしを拾い直す。
  McpInputScanner(MCP23017* mcp23017, EventSink sink,
                  gpio_num_t interrupt_gpio = Esp32Pin::kMcp23017Interrupt)
      : mcp23017_(mcp23017),
        sink_(std::move(sink)),
        interrupt_gpio_(interrupt_gpio) {}

  /// INTA が LOW (割り込み要求中) なら Scan() する。
  ///
  /// **エッジではなくレベルで見る**のがこの関数の要点。MCP23017 の割り込みは
  /// GPIO/INTCAP を読むまでクリアされず、その間 INTA は LOW に張り付く。
  /// 一方 GpioInputWatchTask の通知は「LOW が3サンプル続いた**瞬間**」の
  /// 1回だけで、以降カウンタが飽和して二度と呼ばれない。
  ///
  /// そのため「ロータリーを速く回して、Scan() が走る前に次の変化が起きる」と、
  /// INTA が LOW のまま次の通知も来ず、**入力検知が永久に止まる**。
  /// (実機で発生: 高速に回すと rotary ログが出なくなる)
  ///
  /// レベルで毎周期見れば、取りこぼしても次の周期で必ず拾い直せる。
  void ScanIfPending() {
    if (gpio_get_level(interrupt_gpio_) != 0) {
      return;  // 割り込み要求なし
    }
    Scan();
  }

  /// 全ピンを読み直し、前回値と差分があったピンを通知する。
  /// 起動時の初期状態取得にも使う。
  void Scan() {
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
      }
    }

    has_last_level_ = true;

    // ロータリーは**差分の有無によらず毎回確定を試みる**。
    //
    // 「差分があったときだけ」にすると、接点が離れている過渡状態
    // (全OFF) を読んだスキャンで差分を消費してしまい、その後ピンが
    // 安定しても「差分なし」で確定処理に入れない。読み値から現在位置を
    // 決めるだけの処理なので、毎回呼んでも副作用はない
    // (同一位置なら UpdateRotaryPosition 側で通知を抑止する)。
    UpdateRotaryPosition();
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
  gpio_num_t interrupt_gpio_;

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
