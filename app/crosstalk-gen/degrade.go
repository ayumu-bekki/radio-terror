package main

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
)

// Degrade は生成した音声に**FM 無線の聞き取りにくさ (ノイズと一部の欠落)** を加える指定。
//
// 想定は**電波が弱い FM** (特定小電力無線は狭帯域 FM)。AM のように声の音量が波打つのではなく、
// 声はそのまま・**ヒスノイズ (ザーッ) が電波の弱い瞬間に膨らみ**、スケルチが閉じて
// 声が切れる (ザッ) — この2つで聞きにくさを作る (パチパチというクラックルは、聞いて不要と判断して外した)。
//
// 「ノイズが入って一部が途切れ、聞こえにくい」聞き直しを作るためのもの。
// TTS に崩れた台詞を読ませると途切れ方が毎回変わり、省略記号の多い台詞は
// 400 を返しやすい。**普通に生成してから PCM を加工する**ほうが、
// 位置も長さも指定どおりで、作り直しても同じ音になる。
type Degrade struct {
	// SnrDB は声とヒスノイズの大きさの比 (dB)。**小さいほどノイズが目立つ**。0 ならノイズなし。
	// 3〜40 の範囲で、目安は 20 = ほぼ気にならない / 12 = ノイズが分かるが声はくっきり /
	// 8 = ノイズが目立つが言葉は分かる / 4 = 聞き取りに集中が要る。
	// 声の大きさは元の音声のまま (帯域を絞っても元と同じ音量に戻す) で、ノイズだけで調整する。
	// FM のヒスは高域に寄る (低域寄りの雑音ではなく、シャーという明るい音)。
	SnrDB float64 `toml:"snr_db"`

	// NoiseSwell はノイズが膨らんだり引いたりする幅 (0〜3)。電波が揺れて、弱いときほどノイズが増える。
	// 0 なら一定。平均の SN 比は SnrDB のまま、弱い瞬間はそれより悪く (ノイズが増え)、強い瞬間は良くなる。
	// 1 で SN 比が前後 3dB ほど動く。声の音量は変えない。
	NoiseSwell float64 `toml:"noise_swell"`

	// BandLimit は声の帯域を絞る (300Hz〜2.8kHz)。トランシーバー越しのこもった音になり、
	// 子音が潰れて聞き取りにくくなる。
	BandLimit bool `toml:"band_limit"`

	// DropoutMS は声が途切れる長さ (ミリ秒)。0 なら途切れさせない。
	// 途切れの間は声を消して、代わりに強めのノイズ (ザッという音) を入れる。
	DropoutMS int `toml:"dropout_ms"`

	// DropoutAfterPause が 1 以上なら、N 番目の**間 (句点などの無音)** の直後から途切れさせる。
	// 台詞の区切りから始まるので、1つの句の頭を丸ごと失う形になる (単語の途中から切れない)。
	// TTS は読み上げ位置を返さないため、音の間から位置を決める。
	DropoutAfterPause int `toml:"dropout_after_pause"`

	// DropoutAt は DropoutAfterPause を使わないときの途切れの開始位置 (全体の何割か。0〜1)。
	DropoutAt float64 `toml:"dropout_at"`
}

const (
	// degradeFrameMS は無音の検出に使う窓。
	degradeFrameMS = 10
	// degradeMinPauseMS はこれ以上続く無音を「間」と数える。
	degradeMinPauseMS = 120
	// degradePauseRatio は窓の RMS が最大の何割未満なら無音とみなすか。
	degradePauseRatio = 0.03
	// degradeFadeMS は途切れの前後で声を絞る・戻す時間。ブツッという音を避ける。
	degradeFadeMS = 15
)

func (d Degrade) validate() error {
	if d.SnrDB != 0 && (d.SnrDB < 3 || d.SnrDB > 40) {
		return fmt.Errorf("degrade.snr_db は 0 (ノイズなし) か 3〜40 (got %v)", d.SnrDB)
	}
	if d.NoiseSwell < 0 || d.NoiseSwell > 3 {
		return fmt.Errorf("degrade.noise_swell は 0〜3 (got %v)", d.NoiseSwell)
	}
	if d.DropoutMS < 0 || d.DropoutMS > 1500 {
		return fmt.Errorf("degrade.dropout_ms は 0〜1500 (got %d)", d.DropoutMS)
	}
	if d.DropoutAfterPause < 0 {
		return fmt.Errorf("degrade.dropout_after_pause は 0 以上 (got %d)", d.DropoutAfterPause)
	}
	if d.DropoutAt < 0 || d.DropoutAt >= 1 {
		return fmt.Errorf("degrade.dropout_at は 0〜1 未満 (got %v)", d.DropoutAt)
	}
	if d.DropoutMS > 0 && d.DropoutAfterPause == 0 && d.DropoutAt == 0 {
		return fmt.Errorf("degrade: dropout_ms があるのに位置 (dropout_after_pause / dropout_at) が無い")
	}
	return nil
}

// speechPauses は音声中の「間」(無音) の終了位置 (サンプル) を順に返す。
// **先頭の無音と末尾の無音は数えない** (台詞の中の間だけ)。
func speechPauses(pcm []int16) []int {
	frame := sampleRate * degradeFrameMS / 1000
	n := len(pcm) / frame
	if n == 0 {
		return nil
	}
	rms := make([]float64, n)
	maxRMS := 0.0
	for i := 0; i < n; i++ {
		var sum float64
		for _, v := range pcm[i*frame : (i+1)*frame] {
			sum += float64(v) * float64(v)
		}
		rms[i] = math.Sqrt(sum / float64(frame))
		if rms[i] > maxRMS {
			maxRMS = rms[i]
		}
	}
	threshold := maxRMS * degradePauseRatio

	first, last := -1, -1
	for i, r := range rms {
		if r >= threshold {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}

	minFrames := degradeMinPauseMS / degradeFrameMS
	var ends []int
	run := 0
	for i := first; i <= last; i++ {
		if rms[i] < threshold {
			run++
			continue
		}
		if run >= minFrames {
			ends = append(ends, i*frame)
		}
		run = 0
	}
	return ends
}

// ApplyDegrade は PCM にノイズと欠落を加えた新しい PCM を返す (入力は変えない)。
// 長さは変わらない。戻り値の文字列は欠落の位置のログ表示用。
//
// 乱数は name から決めるので、同じ name・同じ入力なら同じ音になる。
func ApplyDegrade(pcm []int16, d Degrade, name string) ([]int16, string, error) {
	if err := d.validate(); err != nil {
		return nil, "", err
	}
	out := make([]int16, len(pcm))

	// 声: 帯域を絞る。**絞ると音量が下がるので、元の声と同じ大きさへ戻す。**
	// 欠落とノイズはこのあとで重ねる。
	voice := make([]float64, len(pcm))
	for i, v := range pcm {
		voice[i] = float64(v)
	}
	voiceRMS := activeRMS(voice)
	if d.BandLimit {
		voice = bandLimit(voice)
		if r := activeRMS(voice); r > 0 {
			for i := range voice {
				voice[i] *= voiceRMS / r
			}
		}
	}

	h := fnv.New64a()
	h.Write([]byte(name))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	info := ""
	start, end := -1, -1
	if d.DropoutMS > 0 {
		length := sampleRate * d.DropoutMS / 1000
		if d.DropoutAfterPause > 0 {
			pauses := speechPauses(pcm)
			if d.DropoutAfterPause > len(pauses) {
				return nil, "", fmt.Errorf("degrade.dropout_after_pause=%d だが、台詞の中の間は %d か所しか見つからない",
					d.DropoutAfterPause, len(pauses))
			}
			start = pauses[d.DropoutAfterPause-1]
		} else {
			start = int(d.DropoutAt * float64(len(pcm)))
		}
		end = start + length
		if end > len(pcm) {
			end = len(pcm)
		}
		info = fmt.Sprintf("欠落 %.2f〜%.2f秒", float64(start)/sampleRate, float64(end)/sampleRate)
	}

	fade := sampleRate * degradeFadeMS / 1000
	// 声の絞り具合 (1=そのまま、0=消える)
	gain := func(i int) float64 {
		if start < 0 || i < start-fade || i >= end+fade {
			return 1
		}
		if i < start {
			return float64(start-i) / float64(fade)
		}
		if i < end {
			return 0
		}
		return float64(i-end) / float64(fade)
	}

	// FM のヒスは高域に寄る。白色ノイズの差分 (微分) を取ると高域が強い「シャー」になる。
	// 戻り値の実効値は約1になるよう揃えてある (一様乱数 [-1,1] の隣り合う差の実効値は 0.816 なので 1/0.816 倍)。
	var prevW float64
	noise := func() float64 {
		w := rng.Float64()*2 - 1
		n := (w - prevW) * 1.2247
		prevW = w
		return n
	}

	// 電波の強さのうねり: 遅い2つの揺れを重ねる。弱い (swell が高い) ときほどノイズが増える。
	p1, p2 := rng.Float64()*2*math.Pi, rng.Float64()*2*math.Pi
	weak := func(i int) float64 { // 0〜1 (1 が最も弱い)
		t := float64(i) / sampleRate
		return 0.5 + 0.25*math.Sin(2*math.Pi*1.3*t+p1) + 0.25*math.Sin(2*math.Pi*3.1*t+p2)
	}

	// ノイズの実効値。うねりの平均 (1 + swell/2) で割り、平均の SN 比が SnrDB になるようにする。
	noiseRMS := 0.0
	if d.SnrDB > 0 {
		noiseRMS = voiceRMS / math.Pow(10, d.SnrDB/20) / (1 + d.NoiseSwell*0.5)
	}

	for i := range out {
		g := gain(i)
		w := weak(i)
		v := voice[i] * g
		v += noise() * noiseRMS * (1 + d.NoiseSwell*w)
		if start >= 0 && i >= start-fade && i < end+fade {
			// 声が絞られたぶんだけ、強めのノイズ (スケルチの ザッ) を入れる。声の8割ほどの大きさ
			v += noise() * (1 - g) * 0.8 * voiceRMS
		}
		out[i] = int16(math.Max(-32768, math.Min(32767, v)))
	}
	return out, info, nil
}

// activeRMS は声が出ている区間 (最大の窓の 3% 以上) の実効値を返す。
// 無音を含めると、間の多い台詞ほど「声の大きさ」を小さく見積もってしまう。
func activeRMS(x []float64) float64 {
	frame := sampleRate * degradeFrameMS / 1000
	n := len(x) / frame
	if n == 0 {
		return 0
	}
	ms := make([]float64, n)
	maxMS := 0.0
	for i := 0; i < n; i++ {
		var sum float64
		for _, v := range x[i*frame : (i+1)*frame] {
			sum += v * v
		}
		ms[i] = sum / float64(frame)
		if ms[i] > maxMS {
			maxMS = ms[i]
		}
	}
	// 実効値の 3% = 二乗では 0.03²
	threshold := maxMS * degradePauseRatio * degradePauseRatio
	var sum float64
	count := 0
	for _, m := range ms {
		if m >= threshold {
			sum += m
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(count))
}

// bandLimit は 300Hz 未満と 2.8kHz 超を削る (1次のハイパス + ローパス)。
func bandLimit(x []float64) []float64 {
	const dt = 1.0 / sampleRate
	rcHigh := 1 / (2 * math.Pi * 300)
	aHigh := rcHigh / (rcHigh + dt)
	rcLow := 1 / (2 * math.Pi * 2800)
	aLow := dt / (rcLow + dt)

	out := make([]float64, len(x))
	var hp, prev, lp float64
	for i, v := range x {
		hp = aHigh * (hp + v - prev)
		prev = v
		lp += aLow * (hp - lp)
		out[i] = lp
	}
	return out
}
