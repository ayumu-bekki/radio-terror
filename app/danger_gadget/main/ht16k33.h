#ifndef HT16K33_H_
#define HT16K33_H_

#include <driver/i2c_master.h>
#include <freertos/FreeRTOS.h>

#include "logger.h"

class HT16K33 {
 public:
  /// 表示RAMの行数 (16bit x 8行 = 16byte)
  static constexpr uint8_t ROW_NUM = 8;
  static constexpr uint8_t ROW_MAX = ROW_NUM - 1;

  /// 7セグメント表示桁数 (4桁表示器を想定)
  static constexpr uint8_t DIGIT_NUM = 4;

  static constexpr uint8_t BLINK_OFF = 0;
  static constexpr uint8_t BLINK_2HZ = 1;
  static constexpr uint8_t BLINK_1HZ = 2;
  static constexpr uint8_t BLINK_HALFHZ = 3;

  static constexpr uint8_t BRIGHTNESS_MIN = 0x00;
  static constexpr uint8_t BRIGHTNESS_MAX = 0x0F;

 private:
  /// System Setup: bit0 = oscillator on(1)/standby(0)
  static constexpr uint8_t CMD_SYSTEM_SETUP = 0x20;
  static constexpr uint8_t SYSTEM_SETUP_OSC_ON = 0x01;

  /// Display Setup: bit0 = display on(1)/off(0), bit1-2 = blink rate
  static constexpr uint8_t CMD_DISPLAY_SETUP = 0x80;
  static constexpr uint8_t DISPLAY_SETUP_ON = 0x01;
  static constexpr uint8_t DISPLAY_SETUP_BLINK_SHIFT = 1;

  /// Dimming (Brightness): bit0-3 = duty (0-15)
  static constexpr uint8_t CMD_DIMMING = 0xE0;

  /// 表示RAM先頭アドレスポインタ (以降連続書き込みでオートインクリメント)
  static constexpr uint8_t CMD_DISPLAY_ADDRESS = 0x00;

  /// 4桁7セグメント表示器の桁(digit)→表示RAM行(row)マッピング
  /// (row2はコロン用のため、digit2/3はrow3/4を使用する)
  static constexpr uint8_t DIGIT_ROW_TBL[DIGIT_NUM] = {0, 1, 3, 4};

  /// コロン表示用の表示RAM行
  static constexpr uint8_t COLON_ROW = 2;

  /// 0-15の数字を7セグメントパターンに変換するテーブル
  /// (Adafruit_LEDBackpackのnumbertable相当。bit0-6がセグメントa-g、bit7はDP)
  static constexpr uint8_t NUMBER_TABLE[16] = {
      0b00111111,  // 0
      0b00000110,  // 1
      0b01011011,  // 2
      0b01001111,  // 3
      0b01100110,  // 4
      0b01101101,  // 5
      0b01111101,  // 6
      0b00000111,  // 7
      0b01111111,  // 8
      0b01101111,  // 9
      0b01110111,  // A
      0b01111100,  // B
      0b00111001,  // C
      0b01011110,  // D
      0b01111001,  // E
      0b01110001,  // F
  };

 public:
  HT16K33()
      : dev_handle_(nullptr),
        display_buffer_(),
        is_display_on_(true),
        blink_rate_(BLINK_OFF),
        brightness_(BRIGHTNESS_MAX) {}

  /// 初期化用関数 (発振器ON, 表示ON, 最大輝度をデフォルト設定する)
  void Setup(i2c_master_bus_handle_t bus_handle, uint8_t address) {
    constexpr uint32_t I2C_MASTER_FREQ_HZ = 100000;

    i2c_device_config_t dev_config = {};
    dev_config.dev_addr_length = I2C_ADDR_BIT_LEN_7;
    dev_config.device_address = address;
    dev_config.scl_speed_hz = I2C_MASTER_FREQ_HZ;

    const esp_err_t err =
        i2c_master_bus_add_device(bus_handle, &dev_config, &dev_handle_);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "i2c_master_bus_add_device failed err:%s",
               esp_err_to_name(err));
    }

    // 発振器ON
    WriteCommand(CMD_SYSTEM_SETUP | SYSTEM_SETUP_OSC_ON);

    // 表示ON, ブリンクなし
    is_display_on_ = true;
    blink_rate_ = BLINK_OFF;
    WriteDisplaySetup();

    // 輝度最大
    brightness_ = BRIGHTNESS_MAX;
    WriteDimming();

    // 表示RAMクリアして転送
    Clear();
    WriteDisplay();
  }

  /// 表示ON/OFF設定
  void SetDisplayOn(bool is_on) {
    is_display_on_ = is_on;
    WriteDisplaySetup();
  }

  /// ブリンクレート設定 (BLINK_OFF/BLINK_2HZ/BLINK_1HZ/BLINK_HALFHZ)
  void SetBlinkRate(uint8_t blink_rate) {
    blink_rate_ = blink_rate & 0x03;
    WriteDisplaySetup();
  }

  /// 輝度設定 (0-15)
  void SetBrightness(uint8_t brightness) {
    brightness_ = brightness & BRIGHTNESS_MAX;
    WriteDimming();
  }

  /// 表示バッファ全体をクリア (I2C送信はしない。WriteDisplay()で反映する)
  void Clear() {
    for (uint8_t row = 0; row < ROW_NUM; ++row) {
      display_buffer_[row] = 0;
    }
  }

  /// 指定行の表示データをバッファにセット (I2C送信はしない。WriteDisplay()で反映する)
  void SetDisplayRowData(uint8_t row, uint16_t data) {
    if (ROW_NUM <= row) {
      return;
    }
    display_buffer_[row] = data;
  }

  /// 指定行の表示データをバッファから取得
  uint16_t GetDisplayRowData(uint8_t row) const {
    if (ROW_NUM <= row) {
      return 0;
    }
    return display_buffer_[row];
  }

  /// 指定桁に生のセグメントビットマスクを設定 (bit0-6:セグメントa-g, bit7:DP)
  /// (I2C送信はしない。WriteDisplay()で反映する)
  void WriteDigitRaw(uint8_t digit, uint8_t bitmask) {
    if (DIGIT_NUM <= digit) {
      return;
    }
    display_buffer_[DIGIT_ROW_TBL[digit]] = bitmask;
  }

  /// 指定桁に0-15の数字を7セグメントパターンとして設定 (dot:小数点)
  /// (I2C送信はしない。WriteDisplay()で反映する)
  void WriteDigitNum(uint8_t digit, uint8_t number, bool dot = false) {
    if (DIGIT_NUM <= digit || 16 <= number) {
      return;
    }
    WriteDigitRaw(digit, NUMBER_TABLE[number] | (dot ? 0x80 : 0x00));
  }

  /// 桁間のコロンLEDをON/OFF (I2C送信はしない。WriteDisplay()で反映する)
  void DrawColon(bool state) {
    display_buffer_[COLON_ROW] = state ? 0x02 : 0x00;
  }

  /// コロン部の生ビットマスク設定 (I2C送信はしない。WriteDisplay()で反映する)
  void WriteColonRaw(uint8_t bitmask) { display_buffer_[COLON_ROW] = bitmask; }

  /// 表示バッファ全体(16byte)を一括でI2C送信し、表示RAMへ反映する
  void WriteDisplay() {
    uint8_t write_buffer[1 + ROW_NUM * 2];
    write_buffer[0] = CMD_DISPLAY_ADDRESS;
    for (uint8_t row = 0; row < ROW_NUM; ++row) {
      write_buffer[1 + row * 2] =
          static_cast<uint8_t>(display_buffer_[row] & 0xFF);
      write_buffer[1 + row * 2 + 1] =
          static_cast<uint8_t>(display_buffer_[row] >> 8);
    }

    const esp_err_t err = i2c_master_transmit(
        dev_handle_, write_buffer, sizeof(write_buffer),
        1000 / portTICK_PERIOD_MS);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "WriteDisplay failed err:%s", esp_err_to_name(err));
    }
  }

 private:
  /// コマンドバイト1つだけを送信する (システムセットアップ/表示セットアップ/輝度設定用)
  void WriteCommand(uint8_t command) {
    const esp_err_t err = i2c_master_transmit(
        dev_handle_, &command, sizeof(command), 1000 / portTICK_PERIOD_MS);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "WriteCommand failed cmd:0x%02x err:%s", command,
               esp_err_to_name(err));
    }
  }

  void WriteDisplaySetup() {
    const uint8_t command = CMD_DISPLAY_SETUP |
                             (is_display_on_ ? DISPLAY_SETUP_ON : 0) |
                             (blink_rate_ << DISPLAY_SETUP_BLINK_SHIFT);
    WriteCommand(command);
  }

  void WriteDimming() { WriteCommand(CMD_DIMMING | brightness_); }

 private:
  i2c_master_dev_handle_t dev_handle_;
  uint16_t display_buffer_[ROW_NUM];
  bool is_display_on_;
  uint8_t blink_rate_;
  uint8_t brightness_;
};

#endif  // HT16K33_H_

// EOF
