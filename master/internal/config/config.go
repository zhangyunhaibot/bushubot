package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Server      ServerConfig   `json:"server"`
	Database    DatabaseConfig `json:"database"`
	TelegramBot BotConfig      `json:"telegram_bot"`
	ReleaseRepo string         `json:"release_repo"`
	License     LicenseConfig  `json:"license"`
}

type LicenseConfig struct {
	KeypairDir  string `json:"keypair_dir"`
	DefaultDays int    `json:"default_days"`
}

type ServerConfig struct {
	Port int `json:"port"`
}

type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		d.Host, d.Port, d.User, d.Password, d.DBName)
}

type BotConfig struct {
	Token     string `json:"token"`
	AdminTGID int64  `json:"admin_tg_id"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8081
	}
	if c.License.KeypairDir == "" {
		c.License.KeypairDir = "./data/keys"
	}
	if c.License.DefaultDays == 0 {
		c.License.DefaultDays = 365
	}
	return &c, nil
}
