use serde::Deserialize;
use std::path::Path;

#[derive(Debug, Deserialize, Clone)]
pub struct Config {
    pub server: ServerConfig,
    pub gpio: GpioConfig,
    pub audio: AudioConfig,
    pub timing: TimingConfig,
    pub queue: QueueConfig,
}

/// game-server への接続設定。
///
/// 接続方向は反転済み (docs/bridge_connection_design.md §2 決定1) のため、
/// bridge は listen せず、サーバーへダイヤルインする。
#[derive(Debug, Deserialize, Clone)]
pub struct ServerConfig {
    /// 接続先 game-server の gRPC エンドポイント (例: "http://game-server:50051")
    pub server_addr: String,

    /// 自分の bridge ID。環境変数 RADIO_BRIDGE_ID があればそちらを優先する (§2 決定2)。
    #[serde(default)]
    pub bridge_id: String,

    /// 再接続の待機間隔 (秒)
    #[serde(default = "default_reconnect_interval_secs")]
    pub reconnect_interval_secs: u64,
}

fn default_reconnect_interval_secs() -> u64 { 5 }

#[derive(Debug, Deserialize, Clone)]
pub struct GpioConfig {
    /// PTT制御に使うGPIOピン番号 (BCM番号)。環境変数 RADIO_BRIDGE_PTT_PIN を優先する。
    /// 複数 radio-bridge を同一ホストで動かす際、配線(PCB)がプロセスごとに異なるため、
    /// bridge_id 等と同様に環境変数だけで振り分けられるようにしている。
    #[serde(default = "default_unset_ptt_pin")]
    pub ptt_pin: u8,
}

/// TOML未設定・環境変数未設定を示す番兵値。
/// Raspberry Pi の BCM GPIO番号は 0-27 の範囲に収まるため、範囲外の値を「未設定」に使う。
fn default_unset_ptt_pin() -> u8 { u8::MAX }

#[derive(Debug, Deserialize, Clone)]
pub struct AudioConfig {
    /// 出力(スピーカー)デバイス。環境変数 RADIO_BRIDGE_OUTPUT_DEVICE を優先する。
    #[serde(default)]
    pub output_device: String,

    /// 入力(マイク)デバイス。環境変数 RADIO_BRIDGE_INPUT_DEVICE を優先する。
    #[serde(default)]
    pub input_device: String,

    pub input_threshold_rms: u16,
    pub input_silence_ms: u64,
    pub input_max_recording_secs: u64,
    #[serde(default = "default_input_min_recording_ms")]
    pub input_min_recording_ms: u64,
    #[serde(default)]
    pub dump_ogg_enabled: bool,
    #[serde(default = "default_dump_ogg_dir")]
    pub dump_ogg_dir: String,
}

fn default_input_min_recording_ms() -> u64 { 800 }
fn default_dump_ogg_dir() -> String { "/app/dump_audio".to_string() }

#[derive(Debug, Deserialize, Clone)]
pub struct TimingConfig {
    pub ptt_on_delay_ms: u64,
    pub ptt_off_delay_ms: u64,
    pub cooldown_ms: u64,
}

#[derive(Debug, Deserialize, Clone)]
pub struct QueueConfig {
    pub max_audio_duration_secs: u64,
    pub max_queue_size: usize,
}

/// 環境変数が空でなければその値で上書きする。
///
/// bridge ID・オーディオデバイスは**プロセスごとに異なる**値を使うため、
/// 同じ config.toml を bind mount したまま環境変数だけで振り分けられるようにする
/// (チーム=周波数ごとに1プロセス+専用オーディオIF。
/// docs/operation_flow.md §8)。
fn override_from_env(target: &mut String, key: &str) {
    if let Ok(value) = std::env::var(key) {
        if !value.is_empty() {
            *target = value;
        }
    }
}

/// 環境変数が空でなければその値をパースして上書きする。
fn override_from_env_u8(
    target: &mut u8,
    key: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    if let Ok(value) = std::env::var(key) {
        if !value.is_empty() {
            *target = value
                .parse()
                .map_err(|e| format!("{key} must be a valid number (0-255): {e}"))?;
        }
    }
    Ok(())
}

impl Config {
    pub fn load(path: &Path) -> Result<Self, Box<dyn std::error::Error + Send + Sync>> {
        let content = std::fs::read_to_string(path)?;
        let mut config: Config = toml::from_str(&content)?;

        // プロセスごとに変える設定は環境変数を優先する
        override_from_env(&mut config.server.bridge_id, "RADIO_BRIDGE_ID");
        override_from_env(&mut config.audio.input_device, "RADIO_BRIDGE_INPUT_DEVICE");
        override_from_env(&mut config.audio.output_device, "RADIO_BRIDGE_OUTPUT_DEVICE");
        override_from_env_u8(&mut config.gpio.ptt_pin, "RADIO_BRIDGE_PTT_PIN")?;

        // 未設定のまま起動すると、接続拒否や無音といった分かりにくい失敗になるため、
        // 起動時点で落として原因を明示する。
        if config.server.bridge_id.is_empty() {
            return Err("bridge_id is required (set RADIO_BRIDGE_ID or [server].bridge_id)".into());
        }
        if config.audio.input_device.is_empty() {
            return Err(
                "input_device is required (set RADIO_BRIDGE_INPUT_DEVICE or [audio].input_device)"
                    .into(),
            );
        }
        if config.audio.output_device.is_empty() {
            return Err(
                "output_device is required (set RADIO_BRIDGE_OUTPUT_DEVICE or [audio].output_device)"
                    .into(),
            );
        }
        if config.gpio.ptt_pin == default_unset_ptt_pin() {
            return Err(
                "ptt_pin is required (set RADIO_BRIDGE_PTT_PIN or [gpio].ptt_pin)".into(),
            );
        }
        Ok(config)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    const SAMPLE: &str = r#"
[server]
server_addr = "http://game-server:50051"
bridge_id = "TOML_ID"
[gpio]
ptt_pin = 26
[audio]
output_device = "toml_out"
input_device = "toml_in"
input_threshold_rms = 1000
input_silence_ms = 2000
input_max_recording_secs = 30
[timing]
ptt_on_delay_ms = 300
ptt_off_delay_ms = 100
cooldown_ms = 1000
[queue]
max_audio_duration_secs = 40
max_queue_size = 10
"#;

    fn write_config(name: &str, body: &str) -> std::path::PathBuf {
        let path = std::env::temp_dir().join(format!("radio_bridge_cfg_{name}.toml"));
        let mut f = std::fs::File::create(&path).unwrap();
        f.write_all(body.as_bytes()).unwrap();
        path
    }

    fn clear_env() {
        for key in [
            "RADIO_BRIDGE_ID",
            "RADIO_BRIDGE_INPUT_DEVICE",
            "RADIO_BRIDGE_OUTPUT_DEVICE",
            "RADIO_BRIDGE_PTT_PIN",
        ] {
            std::env::remove_var(key);
        }
    }

    /// 環境変数はプロセス共有のため、1テストに集約して順に検証する
    /// (テスト並列実行による相互干渉を避ける)。
    #[test]
    fn env_overrides_and_validation() {
        let path = write_config("base", SAMPLE);

        // 環境変数なし: TOML の値が使われる
        clear_env();
        let config = Config::load(&path).unwrap();
        assert_eq!(config.server.bridge_id, "TOML_ID");
        assert_eq!(config.audio.input_device, "toml_in");
        assert_eq!(config.audio.output_device, "toml_out");
        assert_eq!(config.gpio.ptt_pin, 26);

        // 環境変数あり: 環境変数が優先される
        std::env::set_var("RADIO_BRIDGE_ID", "ENV_ID");
        std::env::set_var("RADIO_BRIDGE_INPUT_DEVICE", "env_in");
        std::env::set_var("RADIO_BRIDGE_OUTPUT_DEVICE", "env_out");
        std::env::set_var("RADIO_BRIDGE_PTT_PIN", "17");
        let config = Config::load(&path).unwrap();
        assert_eq!(config.server.bridge_id, "ENV_ID");
        assert_eq!(config.audio.input_device, "env_in");
        assert_eq!(config.audio.output_device, "env_out");
        assert_eq!(config.gpio.ptt_pin, 17);

        // 空の環境変数は無視され、TOML の値が残る
        std::env::set_var("RADIO_BRIDGE_INPUT_DEVICE", "");
        std::env::set_var("RADIO_BRIDGE_PTT_PIN", "");
        let config = Config::load(&path).unwrap();
        assert_eq!(config.audio.input_device, "toml_in");
        assert_eq!(config.gpio.ptt_pin, 26);

        // 数値としてパースできない環境変数はエラー
        std::env::set_var("RADIO_BRIDGE_PTT_PIN", "not_a_number");
        assert!(
            Config::load(&path).is_err(),
            "ptt_pin の環境変数が数値でなければエラーとするべき"
        );

        // TOML にデバイス・ピン指定が無い場合
        clear_env();
        let without_devices = SAMPLE
            .replace("output_device = \"toml_out\"\n", "")
            .replace("input_device = \"toml_in\"\n", "")
            .replace("ptt_pin = 26\n", "");
        let path = write_config("nodev", &without_devices);

        // 環境変数もTOMLも無ければ起動時エラー
        assert!(
            Config::load(&path).is_err(),
            "デバイス未設定は起動時にエラーとするべき"
        );

        // 環境変数だけで起動できる (config.toml を共有する運用)
        std::env::set_var("RADIO_BRIDGE_INPUT_DEVICE", "env_in");
        std::env::set_var("RADIO_BRIDGE_OUTPUT_DEVICE", "env_out");
        std::env::set_var("RADIO_BRIDGE_PTT_PIN", "27");
        let config = Config::load(&path).unwrap();
        assert_eq!(config.audio.input_device, "env_in");
        assert_eq!(config.audio.output_device, "env_out");
        assert_eq!(config.server.bridge_id, "TOML_ID");
        assert_eq!(config.gpio.ptt_pin, 27);

        // ptt_pin だけ環境変数が無い場合は起動時エラー
        std::env::remove_var("RADIO_BRIDGE_PTT_PIN");
        assert!(
            Config::load(&path).is_err(),
            "ptt_pin 未設定は起動時にエラーとするべき"
        );

        clear_env();
    }
}
