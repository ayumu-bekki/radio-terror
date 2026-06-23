idf_component_register(SRCS "main.cc"
                            "task.cc"
                            "i2c_util.cc"
                            "wifi_util.cc"
                            "danger_gadget_system.cc"
                            "ws_client.cc"
                    INCLUDE_DIRS ""
                    REQUIRES driver esp_event esp_netif esp_wifi nvs_flash
                            esp_driver_gptimer esp_driver_gpio esp_driver_i2c)
