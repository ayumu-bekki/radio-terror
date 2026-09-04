// Core System
// (C)2026 bekki.jp

// Include ----------------------
#include "core_system.h"
#include "startup_banner.h"


/// Entry Point
extern "C" void app_main() {
  // 何より先に出す。以降の初期化で失敗しても、どのビルド・どの設定の
  // 個体だったかがログに残る
  CoreSystem::StartupBanner::Log();

  const auto system = std::make_shared<CoreSystem::System>();
  system->Start();
}

// EOF

