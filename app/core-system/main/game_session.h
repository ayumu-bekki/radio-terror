#ifndef GAME_SESSION_H_
#define GAME_SESSION_H_
// Core System
// (C)2026 bekki.jp
// セッションJSON (docs/game_session_design.md §6) のパース・検証と
// kLED表示パターンのプリコンパイル

// Include ----------------------
#include <cstdint>
#include <string>
#include <vector>

namespace CoreSystem {

/// 配線・LED・プッシュスイッチ共通の色系統 (A=赤, B=黄, C=緑, D=青, E=白)
enum ColorId : int8_t {
  COLOR_NONE = -1,
  COLOR_A = 0,
  COLOR_B = 1,
  COLOR_C = 2,
  COLOR_D = 3,
  COLOR_E = 4,
};
constexpr int kColorNum = 5;

/// ロータリースイッチの位置 (パネル表記・JSONとも 0-5)
constexpr int kRotaryPositionNum = 6;

/// GameTaskのtick周期。LEDパターンはこの粒度で進行する
constexpr int kTickMs = 100;

/// デバイス側状態 (§4)
///
/// 状態から表示を決める処理 (status_indicator.h) も参照するため、
/// GameTask ではなくここに置く。
enum GameState : uint8_t {
  STATE_SETUP,       ///< 準備中 (風船交換・配線復旧)
  STATE_READY,       ///< セッティング完了。開始を待つ
  STATE_PENDING,     ///< 開始申告が通り、ナビゲーターの応答を待っている (§4.2)
  STATE_PLAYING,     ///< ステージ消化中
  STATE_DETONATING,  ///< 失敗確定。detonate_delay_ms 後にソレノイド駆動
  STATE_EXPLODED,    ///< 破裂済み
  STATE_DEFUSED,     ///< 全ステージクリア
};

/// 状態名 (device_status 用。§7.2)
const char* GameStateName(GameState state);

/// HT16K33 4桁 (999.9秒) の表示上限
constexpr int32_t kCountdownMaxMs = 999900;

/// 誤操作・違反時の挙動
enum ActionType : uint8_t {
  ACTION_EXPLODE,
  ACTION_PENALTY,
  ACTION_RETRY,  ///< push_seq の on_wrong_press のみで使用 (列の先頭からやり直し)
};

/// 誤操作時の挙動と減算時間の組
struct ActionSpec {
  ActionType action = ACTION_EXPLODE;
  int32_t penalty_ms = 0;
};

/// プリコンパイル済みLEDパターンの1ステップ (点灯/消灯 + 継続tick数)
struct LedStep {
  bool on = false;
  int32_t ticks = 0;
};

/// 1つのkLEDの表示パターン。ステップ列をループ再生する
struct LedPattern {
  /// ステップが空の場合は常時消灯として扱う
  std::vector<LedStep> steps;

  /// 全ステップが消灯 (= 常に消えている) かどうか。leds_all_off の判定を軽くするため保持する
  bool always_off = true;
};

/// timer_digit の対象桁
enum TimerDigitKind : uint8_t {
  TIMER_DIGIT_ONES,  ///< 残り秒数の一の位
  TIMER_DIGIT_TENS,  ///< 残り秒数の十の位
};

/// timer_digit の比較相手
enum TimerDigitMatch : uint8_t {
  TIMER_MATCH_VALUE,   ///< 固定値と比較する
  TIMER_MATCH_ROTARY,  ///< 現在のロータリー位置と比較する
};

/// 事前条件: 色合わせ (§5.1)
///
/// **時間の指定を持たない。** 点灯した色のボタンを押すと次へ進む形式なので、
/// 点灯時間や出現間隔という概念が無い。
struct ColorMatchSpec {
  int32_t count = 0;
  /// 最後の1つを cut と同色に固定する (206 の「最後に押した色の線を切れ」用)
  bool last_matches_cut = false;
  /// ミス時のペナルティ (ms)。0 なら減算しない。
  ///
  /// **ミスに何の反応も無いと、失敗したことに気づけない**。押し直しになる
  /// だけでは損失が伝わらず、緊張感も出ない (§5)。ブザーと合わせて使う。
  int32_t penalty_ms = 0;
};

/// 事前条件: ボタン列入力の1要素
struct PushSeqEntry {
  ColorId push = COLOR_NONE;
  /// この入力時に要求されるロータリー位置。-1 は条件なし
  int8_t rotary = -1;
};

/// 事前条件: ボタン列入力 (§5)
struct PushSeqSpec {
  std::vector<PushSeqEntry> entries;
  /// ミス時の挙動 (既定 retry)
  ActionSpec on_wrong_press{ACTION_RETRY, 0};
};

/// 事前条件: 切断の瞬間のタイマー桁条件
struct TimerDigitSpec {
  TimerDigitKind digit = TIMER_DIGIT_ONES;
  TimerDigitMatch match = TIMER_MATCH_VALUE;
  /// match == TIMER_MATCH_VALUE のときの比較値 (0-9)
  int8_t value = 0;

  /// 比較前に桁へ加算する値。「タイマーの桁 + offset」を比較相手と突き合わせる。
  /// 「光っているランプの数とタイマーを足した位置にダイヤルを合わせる」のような
  /// 暗算を要する謎に使う (offset=0 なら従来どおり桁そのものの比較)。
  int8_t offset = 0;
};

/// ステージのクリア事前条件 (全てAND)
struct Precondition {
  bool has_rotary = false;
  int8_t rotary = 0;

  /// 切断の瞬間に押されている必要のあるスイッチ (複数指定可)
  bool push_required[kColorNum] = {false, false, false, false, false};
  bool has_push = false;

  bool has_color_match = false;
  ColorMatchSpec color_match;

  bool has_push_seq = false;
  PushSeqSpec push_seq;

  bool has_timer_digit = false;
  TimerDigitSpec timer_digit;

  /// 切断の瞬間に全kLEDが消灯していること
  bool leds_all_off = false;
};

/// ステージ属性: 停止してはいけないロータリー位置 (通過はセーフ)
struct ForbiddenRotary {
  bool enabled = false;
  bool positions[kRotaryPositionNum] = {false, false, false, false, false, false};
  ActionSpec on_violation{ACTION_EXPLODE, 0};
};

/// 1ステージの定義
struct StageConfig {
  LedPattern leds[kColorNum];
  Precondition precondition;
  ForbiddenRotary forbidden_rotary;
  ColorId cut = COLOR_NONE;
};

/// セッション定義一式 (session_start のペイロード)
struct SessionConfig {
  std::string session_id;
  int32_t countdown_ms = 0;
  int32_t detonate_delay_ms = 0;
  std::vector<StageConfig> stages;
};

/// パース結果。失敗時は session_rejected の reason に使う
struct ParseResult {
  bool ok = false;
  /// "parse_error" / "invalid_field" 等。§7.2 の reason にそのまま載せる
  std::string reason;
  /// ログ・サーバー通知用の詳細メッセージ
  std::string detail;
};

/// 'A'-'E' の文字を ColorId に変換する。不正な文字は COLOR_NONE
ColorId ColorFromChar(char c);

/// ColorId を 'A'-'E' の文字に変換する。COLOR_NONE は '?'
char ColorToChar(ColorId color);

/// session_start のJSON文字列を SessionConfig へパース・検証する (§6・§6.2)
/// 検証NGの場合は out を書き換えず ParseResult::ok = false を返す
ParseResult ParseSessionJson(const std::string& json_text, SessionConfig* out);

}  // namespace CoreSystem

#endif  // GAME_SESSION_H_
// EOF
