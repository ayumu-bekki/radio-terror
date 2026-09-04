package main

import "strings"

// countRunes は文字列の文字数 (rune 数) を返す。
//
// 発話長の判定やログに使う。日本語では len() のバイト数が
// 文字数と一致しないため、rune で数える。
func countRunes(s string) int {
	return len([]rune(s))
}

// countBodyRunes は**名乗り(コールサイン)を除いた**文字数を返す。
//
// 発話は「2文以内・60文字以内」を目安にしているが、名乗りはその数えに
// 含めない (navigator/prompt.toml の出力ルール)。含めてしまうと、名乗った
// ぶんだけ**本文の情報が削られる** — 危険の警告や押す順番が先に落ちるのは
// 決定24 以来くり返し起きている失敗で、名乗りを毎回入れる方針
// (ADR N-21) と真っ向からぶつかる。
//
// 名乗りは「こちらフクロウ。」「こっちモズや、」のように**先頭の一句**に
// 現れ、キャラクター名を含む。表情タグを除いた先頭から最初の区切り
// (。、!?,) までを見て、そこにキャラクター名があれば名乗りとして落とす。
//
// **先頭の一句しか見ない。** 文中に出てくる名前は本文の一部なので数える。
func countBodyRunes(text, characterName string) int {
	body := stripTTSTags(text)
	if characterName == "" {
		return countRunes(body)
	}

	runes := []rune(body)
	for i, r := range runes {
		if !isSentenceBreak(r) {
			continue
		}
		head := string(runes[:i])
		if !strings.Contains(head, characterName) {
			// 最初の区切りまでに名前が出てこなければ名乗りではない。
			// 以降を探しても、それは本文の途中で名前を口にしただけ。
			break
		}
		// 区切り文字とそれに続く空白も落とす
		rest := strings.TrimLeft(string(runes[i+1:]), " 　")
		return countRunes(rest)
	}
	return countRunes(body)
}

// isSentenceBreak は名乗りの切れ目になりうる区切り文字か。
//
// 名乗りは「こちらフクロウ。」「こっちモズや、」「こちらヒバリです!」の
// ように、句点・読点・感嘆符のいずれかで本文と切れる。半角と全角の
// どちらも来るので両方見る。
func isSentenceBreak(r rune) bool {
	return r == '\u3002' || r == '\u3001' || // 。 、
		r == '\uff01' || r == '\uff1f' || // ！ ？
		r == '!' || r == '?' || r == ',' || r == '.'
}
