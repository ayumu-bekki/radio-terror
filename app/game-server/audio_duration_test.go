package main

import (
	"math"
	"os"
	"testing"
	"time"
)

// TestOggOpusDuration は、エンコードした音声の長さを正しく読み取れることを
// 確かめる。無線が塞がる時間の見積もりに使うため、実尺と大きくずれると
// 混線が発話に重なる (docs/operation_flow.md §5.1)。
func TestOggOpusDuration(t *testing.T) {
	cases := []struct {
		name    string
		samples int
	}{
		{"0.5秒", sampleRate / 2},
		{"1秒", sampleRate},
		{"3.5秒", sampleRate * 7 / 2},
		{"10秒", sampleRate * 10},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pcm := make([]int16, c.samples)
			for i := range pcm {
				pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
			}

			ogg, err := encodePCMToOggOpus(pcm)
			if err != nil {
				t.Fatalf("encodePCMToOggOpus: %v", err)
			}

			want := time.Duration(c.samples) * time.Second / sampleRate
			got := oggOpusDuration(ogg)

			// フレーム境界 (20ms) の丸めがあるので許容差を持たせる
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 50*time.Millisecond {
				t.Errorf("duration = %v, want %v (差 %v)", got, want, diff)
			}
		})
	}
}

// TestOggOpusDurationInvalid は壊れた入力で落ちないことを確かめる。
// 混線アセットが壊れていても再生見積もりで panic してはいけない。
func TestOggOpusDurationInvalid(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"空", nil},
		{"短すぎる", []byte{0x01, 0x02}},
		{"Oggではない", []byte("this is not an ogg file at all, just text")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := oggOpusDuration(c.data); got != 0 {
				t.Errorf("duration = %v, want 0", got)
			}
		})
	}
}

// TestSFXMergedIntoSingleOgg は効果音と発話が**1つの Ogg にまとまる**ことを
// 確かめる (docs/operation_flow.md §6)。
//
// かつては効果音を別パケットで先に送っていたため、効果音が鳴り終わってから
// TTS 生成を待つ数秒の**無音**が無線に乗っていた。連結して1つの音声にすれば
// 「効果音 → メッセージ」が途切れずに流れる。
//
// ここでは TTS を呼ばずに、連結〜エンコードの経路だけを検証する。
func TestSFXMergedIntoSingleOgg(t *testing.T) {
	sfx, err := os.ReadFile("assets/sfx/failure.ogg")
	if err != nil {
		t.Skipf("効果音アセットが無い: %v", err)
	}

	sfxPCM, err := decodeOggOpusToPCM(sfx)
	if err != nil {
		t.Fatalf("decodeOggOpusToPCM: %v", err)
	}
	if len(sfxPCM) == 0 {
		t.Fatal("効果音の PCM が空")
	}

	// 発話に見立てた 2 秒の音
	speech := makeTone(sampleRate*2, 440)

	merged := make([]int16, 0, len(sfxPCM)+len(speech))
	merged = append(merged, sfxPCM...)
	merged = append(merged, speech...)

	ogg, err := encodePCMToOggOpus(merged)
	if err != nil {
		t.Fatalf("encodePCMToOggOpus: %v", err)
	}

	// 尺が「効果音 + 発話」になっていること (どちらかが欠けていない)
	want := time.Duration(len(merged)) * time.Second / sampleRate
	got := oggOpusDuration(ogg)
	if diff := got - want; diff > 100*time.Millisecond || diff < -100*time.Millisecond {
		t.Errorf("連結後の尺 = %v, want %v (差 %v)", got, want, diff)
	}

	// 効果音だけ・発話だけより長いこと = 両方入っている
	sfxOnly := time.Duration(len(sfxPCM)) * time.Second / sampleRate
	if got <= sfxOnly {
		t.Errorf("連結後 (%v) が効果音だけ (%v) より長くない — 発話が落ちている", got, sfxOnly)
	}
}
