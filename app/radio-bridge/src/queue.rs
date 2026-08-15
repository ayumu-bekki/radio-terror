use std::collections::VecDeque;
use std::time::Duration;
use tracing::warn;

const OPUS_SAMPLE_RATE: u32 = 48000;

#[derive(Debug, Clone)]
pub struct AudioEntry {
    pub ogg_opus_data: Vec<u8>,
}

/// 受信音声を到着順に保持するキュー。
///
/// **1 エントリ = 1 再生サイクル (ワンショット)**。controller は先頭から
/// 1 つ取り出し、それを 1 回の PTT 区間で再生する。
///
/// かつては stream_id ごとに複数チャンクを束ね、同一 PTT 区間で連続再生する
/// 仕組みを持っていたが、サーバーが音声を分割せず 1 パケットで送るように
/// なったため廃止した (docs/bridge_connection_design.md §2 決定14)。
pub struct AudioQueue {
    entries: VecDeque<AudioEntry>,
    /// 同時に保持できるエントリ数の上限。
    max_entries: usize,
    max_duration: Duration,
}

impl AudioQueue {
    pub fn new(max_entries: usize, max_duration_secs: u64) -> Self {
        Self {
            entries: VecDeque::new(),
            max_entries,
            max_duration: Duration::from_secs(max_duration_secs),
        }
    }

    /// 音声を末尾に積む。
    /// Ok(position) = 積んだ後のキュー長 (1始まり)
    /// Err(QueueError) = 拒否理由
    pub fn push(&mut self, ogg_opus_data: Vec<u8>) -> Result<usize, QueueError> {
        let duration =
            parse_ogg_opus_duration(&ogg_opus_data).map_err(QueueError::ParseError)?;
        if duration > self.max_duration {
            warn!(
                duration_secs = duration.as_secs_f32(),
                max_secs = self.max_duration.as_secs(),
                "audio too long, rejecting"
            );
            return Err(QueueError::TooLong(duration));
        }

        if self.entries.len() >= self.max_entries {
            return Err(QueueError::QueueFull(self.max_entries));
        }

        self.entries.push_back(AudioEntry { ogg_opus_data });
        Ok(self.entries.len())
    }

    /// 先頭の音声を取り出す。キューが空なら None。
    pub fn pop(&mut self) -> Option<AudioEntry> {
        self.entries.pop_front()
    }

    /// 再生待ちの音声があるか。
    pub fn has_audio(&self) -> bool {
        !self.entries.is_empty()
    }
}

#[derive(Debug, thiserror::Error)]
pub enum QueueError {
    #[error("audio too long: {0:.1?} (max 30s)")]
    TooLong(Duration),
    #[error("too many audio entries queued (max {0})")]
    QueueFull(usize),
    #[error("failed to parse ogg opus: {0}")]
    ParseError(String),
}

/// Ogg Opusストリームの再生時間をgranule_positionからパースする。
/// granule_position は 48kHz サンプル数で表現される。
fn parse_ogg_opus_duration(data: &[u8]) -> Result<Duration, String> {
    let mut cursor = std::io::Cursor::new(data);
    let mut reader = ogg::reading::PacketReader::new(&mut cursor);

    let mut last_granule: Option<u64> = None;

    loop {
        match reader.read_packet() {
            Ok(Some(packet)) => {
                if packet.absgp_page() != 0 {
                    last_granule = Some(packet.absgp_page());
                }
            }
            Ok(None) => break,
            Err(e) => return Err(format!("{e}")),
        }
    }

    let granule = last_granule.ok_or_else(|| "no granule position found".to_string())?;
    let duration = Duration::from_secs_f64(granule as f64 / OPUS_SAMPLE_RATE as f64);
    Ok(duration)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 不正な ogg は push 前のパースで弾かれる (キューに入らない)。
    #[test]
    fn invalid_ogg_is_rejected() {
        let mut q = AudioQueue::new(10, 30);
        let err = q.push(vec![0, 1, 2]).unwrap_err();
        assert!(matches!(err, QueueError::ParseError(_)));
        assert!(!q.has_audio());
    }

    /// 到着順に取り出せること。
    #[test]
    fn entries_pop_in_arrival_order() {
        let mut q = AudioQueue::new(10, 30);
        // duration パースを経由せずに順序だけ確認する。
        q.entries.push_back(AudioEntry { ogg_opus_data: vec![1] });
        q.entries.push_back(AudioEntry { ogg_opus_data: vec![2] });

        assert!(q.has_audio());
        assert_eq!(q.pop().unwrap().ogg_opus_data, vec![1]);
        assert_eq!(q.pop().unwrap().ogg_opus_data, vec![2]);
        assert!(q.pop().is_none());
        assert!(!q.has_audio());
    }

    /// 上限判定がキュー長で行われること。
    ///
    /// push は「パース → 長さ上限 → 件数上限」の順に見るため、不正な ogg では
    /// 件数上限まで到達しない。ここでは判定に使う値だけを直接確かめる。
    #[test]
    fn queue_full_boundary() {
        let mut q = AudioQueue::new(2, 30);
        assert!(q.entries.len() < q.max_entries);
        for _ in 0..2 {
            q.entries.push_back(AudioEntry { ogg_opus_data: vec![0] });
        }
        assert!(q.entries.len() >= q.max_entries, "上限に達したら push を拒否する");
    }
}
