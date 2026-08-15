use crate::audio::recorder::AudioRecorder;
use crate::queue::{AudioQueue, QueueError};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::metadata::MetadataValue;
use tonic::transport::{Channel, Endpoint};
use tonic::Request;
use tracing::{error, info, warn};

use super::proto;
use proto::transceiver_service_client::TransceiverServiceClient;
use proto::AudioChunk;

/// bridge-id を伝えるメタデータキー
/// (docs/bridge_connection_design.md §2 決定3)。
const BRIDGE_ID_METADATA_KEY: &str = "bridge-id";

/// game-server へダイヤルインする gRPC クライアント。
///
/// 接続方向は設計で反転済み (§2 決定1): radio-bridge がクライアントとなり
/// game-server へ接続する。サーバーは周辺機器のアドレスを管理せず、
/// 確立済みの双方向ストリーム自体が返信路になる。
pub struct BridgeClient {
    /// 接続先 game-server の gRPC エンドポイント
    server_addr: String,
    /// 自分の ID。接続時に bridge-id メタデータで送る (§2 決定2)
    bridge_id: String,
    /// 再接続の待機間隔
    reconnect_interval: Duration,

    queue: Arc<Mutex<AudioQueue>>,
    recorder: Arc<AudioRecorder>,
}

impl BridgeClient {
    pub fn new(
        server_addr: String,
        bridge_id: String,
        reconnect_interval: Duration,
        queue: Arc<Mutex<AudioQueue>>,
        recorder: Arc<AudioRecorder>,
    ) -> Self {
        Self {
            server_addr,
            bridge_id,
            reconnect_interval,
            queue,
            recorder,
        }
    }

    /// 再接続ループ付きで接続を維持する (§3)。
    /// 切断されても待機してから再接続を試み続ける。
    pub async fn run(&self) {
        loop {
            match self.connect_once().await {
                Ok(()) => info!(bridge_id = %self.bridge_id, "stream closed by server"),
                Err(e) => warn!(bridge_id = %self.bridge_id, "connection error: {e}"),
            }

            info!(
                bridge_id = %self.bridge_id,
                secs = self.reconnect_interval.as_secs(),
                "reconnecting after delay"
            );
            tokio::time::sleep(self.reconnect_interval).await;
        }
    }

    /// 1回分の接続を確立し、双方向ストリームを処理する。
    async fn connect_once(&self) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let endpoint = Endpoint::from_shared(self.server_addr.clone())?
            .connect_timeout(Duration::from_secs(10))
            // 無音区間が長い運用のため、keepalive で接続を維持する
            .http2_keep_alive_interval(Duration::from_secs(30))
            .keep_alive_timeout(Duration::from_secs(10));

        let channel: Channel = endpoint.connect().await?;
        let mut client = TransceiverServiceClient::new(channel);

        info!(
            bridge_id = %self.bridge_id,
            server = %self.server_addr,
            "connected to game-server"
        );

        // マイク音声をサーバーへ送るストリーム
        let (tx, rx) = mpsc::channel::<AudioChunk>(32);
        let mut recorder_rx = self.recorder.subscribe();
        let bridge_id_send = self.bridge_id.clone();

        let send_task = tokio::spawn(async move {
            loop {
                match recorder_rx.recv().await {
                    Ok(ogg_data) => {
                        let chunk = AudioChunk { ogg_opus_data: ogg_data };
                        if tx.send(chunk).await.is_err() {
                            info!(bridge_id = %bridge_id_send, "send stream closed");
                            break;
                        }
                    }
                    Err(tokio::sync::broadcast::error::RecvError::Lagged(n)) => {
                        warn!(bridge_id = %bridge_id_send, skipped = n, "recorder broadcast lagged");
                    }
                    Err(tokio::sync::broadcast::error::RecvError::Closed) => {
                        error!(bridge_id = %bridge_id_send, "recorder broadcast closed");
                        break;
                    }
                }
            }
        });

        // 接続時に bridge-id をメタデータで送る。
        // メタデータが無い接続はサーバー側で拒否される (§3)。
        let mut request = Request::new(ReceiverStream::new(rx));
        let value: MetadataValue<_> = self.bridge_id.parse()?;
        request.metadata_mut().insert(BRIDGE_ID_METADATA_KEY, value);

        let response = client.connect(request).await?;
        let mut inbound = response.into_inner();

        // サーバーから届く音声 (TTS・効果音・混線) をキューへ積む
        let result = self.receive_loop(&mut inbound).await;

        send_task.abort();
        result
    }

    /// サーバーからの受信ストリームを処理してキューへ積む。
    async fn receive_loop(
        &self,
        inbound: &mut tonic::Streaming<AudioChunk>,
    ) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        loop {
            let chunk = match inbound.message().await? {
                Some(chunk) => chunk,
                None => return Ok(()),
            };

            let bytes = chunk.ogg_opus_data.len();

            let result = {
                let mut q = self.queue.lock().unwrap();
                q.push(chunk.ogg_opus_data)
            };
            match result {
                Ok(pos) => {
                    info!(bridge_id = %self.bridge_id, bytes, position = pos, "audio queued");
                }
                Err(QueueError::TooLong(d)) => {
                    warn!(
                        bridge_id = %self.bridge_id,
                        duration_secs = d.as_secs_f32(),
                        "audio rejected: too long"
                    );
                }
                Err(QueueError::QueueFull(max)) => {
                    warn!(bridge_id = %self.bridge_id, max, "audio rejected: queue full");
                }
                Err(QueueError::ParseError(msg)) => {
                    warn!(bridge_id = %self.bridge_id, %msg, "audio rejected: parse error");
                }
            }
        }
    }
}
