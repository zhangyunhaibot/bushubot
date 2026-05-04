package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	MasterURL string `json:"master_url"`
	LicenseKey string `json:"license_key"`
	BotToken   string `json:"bot_token"`
	OwnerTGID  int64  `json:"owner_tg_id"`
	// HeartbeatIntervalSeconds 健康心跳间隔 (上报 cpu/内存/磁盘等指标), 默认 1200s (20 min)
	HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds"`
	// CommandPullIntervalSeconds 命令拉取间隔 (拉 notifications: 重启 / 强制升级 / 抓日志等), 默认 60s
	// 心跳频率拉低后, 用这个独立频率保证管理员的远程操作仍然 ≤60s 生效
	CommandPullIntervalSeconds int    `json:"command_pull_interval_seconds"`
	GraceDaysOffline           int    `json:"grace_days_offline"`
	AppName                    string `json:"app_name"`
	AppDir                     string `json:"app_dir"`
	BinaryPath                 string `json:"binary_path"`
	ServiceName                string `json:"service_name"`
	VersionFile                string `json:"version_file"` // 默认 <AppDir>/backend/VERSION
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
		c.HeartbeatIntervalSeconds = 1200 // 20 min, 上报指标用
	}
	if c.CommandPullIntervalSeconds <= 0 {
		c.CommandPullIntervalSeconds = 60 // 1 min, 拉远程命令用
	}
	if c.GraceDaysOffline <= 0 {
		c.GraceDaysOffline = 7
	}
	if c.VersionFile == "" && c.AppDir != "" {
		c.VersionFile = c.AppDir + "/backend/VERSION"
	}
	return &c, nil
}
