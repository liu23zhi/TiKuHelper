package registry

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/spf13/viper"
	"io/fs"
)

var defaultConfig = []byte(`
jwtSecret: ""
limit:
  enable: true
  duration: 60
  requests: 120
`)

// Config get config instance
func Config() configs.Config {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	// 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		logger.SysLog(fmt.Sprintf("无法读取配置文件: %v,将以默认配置启动", err))
		_ = viper.ReadConfig(bytes.NewBuffer(defaultConfig))
	}

	var config configs.Config
	err := viper.Unmarshal(&config)
	if err != nil {
		logger.SysError(fmt.Sprintf("配置文件语法错误: %v", err))
	}
	return config
}

// ConfigWithError keeps existing Viper setup and override/env precedence, but never
// lets a read or decode error escape as a partial/stale configuration.
func ConfigWithError() (configs.Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	return readConfigWithError(viper.GetViper(), defaultConfig)
}

func readConfigWithError(v *viper.Viper, defaults []byte) (configs.Config, error) {
	if err := v.ReadInConfig(); err != nil {
		var missing viper.ConfigFileNotFoundError
		if !errors.As(err, &missing) && !errors.Is(err, fs.ErrNotExist) {
			return configs.Config{}, err
		}
		if err := v.ReadConfig(bytes.NewReader(defaults)); err != nil {
			return configs.Config{}, err
		}
	}
	var config configs.Config
	if err := v.Unmarshal(&config); err != nil {
		return configs.Config{}, err
	}
	return config, nil
}
