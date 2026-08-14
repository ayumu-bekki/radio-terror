package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"time"

	"github.com/kazzmir/opus-go/ogg"
)

// oggOpusDuration は Ogg Opus データの再生時間を返す。
//
// 無線が塞がる時間を見積もるために使う。bridge から再生完了の通知を
// 受け取る経路が proto に無いため、送出する音声の長さから推定する
// (docs/operation_flow.md §5.1)。
//
// Opus の granule position は**常に 48kHz 基準**で数える (RFC 7845)。
// 24kHz で生成した音声でも granule は 48kHz で刻まれるため、
// ogg.OpusSampleRateHz で割る。
//
// 解析できない場合は 0 を返す (呼び出し側で見込み値へフォールバックする)。
func oggOpusDuration(data []byte) time.Duration {
	if len(data) == 0 {
		return 0
	}

	reader := ogg.NewPageReader(bytes.NewReader(data))
	// 壊れたページで打ち切るより、読める範囲の最大 granule を採りたい
	reader.VerifyCRC = false

	var last uint64
	var preSkip uint64
	for {
		page, err := reader.ReadPage()
		if err != nil {
			if err == io.EOF {
				break
			}
			// 途中で壊れていても、そこまでに読めた分で見積もる
			break
		}
		if page.IsBOS() {
			if head, err := parseOpusHeadPreSkip(page.SegmentData); err == nil {
				preSkip = head
			}
		}
		// granule position が未確定のページ (-1) は無視する
		if page.GranulePosition != ^uint64(0) && page.GranulePosition > last {
			last = page.GranulePosition
		}
	}

	if last <= preSkip {
		return 0
	}
	samples := last - preSkip
	return time.Duration(samples) * time.Second / ogg.OpusSampleRateHz
}

// parseOpusHeadPreSkip は OpusHead パケットから pre-skip を取り出す。
// pre-skip はデコード時に捨てられるサンプル数なので、再生時間から差し引く。
//
// OpusHead は先頭固定レイアウト (RFC 7845 §5.1):
//
//	0-7   "OpusHead"
//	8     version
//	9     channel count
//	10-11 pre-skip (リトルエンディアン)
//
// ライブラリにパース関数が公開されていないため、必要な部分だけ読む。
func parseOpusHeadPreSkip(data []byte) (uint64, error) {
	if len(data) < 12 || !bytes.HasPrefix(data, []byte("OpusHead")) {
		return 0, ogg.ErrNotOpusOgg
	}
	return uint64(binary.LittleEndian.Uint16(data[10:12])), nil
}
