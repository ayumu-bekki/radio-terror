fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::configure()
        .build_server(false)
        .build_client(true)
        // RPC 名が `Connect` のため、生成コードに含まれる transport 用の
        // `TransceiverServiceClient::connect(dst)` コンストラクタ (tonic の
        // 組み込み機能) と RPC メソッド `connect` が衝突する (E0592)。
        // client.rs は Endpoint を自前で組み立てて `::new(channel)` で
        // クライアントを作るため、この組み込みコンストラクタは不要。
        .build_transport(false)
        .compile_protos(
            &["../proto/transceiver.proto"],
            &["../proto"],
        )?;
    Ok(())
}
