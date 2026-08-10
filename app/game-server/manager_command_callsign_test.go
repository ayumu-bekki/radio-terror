package main

import "testing"

// コールサイン抽出を廃止しても開始申告・リセットが判定できること。
// 判定は message 内の「4桁ID + 難易度 + 開始の意図」だけを見ており、
// 「マネージャー」という名乗りは元から条件に含まれていない。
func TestManagerCommandWithoutCallsign(t *testing.T) {
	h := &ManagerCommandHandler{secretWord: "でんぱ"}
	cases := []struct {
		name string
		msg  string
		kind string
	}{
		{"名乗りあり(従来の言い方)",
			"こちらマネージャー。CoreID 3701 難易度ノーマルで開始してください。どうぞ", "start"},
		{"名乗りなし", "CoreID 3701 難易度ノーマルで開始してください。どうぞ", "start"},
		{"名乗りも敬語もなし", "3701 ノーマル 開始", "start"},
		{"リセット(名乗りなし)", "3701 でんぱ リセット", "reset"},
		{"難易度なし → 不成立", "こちらマネージャー。3701 で開始してください", ""},
		{"開始の意図なし → 不成立", "3701 ノーマル", ""},
		{"4桁なし → 不成立", "ノーマルで開始してください", ""},
		{"プレイヤーの雑談は誤爆しない", "赤い線を切りました。どうぞ", ""},
	}
	for _, c := range cases {
		if got := h.Parse(c.msg).Kind; got != c.kind {
			t.Errorf("%s: Kind = %q, want %q (msg=%q)", c.name, got, c.kind, c.msg)
		}
	}
}
