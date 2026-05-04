package model

import "time"

type Customer struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	Name            string     `gorm:"size:100;not null" json:"name"`
	TGUserID        int64      `gorm:"column:tg_user_id;not null" json:"tg_user_id"`
	BotToken        string     `gorm:"size:100;not null" json:"-"` // 不下发
	LicenseKey      string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	ServerIP        string     `gorm:"size:45" json:"server_ip"`
	CurrentVersion  string     `gorm:"size:32" json:"current_version"`
	AgentVersion    string     `gorm:"size:32" json:"agent_version"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	Enabled         bool       `gorm:"default:true" json:"enabled"`
	Note            string     `json:"note"`
	CloudProvider   string     `gorm:"size:32" json:"cloud_provider,omitempty"`
	CloudAccountRef string     `gorm:"size:64" json:"cloud_account_ref,omitempty"`

	// 资源指标（每次心跳更新最新值）
	MemUsedMB     int     `json:"mem_used_mb,omitempty"`
	MemTotalMB    int     `json:"mem_total_mb,omitempty"`
	DiskUsedGB    int     `json:"disk_used_gb,omitempty"`
	DiskTotalGB   int     `json:"disk_total_gb,omitempty"`
	Load1m        float64 `gorm:"column:load_1m;type:decimal(6,2)" json:"load_1m,omitempty"`
	CPUCount      int     `json:"cpu_count,omitempty"`
	UptimeSeconds int64   `json:"uptime_seconds,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Customer) TableName() string { return "customers" }

type Release struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Version     string    `gorm:"size:32;uniqueIndex;not null" json:"version"`
	Notes       string    `json:"notes"`
	IsLatest    bool      `gorm:"default:false" json:"is_latest"`
	PublishedAt time.Time `json:"published_at"`
}

func (Release) TableName() string { return "releases" }

type AgentEvent struct {
	ID           uint      `gorm:"primaryKey"`
	CustomerID   uint      `gorm:"index"`
	EventType    string    `gorm:"size:32"`
	Version      string    `gorm:"size:32"`
	ErrorMessage string
	CreatedAt    time.Time
}

func (AgentEvent) TableName() string { return "agent_events" }

type Notification struct {
	ID            uint   `gorm:"primaryKey"`
	CustomerID    uint   `gorm:"index"`
	Type          string `gorm:"size:32"`
	Version       string `gorm:"size:32"`
	Message       string
	Delivered     bool `gorm:"default:false"`
	Attempts      int  `gorm:"not null;default:0"`
	LastAttemptAt *time.Time
	CreatedAt     time.Time
}

func (Notification) TableName() string { return "notifications" }

type Setting struct {
	Key       string    `gorm:"primaryKey;size:64"`
	Value     string    `gorm:"not null"`
	UpdatedAt time.Time
}

func (Setting) TableName() string { return "settings" }

type MetricsSnapshot struct {
	ID          uint64    `gorm:"primaryKey"`
	CustomerID  uint      `gorm:"index"`
	MemUsedMB   int
	MemTotalMB  int
	DiskUsedGB  int
	DiskTotalGB int
	Load1m      float64 `gorm:"column:load_1m;type:decimal(6,2)"`
	CPUCount    int
	SnapshotAt  time.Time
}

func (MetricsSnapshot) TableName() string { return "metrics_snapshots" }

type AgentLog struct {
	ID         uint      `gorm:"primaryKey"`
	CustomerID uint      `gorm:"index"`
	Service    string    `gorm:"size:64"`
	Content    string    `gorm:"not null"`
	Bytes      int
	ReceivedAt time.Time `gorm:"default:now()"`
}

func (AgentLog) TableName() string { return "agent_logs" }

type Broadcast struct {
	ID          uint      `gorm:"primaryKey"`
	Template    string    `gorm:"size:64;not null"` // maintenance / maintenance_done / version_preview / upgrade_failed / custom / auto_alert
	Title       string    `gorm:"size:200"`
	Content     string    `gorm:"not null"`
	TargetCount int       `gorm:"not null;default:0"`
	SentBy      string    `gorm:"size:32;default:admin"`
	Note        string
	CreatedAt   time.Time
}

func (Broadcast) TableName() string { return "broadcasts" }

const (
	BroadcastSentByAdmin   = "admin"
	BroadcastSentByAlerter = "system_alerter"

	BroadcastTplMaintenance     = "maintenance"
	BroadcastTplMaintenanceDone = "maintenance_done"
	BroadcastTplVersionPreview  = "version_preview"
	BroadcastTplUpgradeFailed   = "upgrade_failed"
	BroadcastTplCustom          = "custom"
	BroadcastTplAutoAlert       = "auto_alert"
)

const (
	SettingLatestAgentVersion       = "latest_agent_version"
	SettingMinSupportedAgentVersion = "min_supported_agent_version"
	SettingAgentReleaseRepo         = "agent_release_repo"
)
