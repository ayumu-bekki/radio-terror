#ifndef WHACK_GAME_H_
#define WHACK_GAME_H_
// Core System
// (C)2026 bekki.jp
// モグラ叩き (docs/game_session_design.md §5.1)
//
// kLED A-E と kPush A-E の対応を使った反射ゲーム。
// precondition.whack を持つステージで発動し、完了が切断の事前条件になる。
// 進行中は kLED を専有し、完了した瞬間にステージのヒント表示へ切り替わる。

// Include ----------------------
#include <esp_random.h>

#include <cstdint>

#include "game_session.h"
#include "led_controller.h"

namespace CoreSystem {

/// モグラ叩きの進行状態。GameTask が所有し、100ms tick から進める。
/// モグラ叩きの1操作の結果。
///
/// **ミスを呼び出し側へ返す**のが要点。以前は bool (完了したか) だけを返しており、
/// **ミスに何の反応も無かった** — 叩き直しになるだけで、失敗したことに
/// 気づけなかった (§5.1)。
///
/// ミスは**誤ったボタンを押したときだけ**。押せずに消灯した場合は
/// 静かに出し直す — 無操作でブザーが鳴り続けるのを避けるため。
enum WhackResult : uint8_t {
  WHACK_NONE,       ///< 何も起きていない
  WHACK_HIT,        ///< 正しく叩いた (まだ完了していない)
  WHACK_MISSED,     ///< ミス (誤ったボタンを押した。時間切れは含まない)
  WHACK_COMPLETED,  ///< 規定数を叩き終えた
};

class WhackGame final {
 public:
  /// ステージ開始時に発動する。最初のモグラは gap 経過後に出現する
  void Start(const WhackSpec& spec, LedController* leds) {
    active_ = true;
    completed_ = false;
    hits_ = 0;
    in_gap_ = true;
    timer_ms_ = spec.gap_ms;
    current_mole_ = COLOR_NONE;
    leds->ClearOverride();
  }

  /// 発動していない状態へ戻す (ステージ切り替え時)
  void Reset() {
    active_ = false;
    completed_ = false;
    hits_ = 0;
    current_mole_ = COLOR_NONE;
    timer_ms_ = 0;
    in_gap_ = false;
  }

  /// 進行中か (kLED を専有している状態か)
  bool IsRunning() const { return active_ && !completed_; }

  /// 規定匹数を叩き終えたか (切断の事前条件)
  bool IsCompleted() const { return completed_; }

  /// 100ms tick で出現・消灯を進める。
  ///
  /// **常に WHACK_NONE を返す** — 時間切れはミス扱いにしない。
  /// ミスは「誤ったボタンを押した」ときだけで、HandlePush が返す (§5.1)。
  WhackResult Tick(const StageConfig& stage, LedController* leds) {
    if (!IsRunning()) {
      return WHACK_NONE;
    }
    const WhackSpec& spec = stage.precondition.whack;

    timer_ms_ -= kTickMs;
    if (0 < timer_ms_) {
      return WHACK_NONE;
    }

    if (in_gap_ || current_mole_ == COLOR_NONE) {
      // 次のモグラを出現させる
      in_gap_ = false;
      current_mole_ = PickNextMole(stage);
      timer_ms_ = spec.mole_on_ms;
      leds->ClearOverride();
      leds->SetOverride(current_mole_, true);
      return WHACK_NONE;
    }

    // 点灯時間内に押せなかった。進捗を進めずに同じ枠を出し直すだけで、
    // **ミスとしては扱わない** (§5.1)。
    //
    // ここでミスを返すと**無操作でもブザーとペナルティが鳴り続ける**。
    // 押下が無ければ必ずこの枠に落ちるため、放置しているだけで
    // (mole_on_ms + gap_ms) ごとに減点され、勝手に時間が溶けて爆発する。
    // ペナルティで伝えたいのは「**間違えた**」ことなので、
    // 対象は誤ったボタンを押した場合だけでよい。
    current_mole_ = COLOR_NONE;
    in_gap_ = true;
    timer_ms_ = spec.gap_ms;
    leds->ClearOverride();
    return WHACK_NONE;
  }

  /// ボタン押下を処理する。
  ///
  /// **ミスも呼び出し側へ返す** — ブザー・ペナルティで
  /// 「失敗した」ことを伝えないと、叩き直しになるだけで気づけない (§5)。
  WhackResult HandlePush(ColorId color, const StageConfig& stage, LedController* leds) {
    if (!IsRunning()) {
      return WHACK_NONE;
    }
    const WhackSpec& spec = stage.precondition.whack;

    if (color != current_mole_) {
      // 点灯中に別のkPushを押したミス。同じ枠を出し直す
      current_mole_ = COLOR_NONE;
      in_gap_ = true;
      timer_ms_ = spec.gap_ms;
      leds->ClearOverride();
      return WHACK_MISSED;
    }

    // ヒット: 即消灯して gap 後に次のモグラを出す
    ++hits_;
    current_mole_ = COLOR_NONE;
    in_gap_ = true;
    timer_ms_ = spec.gap_ms;
    leds->SetOverride(color, false);

    if (hits_ < spec.count) {
      return WHACK_HIT;
    }

    // 完了。kLED の専有を解除してヒント表示へ切り替える (§5.1)
    completed_ = true;
    active_ = false;
    leds->ClearOverride();
    return WHACK_COMPLETED;
  }

  /// 現在出現中のモグラの色 (出ていなければ COLOR_NONE)
  ColorId CurrentMole() const { return current_mole_; }

 private:
  /// 次のモグラを抽選する。出題順はデバイス側で決める (JSONは匹数・速度のみ)
  ColorId PickNextMole(const StageConfig& stage) const {
    const WhackSpec& spec = stage.precondition.whack;

    // last_mole_matches_cut: 最後の1匹だけ cut と同色に固定する
    // (「最後に押した色の線を切れ」の謎用)
    if (spec.last_mole_matches_cut && hits_ == spec.count - 1) {
      return stage.cut;
    }

    // 直前と同じLEDは避ける (同じ色が連続すると押し間違いが増えるため)
    for (int attempt = 0; attempt < 16; ++attempt) {
      const ColorId candidate = static_cast<ColorId>(esp_random() % kColorNum);
      if (candidate != current_mole_) {
        return candidate;
      }
    }
    return static_cast<ColorId>(esp_random() % kColorNum);
  }

 private:
  bool active_ = false;
  bool completed_ = false;
  int32_t hits_ = 0;
  ColorId current_mole_ = COLOR_NONE;
  int32_t timer_ms_ = 0;
  /// モグラ出現までの待ち時間中か
  bool in_gap_ = false;
};

}  // namespace CoreSystem

#endif  // WHACK_GAME_H_
// EOF
