package main

import (
	"context"
	"log"
	"time"
)

// speakTTS は発話テキストを TTS で音声化し、1 パケットで送出する。
//
// **発話全文を1回の呼び出しで生成し、分割せずに送る**。
// 生成AIの応答はストリーミングで細かく届くが、全て連結してから
// 1つの Ogg Opus にする (`tts.go`)。
//
// かつては句点で分割して並列生成し、チャンクごとに送っていたが、
// 逐次送信は1チャンクの遅延が発話全体を人質に取るうえ、発話長を
// 2文・60文字以内に絞った結果ほとんどが1チャンクに収まるようになったため
// 廃止した (docs/navigator_design.md §6 決定12・14・18)。失敗の機会も1つで済む。
//
// 戻り値は送出した音声の再生時間 (送出しなかった場合は 0)。呼び出し元は
// これを使って「無線が塞がっている時間」を知る (crosstalk の割り込み防止)。
// ctx がキャンセルされた場合は ctx.Err() を返す。logPrefix はログ出力の接頭辞。
func speakTTS(ctx context.Context, ttsClient *TTSClient, sender *AudioSender, text string, buildPrompt func(string) TTSRequest, voice, logPrefix string) (time.Duration, error) {
	ogg, duration, err := synthesizeTTS(ctx, ttsClient, text, buildPrompt, voice, logPrefix)
	if err != nil || ogg == nil {
		return 0, err
	}
	if !sender.Send(oneshot(ogg)) {
		log.Printf("%s send failed (bridge=%s)", logPrefix, sender.BridgeID())
		return 0, nil
	}
	log.Printf("%s sent", logPrefix)
	return duration, nil
}

// synthesizeTTS は発話テキストを音声化して Ogg Opus にするところまでを行う (送出しない)。
//
// 開始時の第一声のように、**生成を先に済ませて送出を後に回す**呼び出しのために
// speakTTS から切り出した。音声にできなかった場合は ogg が nil (エラーではない)。
// ctx がキャンセルされた場合は ctx.Err() を返す。
func synthesizeTTS(ctx context.Context, ttsClient *TTSClient, text string, buildPrompt func(string) TTSRequest, voice, logPrefix string) ([]byte, time.Duration, error) {
	if text == "" {
		return nil, 0, nil
	}

	pcm, err := ttsClient.Generate(ctx, buildPrompt(text), voice)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		// 音声にできなかった。この回はナビゲーターが黙ることになるため、
		// 運用中に気づけるよう目立つログを残す。
		log.Printf("%s WARN speech dropped: %v", logPrefix, err)
		return nil, 0, nil
	}
	if len(pcm) == 0 {
		log.Printf("%s no TTS audio generated, nothing to send", logPrefix)
		return nil, 0, nil
	}

	ogg, err := encodePCMToOggOpus(pcm)
	if err != nil {
		log.Printf("%s encode error: %v", logPrefix, err)
		return nil, 0, nil
	}

	// 再生時間は PCM のサンプル数から直接求まる (エンコード後の Ogg を
	// 読み直す必要はない)。無線が塞がる時間の見積もりに使う。
	duration := time.Duration(len(pcm)) * time.Second / sampleRate
	log.Printf("%s TTS complete: %.1fs audio, %d bytes", logPrefix, duration.Seconds(), len(ogg))
	return ogg, duration, nil
}
