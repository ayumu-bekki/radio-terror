mod audio;
mod config;
mod controller;
mod grpc;
mod queue;

use crate::audio::recorder::AudioRecorder;
use crate::config::Config;
use crate::controller::Controller;
use crate::grpc::client::BridgeClient;
use crate::queue::AudioQueue;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tracing::info;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::from_default_env()
                .add_directive("radio_bridge=debug".parse()?),
        )
        .init();

    let config_path = std::env::args()
        .nth(1)
        .unwrap_or_else(|| "config.toml".to_string());

    let config = Config::load(Path::new(&config_path))?;
    info!("loaded config from {config_path}");
    // 解決後の値をログに出す (環境変数とTOMLのどちらが効いたか運用で判別できるようにする)
    info!(
        bridge_id = %config.server.bridge_id,
        server = %config.server.server_addr,
        input_device = %config.audio.input_device,
        output_device = %config.audio.output_device,
        ptt_pin = config.gpio.ptt_pin,
        "starting radio-bridge (client mode)"
    );

    let queue = Arc::new(Mutex::new(AudioQueue::new(
        config.queue.max_queue_size,
        config.queue.max_audio_duration_secs,
    )));

    let dump_ogg_dir = config.audio.dump_ogg_enabled
        .then(|| std::path::PathBuf::from(&config.audio.dump_ogg_dir));
    let recorder = Arc::new(
        AudioRecorder::start(
            &config.audio.input_device,
            config.audio.input_threshold_rms,
            Duration::from_millis(config.audio.input_silence_ms),
            Duration::from_millis(config.audio.input_min_recording_ms),
            Duration::from_secs(config.audio.input_max_recording_secs),
            dump_ogg_dir,
        )?,
    );
    info!("audio recorder started on device: {}", config.audio.input_device);

    // game-server へダイヤルインするクライアント
    // (docs/bridge_connection_design.md §2 決定1: 接続方向の反転)
    let client = BridgeClient::new(
        config.server.server_addr.clone(),
        config.server.bridge_id.clone(),
        Duration::from_secs(config.server.reconnect_interval_secs),
        Arc::clone(&queue),
        Arc::clone(&recorder),
    );

    let controller = Controller::new(Arc::clone(&queue), Arc::clone(&recorder), config);
    let controller_task = tokio::spawn(async move {
        if let Err(e) = controller.run().await {
            tracing::error!("controller error: {e}");
        }
    });

    // 再接続ループを内蔵しているため、通常このタスクは終了しない
    let client_task = tokio::spawn(async move { client.run().await });

    tokio::select! {
        _ = controller_task => tracing::error!("controller task exited unexpectedly"),
        _ = client_task => tracing::error!("bridge client task exited unexpectedly"),
    }

    Ok(())
}
