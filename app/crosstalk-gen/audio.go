package main

import (
	"bytes"
	"encoding/binary"
	"fmt"

	hrabanopus "github.com/hraban/opus"
	"github.com/kazzmir/opus-go/ogg"
)

// Gemini TTS の出力は 24kHz / mono / 16bit PCM。
// game-server の tts.go と同じ値に揃えてある。
const (
	sampleRate = 24000
	channels   = 1
	frameSize  = 480 // 20ms @ 24kHz
	preSkip    = 312 // Opus standard pre-skip
)

// stripWAVHeader は先頭に "RIFF" がある場合、"data" チャンクのペイロードを返す。
// Gemini は WAV ヘッダ付きで返すことがある。
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

// buildWAV は 24kHz mono 16bit の WAV ファイルを組み立てる (keep_wav 用)。
func buildWAV(pcm []int16) []byte {
	dataSize := len(pcm) * 2
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataSize))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))           // fmt chunk size
	binary.Write(&b, binary.LittleEndian, uint16(1))            // PCM
	binary.Write(&b, binary.LittleEndian, uint16(channels))     // channels
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate))   // sample rate
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	binary.Write(&b, binary.LittleEndian, uint16(2))            // block align
	binary.Write(&b, binary.LittleEndian, uint16(16))           // bits per sample
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataSize))
	binary.Write(&b, binary.LittleEndian, pcm)
	return b.Bytes()
}

// encodePCMToOggOpus は PCM を Opus エンコードして Ogg Opus コンテナに格納する。
// radio-bridge がそのままキューへ積んで再生できる形式 (docs/operation_flow.md §5.1)。
func encodePCMToOggOpus(pcm []int16, bitrate int) ([]byte, error) {
	if len(pcm) < frameSize {
		return nil, fmt.Errorf("PCM too short: %d samples (< 1 frame)", len(pcm))
	}

	enc, err := hrabanopus.NewEncoder(sampleRate, channels, hrabanopus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("opus.NewEncoder: %w", err)
	}
	if err := enc.SetBitrate(bitrate); err != nil {
		return nil, fmt.Errorf("opus.SetBitrate: %w", err)
	}

	var buf bytes.Buffer
	pw := ogg.NewPacketWriter(&buf, 0x58544c4b) // "XTLK"

	headPkt, err := ogg.BuildOpusHeadPacket(ogg.OpusHead{
		Version:              1,
		Channels:             channels,
		PreSkip:              preSkip,
		InputSampleRate:      sampleRate,
		ChannelMappingFamily: 0,
	})
	if err != nil {
		return nil, fmt.Errorf("BuildOpusHeadPacket: %w", err)
	}
	if err := pw.WritePacket(headPkt, 0, true, false); err != nil {
		return nil, fmt.Errorf("write OpusHead: %w", err)
	}

	tagsPkt, err := ogg.BuildOpusTagsPacket(ogg.OpusTags{Vendor: "radio-terror-crosstalk"})
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
		granule := uint64(preSkip) + totalSamples
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
