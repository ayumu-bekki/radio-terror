#ifndef EVENT_SENDER_H_
#define EVENT_SENDER_H_
// Core System
// (C)2026 bekki.jp
// デバイス → サーバーのメッセージ送信 (docs/game_session_design.md §7.2)

// Include ----------------------
#include <cJSON.h>

#include <functional>
#include <string>

#include "game_session.h"
#include "logger.h"

namespace CoreSystem {

/// GameTaskから外部 (WSClient) へメッセージを送るための送信関数
using SendFunc = std::function<void(const std::string&)>;

/// 送信時点のゲーム状態。
///
/// 送信処理がゲーム状態を直接覗くと結合が強くなるため、
/// 必要な値だけをまとめて渡す。
struct StatusSnapshot {
  const char* state = "unknown";
  const char* session_id = "";
  int32_t stage_index = 0;
  int32_t remaining_ms = 0;
  float battery = 0.0f;
  bool low_battery = false;

  /// ロータリースイッチの現在位置 (0-5)。
  /// マネージャー画面で現場の状況を把握するために送る。
  int32_t rotary = 0;

  /// kLineの結線状態 (true=結線)
  const bool* line_connected = nullptr;
};

/// サーバーへ送るメッセージを組み立てて送信する。
///
/// **サーバー → デバイスと同様、送信にも必ず `device_id` を含める** (§7.1)。
/// 宛先解決の誤りが誤爆発に直結するため、どちらの向きでも識別子を載せる。
class EventSender final {
 public:
  explicit EventSender(std::string device_id) : device_id_(std::move(device_id)) {}

  void SetSendFunc(SendFunc func) { send_func_ = std::move(func); }

  void SendDeviceStatus(const StatusSnapshot& status) {
    SendJson("device_status", [&status](cJSON* root) {
      cJSON_AddStringToObject(root, "state", status.state);
      cJSON_AddStringToObject(root, "session_id", status.session_id);
      cJSON_AddNumberToObject(root, "stage_index", status.stage_index);
      cJSON_AddNumberToObject(root, "remaining_ms", status.remaining_ms);
      cJSON_AddNumberToObject(root, "battery", status.battery);
      cJSON_AddBoolToObject(root, "low_battery", status.low_battery);
      cJSON_AddNumberToObject(root, "rotary", status.rotary);

      // 各配線の結線状態 (Setup中の復旧進捗をマネージャーが確認するのに使う)
      if (status.line_connected) {
        cJSON* lines = cJSON_CreateObject();
        for (int i = 0; i < kColorNum; ++i) {
          const char key[2] = {ColorToChar(static_cast<ColorId>(i)), '\0'};
          cJSON_AddBoolToObject(lines, key, status.line_connected[i]);
        }
        cJSON_AddItemToObject(root, "lines", lines);
      }
    });
  }

  void SendSessionAccepted(const std::string& session_id) {
    SendJson("session_accepted", [&session_id](cJSON* root) {
      cJSON_AddStringToObject(root, "session_id", session_id.c_str());
    });
  }

  void SendSessionRejected(const char* reason, const std::string& detail) {
    ESP_LOGW(TAG, "session_rejected: %s (%s)", reason, detail.c_str());
    SendJson("session_rejected", [reason, &detail](cJSON* root) {
      cJSON_AddStringToObject(root, "reason", reason);
      if (!detail.empty()) {
        cJSON_AddStringToObject(root, "detail", detail.c_str());
      }
    });
  }

  void SendStageCleared(int32_t stage_index, int32_t remaining_ms) {
    SendJson("stage_cleared", [stage_index, remaining_ms](cJSON* root) {
      cJSON_AddNumberToObject(root, "stage_index", stage_index);
      cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms);
    });
  }

  void SendColorMatchCompleted(int32_t stage_index, int32_t remaining_ms) {
    SendJson("color_match_completed", [stage_index, remaining_ms](cJSON* root) {
      cJSON_AddNumberToObject(root, "stage_index", stage_index);
      cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms);
    });
  }

  void SendPushProgress(int32_t stage_index, int32_t seq_index, int32_t remaining_ms) {
    SendJson("push_progress", [stage_index, seq_index, remaining_ms](cJSON* root) {
      cJSON_AddNumberToObject(root, "stage_index", stage_index);
      cJSON_AddNumberToObject(root, "seq_index", seq_index);
      cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms);
    });
  }

  void SendWrongAction(const char* detail, ColorId line, int32_t penalty_ms,
                       int32_t remaining_ms) {
    SendJson("wrong_action", [detail, line, penalty_ms, remaining_ms](cJSON* root) {
      cJSON_AddStringToObject(root, "detail", detail);
      AddColorIfPresent(root, "line", line);
      cJSON_AddNumberToObject(root, "penalty_ms", penalty_ms);
      cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms);
    });
  }

  void SendExploded(const char* reason, const char* detail, ColorId line) {
    SendJson("exploded", [reason, detail, line](cJSON* root) {
      cJSON_AddStringToObject(root, "reason", reason);
      if (detail) {
        cJSON_AddStringToObject(root, "detail", detail);
      }
      AddColorIfPresent(root, "line", line);
    });
  }

  void SendDefused(int32_t remaining_ms) {
    SendJson("defused", [remaining_ms](cJSON* root) {
      cJSON_AddNumberToObject(root, "remaining_ms", remaining_ms);
    });
  }

 private:
  /// 共通項目 (type / device_id) を載せて送信する。
  /// fill で個別の項目を足す。
  void SendJson(const char* type, const std::function<void(cJSON*)>& fill) {
    if (!send_func_) {
      return;
    }

    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", type);
    cJSON_AddStringToObject(root, "device_id", device_id_.c_str());
    if (fill) {
      fill(root);
    }

    char* json_text = cJSON_PrintUnformatted(root);
    if (json_text) {
      send_func_(json_text);
      cJSON_free(json_text);
    }
    cJSON_Delete(root);
  }

  /// 色を1文字の文字列として載せる。COLOR_NONE のときは項目ごと省く。
  static void AddColorIfPresent(cJSON* root, const char* key, ColorId color) {
    if (color == COLOR_NONE) {
      return;
    }
    const char value[2] = {ColorToChar(color), '\0'};
    cJSON_AddStringToObject(root, key, value);
  }

  std::string device_id_;
  SendFunc send_func_;
};

}  // namespace CoreSystem

#endif  // EVENT_SENDER_H_
// EOF
