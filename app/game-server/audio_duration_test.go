package main

import (
	"math"
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
