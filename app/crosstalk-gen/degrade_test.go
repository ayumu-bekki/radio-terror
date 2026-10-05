package main

import (
	"math"
	"testing"
)

// tone は 300Hz の正弦波を ms ミリ秒ぶん作る。silence は無音。
func tone(ms int) []int16 { return sine(300, ms) }

// sine は freq Hz の正弦波を ms ミリ秒ぶん作る。
func sine(freq float64, ms int) []int16 {
	n := sampleRate * ms / 1000
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
	}
	return out
}

func silence(ms int) []int16 { return make([]int16, sampleRate*ms/1000) }

func concat(parts ...[]int16) []int16 {
	var out []int16
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func mix(a, b []int16) []int16 {
	out := make([]int16, len(a))
	for i := range out {
		out[i] = a[i]/2 + b[i]/2
	}
	return out
}

func rmsOf(pcm []int16) float64 {
	var sum float64
	for _, v := range pcm {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(pcm)))
}

func ms(x int) int { return sampleRate * x / 1000 }

// 「間」を数え、先頭と末尾の無音は数えないこと。
func TestSpeechPausesIgnoreEdges(t *testing.T) {
	pcm := concat(silence(300), tone(500), silence(200), tone(500), silence(300), tone(500), silence(400))
	ends := speechPauses(pcm)
	if len(ends) != 2 {
		t.Fatalf("間の数 = %d, want 2 (先頭・末尾の無音は数えない)", len(ends))
	}
	// 1番目の間の終わりは 300+500+200 = 1000ms
	if got := ends[0] * 1000 / sampleRate; got < 990 || got > 1010 {
		t.Errorf("1番目の間の終わり = %dms, want 約1000ms", got)
	}
	// 短い無音 (60ms) は間と数えない
	short := concat(tone(500), silence(60), tone(500))
	if got := len(speechPauses(short)); got != 0 {
		t.Errorf("60ms の無音を間と数えた (%d)", got)
	}
}

// proj は x に含まれる ref の成分の大きさ (ref の実効値に対する割合)。
// 声が残っているかを見る。ノイズは ref と無相関なので 0 に近い。
func proj(x, ref []int16) float64 {
	var dot, rr float64
	for i := range x {
		dot += float64(x[i]) * float64(ref[i])
		rr += float64(ref[i]) * float64(ref[i])
	}
	return math.Abs(dot) / rr
}

// 指定した間の直後の区間だけ声が消え、ほかは残り、長さが変わらないこと。
func TestApplyDegradeDropsOnlyOneSpot(t *testing.T) {
	pcm := concat(tone(600), silence(200), tone(1500), silence(200), tone(600))
	d := Degrade{SnrDB: 20, DropoutMS: 400, DropoutAfterPause: 1}

	out, info, err := ApplyDegrade(pcm, d, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(pcm) {
		t.Fatalf("長さが変わった: %d → %d", len(pcm), len(out))
	}
	if info == "" {
		t.Error("欠落位置の情報が空")
	}

	// 欠落: 800〜1200ms (1番目の間の直後から400ms)。声の成分が消える
	if p := proj(out[ms(850):ms(1150)], pcm[ms(850):ms(1150)]); p > 0.15 {
		t.Errorf("欠落区間に声が残っている: 成分 %.2f", p)
	}
	// 欠落の外は声が残る (元と同じ大きさ)
	for _, span := range [][2]int{{100, 500}, {1300, 1900}, {2500, 2800}} {
		if p := proj(out[ms(span[0]):ms(span[1])], pcm[ms(span[0]):ms(span[1])]); p < 0.9 {
			t.Errorf("%d〜%dms の声が欠けた: 成分 %.2f", span[0], span[1], p)
		}
	}
	// 欠落は1か所だけ: 2番目の間の前後も残る
	if p := proj(out[ms(2000):ms(2300)], pcm[ms(2000):ms(2300)]); p < 0.9 {
		t.Error("欠落が2か所以上ある")
	}
}

// 同じ名前なら同じ音、別の名前なら別のノイズになること。入力は書き換えない。
func TestApplyDegradeIsDeterministic(t *testing.T) {
	pcm := concat(tone(600), silence(200), tone(900))
	orig := append([]int16(nil), pcm...)
	d := Degrade{SnrDB: 10, DropoutMS: 300, DropoutAfterPause: 1}

	a, _, _ := ApplyDegrade(pcm, d, "x")
	b, _, _ := ApplyDegrade(pcm, d, "x")
	c, _, _ := ApplyDegrade(pcm, d, "y")
	same, diff := true, false
	for i := range a {
		if a[i] != b[i] {
			same = false
		}
		if a[i] != c[i] {
			diff = true
		}
	}
	if !same {
		t.Error("同じ名前なのに結果が違う (作り直しで音が変わる)")
	}
	if !diff {
		t.Error("名前が違うのにノイズが同じ")
	}
	for i := range pcm {
		if pcm[i] != orig[i] {
			t.Fatal("入力の PCM を書き換えた")
		}
	}
}

// 位置が見つからない・指定が不正なときは黙って素通しせず、エラーにすること。
func TestApplyDegradeRejectsBadSpec(t *testing.T) {
	pcm := concat(tone(600), silence(200), tone(600))
	for name, d := range map[string]Degrade{
		"間が足りない":         {DropoutMS: 300, DropoutAfterPause: 3},
		"位置が無い":          {DropoutMS: 300},
		"SN比が小さすぎる":      {SnrDB: 1},
		"SN比が大きすぎる":      {SnrDB: 60},
		"途切れが長すぎる":       {DropoutMS: 5000, DropoutAt: 0.5},
		"開始位置が範囲外 (1.0)": {DropoutMS: 300, DropoutAt: 1},
		"うねりが範囲外":        {NoiseSwell: 5},
	} {
		if _, _, err := ApplyDegrade(pcm, d, "t"); err == nil {
			t.Errorf("%s: エラーにならない", name)
		}
	}
}

// ノイズを重ねても声の大きさは変わらず、指定した SN 比でノイズが入ること。
func TestApplyDegradeSNR(t *testing.T) {
	in := concat(tone(1000), silence(200), tone(1000))
	for _, snr := range []float64{6, 12, 20} {
		out, _, err := ApplyDegrade(in, Degrade{SnrDB: snr}, "t")
		if err != nil {
			t.Fatal(err)
		}
		// ノイズ = 出力 - 入力。声の区間の実効値の比が指定の SN 比になる
		noise := make([]float64, len(in))
		for i := range noise {
			noise[i] = float64(out[i]) - float64(in[i])
		}
		got := 20 * math.Log10(rmsOf(in[:ms(1000)])/rmsF(noise[:ms(1000)]))
		if math.Abs(got-snr) > 1.5 {
			t.Errorf("SN比 指定 %vdB に対し実測 %.1fdB", snr, got)
		}
		// 声の成分は元のまま (大きさが変わらない)
		if p := proj(out[:ms(1000)], in[:ms(1000)]); p < 0.95 || p > 1.05 {
			t.Errorf("SN比 %vdB: 声の大きさが変わった (成分 %.2f)", snr, p)
		}
	}
}

// ノイズだけを取り出す (同じ指定でノイズ 0 の出力との差)。声・欠落は同じなので引き算で消える。
func residual(t *testing.T, in []int16, d Degrade) []float64 {
	t.Helper()
	with, _, err := ApplyDegrade(in, d, "t")
	if err != nil {
		t.Fatal(err)
	}
	d0 := d
	d0.SnrDB, d0.NoiseSwell = 0, 0
	without, _, _ := ApplyDegrade(in, d0, "t")
	out := make([]float64, len(in))
	for i := range out {
		out[i] = float64(with[i]) - float64(without[i])
	}
	return out
}

func rmsF(x []float64) float64 {
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(x)))
}

// ノイズのうねり: 掛けるとノイズの大きさが時間で変わり、掛けなければ一定。
// 平均の SN 比は変わらない。
func TestApplyDegradeNoiseSwell(t *testing.T) {
	in := sine(500, 6000)
	spread := func(x []float64) float64 {
		win := sampleRate / 4
		lo, hi := math.MaxFloat64, 0.0
		for i := 0; i+win <= len(x); i += win {
			r := rmsF(x[i : i+win])
			lo, hi = math.Min(lo, r), math.Max(hi, r)
		}
		return hi / lo
	}
	flat := residual(t, in, Degrade{SnrDB: 12})
	swell := residual(t, in, Degrade{SnrDB: 12, NoiseSwell: 2})
	if s := spread(flat); s > 1.15 {
		t.Errorf("うねり無しなのにノイズが変動した: 最大/最小 %.2f", s)
	}
	if s := spread(swell); s < 1.4 {
		t.Errorf("うねりを掛けたのにノイズがほぼ一定: 最大/最小 %.2f", s)
	}
	// 平均のノイズの大きさはうねりの有無で大きく変わらない (3dB 以内)
	if r := rmsF(swell) / rmsF(flat); r < 0.7 || r > 1.4 {
		t.Errorf("うねりでノイズの平均が変わった: 比 %.2f", r)
	}
}

// FM のヒスは高域寄り: 差分の rms / rms が白色ノイズ (約1.4) に近いか上であること。
// 低域寄りのノイズは 1 を大きく下回る。
func TestApplyDegradeHissIsBright(t *testing.T) {
	r := residual(t, sine(500, 3000), Degrade{SnrDB: 10})
	var dsum float64
	for i := 1; i < len(r); i++ {
		d := r[i] - r[i-1]
		dsum += d * d
	}
	ratio := math.Sqrt(dsum/float64(len(r)-1)) / rmsF(r)
	if ratio < 1.2 {
		t.Errorf("ノイズが低域寄り: 差分比 %.2f (高域寄りなら 1.2 以上)", ratio)
	}
}

// 帯域を絞ると、低い音は削れ、声の帯域 (1kHz) が残り、**全体の音量は元のまま**なこと。
func TestApplyDegradeBandLimitKeepsLevel(t *testing.T) {
	low, mid := sine(80, 1000), sine(1000, 1000)
	in := mix(low, mid)
	out, _, err := ApplyDegrade(in, Degrade{BandLimit: true}, "t")
	if err != nil {
		t.Fatal(err)
	}
	half := len(in) / 2 // 立ち上がりの過渡を避ける
	pLow := proj(out[half:], low[half:])
	pMid := proj(out[half:], mid[half:])
	if pLow > pMid*0.3 {
		t.Errorf("低域が削れていない: 80Hz成分 %.2f / 1kHz成分 %.2f", pLow, pMid)
	}
	// 絞ったあとも元と同じ音量に戻す (声が小さくなって聞きにくくならない)
	if r := rmsOf(out[half:]) / rmsOf(in[half:]); r < 0.9 || r > 1.1 {
		t.Errorf("帯域を絞って音量が変わった: 元の %.2f 倍", r)
	}
}
