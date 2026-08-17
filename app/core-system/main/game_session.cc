// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "game_session.h"

#include <cJSON.h>

#include <cstring>

#include "led_pattern.h"
#include "logger.h"

namespace CoreSystem {

namespace {

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
      *out = LedPatternBuilder::MakeSolidOn();
      return true;
    }
    if (std::strcmp(text, "off") == 0) {
      *out = LedPatternBuilder::MakeSolidOff();
      return true;
    }
    if (std::strcmp(text, "blink") == 0) {
      // デューティ50%: led_blink_ms が周期なので点灯・消灯は各その半分
      const int32_t half = led_blink_ms / 2;
      *out = LedPatternBuilder::MakeBlink(half, half);
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
    *out = LedPatternBuilder::MakeBlink(on_ms, off_ms);
    return true;
  }

  if (std::strcmp(pattern_item->valuestring, "morse") == 0) {
    const cJSON* word_item = cJSON_GetObjectItemCaseSensitive(value, "word");
    if (!cJSON_IsString(word_item)) {
      *error_detail = "morse requires \"word\"";
      return false;
    }
    int32_t unit_ms = GetInt(value, "unit_ms", LedPatternBuilder::kDefaultMorseUnitMs);
    if (unit_ms < LedPatternBuilder::kMinMorseUnitMs) {
      unit_ms = LedPatternBuilder::kMinMorseUnitMs;
    }
    if (!LedPatternBuilder::MakeMorse(word_item->valuestring, unit_ms, out)) {
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

  const cJSON* color_match_item = cJSON_GetObjectItemCaseSensitive(obj, "color_match");
  if (cJSON_IsObject(color_match_item)) {
    ColorMatchSpec color_match;
    color_match.count = GetInt(color_match_item, "count", 0);
    if (color_match.count < 1) {
      *error_detail = "color_match.count must be >= 1";
      return false;
    }
    color_match.last_matches_cut =
        cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(color_match_item, "last_matches_cut"));
    color_match.penalty_ms = GetInt(color_match_item, "penalty_ms", color_match.penalty_ms);
    if (color_match.penalty_ms < 0) {
      *error_detail = "color_match penalty_ms must be non-negative";
      return false;
    }
    out->has_color_match = true;
    out->color_match = color_match;
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

    // offset: 桁に加算してから比較する (暗算を要する謎用)
    const cJSON* offset_item =
        cJSON_GetObjectItemCaseSensitive(timer_digit_item, "offset");
    if (cJSON_IsNumber(offset_item)) {
      const int32_t offset = static_cast<int32_t>(offset_item->valuedouble);
      if (offset < -9 || 9 < offset) {
        *error_detail = "timer_digit.offset out of range (-9..9)";
        return false;
      }
      timer_digit.offset = static_cast<int8_t>(offset);
    }

    out->has_timer_digit = true;
    out->timer_digit = timer_digit;
  }

  out->leds_all_off = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(obj, "leds_all_off"));

  // color_match と push_seq はどちらもkLED表示を専有するため併用不可 (§6.2)
  if (out->has_color_match && out->has_push_seq) {
    *error_detail = "color_match and push_seq cannot be combined";
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
  const int32_t led_blink_ms = GetInt(obj, "led_blink_ms", LedPatternBuilder::kDefaultBlinkMs);
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

/// 色は大文字のみ受け付ける。サーバーが送る色は常に大文字 A-E で
/// (`allColors` / ステージ定義TOML)、小文字は仕様上ありえない。
/// 受け入れを広げるとJSONの不正を取りこぼすため、意図的に厳しくしている。
ColorId ColorFromChar(char c) {
  if (c < 'A' || 'A' + kColorNum <= c) {
    return COLOR_NONE;
  }
  return static_cast<ColorId>(c - 'A');
}

char ColorToChar(ColorId color) {
  if (color < COLOR_A || kColorNum <= color) {
    return '?';
  }
  return static_cast<char>('A' + color);
}

const char* GameStateName(GameState state) {
  if (state == STATE_SETUP) {
    return "setup";
  }
  if (state == STATE_READY) {
    return "ready";
  }
  if (state == STATE_PENDING) {
    return "pending";
  }
  if (state == STATE_PLAYING) {
    return "playing";
  }
  if (state == STATE_DETONATING) {
    return "detonating";
  }
  if (state == STATE_EXPLODED) {
    return "exploded";
  }
  if (state == STATE_DEFUSED) {
    return "defused";
  }
  return "unknown";
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
