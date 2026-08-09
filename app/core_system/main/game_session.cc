// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "game_session.h"

#include <cJSON.h>

#include <cstring>

#include "logger.h"

namespace CoreSystem {

namespace {

/// 文字列 "blink" 指定時の既定点滅周期
constexpr int32_t kDefaultBlinkMs = 500;

/// モールス信号の既定単位長 (ITU標準タイミングの1単位)
constexpr int32_t kDefaultMorseUnitMs = 300;

/// モールス信号の単位長の下限 (tick粒度)
constexpr int32_t kMinMorseUnitMs = kTickMs;

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

/// 常時点灯のパターンを構築する
LedPattern MakeSolidOn() {
  LedPattern pattern;
  AppendOn(&pattern, 1);
  return pattern;
}

/// 常時消灯のパターンを構築する (ステップ列は空のままにする)
LedPattern MakeSolidOff() { return LedPattern(); }

/// 点灯・消灯時間を指定した点滅パターンを構築する
LedPattern MakeBlink(int32_t on_ms, int32_t off_ms) {
  LedPattern pattern;
  AppendOn(&pattern, MsToTicks(on_ms));
  AppendOff(&pattern, MsToTicks(off_ms));
  return pattern;
}

/// word をモールス信号として1周期分のパターンへ展開する (§6.1)
/// 対応外の文字が含まれる場合は false を返す
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

/// cJSON の数値を int32_t として取り出す。存在しない場合は default_value
int32_t GetInt(const cJSON* obj, const char* key, int32_t default_value) {
  const cJSON* item = cJSON_GetObjectItemCaseSensitive(obj, key);
  if (!cJSON_IsNumber(item)) {
    return default_value;
  }
  return static_cast<int32_t>(item->valuedouble);
}

/// パースエラーを組み立てる
ParseResult MakeError(const char* reason, const std::string& detail) {
  ParseResult result;
  result.ok = false;
  result.reason = reason;
  result.detail = detail;
  ESP_LOGW(TAG, "session parse error: %s (%s)", reason, detail.c_str());
  return result;
}

/// leds の1要素 (文字列またはオブジェクト) をLEDパターンへ変換する (§6.1)
bool ParseLedValue(const cJSON* value, int32_t led_blink_ms, LedPattern* out,
                   std::string* error_detail) {
  if (cJSON_IsString(value)) {
    const char* text = value->valuestring;
    if (std::strcmp(text, "on") == 0) {
      *out = MakeSolidOn();
      return true;
    }
    if (std::strcmp(text, "off") == 0) {
      *out = MakeSolidOff();
      return true;
    }
    if (std::strcmp(text, "blink") == 0) {
      // デューティ50%: led_blink_ms が周期なので点灯・消灯は各その半分
      const int32_t half = led_blink_ms / 2;
      *out = MakeBlink(half, half);
      return true;
    }
    *error_detail = std::string("unknown led string: ") + text;
    return false;
  }

  if (!cJSON_IsObject(value)) {
    *error_detail = "led value must be string or object";
    return false;
  }

  const cJSON* pattern_item = cJSON_GetObjectItemCaseSensitive(value, "pattern");
  if (!cJSON_IsString(pattern_item)) {
    *error_detail = "led object requires \"pattern\"";
    return false;
  }

  if (std::strcmp(pattern_item->valuestring, "blink") == 0) {
    const int32_t on_ms = GetInt(value, "on_ms", led_blink_ms / 2);
    const int32_t off_ms = GetInt(value, "off_ms", led_blink_ms / 2);
    if (on_ms <= 0 && off_ms <= 0) {
      *error_detail = "blink requires positive on_ms/off_ms";
      return false;
    }
    *out = MakeBlink(on_ms, off_ms);
    return true;
  }

  if (std::strcmp(pattern_item->valuestring, "morse") == 0) {
    const cJSON* word_item = cJSON_GetObjectItemCaseSensitive(value, "word");
    if (!cJSON_IsString(word_item)) {
      *error_detail = "morse requires \"word\"";
      return false;
    }
    int32_t unit_ms = GetInt(value, "unit_ms", kDefaultMorseUnitMs);
    if (unit_ms < kMinMorseUnitMs) {
      unit_ms = kMinMorseUnitMs;
    }
    if (!MakeMorse(word_item->valuestring, unit_ms, out)) {
      *error_detail =
          std::string("morse word contains unsupported char: ") + word_item->valuestring;
      return false;
    }
    return true;
  }

  *error_detail = std::string("unknown led pattern: ") + pattern_item->valuestring;
  return false;
}

/// on_wrong_cut / on_violation / on_wrong_press の共通パース
bool ParseActionSpec(const cJSON* obj, bool allow_retry, ActionSpec* out,
                     std::string* error_detail) {
  const cJSON* action_item = cJSON_GetObjectItemCaseSensitive(obj, "action");
  if (!cJSON_IsString(action_item)) {
    *error_detail = "action must be a string";
    return false;
  }

  const char* action = action_item->valuestring;
  if (std::strcmp(action, "explode") == 0) {
    out->action = ACTION_EXPLODE;
    out->penalty_ms = 0;
    return true;
  }
  if (std::strcmp(action, "penalty") == 0) {
    out->action = ACTION_PENALTY;
    out->penalty_ms = GetInt(obj, "penalty_ms", 0);
    if (out->penalty_ms <= 0) {
      *error_detail = "penalty requires positive penalty_ms";
      return false;
    }
    return true;
  }
  if (allow_retry && std::strcmp(action, "retry") == 0) {
    out->action = ACTION_RETRY;
    out->penalty_ms = 0;
    return true;
  }

  *error_detail = std::string("unknown action: ") + action;
  return false;
}

/// precondition オブジェクトをパースする (§5・§6)
bool ParsePrecondition(const cJSON* obj, Precondition* out, std::string* error_detail) {
  const cJSON* rotary_item = cJSON_GetObjectItemCaseSensitive(obj, "rotary");
  if (cJSON_IsNumber(rotary_item)) {
    const int32_t position = static_cast<int32_t>(rotary_item->valuedouble);
    if (position < 0 || kRotaryPositionNum <= position) {
      *error_detail = "precondition.rotary out of range (0-5)";
      return false;
    }
    out->has_rotary = true;
    out->rotary = static_cast<int8_t>(position);
  }

  const cJSON* push_item = cJSON_GetObjectItemCaseSensitive(obj, "push");
  if (cJSON_IsArray(push_item)) {
    const cJSON* element = nullptr;
    cJSON_ArrayForEach(element, push_item) {
      if (!cJSON_IsString(element)) {
        *error_detail = "precondition.push must be an array of color strings";
        return false;
      }
      const ColorId color = ColorFromChar(element->valuestring[0]);
      if (color == COLOR_NONE) {
        *error_detail = std::string("unknown push color: ") + element->valuestring;
        return false;
      }
      out->push_required[color] = true;
      out->has_push = true;
    }
  }

  const cJSON* whack_item = cJSON_GetObjectItemCaseSensitive(obj, "whack");
  if (cJSON_IsObject(whack_item)) {
    WhackSpec whack;
    whack.count = GetInt(whack_item, "count", 0);
    if (whack.count < 1) {
      *error_detail = "whack.count must be >= 1";
      return false;
    }
    whack.mole_on_ms = GetInt(whack_item, "mole_on_ms", whack.mole_on_ms);
    whack.gap_ms = GetInt(whack_item, "gap_ms", whack.gap_ms);
    if (whack.mole_on_ms <= 0 || whack.gap_ms < 0) {
      *error_detail = "whack requires positive mole_on_ms and non-negative gap_ms";
      return false;
    }
    whack.last_mole_matches_cut =
        cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(whack_item, "last_mole_matches_cut"));
    out->has_whack = true;
    out->whack = whack;
  }

  const cJSON* push_seq_item = cJSON_GetObjectItemCaseSensitive(obj, "push_seq");
  if (cJSON_IsObject(push_seq_item)) {
    PushSeqSpec push_seq;
    const cJSON* entries = cJSON_GetObjectItemCaseSensitive(push_seq_item, "entries");
    if (!cJSON_IsArray(entries)) {
      *error_detail = "push_seq requires \"entries\" array";
      return false;
    }
    const cJSON* element = nullptr;
    cJSON_ArrayForEach(element, entries) {
      PushSeqEntry entry;

      // {"push": "A"} 形式に加え、"A" の簡易記法も受け付ける
      const cJSON* push_field =
          cJSON_IsObject(element) ? cJSON_GetObjectItemCaseSensitive(element, "push") : element;
      if (!cJSON_IsString(push_field)) {
        *error_detail = "push_seq entry requires \"push\"";
        return false;
      }
      entry.push = ColorFromChar(push_field->valuestring[0]);
      if (entry.push == COLOR_NONE) {
        *error_detail = std::string("unknown push_seq color: ") + push_field->valuestring;
        return false;
      }

      if (cJSON_IsObject(element)) {
        const cJSON* entry_rotary = cJSON_GetObjectItemCaseSensitive(element, "rotary");
        if (cJSON_IsNumber(entry_rotary)) {
          const int32_t position = static_cast<int32_t>(entry_rotary->valuedouble);
          if (position < 0 || kRotaryPositionNum <= position) {
            *error_detail = "push_seq entry rotary out of range (0-5)";
            return false;
          }
          entry.rotary = static_cast<int8_t>(position);
        }
      }

      push_seq.entries.push_back(entry);
    }

    if (push_seq.entries.empty()) {
      *error_detail = "push_seq.entries must not be empty";
      return false;
    }

    const cJSON* on_wrong_press =
        cJSON_GetObjectItemCaseSensitive(push_seq_item, "on_wrong_press");
    if (cJSON_IsObject(on_wrong_press)) {
      if (!ParseActionSpec(on_wrong_press, true, &push_seq.on_wrong_press, error_detail)) {
        return false;
      }
    }

    out->has_push_seq = true;
    out->push_seq = std::move(push_seq);
  }

  const cJSON* timer_digit_item = cJSON_GetObjectItemCaseSensitive(obj, "timer_digit");
  if (cJSON_IsObject(timer_digit_item)) {
    TimerDigitSpec timer_digit;

    const cJSON* digit_item = cJSON_GetObjectItemCaseSensitive(timer_digit_item, "digit");
    if (!cJSON_IsString(digit_item)) {
      *error_detail = "timer_digit requires \"digit\" (ones/tens)";
      return false;
    }
    if (std::strcmp(digit_item->valuestring, "ones") == 0) {
      timer_digit.digit = TIMER_DIGIT_ONES;
    } else if (std::strcmp(digit_item->valuestring, "tens") == 0) {
      timer_digit.digit = TIMER_DIGIT_TENS;
    } else {
      *error_detail = std::string("unknown timer_digit.digit: ") + digit_item->valuestring;
      return false;
    }

    const cJSON* match_item = cJSON_GetObjectItemCaseSensitive(timer_digit_item, "match");
    const cJSON* value_item = cJSON_GetObjectItemCaseSensitive(timer_digit_item, "value");
    if (cJSON_IsString(match_item)) {
      if (std::strcmp(match_item->valuestring, "rotary") != 0) {
        *error_detail = std::string("unknown timer_digit.match: ") + match_item->valuestring;
        return false;
      }
      timer_digit.match = TIMER_MATCH_ROTARY;
    } else if (cJSON_IsNumber(value_item)) {
      const int32_t value = static_cast<int32_t>(value_item->valuedouble);
      if (value < 0 || 9 < value) {
        *error_detail = "timer_digit.value out of range (0-9)";
        return false;
      }
      timer_digit.match = TIMER_MATCH_VALUE;
      timer_digit.value = static_cast<int8_t>(value);
    } else {
      *error_detail = "timer_digit requires \"value\" or match=\"rotary\"";
      return false;
    }

    out->has_timer_digit = true;
    out->timer_digit = timer_digit;
  }

  out->leds_all_off = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(obj, "leds_all_off"));

  // whack と push_seq はどちらもkLED表示を専有するため併用不可 (§6.2)
  if (out->has_whack && out->has_push_seq) {
    *error_detail = "whack and push_seq cannot be combined";
    return false;
  }

  return true;
}

/// forbidden_rotary をパースする (§5)
bool ParseForbiddenRotary(const cJSON* obj, ForbiddenRotary* out, std::string* error_detail) {
  const cJSON* positions = cJSON_GetObjectItemCaseSensitive(obj, "positions");
  if (!cJSON_IsArray(positions)) {
    *error_detail = "forbidden_rotary requires \"positions\" array";
    return false;
  }

  const cJSON* element = nullptr;
  cJSON_ArrayForEach(element, positions) {
    if (!cJSON_IsNumber(element)) {
      *error_detail = "forbidden_rotary.positions must be numbers";
      return false;
    }
    const int32_t position = static_cast<int32_t>(element->valuedouble);
    if (position < 0 || kRotaryPositionNum <= position) {
      *error_detail = "forbidden_rotary position out of range (0-5)";
      return false;
    }
    out->positions[position] = true;
    out->enabled = true;
  }

  const cJSON* on_violation = cJSON_GetObjectItemCaseSensitive(obj, "on_violation");
  if (cJSON_IsObject(on_violation)) {
    if (!ParseActionSpec(on_violation, false, &out->on_violation, error_detail)) {
      return false;
    }
  }

  return true;
}

/// stages の1要素をパースする
bool ParseStage(const cJSON* obj, StageConfig* out, std::string* error_detail) {
  const int32_t led_blink_ms = GetInt(obj, "led_blink_ms", kDefaultBlinkMs);
  if (led_blink_ms <= 0) {
    *error_detail = "led_blink_ms must be positive";
    return false;
  }

  const cJSON* leds = cJSON_GetObjectItemCaseSensitive(obj, "leds");
  if (cJSON_IsObject(leds)) {
    const cJSON* element = nullptr;
    cJSON_ArrayForEach(element, leds) {
      const ColorId color = ColorFromChar(element->string ? element->string[0] : '\0');
      if (color == COLOR_NONE) {
        *error_detail = std::string("unknown led key: ") + (element->string ? element->string : "");
        return false;
      }
      if (!ParseLedValue(element, led_blink_ms, &out->leds[color], error_detail)) {
        return false;
      }
    }
  }

  const cJSON* cut_item = cJSON_GetObjectItemCaseSensitive(obj, "cut");
  if (!cJSON_IsString(cut_item)) {
    *error_detail = "stage requires \"cut\"";
    return false;
  }
  out->cut = ColorFromChar(cut_item->valuestring[0]);
  if (out->cut == COLOR_NONE) {
    *error_detail = std::string("unknown cut color: ") + cut_item->valuestring;
    return false;
  }

  const cJSON* precondition = cJSON_GetObjectItemCaseSensitive(obj, "precondition");
  if (cJSON_IsObject(precondition)) {
    if (!ParsePrecondition(precondition, &out->precondition, error_detail)) {
      return false;
    }
  }

  const cJSON* forbidden = cJSON_GetObjectItemCaseSensitive(obj, "forbidden_rotary");
  if (cJSON_IsObject(forbidden)) {
    if (!ParseForbiddenRotary(forbidden, &out->forbidden_rotary, error_detail)) {
      return false;
    }
  }

  const cJSON* on_wrong_cut = cJSON_GetObjectItemCaseSensitive(obj, "on_wrong_cut");
  if (cJSON_IsObject(on_wrong_cut)) {
    if (!ParseActionSpec(on_wrong_cut, false, &out->on_wrong_cut, error_detail)) {
      return false;
    }
  }

  return true;
}

}  // namespace

ColorId ColorFromChar(char c) {
  switch (c) {
    case 'A': case 'a': return COLOR_A;
    case 'B': case 'b': return COLOR_B;
    case 'C': case 'c': return COLOR_C;
    case 'D': case 'd': return COLOR_D;
    case 'E': case 'e': return COLOR_E;
    default: return COLOR_NONE;
  }
}

char ColorToChar(ColorId color) {
  if (color < COLOR_A || kColorNum <= color) {
    return '?';
  }
  return static_cast<char>('A' + color);
}

ParseResult ParseSessionJson(const std::string& json_text, SessionConfig* out) {
  cJSON* root = cJSON_Parse(json_text.c_str());
  if (!root) {
    return MakeError("parse_error", "invalid JSON");
  }

  SessionConfig config;
  std::string error_detail;

  const cJSON* session_id = cJSON_GetObjectItemCaseSensitive(root, "session_id");
  if (!cJSON_IsString(session_id)) {
    cJSON_Delete(root);
    return MakeError("parse_error", "session_id is required");
  }
  config.session_id = session_id->valuestring;

  config.countdown_ms = GetInt(root, "countdown_ms", 0);
  if (config.countdown_ms <= 0 || kCountdownMaxMs < config.countdown_ms) {
    cJSON_Delete(root);
    return MakeError("parse_error", "countdown_ms out of range (1-999900)");
  }

  config.detonate_delay_ms = GetInt(root, "detonate_delay_ms", 0);
  if (config.detonate_delay_ms < 0) {
    cJSON_Delete(root);
    return MakeError("parse_error", "detonate_delay_ms must be non-negative");
  }

  const cJSON* stages = cJSON_GetObjectItemCaseSensitive(root, "stages");
  if (!cJSON_IsArray(stages) || cJSON_GetArraySize(stages) == 0) {
    cJSON_Delete(root);
    return MakeError("parse_error", "stages must be a non-empty array");
  }

  bool cut_used[kColorNum] = {false, false, false, false, false};
  const cJSON* stage_item = nullptr;
  cJSON_ArrayForEach(stage_item, stages) {
    if (!cJSON_IsObject(stage_item)) {
      cJSON_Delete(root);
      return MakeError("parse_error", "stage must be an object");
    }

    StageConfig stage;
    if (!ParseStage(stage_item, &stage, &error_detail)) {
      cJSON_Delete(root);
      return MakeError("parse_error", error_detail);
    }

    // 配線は物理的に1本ずつしか切れないため、cut はステージ間で重複できない (§6.2)
    if (cut_used[stage.cut]) {
      cJSON_Delete(root);
      return MakeError("parse_error",
                       std::string("duplicated cut line: ") + ColorToChar(stage.cut));
    }
    cut_used[stage.cut] = true;

    config.stages.push_back(std::move(stage));
  }

  // 配線は5本しかないため、6ステージ以上は物理的に成立しない。
  // (サーバー側はコンテンツ方針として更に少ない上限を設けているが、
  //  デバイス側はハードウェアの制約だけを見る)
  if (kColorNum < static_cast<int>(config.stages.size())) {
    cJSON_Delete(root);
    return MakeError("parse_error", "too many stages (max 5 lines)");
  }

  cJSON_Delete(root);

  *out = std::move(config);

  ParseResult result;
  result.ok = true;
  return result;
}

}  // namespace CoreSystem

// EOF
