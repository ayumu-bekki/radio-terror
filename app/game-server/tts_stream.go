package main

import (
	"context"
	"log"
	"time"

	pb "game-server/gen"
)

// ttsChunkResult は 1 チャンクの並列 TTS 生成結果を表す。
//
// エンコードはしない。全チャンクの PCM を連結してから 1 回だけ Opus に
// エンコードするため (buildTTSAudio 参照)。
type ttsChunkResult struct {
	pcm []int16
	err error
}

// generateChunksParallel は分割済みプロンプト(組み立て済み文字列)を並列で TTS 生成し、
// 各チャンクの結果チャネルをインデックス順に返す。呼び出し側は results[i] を昇順に待ち合わせることで、
// 全チャンクを同時生成しつつ順序を保って取り出せる。
// プロンプトの組み立ては呼び出し元(speakTTSChunks)に委ねる。
func generateChunksParallel(ctx context.Context, ttsClient *TTSClient, prompts []string, voice string) []chan ttsChunkResult {
	results := make([]chan ttsChunkResult, len(prompts))
	for i := range results {
		results[i] = make(chan ttsChunkResult, 1)
	}
	for i, prompt := range prompts {
		go func(idx int, ttsPrompt string) {
			pcm, err := ttsClient.GeneratePCM24kFromPrompt(ctx, ttsPrompt, voice)
			results[idx] <- ttsChunkResult{pcm: pcm, err: err}
		}(i, prompt)
	}
	return results
}

// speakTTSChunks は分割済みチャンクを並列で TTS 生成し、**全チャンクが揃ってから**
// 連結して 1 つの Ogg Opus にエンコードし、1 パケット (ONESHOT) で送出する。
//
// 分割送信 (START/CONTINUE/END) をやめた理由:
//
// チャンクごとに送ると、先頭チャンクができ次第すぐ流せるぶん体感レイテンシは
// 小さい。しかし1チャンクでも生成が遅れると、その完成を待つ間ずっと後続を
// 送れず、bridge 側の再生キューに中途半端な状態で積まれる。実運用で
// TTS が 58 秒かかり、先に完成していた後続チャンクが 1 分近く滞留して
// 発話が丸ごと遅れる事象が出た。
//
// **レイテンシより確実性を取る**。全チャンクが揃うまで送出しないので、
// 出るときには必ず発話全体が揃っている。単一の Ogg ストリームになるため、
// bridge 側で別々の Ogg を繋ぐ必要もなくなる。
//
// 1 チャンクの TTS が失敗した場合はログを残してそのチャンクのみスキップし、
// 残りを繋いで送る。全チャンク失敗の場合は何も送らない。
// ctx がキャンセルされた場合は ctx.Err() を返す。logPrefix はログ出力の接頭辞。
// buildPrompt はテキストチャンクを TTS 用の完成プロンプトに変換する関数。呼び出し元が
// ペルソナ・シーン設定をここで差し込むことで、generateChunksParallel は純粋な生成処理に徹する。
// voice は話者のボイス名 (空なら既定値)。チャンク間で声が変わらないよう全体で同一の値を使う。
// 戻り値は送出した音声の再生時間 (送出しなかった場合は 0)。呼び出し元は
// これを使って「無線が塞がっている時間」を知る (crosstalk の割り込み防止)。
func speakTTSChunks(ctx context.Context, ttsClient *TTSClient, sender *AudioSender, chunks []string, buildPrompt func(string) string, voice, logPrefix string) (time.Duration, error) {
	if len(chunks) == 0 {
		return 0, nil
	}

	prompts := make([]string, len(chunks))
	for i, chunk := range chunks {
		prompts[i] = buildPrompt(chunk)
	}
	results := generateChunksParallel(ctx, ttsClient, prompts, voice)

	// インデックス昇順に全チャンクを待ち合わせ、PCM を連結する。
	// 並列生成なので待ち時間は「最も遅いチャンク」で決まる。
	n := len(prompts)
	var merged []int16
	okCount := 0
	for i := 0; i < n; i++ {
		var res ttsChunkResult
		select {
		case res = <-results[i]:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		if res.err != nil {
			log.Printf("%s TTS error (chunk %d/%d): %v", logPrefix, i+1, n, res.err)
			continue
		}
		merged = append(merged, res.pcm...)
		okCount++
	}

	if okCount == 0 {
		log.Printf("%s no TTS audio generated, nothing to send", logPrefix)
		return 0, nil
	}

	ogg, err := encodePCMToOggOpus(merged)
	if err != nil {
		log.Printf("%s encode error: %v", logPrefix, err)
		return 0, nil
	}

	// 再生時間は PCM のサンプル数から直接求まる (エンコード後の Ogg を
	// 読み直す必要はない)。無線が塞がる時間の見積もりに使う。
	duration := time.Duration(len(merged)) * time.Second / sampleRate
	log.Printf("%s TTS complete: %d/%d chunks, %.1fs audio, %d bytes",
		logPrefix, okCount, n, duration.Seconds(), len(ogg))

	if !sender.Send(outgoingAudio{Data: ogg, Status: pb.StreamStatus_ONESHOT}) {
		log.Printf("%s send failed (bridge=%s)", logPrefix, sender.BridgeID())
		return 0, nil
	}
	log.Printf("%s sent (status=ONESHOT)", logPrefix)
	return duration, nil
}
