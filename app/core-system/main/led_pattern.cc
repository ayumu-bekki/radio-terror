// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "led_pattern.h"

#include <utility>

namespace CoreSystem {
namespace LedPatternBuilder {

namespace {

/// ミリ秒をtick数へ切り上げ変換する (0msでも最低1tickは確保する)
int32_t MsToTicks(int32_t ms) {
  if (ms <= 0) {
    return 1;
  }
  return (ms + kTickMs - 1) / kTickMs;
}

/// A-Z / 0-9 のモールス符号表 ('.' = 短点, '-' = 長点)
struct MorseEntry {
  char c;
  const char* code;
};
constexpr MorseEntry kMorseTable[] = {
    {'A', ".-"},    {'B', "-..."},  {'C', "-.-."},  {'D', "-.."},
    {'E', "."},     {'F', "..-."},  {'G', "--."},   {'H', "...."},
    {'I', ".."},    {'J', ".---"},  {'K', "-.-"},   {'L', ".-.."},
    {'M', "--"},    {'N', "-."},    {'O', "---"},   {'P', ".--."},
    {'Q', "--.-"},  {'R', ".-."},   {'S', "..."},   {'T', "-"},
    {'U', "..-"},   {'V', "...-"},  {'W', ".--"},   {'X', "-..-"},
    {'Y', "-.--"},  {'Z', "--.."},  {'0', "-----"}, {'1', ".----"},
    {'2', "..---"}, {'3', "...--"}, {'4', "....-"}, {'5', "....."},
    {'6', "-...."}, {'7', "--..."}, {'8', "---.."}, {'9', "----."},
};

/// 英字1文字のモールス符号を引く。対応外の文字は nullptr
const char* FindMorseCode(char c) {
  const char upper = (c >= 'a' && c <= 'z') ? static_cast<char>(c - 'a' + 'A') : c;
  for (const auto& entry : kMorseTable) {
    if (entry.c == upper) {
      return entry.code;
    }
  }
  return nullptr;
}

/// パターン末尾に「消灯」のステップを積む (直前が消灯なら継続時間を延ばす)
void AppendOff(LedPattern* pattern, int32_t ticks) {
  if (ticks <= 0) {
    return;
  }
  if (!pattern->steps.empty() && !pattern->steps.back().on) {
    pattern->steps.back().ticks += ticks;
    return;
  }
  pattern->steps.push_back(LedStep{false, ticks});
}

/// パターン末尾に「点灯」のステップを積む
void AppendOn(LedPattern* pattern, int32_t ticks) {
  if (ticks <= 0) {
    return;
  }
  pattern->always_off = false;
  if (!pattern->steps.empty() && pattern->steps.back().on) {
    pattern->steps.back().ticks += ticks;
    return;
  }
  pattern->steps.push_back(LedStep{true, ticks});
}

}  // namespace

LedPattern MakeSolidOn() {
  LedPattern pattern;
  AppendOn(&pattern, 1);
  return pattern;
}

LedPattern MakeSolidOff() { return LedPattern(); }

LedPattern MakeBlink(int32_t on_ms, int32_t off_ms) {
  LedPattern pattern;
  AppendOn(&pattern, MsToTicks(on_ms));
  AppendOff(&pattern, MsToTicks(off_ms));
  return pattern;
}

bool MakeMorse(const std::string& word, int32_t unit_ms, LedPattern* out) {
  if (word.empty()) {
    return false;
  }

  const int32_t unit_ticks = MsToTicks(unit_ms);
  LedPattern pattern;

  for (size_t i = 0; i < word.size(); ++i) {
    const char* code = FindMorseCode(word[i]);
    if (!code) {
      return false;
    }

    for (size_t j = 0; code[j] != '\0'; ++j) {
      // 短点 = 1単位点灯, 長点 = 3単位点灯
      AppendOn(&pattern, code[j] == '-' ? unit_ticks * 3 : unit_ticks);
      // 符号内の要素間 = 1単位消灯
      if (code[j + 1] != '\0') {
        AppendOff(&pattern, unit_ticks);
      }
    }

    // 文字間 = 3単位消灯
    if (i + 1 < word.size()) {
      AppendOff(&pattern, unit_ticks * 3);
    }
  }

  // 語の末尾は7単位消灯を挟んで先頭から繰り返す
  AppendOff(&pattern, unit_ticks * 7);

  *out = std::move(pattern);
  return true;
}

}  // namespace LedPatternBuilder
}  // namespace CoreSystem

// EOF
