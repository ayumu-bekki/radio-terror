package main

import (
	"math"
	"testing"
)

// makeTone はテスト用の PCM (正弦波) を作る。
func makeTone(samples int, freq float64) []int16 {
	pcm := make([]int16, samples)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
	}
	return pcm
}

// TestConcatenatedPCMEncodesAsOneStream は、チャンクごとの PCM を連結してから
// エンコードすると、全体が 1 つの Ogg Opus になることを確かめる。
//
// 分割送信をやめて一括送出にした (docs/navigator_design.md §5 決定12) ため、
// 連結後の長さが各チャンクの合計と一致していることが前提になる。
func TestConcatenatedPCMEncodesAsOneStream(t *testing.T) {
	chunks := [][]int16{
		makeTone(sampleRate/2, 440), // 0.5s
		makeTone(sampleRate/4, 880), // 0.25s
		makeTone(sampleRate, 220),   // 1.0s
	}

	var merged []int16
	for _, c := range chunks {
		merged = append(merged, c...)
	}

	wantSamples := sampleRate/2 + sampleRate/4 + sampleRate
	if len(merged) != wantSamples {
		t.Fatalf("連結後のサンプル数 = %d, want %d", len(merged), wantSamples)
	}

	ogg, err := encodePCMToOggOpus(merged)
	if err != nil {
		t.Fatalf("encodePCMToOggOpus: %v", err)
	}
	if len(ogg) == 0 {
		t.Fatal("エンコード結果が空")
	}

	// Ogg のページ先頭は "OggS"。単一ストリームなので先頭は1回だけ現れる形になる
	if string(ogg[:4]) != "OggS" {
		t.Errorf("Ogg マジックがない: %q", ogg[:4])
	}

	// 分割してそれぞれエンコードした場合との比較:
	// 個別エンコードは各々にヘッダ (OpusHead/OpusTags) が付くため、
	// 連結してから1回エンコードするほうが小さくなる。
	var separateTotal int
	for _, c := range chunks {
		b, err := encodePCMToOggOpus(c)
		if err != nil {
			t.Fatalf("encodePCMToOggOpus(chunk): %v", err)
		}
		separateTotal += len(b)
	}
	if len(ogg) >= separateTotal {
		t.Errorf("連結エンコード %d bytes >= 個別エンコード合計 %d bytes"+
			" (ヘッダが1組で済むぶん小さくなるはず)", len(ogg), separateTotal)
	}
}

// TestEncodeEmptyPCM は音声が1つも生成できなかった場合に
// エンコードが落ちないことを確かめる (全チャンク失敗時の経路)。
func TestEncodeEmptyPCM(t *testing.T) {
	ogg, err := encodePCMToOggOpus(nil)
	if err != nil {
		t.Fatalf("空 PCM でエラー: %v", err)
	}
	// ヘッダのみのストリームになる (再生しても無音)
	if len(ogg) == 0 {
		t.Error("空 PCM でも Ogg ヘッダは出力されるべき")
	}
}
