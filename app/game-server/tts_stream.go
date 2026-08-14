package main

import (
	"context"
	"log"
	"time"

	pb "game-server/gen"
)

// speakTTS は発話テキストを TTS で音声化し、1 パケット (ONESHOT) で送出する。
//
// **発話全文を1回の呼び出しで生成する**。
//
// 以前は句点で分割し、チャンクごとに並列生成していた。チャンクができ次第
// 順次送ることで最初の音を早く出す狙いだったが、
//
//   - 逐次送信は**1チャンクの遅延が発話全体を人質に取る**ため取りやめ
//     (docs/navigator_design.md §5 決定12)。送出は全チャンクが揃ってから
//     になり、分割の利点は「生成の並列化」だけになった
//   - 発話長を2文・60文字以内に絞ったため (決定14)、分割してもほとんどが
//     1チャンクに収まるようになった。むしろ「…どうぞ」だけが3文字の
//     チャンクとして切り出され、**3文字の生成に2〜3秒の往復**を費やしていた
//
// 分割をやめて呼び出しを1回に固定した (決定18)。失敗の機会も1つで済む。
//
// 戻り値は送出した音声の再生時間 (送出しなかった場合は 0)。呼び出し元は
// これを使って「無線が塞がっている時間」を知る (crosstalk の割り込み防止)。
// ctx がキャンセルされた場合は ctx.Err() を返す。logPrefix はログ出力の接頭辞。
func speakTTS(ctx context.Context, ttsClient *TTSClient, sender *AudioSender, text string, buildPrompt func(string) string, voice, logPrefix string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}

	pcm, err := ttsClient.GeneratePCM24kFromPrompt(ctx, buildPrompt(text), voice)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// 音声にできなかった。この回はナビゲーターが黙ることになるため、
		// 運用中に気づけるよう目立つログを残す。
		log.Printf("%s WARN speech dropped: %v", logPrefix, err)
		return 0, nil
	}
	if len(pcm) == 0 {
		log.Printf("%s no TTS audio generated, nothing to send", logPrefix)
		return 0, nil
	}

	ogg, err := encodePCMToOggOpus(pcm)
	if err != nil {
		log.Printf("%s encode error: %v", logPrefix, err)
		return 0, nil
	}

	// 再生時間は PCM のサンプル数から直接求まる (エンコード後の Ogg を
	// 読み直す必要はない)。無線が塞がる時間の見積もりに使う。
	duration := time.Duration(len(pcm)) * time.Second / sampleRate
	log.Printf("%s TTS complete: %.1fs audio, %d bytes", logPrefix, duration.Seconds(), len(ogg))

	if !sender.Send(outgoingAudio{Data: ogg, Status: pb.StreamStatus_ONESHOT}) {
		log.Printf("%s send failed (bridge=%s)", logPrefix, sender.BridgeID())
		return 0, nil
	}
	log.Printf("%s sent (status=ONESHOT)", logPrefix)
	return duration, nil
}
