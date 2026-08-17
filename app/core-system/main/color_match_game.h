#ifndef COLOR_MATCH_GAME_H_
#define COLOR_MATCH_GAME_H_
// Core System
// (C)2026 bekki.jp
// 色合わせ (docs/game_session_design.md §5.1)
//
// kLED A-E と kPush A-E の対応を使った入力ゲーム。
// precondition.color_match を持つステージで発動し、完了が切断の事前条件になる。
// 進行中は kLED を専有し、完了した瞬間に全消灯する。
//
// **時間で切り替わらない。** 1色だけ点灯した状態で待ち、正しいボタンを
// 押した瞬間に次の色へ進む。急かす要素は無く、押し間違いだけがミスになる。

// Include ----------------------
#include <esp_random.h>

#include <cstdint>

#include "game_session.h"
#include "led_controller.h"

namespace CoreSystem {

/// 色合わせの1操作の結果。
///
/// **ミスを呼び出し側へ返す**のが要点。以前は bool (完了したか) だけを返しており、
/// **ミスに何の反応も無かった** — 押し直しになるだけで、失敗したことに
/// 気づけなかった (§5.1)。
enum ColorMatchResult : uint8_t {
  COLOR_MATCH_NONE,       ///< 何も起きていない
  COLOR_MATCH_HIT,        ///< 正しく押した (まだ完了していない)
  COLOR_MATCH_MISSED,     ///< ミス (点灯中の色と違うボタンを押した)
  COLOR_MATCH_COMPLETED,  ///< 規定数を押し終えた
};

class ColorMatchGame final {
 public:
  /// ステージ開始時に発動する。最初の色をすぐ点灯させる
  void Start(const ColorMatchSpec& spec, const StageConfig& stage, LedController* leds) {
    active_ = true;
    completed_ = false;
    hits_ = 0;
    current_ = PickNext(stage);
    leds->ClearOverride();
    leds->SetOverride(current_, true);
  }

  /// 発動していない状態へ戻す (ステージ切り替え時)
  void Reset() {
    active_ = false;
    completed_ = false;
    hits_ = 0;
    current_ = COLOR_NONE;
  }

  /// 進行中か (kLED を専有している状態か)
  bool IsRunning() const { return active_ && !completed_; }

  /// 規定数を押し終えたか (切断の事前条件)
  bool IsCompleted() const { return completed_; }

  /// ボタン押下を処理する。**進行はこの関数だけが進める** (tick は無い)。
  ///
  /// 点灯している色と違うボタンならミス。同じ色を出したまま待ち続ける。
  ColorMatchResult HandlePush(ColorId color, const StageConfig& stage, LedController* leds) {
    if (!IsRunning()) {
      return COLOR_MATCH_NONE;
    }

    if (color != current_) {
      // 点灯中の色と違うボタンを押したミス。同じ色を出したまま待つ
      return COLOR_MATCH_MISSED;
    }

    ++hits_;
    const ColorMatchSpec& spec = stage.precondition.color_match;
    if (hits_ < spec.count) {
      // 次の色へ。押した瞬間に切り替わる
      current_ = PickNext(stage);
      leds->ClearOverride();
      leds->SetOverride(current_, true);
      return COLOR_MATCH_HIT;
    }

    // 完了。**全消灯して終わる** — 「最後に押した色」だけが手がかりとして残る。
    // ClearOverride ではステージのヒント表示に戻ってしまうため、
    // SetOverrideAll(false) で消灯を保つ (§5.1)。
    completed_ = true;
    active_ = false;
    current_ = COLOR_NONE;
    leds->SetOverrideAll(false);
    return COLOR_MATCH_COMPLETED;
  }

  /// 現在点灯中の色 (出ていなければ COLOR_NONE)
  ColorId Current() const { return current_; }

 private:
  /// 次の色を抽選する。出題順はデバイス側で決める (JSONは数のみ)
  ColorId PickNext(const StageConfig& stage) const {
    const ColorMatchSpec& spec = stage.precondition.color_match;

    // last_matches_cut: 最後の1つだけ cut と同色に固定する
    // (「最後に押した色の線を切れ」の謎用)
    if (spec.last_matches_cut && hits_ == spec.count - 1) {
      return stage.cut;
    }

    // 直前と同じLEDは避ける (同じ色が連続すると押し間違いが増えるため)
    for (int attempt = 0; attempt < 16; ++attempt) {
      const ColorId candidate = static_cast<ColorId>(esp_random() % kColorNum);
      if (candidate != current_) {
        return candidate;
      }
    }
    return static_cast<ColorId>(esp_random() % kColorNum);
  }

 private:
  bool active_ = false;
  bool completed_ = false;
  int32_t hits_ = 0;
  ColorId current_ = COLOR_NONE;
};

}  // namespace CoreSystem

#endif  // COLOR_MATCH_GAME_H_
// EOF
