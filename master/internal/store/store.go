package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"bushubot-master/internal/model"

	"gorm.io/gorm"
)

type Store struct{ DB *gorm.DB }

func New(db *gorm.DB) *Store { return &Store{DB: db} }

// ---------- customers ----------

func (s *Store) CreateCustomer(name string, tgUserID int64, botToken, note string) (*model.Customer, error) {
	if existing, _ := s.FindByName(name); existing != nil {
		return nil, fmt.Errorf("客户名 %q 已存在（ID=%d）", name, existing.ID)
	}
	licenseKey, err := genLicenseKey()
	if err != nil {
		return nil, err
	}
	c := &model.Customer{
		Name:       name,
		TGUserID:   tgUserID,
		BotToken:   botToken,
		LicenseKey: licenseKey,
		Enabled:    true,
		Note:       note,
	}
	if err := s.DB.Create(c).Error; err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) FindByLicense(licenseKey string) (*model.Customer, error) {
	var c model.Customer
	err := s.DB.Where("license_key = ?", licenseKey).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &c, err
}

func (s *Store) FindByID(id uint) (*model.Customer, error) {
	var c model.Customer
	err := s.DB.First(&c, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &c, err
}

func (s *Store) FindByName(name string) (*model.Customer, error) {
	var c model.Customer
	err := s.DB.Where("name = ?", name).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &c, err
}

func (s *Store) ListCustomers() ([]model.Customer, error) {
	var list []model.Customer
	err := s.DB.Order("id ASC").Find(&list).Error
	return list, err
}

// HeartbeatUpdate 心跳带回的所有字段（一次更新）
type HeartbeatUpdate struct {
	CurrentVersion string
	AgentVersion   string
	ServerIP       string
	MemUsedMB      int
	MemTotalMB     int
	DiskUsedGB     int
	DiskTotalGB    int
	Load1m         float64
	CPUCount       int
	UptimeSeconds  int64
}

func (s *Store) UpdateHeartbeat(id uint, h HeartbeatUpdate) error {
	now := time.Now()
	updates := map[string]any{
		"current_version":   h.CurrentVersion,
		"server_ip":         h.ServerIP,
		"last_heartbeat_at": now,
	}
	if h.AgentVersion != "" {
		updates["agent_version"] = h.AgentVersion
	}
	if h.MemTotalMB > 0 {
		updates["mem_used_mb"] = h.MemUsedMB
		updates["mem_total_mb"] = h.MemTotalMB
	}
	if h.DiskTotalGB > 0 {
		updates["disk_used_gb"] = h.DiskUsedGB
		updates["disk_total_gb"] = h.DiskTotalGB
	}
	if h.CPUCount > 0 {
		updates["cpu_count"] = h.CPUCount
	}
	if h.Load1m > 0 {
		updates["load_1m"] = h.Load1m
	}
	if h.UptimeSeconds > 0 {
		updates["uptime_seconds"] = h.UptimeSeconds
	}
	return s.DB.Model(&model.Customer{}).Where("id = ?", id).Updates(updates).Error
}

func (s *Store) SetEnabled(id uint, enabled bool) error {
	return s.DB.Model(&model.Customer{}).Where("id = ?", id).
		Update("enabled", enabled).Error
}

// SetLicenseExpires 写入 license 到期时间, 在签发 / 重签 token 后调用,
// 让客户详情页能展示倒计时。token 本身不入库（防泄漏）, 只存 expires。
func (s *Store) SetLicenseExpires(id uint, expires time.Time) error {
	return s.DB.Model(&model.Customer{}).Where("id = ?", id).
		Update("license_expires_at", expires).Error
}

// ---------- releases ----------

func (s *Store) GetLatestRelease() (*model.Release, error) {
	var r model.Release
	err := s.DB.Where("is_latest = ?", true).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &r, err
}

func (s *Store) PublishRelease(version, notes string) (*model.Release, error) {
	var r *model.Release
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Release{}).Where("is_latest = ?", true).
			Update("is_latest", false).Error; err != nil {
			return err
		}
		rec := &model.Release{Version: version, Notes: notes, IsLatest: true, PublishedAt: time.Now()}
		// upsert：如果版本已存在就更新
		if err := tx.Where("version = ?", version).Assign(map[string]any{
			"notes":     notes,
			"is_latest": true,
		}).FirstOrCreate(rec).Error; err != nil {
			return err
		}
		r = rec
		return nil
	})
	return r, err
}

// ---------- events / notifications ----------

func (s *Store) RecordEvent(customerID uint, eventType, version, errMsg string) error {
	return s.DB.Create(&model.AgentEvent{
		CustomerID:   customerID,
		EventType:    eventType,
		Version:      version,
		ErrorMessage: errMsg,
	}).Error
}

func (s *Store) PendingNotifications(customerID uint) ([]model.Notification, error) {
	var list []model.Notification
	err := s.DB.Where("customer_id = ? AND delivered = false", customerID).
		Order("id ASC").Find(&list).Error
	return list, err
}

// MarkAttempted 拉取时调用：attempts++ 并记录时间。
// 不立刻 deliver，等 agent 执行成功后调 /ack 才算 delivered。
// 但 attempts >= maxAttempts 时强制 deliver，避免 stuck（兼容老 agent）
func (s *Store) MarkAttempted(ids []uint, maxAttempts int) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	if err := s.DB.Model(&model.Notification{}).Where("id IN ?", ids).
		Updates(map[string]any{
			"attempts":        gorm.Expr("attempts + 1"),
			"last_attempt_at": now,
		}).Error; err != nil {
		return err
	}
	// 兜底：超过 maxAttempts 还没 ack，强制 deliver 防止无限重发
	return s.DB.Model(&model.Notification{}).
		Where("id IN ? AND attempts >= ?", ids, maxAttempts).
		Update("delivered", true).Error
}

// MarkDeliveredOne agent 执行成功后调 /ack 触发，按 ID 标 delivered
// 必须带 customerID 做跨租户校验，避免客户 A 标记客户 B 的指令
func (s *Store) MarkDeliveredOne(id uint, customerID uint) error {
	return s.DB.Model(&model.Notification{}).Where("id = ? AND customer_id = ?", id, customerID).
		Update("delivered", true).Error
}

// 保留 MarkDelivered 给广播兜底使用
func (s *Store) MarkDelivered(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return s.DB.Model(&model.Notification{}).Where("id IN ?", ids).
		Update("delivered", true).Error
}

func (s *Store) BroadcastNotification(typ, version, message string) error {
	customers, err := s.ListCustomers()
	if err != nil {
		return err
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		for _, c := range customers {
			if !c.Enabled {
				continue
			}
			if err := tx.Create(&model.Notification{
				CustomerID: c.ID, Type: typ, Version: version, Message: message,
			}).Error; err != nil {
				return fmt.Errorf("给客户 %s 排队通知失败: %w", c.Name, err)
			}
		}
		return nil
	})
}

// BroadcastWithRecord 既向所有启用客户分发 manual 通知，也在 broadcasts 表写一条历史记录。
// 整体放在事务里，要么全成功要么全回滚（避免"历史显示已推送 N 人但通知未入队"的不一致）。
func (s *Store) BroadcastWithRecord(template, title, content, sentBy string) (*model.Broadcast, error) {
	customers, err := s.ListCustomers()
	if err != nil {
		return nil, err
	}
	enabled := 0
	for _, c := range customers {
		if c.Enabled {
			enabled++
		}
	}

	bc := &model.Broadcast{
		Template:    template,
		Title:       title,
		Content:     content,
		TargetCount: enabled,
		SentBy:      sentBy,
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(bc).Error; err != nil {
			return err
		}
		for _, c := range customers {
			if !c.Enabled {
				continue
			}
			if err := tx.Create(&model.Notification{
				CustomerID: c.ID, Type: "manual", Message: content,
			}).Error; err != nil {
				return fmt.Errorf("给客户 %s 排队通知失败: %w", c.Name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bc, nil
}

// ---------- metrics_snapshots ----------

// SaveSnapshot 写一条指标快照。
// 旧数据清理由 main 启动的定时 goroutine（CleanupOldMetrics）负责。
func (s *Store) SaveSnapshot(snap *model.MetricsSnapshot) error {
	if snap.SnapshotAt.IsZero() {
		snap.SnapshotAt = time.Now()
	}
	return s.DB.Create(snap).Error
}

// CleanupOldMetrics 删除 30 天前的快照。由后台 goroutine 每天调一次。
func (s *Store) CleanupOldMetrics() (int64, error) {
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	result := s.DB.Where("snapshot_at < ?", cutoff).Delete(&model.MetricsSnapshot{})
	return result.RowsAffected, result.Error
}

// GetSnapshots 按时间倒序拉客户的快照（限制行数避免一次拿太多）
func (s *Store) GetSnapshots(customerID uint, since time.Time, limit int) ([]model.MetricsSnapshot, error) {
	var list []model.MetricsSnapshot
	q := s.DB.Where("customer_id = ? AND snapshot_at >= ?", customerID, since).
		Order("snapshot_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&list).Error
	return list, err
}

// ---------- agent_logs ----------

func (s *Store) SaveAgentLog(customerID uint, service, content string) (*model.AgentLog, error) {
	l := &model.AgentLog{
		CustomerID: customerID,
		Service:    service,
		Content:    content,
		Bytes:      len(content),
	}
	if err := s.DB.Create(l).Error; err != nil {
		return nil, err
	}
	// 只保留每个客户最近 20 份日志
	var ids []uint
	s.DB.Model(&model.AgentLog{}).
		Where("customer_id = ?", customerID).
		Order("received_at DESC").
		Offset(20).
		Pluck("id", &ids)
	if len(ids) > 0 {
		s.DB.Where("id IN ?", ids).Delete(&model.AgentLog{})
	}
	return l, nil
}

// ---------- broadcasts ----------

func (s *Store) RecentBroadcasts(limit int) ([]model.Broadcast, error) {
	var list []model.Broadcast
	err := s.DB.Order("created_at DESC").Limit(limit).Find(&list).Error
	return list, err
}

// ---------- 告警相关 ----------

// PendingFailureEvents 找最近 since 时间内、未告警的 update_failed/agent_self_update_failed/restart_failed 事件
func (s *Store) PendingFailureEvents(since time.Time) ([]model.AgentEvent, error) {
	var events []model.AgentEvent
	err := s.DB.Where(
		"alerted = ? AND created_at >= ? AND event_type IN ?",
		false, since,
		[]string{"update_failed", "agent_self_update_failed", "restart_failed"},
	).Order("created_at ASC").Find(&events).Error
	return events, err
}

// MarkEventsAlerted 标记一批事件为已告警，避免重复
func (s *Store) MarkEventsAlerted(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return s.DB.Model(&model.AgentEvent{}).Where("id IN ?", ids).
		Update("alerted", true).Error
}

func (s *Store) NotifyCustomer(customerID uint, typ, message string) error {
	return s.DB.Create(&model.Notification{
		CustomerID: customerID, Type: typ, Message: message,
	}).Error
}

// ---------- settings ----------

func (s *Store) GetSetting(key string) (string, error) {
	var st model.Setting
	err := s.DB.Where("key = ?", key).First(&st).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return st.Value, err
}

func (s *Store) SetSetting(key, value string) error {
	st := model.Setting{Key: key, Value: value, UpdatedAt: time.Now()}
	return s.DB.Save(&st).Error
}

// ---------- helpers ----------

func genLicenseKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
