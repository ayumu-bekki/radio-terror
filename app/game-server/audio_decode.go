package main

import (
	"bytes"
	"fmt"
	"io"

	hrabanopus "github.com/hraban/opus"
	"github.com/kazzmir/opus-go/ogg"
)

// decodeOggOpusToPCM は Ogg Opus を 24kHz mono の PCM へ戻す。
//
// 効果音アセットを TTS の音声と**1つの Ogg にまとめて送る**ために使う
// (docs/operation_flow.md §6)。効果音とメッセージを別パケットで送ると、
// 効果音が鳴り終わってから TTS 生成を待つ数秒の**無音**が無線に乗る。
//
// 効果音アセットは TTS と同じ 24kHz mono で作ってあるため、リサンプルは
// 行わない。異なるレート・チャンネル数のファイルはエラーにして、
// 呼び出し側で連結を諦めさせる (無理に混ぜると再生速度が狂うため)。
func decodeOggOpusToPCM(data []byte) ([]int16, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	reader, err := ogg.NewOpusReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("NewOpusReader: %w", err)
	}
	reader.SetVerifyCRC(false)

	if int(reader.Head.Channels) != channels {
		return nil, fmt.Errorf("unsupported channels: %d (want %d)",
			reader.Head.Channels, channels)
	}
	if int(reader.Head.InputSampleRate) != sampleRate {
		return nil, fmt.Errorf("unsupported sample rate: %d (want %d)",
			reader.Head.InputSampleRate, sampleRate)
	}

	dec, err := hrabanopus.NewDecoder(sampleRate, channels)
	if err != nil {
		return nil, fmt.Errorf("opus.NewDecoder: %w", err)
	}

	var pcm []int16
	// Opus の1パケットは最大 120ms。24kHz なら 2880 サンプル。
	frame := make([]int16, 2880)

	for {
		packet, err := reader.ReadAudioPacket()
		if err != nil {
			if err == io.EOF {
				break
			}
			// 途中で壊れていても、そこまでに読めた分を使う
			break
		}
		if packet == nil || len(packet.Data) == 0 {
			continue
		}
		n, err := dec.Decode(packet.Data, frame)
		if err != nil {
			// 1パケットの破損で全体を捨てない
			continue
		}
		pcm = append(pcm, frame[:n]...)
	}

	if len(pcm) == 0 {
		return nil, fmt.Errorf("no audio decoded")
	}

	// エンコーダが入れた先頭の無音 (pre-skip) を落とす。
	// 残したまま連結すると効果音の頭に僅かな無音が付く。
	if preSkip := int(reader.Head.PreSkip); preSkip > 0 && preSkip < len(pcm) {
		pcm = pcm[preSkip:]
	}
	return pcm, nil
}
