package main

import "testing"

// countBodyRunes は名乗り (コールサイン) を字数から外す。
// 名乗りは緊迫時を除いて毎回入れる方針 (ADR N-21) なので、
// 含めて数えると名乗ったぶんが超過に見え、本文が削られる。
func TestCountBodyRunes(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		character string
		want      int
	}{
		{
			name:      "名乗りを句点で落とす",
			text:      "こちらフクロウ。赤を切れ。どうぞ",
			character: "フクロウ",
			want:      countRunes("赤を切れ。どうぞ"),
		},
		{
			name:      "読点で切れる名乗りも落とす",
			text:      "こっちモズや、赤切ってや。どうぞ",
			character: "モズ",
			want:      countRunes("赤切ってや。どうぞ"),
		},
		{
			name:      "表情タグは名乗りの前にあっても落とす",
			text:      "[calm] こちらフクロウ。赤を切れ。どうぞ",
			character: "フクロウ",
			want:      countRunes("赤を切れ。どうぞ"),
		},
		{
			name:      "名乗りが無ければ全文を数える",
			text:      "赤を切れ。どうぞ",
			character: "フクロウ",
			want:      countRunes("赤を切れ。どうぞ"),
		},
		{
			// **先頭の一句しか見ない。** 文の途中で名前を口にするのは
			// 名乗りではなく本文なので、落としてはいけない。
			name:      "文中の名前は落とさない",
			text:      "落ち着け。フクロウがついている。どうぞ",
			character: "フクロウ",
			want:      countRunes("落ち着け。フクロウがついている。どうぞ"),
		},
		{
			name:      "全角の感嘆符でも切れる",
			text:      "こちらヒバリです!赤を切ってください!どうぞ",
			character: "ヒバリ",
			want:      countRunes("赤を切ってください!どうぞ"),
		},
		{
			name:      "キャラクター名が空なら全文を数える",
			text:      "こちらフクロウ。赤を切れ。どうぞ",
			character: "",
			want:      countRunes("こちらフクロウ。赤を切れ。どうぞ"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countBodyRunes(tc.text, tc.character); got != tc.want {
				t.Errorf("countBodyRunes(%q, %q) = %d, want %d",
					tc.text, tc.character, got, tc.want)
			}
		})
	}
}
