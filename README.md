RADIO TERROR

## ドキュメント

- [ゲームセッション設計 (Core)](docs/game_session_design.md)
- [radio-bridge 接続設計](docs/bridge_connection_design.md)
- [体験運用フロー](docs/operation_flow.md)
- [謎解きステージ集](docs/puzzle_stage_ideas.md)
- [ナビゲーター設計](docs/navigator_design.md)
- [シナリオテンプレート設計](docs/scenario_design.md)

## 実装ロードマップ

設計は確定済み(2026-08-08)。実装の推奨順序:

1. **Coreファーム** (`app/core_system`) — `game_session_design.md` §11 の手順
   (game_session のJSONパース・検証 → GameTask状態機械 → 入力・演出)
2. **wl-game-server** — `bridge_connection_design.md` §7(gRPC反転+レジストリ)
   → デバイス用WSメッセージ対応 → `scenario_design.md`(テンプレート・Valkey)
   → ナビゲーター(`navigator_design.md`) → マネージャー向けWeb画面
3. **radio-bridge / emulator** — クライアント化+IDメタデータ
   (`bridge_connection_design.md` §6-7)
4. **コンテンツ制作** — 紙資料(`puzzle_stage_ideas.md` §6)・
   混線音声アセット(`operation_flow.md` §5.1)・効果音
