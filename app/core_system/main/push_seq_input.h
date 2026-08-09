#ifndef PUSH_SEQ_INPUT_H_
#define PUSH_SEQ_INPUT_H_
// Core System
// (C)2026 bekki.jp
// ボタン列入力 (docs/game_session_design.md §5 の precondition.push_seq)
//
// 指定順のボタン列を入力させる。正入力のたびに進捗を通知し、ミス時は
// on_wrong_press (retry / penalty / explode) に従う。

// Include ----------------------
#include <cstdint>

#include "game_session.h"
#include "led_controller.h"

namespace CoreSystem {

/// push_seq の入力結果。副作用 (通知・ペナルティ・爆発) は GameTask 側で行う。
enum PushSeqResult : uint8_t {
  PUSH_SEQ_IGNORED,   ///< 対象外の入力 (既に完了している等)
  PUSH_SEQ_ADVANCED,  ///< 正しい入力。列を1つ進めた
  PUSH_SEQ_COMPLETED, ///< 正しい入力で列が完了した
  PUSH_SEQ_WRONG,     ///< 誤入力。on_wrong_press に従う必要がある
};

/// ボタン列入力の進行状態。GameTask が所有する。
class PushSeqInput final {
 public:
  /// フィードバック表示の長さ (正入力・ミスとも)
  static constexpr int32_t kFeedbackMs = 200;

  /// ステージ切り替え時に初期化する
  void Reset() {
    completed_ = false;
    index_ = 0;
    feedback_ms_ = 0;
  }

  /// 列を最後まで入力し終えたか (切断の事前条件)
  bool IsCompleted() const { return completed_; }

  /// 現在の進捗 (何個目まで入力できたか。push_progress 通知に使う)
  int32_t Index() const { return index_; }

  /// ボタン押下を処理する。副作用は呼び出し側が結果を見て行う。
  ///
  /// rotary_position は entry ごとのロータリー位置条件の判定に使う。
  PushSeqResult HandlePush(ColorId color, const PushSeqSpec& spec,
                           int8_t rotary_position, LedController* leds) {
    if (completed_ || static_cast<int32_t>(spec.entries.size()) <= index_) {
      return PUSH_SEQ_IGNORED;
    }

    const PushSeqEntry& entry = spec.entries[index_];
    const bool color_ok = (entry.push == color);
    const bool rotary_ok = (entry.rotary < 0) || (entry.rotary == rotary_position);

    if (!color_ok || !rotary_ok) {
      // ミス: 全LEDを短く点滅させて知らせる
      feedback_ms_ = kFeedbackMs;
      leds->SetOverrideAll(true);

      // retry / penalty は列の先頭からやり直し (explode の場合は呼び出し側で爆発)
      index_ = 0;
      return PUSH_SEQ_WRONG;
    }

    ++index_;

    // 正入力: 対応色のLEDを短点滅させる
    feedback_ms_ = kFeedbackMs;
    leds->ClearOverride();
    leds->SetOverride(color, true);

    if (static_cast<int32_t>(spec.entries.size()) <= index_) {
      completed_ = true;
      return PUSH_SEQ_COMPLETED;
    }
    return PUSH_SEQ_ADVANCED;
  }

  /// 100ms tick でフィードバック表示の時間を進める。
  /// 表示が終わった瞬間に true を返す (呼び出し側が上書き表示を解除する)
  bool TickFeedback() {
    if (feedback_ms_ <= 0) {
      return false;
    }
    feedback_ms_ -= kTickMs;
    return feedback_ms_ <= 0;
  }

 private:
  bool completed_ = false;
  int32_t index_ = 0;
  /// フィードバック表示の残り時間 (0より大きい間は上書き表示中)
  int32_t feedback_ms_ = 0;
};

}  // namespace CoreSystem

#endif  // PUSH_SEQ_INPUT_H_
// EOF
