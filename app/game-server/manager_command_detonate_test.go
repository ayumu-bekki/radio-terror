package main

import "testing"

// 強制破裂の判定。**風船が実際に割れる**ため、成立条件を厳密に固定する。
//
// 必要なのは「CoreID + 爆破のキーワード + 秘密ワード」の3要素。
// 1つでも欠けたら成立してはいけない。
func TestManagerCommandDetonate(t *testing.T) {
	h := &ManagerCommandHandler{secretWord: "でんぱ"}

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
	h := &ManagerCommandHandler{secretWord: ""}

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
	h := &ManagerCommandHandler{secretWord: "でんぱ"}

	if got := h.Parse("3701 リセット でんぱ").Kind; got != managerCommandReset {
		t.Errorf("リセットが %q になった", got)
	}
	if got := h.Parse("3701 強制爆破 でんぱ").Kind; got != managerCommandDetonate {
		t.Errorf("強制破裂が %q になった", got)
	}
}
