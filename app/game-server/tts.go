package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"time"

	hrabanopus "github.com/hraban/opus"
	"github.com/kazzmir/opus-go/ogg"
	"google.golang.org/genai"
)

const (
	sampleRate  = 24000 // Gemini TTS出力 / Opusエンコード: 24kHz
	channels    = 1
	frameSize   = 480   // 20ms @ 24kHz
	opusBitrate = 16000 // 16kbps

	// opusGranuleRate は Ogg の granule position が使う基準レート。
	// Opus は入力が何 Hz でも granule を 48kHz で数える (RFC 7845 §4)。
	opusGranuleRate = 48000
)

const defaultTTSModel = "gemini-3.1-flash-tts-preview"

// ttsSlowThreshold を超えた TTS 呼び出しはプロンプト全文をログに残す。
// 実測の正常値は 2.2〜5.7 秒なので、その上に取ってある。
const ttsSlowThreshold = 10 * time.Second

// defaultTTSVoice は tts_voice が未指定だった場合のフォールバック。
// 通常はキャラクター定義側で必ず指定する (navigator_character.go が検証する)。
const defaultTTSVoice = "Despina"

const ttsPersona = `# VOICE CHARACTER: 無線オペレーターA (Despina)
## 基本プロフィール
- 話者: 日本人女性の熟練アマチュア無線オペレーター
- 声質: 明るく明瞭な女性の声。FM変調がかかった状態でも一語一語がはっきり聞き取れる
- 話速: やや速め。ただし子音・語尾を明確に発音し、早口でも聞き取りやすさを保つ
- 感情: 落ち着いた親しみやすさ。抑揚は最小限に抑え、チャンクをまたいでも同じトーンを維持する
- 語尾を伸ばさない。文末は平坦〜下がり調子で終わる

## 発音の特徴
- コールサイン (英数字の組み合わせ) は一文字ずつ日本語読みで発音する: S4CA → "エス ヨン シー エー"
- 「どうぞ」は無線用語として明確に発音し、その後は無音にする
- 助詞・助動詞を略さず丁寧に発音する

## 音響特性 (毎回一定に保つこと)
- Pitch: 一定 (変動なし)
- Tempo: 1.1x (やや速め、一定)
- Breathiness: minimal
- Vocal fry: none`

// TTSClient は Gemini TTS クライアントを保持する。
type TTSClient struct {
	client  *genai.Client
	model   string
	timeout time.Duration
}

// NewTTSClient は設定に応じたバックエンド (Gemini API / Vertex AI) で
// TTS クライアントを作る。model が空なら defaultTTSModel を使う。
func NewTTSClient(ctx context.Context, cfg GeminiConfig) (*TTSClient, error) {
	model := cfg.TTSModel
	if model == "" {
		model = defaultTTSModel
	}
	client, err := NewGenAIClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("genai.NewClient: %w", err)
	}
	return &TTSClient{client: client, model: model, timeout: cfg.TTSTimeout()}, nil
}

// GenerateOggOpusFromPrompt は組み立て済みプロンプトから TTS 音声を生成して Ogg Opus で返す。
// voice が空の場合は defaultTTSVoice が使われる。
func (t *TTSClient) GenerateOggOpusFromPrompt(ctx context.Context, prompt, voice string) ([]byte, error) {
	pcm24k, err := t.GeneratePCM24kFromPrompt(ctx, prompt, voice)
	if err != nil {
		return nil, err
	}
	return encodePCMToOggOpus(pcm24k)
}

// GeneratePCM24kFromPrompt はプロンプトからTTS音声を生成し、24kHz mono の PCM(int16)で返す。
//
// voice はキャラクターごとのボイス名 (navigator/characters/*.toml の tts_voice、
// 疎通確認は testResponderTTSVoice)。空文字の場合のみ既定値を使う。
func (t *TTSClient) GeneratePCM24kFromPrompt(ctx context.Context, prompt, voice string) ([]int16, error) {
	if voice == "" {
		voice = defaultTTSVoice
	}

	// チャンクごとに上限を切る。1つが詰まっても他のチャンクは流れる
	// (speakTTSChunks が失敗チャンクをスキップして続行する)。
	if t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}

	start := time.Now()
	resp, err := t.client.Models.GenerateContent(ctx, t.model,
		[]*genai.Content{
			genai.NewContentFromText(prompt, genai.RoleUser),
		},
		&genai.GenerateContentConfig{
			ResponseModalities: []string{"audio"},
			SpeechConfig: &genai.SpeechConfig{
				VoiceConfig: &genai.VoiceConfig{
					PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
						VoiceName: voice,
					},
				},
			},
		},
	)
	elapsed := time.Since(start)
	log.Printf("[gemini] TTS latency: %v (voice=%s, %d runes)", elapsed, voice, countRunes(prompt))

	// 遅かった呼び出しはプロンプト全文を残す。
	//
	// 過去に角括弧タグ入りの本文で応答が不安定になった前例があり
	// (通常2.6秒が最大21.7秒。tts_prompt.go)、**どの入力で詰まるか**が
	// 分からないと対処できない。チャンクは並列生成されるためレイテンシ行
	// 単体では対応が取れないので、遅い時だけ本文ごと出す。
	if elapsed >= ttsSlowThreshold {
		log.Printf("[gemini] TTS SLOW (%v) prompt=%q", elapsed, prompt)
	}

	if err != nil {
		return nil, fmt.Errorf("GenerateContent: %w", err)
	}

	if len(resp.Candidates) == 0 ||
		resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty TTS response")
	}

	blob := resp.Candidates[0].Content.Parts[0].InlineData
	if blob == nil {
		return nil, fmt.Errorf("no inline audio data in TTS response")
	}

	pcmData := stripWAVHeader(blob.Data)

	pcm, err := parsePCM16(pcmData)
	if err != nil {
		return nil, fmt.Errorf("parsePCM16: %w", err)
	}
	// 秒数とプロンプト長を併記する。読み上げ本文に対して音声が不自然に
	// 長い場合、プロンプトの指示文まで読み上げている疑いがある。
	seconds := float64(len(pcm)) / sampleRate
	log.Printf("[tts] decoded %d samples (%.1fs @ 24kHz, prompt %d runes)",
		len(pcm), seconds, countRunes(prompt))

	return pcm, nil
}

// stripWAVHeader はデータ先頭に "RIFF" マジックがある場合、"data" チャンクのペイロードを返す。
func stripWAVHeader(data []byte) []byte {
	if len(data) < 12 || !bytes.HasPrefix(data, []byte("RIFF")) {
		return data
	}
	pos := 12
	for pos+8 <= len(data) {
		chunkID := string(data[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if chunkID == "data" {
			end := pos + chunkSize
			if end > len(data) {
				end = len(data)
			}
			return data[pos:end]
		}
		pos += chunkSize
		if chunkSize%2 != 0 {
			pos++
		}
	}
	return data
}

// parsePCM16 はリトルエンディアン16bit PCMバイト列をint16スライスに変換する。
func parsePCM16(data []byte) ([]int16, error) {
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("odd PCM byte length: %d", len(data))
	}
	samples := make([]int16, len(data)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
	}
	return samples, nil
}

// encodePCMToOggOpus はPCMをOpusエンコードしてOgg Opusコンテナに格納する。
// エンコーダ: hraban/opus (libopus cgo)
// Oggコンテナ: kazzmir/opus-go/ogg
func encodePCMToOggOpus(pcm []int16) ([]byte, error) {
	const preSkip = 312 // Opus standard pre-skip

	enc, err := hrabanopus.NewEncoder(sampleRate, channels, hrabanopus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("opus.NewEncoder: %w", err)
	}
	if err := enc.SetBitrate(opusBitrate); err != nil {
		return nil, fmt.Errorf("opus.SetBitrate: %w", err)
	}

	var buf bytes.Buffer
	pw := ogg.NewPacketWriter(&buf, 0x57485052) // "WHPR"

	head := ogg.OpusHead{
		Version:              1,
		Channels:             channels,
		PreSkip:              preSkip,
		InputSampleRate:      sampleRate,
		ChannelMappingFamily: 0,
	}
	headPkt, err := ogg.BuildOpusHeadPacket(head)
	if err != nil {
		return nil, fmt.Errorf("BuildOpusHeadPacket: %w", err)
	}
	if err := pw.WritePacket(headPkt, 0, true, false); err != nil {
		return nil, fmt.Errorf("write OpusHead: %w", err)
	}

	tags := ogg.OpusTags{Vendor: "whisper-link"}
	tagsPkt, err := ogg.BuildOpusTagsPacket(tags)
	if err != nil {
		return nil, fmt.Errorf("BuildOpusTagsPacket: %w", err)
	}
	if err := pw.WritePacket(tagsPkt, 0, false, false); err != nil {
		return nil, fmt.Errorf("write OpusTags: %w", err)
	}

	packet := make([]byte, 4096)
	var totalSamples uint64

	for i := 0; i+frameSize <= len(pcm); i += frameSize {
		n, err := enc.Encode(pcm[i:i+frameSize], packet)
		if err != nil {
			return nil, fmt.Errorf("opus.Encode: %w", err)
		}

		totalSamples += uint64(frameSize)
		// granule position は**常に 48kHz 基準**で数える (RFC 7845 §4)。
		// 入力は 24kHz なのでサンプル数を2倍する。
		// ここを 24kHz のまま書くと、granule から尺を求める側 (radio-bridge の
		// 長さ上限チェック) が実尺の半分と誤認する。
		granule := uint64(preSkip) + totalSamples*(opusGranuleRate/sampleRate)
		isLast := i+frameSize+frameSize > len(pcm)

		if err := pw.WritePacket(packet[:n], granule, false, isLast); err != nil {
			return nil, fmt.Errorf("ogg.WritePacket: %w", err)
		}
	}

	if err := pw.Flush(); err != nil {
		return nil, fmt.Errorf("ogg.Flush: %w", err)
	}

	return buf.Bytes(), nil
}
