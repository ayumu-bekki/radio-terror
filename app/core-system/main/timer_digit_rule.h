#ifndef TIMER_DIGIT_RULE_H_
#define TIMER_DIGIT_RULE_H_
// Core System
// (C)2026 bekki.jp
// timer_digit の桁一致判定と猶予窓 (docs/game_session_design.md §5)

// Include ----------------------
#include <cstdint>

#include "game_session.h"

namespace CoreSystem {

/// timer_digit 事前条件の判定。
///
/// 判定窓は「対象桁が一致する期間 + 直後1秒の猶予」(§5)。
/// 一致している間は猶予を満タンに保ち、一致から外れた瞬間から減り始める。
/// これにより「切ろうとしたら桁が変わってしまった」を救済する。
///
/// 猶予の残量だけを状態として持ち、ゲーム状態には触れない。
class TimerDigitRule final {
 public:
  /// 桁が一致しなくなった後の猶予 (§5)
  static constexpr int32_t kGraceMs = 1000;

  /// 残り時間の対象桁が条件に一致しているか (猶予を考慮しない素の判定)。
  static bool IsMatchedNow(const TimerDigitSpec& spec, int32_t remaining_ms,
                           int8_t rotary_position) {
    const int32_t seconds = remaining_ms / 1000;
    const int32_t raw_digit =
        (spec.digit == TIMER_DIGIT_ONES) ? (seconds % 10) : (seconds / 10 % 10);

    // offset を加算してから比較する (暗算を要する謎用。offset=0 なら桁そのもの)
    const int32_t digit = raw_digit + spec.offset;

    const int32_t target =
        (spec.match == TIMER_MATCH_ROTARY) ? rotary_position : spec.value;

    return digit == target;
  }

  /// 猶予を含めて条件を満たしているか。
  bool IsMet(const TimerDigitSpec& spec, int32_t remaining_ms,
             int8_t rotary_position) const {
    if (IsMatchedNow(spec, remaining_ms, rotary_position)) {
      return true;
    }
    return 0 < grace_ms_;
  }

  /// 猶予タイマーを1tick進める (一致中は満タン、外れたら減衰)。
  void Tick(const TimerDigitSpec& spec, int32_t remaining_ms, int8_t rotary_position) {
    if (IsMatchedNow(spec, remaining_ms, rotary_position)) {
      grace_ms_ = kGraceMs;
    } else if (0 < grace_ms_) {
      grace_ms_ -= kTickMs;
    }
  }

  /// 猶予を捨てる。条件の無いステージ・Playing以外では常にこれを呼ぶ。
  void Reset() { grace_ms_ = 0; }

 private:
  /// 猶予の残量。0 なら猶予切れ
  int32_t grace_ms_ = 0;
};

}  // namespace CoreSystem

#endif  // TIMER_DIGIT_RULE_H_
// EOF
