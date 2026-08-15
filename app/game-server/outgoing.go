package main

// outgoingAudio は radio-bridge へ送出する 1 つの音声パケットを表す。
// BridgeServer の送信ループがこの型を AudioChunk へ変換して送信する。
//
// **1 パケット = 1 再生サイクル (ワンショット)**。radio-bridge は受け取った
// 音声をそのまま 1 回の PTT 区間で再生する。
//
// かつては同一 StreamID に START → CONTINUE... → END を付けて複数チャンクを
// 1 区間へ束ねる分割送信を持っていたが、TTS をストリーミング受信して
// 分割せず生成する方式へ移行したため廃止した
// (docs/bridge_connection_design.md §2 決定14)。
type outgoingAudio struct {
	Data []byte
}

// oneshot は送出用の outgoingAudio を組み立てるヘルパー。
func oneshot(data []byte) outgoingAudio {
	return outgoingAudio{Data: data}
}

// AudioSender は送出先の bridge が確定した状態の送信口。
//
// 接続反転 (docs/bridge_connection_design.md §2) により送信は宛先 bridge_id を
// 指定する形になったため、宛先を1つ束縛したこの型を各ハンドラへ渡す。
type AudioSender struct {
	registry *BridgeRegistry
	bridgeID string
}

func NewAudioSender(registry *BridgeRegistry, bridgeID string) *AudioSender {
	return &AudioSender{registry: registry, bridgeID: bridgeID}
}

// Send は束縛済みの bridge へ音声を送る。
func (s *AudioSender) Send(audio outgoingAudio) bool {
	if s == nil || s.registry == nil {
		return false
	}
	return s.registry.Send(s.bridgeID, audio)
}

// BridgeID は送出先の bridge_id を返す。
func (s *AudioSender) BridgeID() string {
	if s == nil {
		return ""
	}
	return s.bridgeID
}
