#ifndef LED_PATTERN_H_
#define LED_PATTERN_H_
// Core System
// (C)2026 bekki.jp
// kLED表示パターンの構築 (docs/game_session_design.md §6.1)

// Include ----------------------
#include <cstdint>
#include <string>

#include "game_session.h"  // LedPattern / LedStep / kTickMs

namespace CoreSystem {

/// LEDパターンの構築。
///
/// セッションJSONの解釈とは独立した「時間軸の組み立て」なので、
/// パース処理 (game_session.cc) から分けている。
namespace LedPatternBuilder {

/// 文字列 "blink" 指定時の既定点滅周期
inline constexpr int32_t kDefaultBlinkMs = 500;

/// モールス信号の既定単位長 (ITU標準タイミングの1単位)
inline constexpr int32_t kDefaultMorseUnitMs = 300;

/// モールス信号の単位長の下限 (tick粒度)
inline constexpr int32_t kMinMorseUnitMs = kTickMs;

/// 常時点灯のパターンを構築する
LedPattern MakeSolidOn();

/// 常時消灯のパターンを構築する (ステップ列は空のままにする)
LedPattern MakeSolidOff();

/// 点灯・消灯時間を指定した点滅パターンを構築する
LedPattern MakeBlink(int32_t on_ms, int32_t off_ms);

/// word をモールス信号として1周期分のパターンへ展開する (§6.1)。
/// 対応外の文字が含まれる場合は false を返し、*out は変更しない。
bool MakeMorse(const std::string& word, int32_t unit_ms, LedPattern* out);

}  // namespace LedPatternBuilder

}  // namespace CoreSystem

#endif  // LED_PATTERN_H_
// EOF
