#ifndef LOGGER_H_
#define LOGGER_H_

// Include ----------------------
#include <esp_log.h>

// systen Loglevel Redefine
#undef LOG_LOCAL_LEVEL
#define LOG_LOCAL_LEVEL ESP_LOG_VERBOSE

/// Application Log Tag
static constexpr char TAG[] = "BusyBoard";

#endif  // LOGGER_H_
