#ifndef MCP23017_H_
#define MCP23017_H_

#include <driver/i2c_master.h>
#include <freertos/FreeRTOS.h>

#include "logger.h"

class MCP23017 {
 public:
  static constexpr uint8_t GPIO_GROUP_A = 0;
  static constexpr uint8_t GPIO_GROUP_B = 1;
  static constexpr uint8_t GPIO_GROUP_NUM = GPIO_GROUP_B + 1;
  static constexpr uint8_t GPIO_MIN = 0;
  static constexpr uint8_t GPIO_MAX = 7;
  static constexpr uint8_t GPIO_NUM = GPIO_MAX + 1;

 private:
  /// GPIOA INPUT OUTPUT DIR (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_IODIRA = 0x00;
  /// GPIOA INPUT OUTPUT DIR (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_IODIRB = 0x01;
  /// GPINTENA INTERRUPT ON CHANGE ENABLE (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_GPINTENA = 0x04;
  /// GPINTENB INTERRUPT ON CHANGE ENABLE (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_GPINTENB = 0x05;
  /// INTCONA INTERRUPT CONTROL (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_INTCONA = 0x08;
  /// INTCONB INTERRUPT CONTROL (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_INTCONB = 0x09;
  /// IOCON CONFIGURATION (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_IOCON = 0x0a;
  /// GPPUA PULLUP (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_GPPUA = 0x0c;
  /// GPIOB PULLUP (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_GPPUB = 0x0d;
  /// INTFA INTERRUPT FLAG (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_INTFA = 0x0e;
  /// INTFB INTERRUPT FLAG (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_INTFB = 0x0f;
  // GPIOA (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_GPIOA = 0x12;
  // GPIOB (IOCON.BANK = 0)
  static constexpr uint8_t REGADDR_GPIOB = 0x13;

  /// IOCON.MIRROR : INTAとINTBを内部でOR結合し、1本のピンで通知する
  static constexpr uint8_t IOCON_MIRROR = 1 << 6;

  static constexpr uint8_t GPIO_REGADDR_IODIR_TBLE[GPIO_GROUP_NUM] = {
      REGADDR_IODIRA,
      REGADDR_IODIRB,
  };

  static constexpr uint8_t GPIO_REGADDR_GPINTEN_TBLE[GPIO_GROUP_NUM] = {
      REGADDR_GPINTENA,
      REGADDR_GPINTENB,
  };

  static constexpr uint8_t GPIO_REGADDR_INTCON_TBLE[GPIO_GROUP_NUM] = {
      REGADDR_INTCONA,
      REGADDR_INTCONB,
  };

  static constexpr uint8_t GPIO_REGADDR_GPPU_TBLE[GPIO_GROUP_NUM] = {
      REGADDR_GPPUA,
      REGADDR_GPPUB,
  };

  static constexpr uint8_t GPIO_REGADDR_GPIO_TBL[GPIO_GROUP_NUM] = {
      REGADDR_GPIOA,
      REGADDR_GPIOB,
  };

 public:
  class GPIO {
   public:
    GPIO() : is_input_(false), is_up_(false) {}

    bool is_input_;
    bool is_up_;
  };

  class GPIOGroup {
   public:
    GPIOGroup() : gpio_() {}

    uint8_t GetIsInputData() const {
      return (gpio_[0].is_input_ ? 1 : 0) | (gpio_[1].is_input_ ? 1 : 0) << 1 |
             (gpio_[2].is_input_ ? 1 : 0) << 2 |
             (gpio_[3].is_input_ ? 1 : 0) << 3 |
             (gpio_[4].is_input_ ? 1 : 0) << 4 |
             (gpio_[5].is_input_ ? 1 : 0) << 5 |
             (gpio_[6].is_input_ ? 1 : 0) << 6 |
             (gpio_[7].is_input_ ? 1 : 0) << 7;
    }

    uint8_t GetIsUpData() const {
      return (gpio_[0].is_up_ ? 1 : 0) | (gpio_[1].is_up_ ? 1 : 0) << 1 |
             (gpio_[2].is_up_ ? 1 : 0) << 2 | (gpio_[3].is_up_ ? 1 : 0) << 3 |
             (gpio_[4].is_up_ ? 1 : 0) << 4 | (gpio_[5].is_up_ ? 1 : 0) << 5 |
             (gpio_[6].is_up_ ? 1 : 0) << 6 | (gpio_[7].is_up_ ? 1 : 0) << 7;
    }

    void SetIsUp(uint8_t data) {
      gpio_[0].is_up_ = (data & 0x01) != 0;
      gpio_[1].is_up_ = (data >> 1 & 0x01) != 0;
      gpio_[2].is_up_ = (data >> 2 & 0x01) != 0;
      gpio_[3].is_up_ = (data >> 3 & 0x01) != 0;
      gpio_[4].is_up_ = (data >> 4 & 0x01) != 0;
      gpio_[5].is_up_ = (data >> 5 & 0x01) != 0;
      gpio_[6].is_up_ = (data >> 6 & 0x01) != 0;
      gpio_[7].is_up_ = (data >> 7 & 0x01) != 0;
    }

    GPIO gpio_[GPIO_NUM];
  };

 public:
  MCP23017() : dev_handle_(nullptr), gpio_group_() {}

  /// Input/Output設定 (Setup関数の前に呼ぶ必要があります)
  void SetInputOutput(uint8_t group_id, uint8_t gpio_no, bool is_input) {
    if (GPIO_GROUP_NUM <= group_id || GPIO_NUM <= gpio_no) {
      return;
    }
    gpio_group_[group_id].gpio_[gpio_no].is_input_ = is_input;
  }

  /// 初期化用関数
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

    // IOCON.MIRROR有効化 (INTA/INTBを内部でOR結合し、INTA1本にまとめる)
    WriteRegister(REGADDR_IOCON, IOCON_MIRROR);

    // Input/Output設定反映
    for (uint8_t group_id = GPIO_GROUP_A; group_id < GPIO_GROUP_NUM;
         ++group_id) {
      WriteRegister(GPIO_REGADDR_IODIR_TBLE[group_id],
                    gpio_group_[group_id].GetIsInputData());
    }

    // Input内部プルアップ有効化
    for (uint8_t group_id = GPIO_GROUP_A; group_id < GPIO_GROUP_NUM;
         ++group_id) {
      WriteRegister(GPIO_REGADDR_GPPU_TBLE[group_id],
                    gpio_group_[group_id].GetIsInputData());
    }

    // 割り込み設定 (Input設定したピンのみ前回値との比較で割り込み発生)
    for (uint8_t group_id = GPIO_GROUP_A; group_id < GPIO_GROUP_NUM;
         ++group_id) {
      WriteRegister(GPIO_REGADDR_INTCON_TBLE[group_id], 0x00);
      WriteRegister(GPIO_REGADDR_GPINTEN_TBLE[group_id],
                    gpio_group_[group_id].GetIsInputData());
    }

    // 割り込みフラグクリアのため、現在値を読み出しておく
    for (uint8_t group_id = GPIO_GROUP_A; group_id < GPIO_GROUP_NUM;
         ++group_id) {
      GetInputGpio(group_id, GPIO_MIN);
    }
  }

  /// GPIO出力初期化(ALL DOWN)
  void Clear() {
    for (int group = 0; group < GPIO_GROUP_NUM; ++group) {
      for (int gpio_no = 0; gpio_no < GPIO_NUM; ++gpio_no) {
        SetOutputGpio(group, gpio_no, false, true);
      }
    }
  }

  /// GPIO出力設定
  void SetOutputGpio(uint8_t group_id, uint8_t gpio_no, bool is_output,
                     bool is_force = false) {
    if (GPIO_GROUP_NUM <= group_id || GPIO_NUM <= gpio_no) {
      return;
    }

    const bool output = gpio_group_[group_id].gpio_[gpio_no].is_up_;
    if (output == is_output && !is_force) {
      return;
    }

    // ESP_LOGI(TAG, "Set GPIO group:%d gpio_no:%d output:%s",
    // static_cast<int>(group_id), static_cast<int>(gpio_no), is_output ? "1" :
    // "0");
    gpio_group_[group_id].gpio_[gpio_no].is_up_ = is_output;

    WriteRegister(GPIO_REGADDR_GPIO_TBL[group_id],
                  gpio_group_[group_id].GetIsUpData());
  }

  /// GPIO入力状況取得 (true:High) (グループ単位でI2Cを読み直す)
  bool GetInputGpio(uint8_t group_id, uint8_t gpio_no) {
    if (GPIO_GROUP_NUM <= group_id || GPIO_NUM <= gpio_no) {
      return false;
    }

    RefreshInputGroup(group_id);

    return gpio_group_[group_id].gpio_[gpio_no].is_up_ != 0;
  }

  /// GPIOグループ単位でI2Cを1回だけ読み直してキャッシュを更新する
  void RefreshInputGroup(uint8_t group_id) {
    if (GPIO_GROUP_NUM <= group_id) {
      return;
    }

    const uint8_t buffer = ReadRegister(GPIO_REGADDR_GPIO_TBL[group_id]);
    gpio_group_[group_id].SetIsUp(buffer);
  }

  /// キャッシュ済みの入力状況を取得 (true:High) (I2Cは読みに行かない)
  bool GetCachedInputGpio(uint8_t group_id, uint8_t gpio_no) const {
    if (GPIO_GROUP_NUM <= group_id || GPIO_NUM <= gpio_no) {
      return false;
    }
    return gpio_group_[group_id].gpio_[gpio_no].is_up_ != 0;
  }

 private:
  void WriteRegister(uint8_t reg_addr, uint8_t data) {
    const uint8_t write_buffer[2] = {reg_addr, data};
    const esp_err_t err = i2c_master_transmit(
        dev_handle_, write_buffer, sizeof(write_buffer),
        1000 / portTICK_PERIOD_MS);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "WriteRegister failed reg:0x%02x err:%s", reg_addr,
               esp_err_to_name(err));
    }
  }

  uint8_t ReadRegister(uint8_t reg_addr) {
    uint8_t buffer = 0;
    const esp_err_t err = i2c_master_transmit_receive(
        dev_handle_, &reg_addr, sizeof(reg_addr), &buffer, sizeof(buffer),
        1000 / portTICK_PERIOD_MS);
    if (err != ESP_OK) {
      ESP_LOGE(TAG, "ReadRegister failed reg:0x%02x err:%s", reg_addr,
               esp_err_to_name(err));
    }
    return buffer;
  }

 private:
  i2c_master_dev_handle_t dev_handle_;
  GPIOGroup gpio_group_[GPIO_GROUP_NUM];
};

#endif  // MCP23017_H_

// EOF
