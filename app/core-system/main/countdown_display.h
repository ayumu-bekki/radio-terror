#ifndef COUNTDOWN_DISPLAY_H_
#define COUNTDOWN_DISPLAY_H_
// Core System
// (C)2026 bekki.jp
// 残り時間の7セグ表示 (docs/game_session_design.md §4.1)

// Include ----------------------
#include <cstdint>

#include "ht16k33.h"

namespace CoreSystem {

/// カウントダウンの7セグ表示。
///
/// 残り時間を表示形式へ変換するだけで、ゲーム状態には触れない。
namespace CountdownDisplay {

/// 残り時間を「秒 + 0.1秒」で表示する (最大 999.9 秒)。
///
/// 整数部3桁+小数点1桁を右詰めで出し、先頭の不要なゼロは消灯する。
inline void Render(HT16K33* ht16k33, int32_t remaining_ms) {
  const int32_t tenths_total = remaining_ms / 100;
  const int seconds = static_cast<int>(tenths_total / 10);
  const int tenths = static_cast<int>(tenths_total % 10);

  const int int_digits[3] = {seconds / 100 % 10, seconds / 10 % 10, seconds % 10};

  ht16k33->Clear();
  bool leading = true;
  for (int i = 0; i < 3; ++i) {
    // 先頭のゼロは消す。ただし一の位 (i==2) は 0 でも必ず出す
    if (leading && int_digits[i] == 0 && i < 2) {
      continue;
    }
    leading = false;
    // 一の位に小数点を付ける
    ht16k33->WriteDigitNum(i, int_digits[i], i == 2);
  }
  ht16k33->WriteDigitNum(3, tenths);
  ht16k33->WriteDisplay();
}

}  // namespace CountdownDisplay

}  // namespace CoreSystem

#endif  // COUNTDOWN_DISPLAY_H_
// EOF
