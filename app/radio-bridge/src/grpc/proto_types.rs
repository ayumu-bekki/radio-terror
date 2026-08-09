use crate::queue::StreamStatus;

pub mod proto {
    tonic::include_proto!("transceiver");
}

/// proto の status (i32) を queue 層の StreamStatus に変換する。
/// UNKNOWN(0) / 未知値は無効として None を返し、呼び出し側で破棄する
/// (送信側は ONESHOT/START/CONTINUE/END を明示する必要がある)。
pub fn map_status(raw: i32) -> Option<StreamStatus> {
    match proto::StreamStatus::try_from(raw) {
        Ok(proto::StreamStatus::Oneshot) => Some(StreamStatus::Oneshot),
        Ok(proto::StreamStatus::Start) => Some(StreamStatus::Start),
        Ok(proto::StreamStatus::Continue) => Some(StreamStatus::Continue),
        Ok(proto::StreamStatus::End) => Some(StreamStatus::End),
        // UNKNOWN / 未知値は無効。
        _ => None,
    }
}
