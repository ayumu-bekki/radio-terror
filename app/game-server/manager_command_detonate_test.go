package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// 強制破裂の判定。**風船が実際に割れる**ため、成立条件を厳密に固定する。
//
// 必要なのは「CoreID + 爆破のキーワード + 秘密ワード」の3要素。
// 1つでも欠けたら成立してはいけない。
func TestManagerCommandDetonate(t *testing.T) {
	h := NewManagerCommandHandler(nil, "でんぱ")

	cases := []struct {
		name     string
		msg      string
		kind     string
		deviceID string
	}{
		// --- 成立する ---
		{"定型文", "こちらマネージャー。CoreID 3701 強制爆破 でんぱ", "detonate", "3701"},
		{"名乗りなし", "3701 強制爆破 でんぱ", "detonate", "3701"},
		{"破裂の表記", "3701 強制破裂 でんぱ", "detonate", "3701"},
		{"かな書き", "3701 きょうせいばくは でんぱ", "detonate", "3701"},
		{"カタカナの秘密ワード", "3701 強制爆破 デンパ", "detonate", "3701"},
		{"語順が違っても成立", "でんぱ 強制爆破 3701", "detonate", "3701"},

		// --- 成立してはいけない (どれか1要素が欠ける) ---
		{"秘密ワードなし → 不成立", "3701 強制爆破", "", ""},
		{"CoreIDなし → 不成立", "強制爆破 でんぱ", "", ""},
		{"キーワードなし → 不成立", "3701 でんぱ", "", ""},
		{"秘密ワードが違う → 不成立", "3701 強制爆破 でんき", "", ""},

		// --- プレイヤーの発話で誤爆しない ---
		{"爆破の話をしただけ", "この装置、爆破されるんですか。どうぞ", "", ""},
		{"CoreIDと爆破を言っても秘密ワードがない",
			"3701番が爆破されそうです。どうぞ", "", ""},
	}

	for _, c := range cases {
		got := h.Parse(c.msg)
		if got.Kind != c.kind {
			t.Errorf("%s: Kind = %q, want %q (msg=%q)", c.name, got.Kind, c.kind, c.msg)
			continue
		}
		if c.deviceID != "" && got.DeviceID != c.deviceID {
			t.Errorf("%s: DeviceID = %q, want %q", c.name, got.DeviceID, c.deviceID)
		}
	}
}

// 秘密ワードが未設定なら、強制破裂もリセットも成立しない。
// 設定漏れで誰でも爆破できる状態にならないことを保証する。
func TestManagerCommandDetonateWithoutSecret(t *testing.T) {
	h := NewManagerCommandHandler(nil, "")

	for _, msg := range []string{
		"3701 強制爆破 でんぱ",
		"3701 強制爆破",
		"3701 リセット でんぱ",
	} {
		if got := h.Parse(msg).Kind; got != "" {
			t.Errorf("秘密ワード未設定で成立した: Kind = %q (msg=%q)", got, msg)
		}
	}
}

// 強制破裂とリセットが取り違えられないこと。
// 同じ秘密ワードを使うため、キーワードだけが両者を分ける。
func TestManagerCommandDetonateVsReset(t *testing.T) {
	h := NewManagerCommandHandler(nil, "でんぱ")

	if got := h.Parse("3701 リセット でんぱ").Kind; got != managerCommandReset {
		t.Errorf("リセットが %q になった", got)
	}
	if got := h.Parse("3701 強制爆破 でんぱ").Kind; got != managerCommandDetonate {
		t.Errorf("強制破裂が %q になった", got)
	}
}

// TestSecretWordAcceptsAlternateSpellings は秘密ワードの**表記ゆれ**を
// カンマ区切りで登録でき、どれでもキルスイッチが成立することを確かめる。
//
// 音声認識は同じ音を別の表記で返す。「でんぱ」と設定しても STT は「電波」と
// 書き起こすことがあり、かな正規化では**漢字をかなへ戻せない**ため一致しない。
// 実運用でこれが原因でキルスイッチが効かず、爆発後のリセット申告に
// ナビゲーターが応答し続けた (docs/operation_flow.md §7)。
func TestSecretWordAcceptsAlternateSpellings(t *testing.T) {
	h := NewManagerCommandHandler(nil, "でんぱ,電波")

	cases := []struct {
		name string
		msg  string
		kind string
	}{
		// 実プレイのログに出た文字起こし (漢字表記)
		{"漢字で書き起こされた", "こちらマネージャー 5IIT3701 リセット 電波", "reset"},
		// 設定どおりのかな表記
		{"かな表記", "こちらマネージャー 3701 リセット でんぱ", "reset"},
		// カタカナはかな正規化で吸収される
		{"カタカナ表記", "こちらマネージャー 3701 リセット デンパ", "reset"},
		// 強制破裂も同じ秘密ワードを使う
		{"強制破裂でも漢字が通る", "3701 強制爆破 電波", "detonate"},

		// 秘密ワードが無ければ成立しない (プレイヤーが真似ても通らない)
		{"秘密ワードなし → 不成立", "こちらマネージャー 3701 リセット", ""},
		{"別の語 → 不成立", "こちらマネージャー 3701 リセット でんき", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := h.Parse(c.msg).Kind; got != c.kind {
				t.Errorf("Parse(%q).Kind = %q, want %q", c.msg, got, c.kind)
			}
		})
	}
}

// TestSecretWordUnsetRejectsEverything は秘密ワード未設定時に
// 秘密ワード必須のコマンドが**一切通らない**ことを確かめる。
func TestSecretWordUnsetRejectsEverything(t *testing.T) {
	h := NewManagerCommandHandler(nil, "")

	for _, msg := range []string{
		"3701 リセット でんぱ",
		"3701 強制爆破 でんぱ",
		"3701 リセット",
	} {
		if got := h.Parse(msg).Kind; got != "" {
			t.Errorf("秘密ワード未設定で %q が成立した (kind=%q)", msg, got)
		}
	}
}

// TestResetTargetFallsBackToBoundDevice は、CoreID を**聞き間違えても
// その無線が担当しているデバイスがリセットされる**ことを確かめる。
//
// 音声認識は数字を取り違える。実運用で CoreID 3701 が「3710」と書き起こされ、
// 存在しないデバイスへリセットを送って**何も起きなかった**
// (無線側にもフィードバックが無く、マネージャーは失敗に気づけない)。
//
// 無線 ⇔ デバイスの対応は開始申告で確立済みなので、
// 聞き取れた数字よりバインドの方が確かな情報になる。
func TestResetTargetFallsBackToBoundDevice(t *testing.T) {
	store := NewMemoryStore()
	game := NewGameCoordinator(NewDeviceRegistry(), NewBridgeRegistry(), nil, store,
		rand.New(rand.NewSource(1)))

	session := &GameSession{
		SessionID: "s-1", DeviceID: "3701", BridgeID: "BR01",
		State: deviceStatePlaying, StartedAt: time.Now(),
	}
	game.binder.Bind("BR01", "3701", session)

	cases := []struct {
		name   string
		bridge string
		spoken string
		want   string
	}{
		{"桁の入れ替わり (実例)", "BR01", "3710", "3701"},
		{"正しく聞き取れた場合はそのまま", "BR01", "3701", "3701"},
		{"別の数字でもバインド優先", "BR01", "9999", "3701"},
		{"未バインドの無線は申告どおり", "BR99", "3710", "3710"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := game.ResolveResetTarget(c.bridge, c.spoken); got != c.want {
				t.Errorf("ResolveResetTarget(%q, %q) = %q, want %q",
					c.bridge, c.spoken, got, c.want)
			}
		})
	}
}

// TestRejectReasonExplainsFailure は、マネージャーの操作が成立しなかったとき
// **理由がログに出せる**ことを確かめる。
//
// 成立しない発話はそのままナビゲーター/カラスへ流れるため、ログに
// 「なぜ通らなかったか」が無いと切り分けられない。実運用で秘密ワードの
// 表記ゆれによりキルスイッチが効かず、原因特定に時間を要した。
func TestRejectReasonExplainsFailure(t *testing.T) {
	h := NewManagerCommandHandler(nil, "でんぱ")

	cases := []struct {
		name string
		msg  string
		want string // 部分一致
	}{
		{"秘密ワードの表記ゆれ", "こちらマネージャー 3701 リセット 電波", "秘密ワードが一致しない"},
		{"CoreIDが取れない", "こちらマネージャー リセット でんぱ", "CoreID"},
		{"操作と無関係な発話は黙る", "ダイヤルを3に合わせました", ""},
		{"報告も黙る", "赤が光っています", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.rejectReason(c.msg)
			if c.want == "" {
				if got != "" {
					t.Errorf("無関係な発話で理由が出た: %q → %q", c.msg, got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("rejectReason(%q) = %q, want contains %q", c.msg, got, c.want)
			}
		})
	}

	// 未設定はその旨が分かること
	h2 := NewManagerCommandHandler(nil, "")
	if got := h2.rejectReason("3701 リセット でんぱ"); !strings.Contains(got, "未設定") {
		t.Errorf("秘密ワード未設定の理由が出ない: %q", got)
	}
}
