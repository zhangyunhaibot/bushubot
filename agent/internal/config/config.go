package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	MasterURL                string `json:"master_url"`
	LicenseKey               string `json:"license_key"`
	BotToken                 string `json:"bot_token"`
	OwnerTGID                int64  `json:"owner_tg_id"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	GraceDaysOffline         int    `json:"grace_days_offline"`
	AppName                  string `json:"app_name"`
	AppDir                   string `json:"app_dir"`
	BinaryPath               string `json:"binary_path"`
	ServiceName              string `json:"service_name"`
	VersionFile              string `json:"version_file"` // 默认 <AppDir>/backend/VERSION
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if c.LicenseKey == "" || c.BotToken == "" || c.MasterURL == "" {
		return nil, fmt.Errorf("master_url / license_key / bot_token 必填")
	}
	if c.HeartbeatIntervalSeconds <= 0 {
		c.HeartbeatIntervalSeconds = 60
	}
	if c.GraceDaysOffline <= 0 {
		c.GraceDaysOffline = 7
	}
	if c.VersionFile == "" && c.AppDir != "" {
		c.VersionFile = c.AppDir + "/backend/VERSION"
	}
	return &c, nil
}
