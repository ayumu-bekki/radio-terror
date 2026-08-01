// Danger Gadget
// (C)2026 bekki.jp

// Include ----------------------
#include "danger_gadget_system.h"


/// Entry Point
extern "C" void app_main() {
  const auto system = std::make_shared<DangerGadget::DangerGadgetSystem>();
  system->Start();
}

// EOF

