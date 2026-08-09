package main

import pb "wl-game-server/gen"

// outgoingAudio は radio-bridge へ送出する 1 つの音声パケットを表す。
// BridgeServer の送信ループがこの型を AudioChunk へ変換して送信する。
//
//   - 単発再生: Status=ONESHOT, StreamID="" で 1 パケット送出。
//   - 連続再生 (分割送信): 同一 StreamID に START → CONTINUE... → END を付けて
//     チャンクごとに送出。radio-bridge は同一 StreamID を 1 区間で連続再生する。
type outgoingAudio struct {
	Data     []byte
	Status   pb.StreamStatus
	StreamID string
}

// oneshot は単発再生用の outgoingAudio を組み立てるヘルパー。
func oneshot(data []byte) outgoingAudio {
	return outgoingAudio{Data: data, Status: pb.StreamStatus_ONESHOT}
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
