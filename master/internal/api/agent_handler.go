package api

import (
	"crypto/rsa"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"bushubot-master/internal/license"
	"bushubot-master/internal/model"
	"bushubot-master/internal/store"

	"github.com/gin-gonic/gin"
)

const agentBinaryName = "bushubot-agent"

// validReportEvents 是 /report 允许的 event_type 白名单
var validReportEvents = map[string]bool{
	"update_started":            true,
	"update_done":               true,
	"update_failed":             true,
	"agent_self_update_started": true,
	"agent_self_update_done":    true,
	"agent_self_update_failed":  true,
	"restart_done":              true,
	"restart_failed":            true,
}

type LogNotifier interface {
	NotifyAdmin(text string) error
	SendLogDocument(title string, content []byte) error
}

type AgentHandler struct {
	store       *store.Store
	releaseRepo string         // zhangyunhaibot/TGfulibot-releases
	pub         *rsa.PublicKey // 用来验证 agent 带来的 token
	notifier    LogNotifier    // 收到日志后转发给 admin

	snapMu       sync.Mutex
	lastSnapshot map[uint]time.Time // customer_id → 最后一次写快照时间
}

func NewAgent(s *store.Store, releaseRepo string, pub *rsa.PublicKey, n LogNotifier) *AgentHandler {
	h := &AgentHandler{
		store: s, releaseRepo: releaseRepo, pub: pub, notifier: n,
		lastSnapshot: make(map[uint]time.Time),
	}
	// 后台定期清理 lastSnapshot map：客户被删除后对应 entry 不会再更新，
	// 不清理会导致 map 无限增长（虽然慢）
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			h.CleanupStaleSnapshotMarkers(2 * time.Hour)
		}
	}()
	return h
}

// CleanupStaleSnapshotMarkers 清理 lastSnapshot map 中超过 staleThreshold 没更新的 entry
func (h *AgentHandler) CleanupStaleSnapshotMarkers(staleThreshold time.Duration) {
	cutoff := time.Now().Add(-staleThreshold)
	h.snapMu.Lock()
	defer h.snapMu.Unlock()
	for id, t := range h.lastSnapshot {
		if t.Before(cutoff) {
			delete(h.lastSnapshot, id)
		}
	}
}

const snapshotInterval = 5 * time.Minute

// maybeSaveSnapshot 距上次写库 ≥5 分钟才插一条快照
func (h *AgentHandler) maybeSaveSnapshot(customerID uint, req heartbeatRequest) {
	h.snapMu.Lock()
	last, ok := h.lastSnapshot[customerID]
	if ok && time.Since(last) < snapshotInterval {
		h.snapMu.Unlock()
		return
	}
	h.lastSnapshot[customerID] = time.Now()
	h.snapMu.Unlock()

	if req.MemTotalMB == 0 && req.DiskTotalGB == 0 {
		return // 没指标可存
	}
	_ = h.store.SaveSnapshot(&model.MetricsSnapshot{
		CustomerID:  customerID,
		MemUsedMB:   req.MemUsedMB,
		MemTotalMB:  req.MemTotalMB,
		DiskUsedGB:  req.DiskUsedGB,
		DiskTotalGB: req.DiskTotalGB,
		Load1m:      req.Load1m,
		CPUCount:    req.CPUCount,
	})
}

func (h *AgentHandler) Register(r *gin.RouterGroup) {
	r.POST("/heartbeat", h.heartbeat)
	r.POST("/report", h.report)
	r.GET("/notifications", h.notifications)
	r.POST("/notifications/ack", h.ackNotification)
	r.POST("/logs", h.logs)
}

const maxNotificationAttempts = 3

// Auth 中间件：解析 Bearer license token（RSA 签名），找到对应客户
func (h *AgentHandler) Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := bearer(c.GetHeader("Authorization"))
		if tok == "" {
			c.AbortWithStatusJSON(401, gin.H{"error": "missing license"})
			return
		}
		payload, err := license.Verify(tok, h.pub)
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid license: " + err.Error()})
			return
		}
		// 用 token 里的 customer_id 找客户
		cust, err := h.store.FindByID(payload.CustomerID)
		if err != nil || cust == nil {
			c.AbortWithStatusJSON(401, gin.H{"error": "customer not found"})
			return
		}
		c.Set("customer", cust)
		c.Set("license_payload", payload)
		c.Next()
	}
}

type heartbeatRequest struct {
	CurrentVersion string  `json:"current_version"`
	AgentVersion   string  `json:"agent_version"`
	Hostname       string  `json:"hostname"`
	ServerIP       string  `json:"server_ip"`
	MemUsedMB      int     `json:"mem_used_mb"`
	MemTotalMB     int     `json:"mem_total_mb"`
	DiskUsedGB     int     `json:"disk_used_gb"`
	DiskTotalGB    int     `json:"disk_total_gb"`
	Load1m         float64 `json:"load_1m"`
	CPUCount       int     `json:"cpu_count"`
	UptimeSeconds  int64   `json:"uptime_seconds"`
}

func (h *AgentHandler) heartbeat(c *gin.Context) {
	cust := getCustomer(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024)
	var req heartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// agent 在云主机上 net.Dial 拿到的是网卡内网 IP, 这里用 HTTP 请求的真实来源 IP
	// (nginx 反代会注入 X-Real-IP / X-Forwarded-For). 拿不到 (直连 / loopback) 时 fallback 到 agent 上报值
	serverIP := strings.TrimSpace(c.GetHeader("X-Real-IP"))
	if serverIP == "" {
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			serverIP = strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	if serverIP == "" || strings.HasPrefix(serverIP, "127.") || serverIP == "::1" {
		serverIP = req.ServerIP
	}

	if err := h.store.UpdateHeartbeat(cust.ID, store.HeartbeatUpdate{
		CurrentVersion: req.CurrentVersion,
		AgentVersion:   req.AgentVersion,
		ServerIP:       serverIP,
		MemUsedMB:      req.MemUsedMB,
		MemTotalMB:     req.MemTotalMB,
		DiskUsedGB:     req.DiskUsedGB,
		DiskTotalGB:    req.DiskTotalGB,
		Load1m:         req.Load1m,
		CPUCount:       req.CPUCount,
		UptimeSeconds:  req.UptimeSeconds,
	}); err != nil {
		// 写库失败时返回 500，agent 看到错误会重试，不会让 master "假装一切正常"
		c.JSON(http.StatusInternalServerError, gin.H{"error": "heartbeat write failed: " + err.Error()})
		return
	}
	h.maybeSaveSnapshot(cust.ID, req)

	rel, _ := h.store.GetLatestRelease()
	latestAgent, _ := h.store.GetSetting(model.SettingLatestAgentVersion)
	minAgent, _ := h.store.GetSetting(model.SettingMinSupportedAgentVersion)
	agentRepo, _ := h.store.GetSetting(model.SettingAgentReleaseRepo)

	resp := gin.H{
		"latest_version":              "",
		"download_url":                "",
		"latest_agent_version":        latestAgent,
		"agent_download_url":          "",
		"min_supported_agent_version": minAgent,
		"enabled":                     cust.Enabled,
		"message":                     "",
	}
	if rel != nil {
		resp["latest_version"] = rel.Version
		resp["download_url"] = h.downloadURL(rel.Version)
	}
	if latestAgent != "" && agentRepo != "" {
		resp["agent_download_url"] = h.agentDownloadURL(agentRepo, latestAgent)
	}
	c.JSON(http.StatusOK, resp)
}

type reportRequest struct {
	EventType    string `json:"event_type"`
	Version      string `json:"version"`
	ErrorMessage string `json:"error_message"`
}

func (h *AgentHandler) report(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	cust := getCustomer(c)
	var req reportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validReportEvents[req.EventType] {
		c.JSON(400, gin.H{"error": "invalid event_type"})
		return
	}
	_ = h.store.RecordEvent(cust.ID, req.EventType, req.Version, req.ErrorMessage)
	c.JSON(http.StatusOK, gin.H{})
}

func (h *AgentHandler) notifications(c *gin.Context) {
	cust := getCustomer(c)
	pending, _ := h.store.PendingNotifications(cust.ID)
	out := make([]gin.H, 0, len(pending))
	ids := make([]uint, 0, len(pending))
	for _, n := range pending {
		out = append(out, gin.H{
			"id":      n.ID,
			"type":    n.Type,
			"version": n.Version,
			"message": n.Message,
		})
		ids = append(ids, n.ID)
	}
	// attempts++; 超过上限的兜底 deliver（防 stuck，比如 agent 始终崩在某条指令上）
	_ = h.store.MarkAttempted(ids, maxNotificationAttempts)
	c.JSON(http.StatusOK, gin.H{"notifications": out})
}

type ackRequest struct {
	IDs []uint `json:"ids"`
}

// ackNotification agent 执行成功后调，按 ID 标记 delivered
func (h *AgentHandler) ackNotification(c *gin.Context) {
	cust := getCustomer(c)
	var req ackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, id := range req.IDs {
		if err := h.store.MarkDeliveredOne(id, cust.ID); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"acked": len(req.IDs)})
}

type logsRequest struct {
	Service string `json:"service"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
}

const (
	maxLogsBodyBytes = 256 * 1024 // 256 KB body 上限
	maxLogContent    = 64 * 1024  // content 字段最多 64 KB（多余截断）
)

func (h *AgentHandler) logs(c *gin.Context) {
	cust := getCustomer(c)

	// 限制 body 大小（防恶意 / 故障 agent 把 master 撑爆）
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLogsBodyBytes)

	var req logsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
		return
	}
	if req.Service == "" {
		req.Service = "tgfulibot.service"
	}
	// 服务名只允许 [a-zA-Z0-9._-]
	if !validServiceName(req.Service) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid service name"})
		return
	}
	// content 强截断
	if len(req.Content) > maxLogContent {
		req.Content = req.Content[len(req.Content)-maxLogContent:]
		req.Content = "...(已截断头部)\n" + req.Content
	}

	rec, err := h.store.SaveAgentLog(cust.ID, req.Service, req.Content)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 推送给 admin：日志短就发文本，长就发文件
	if h.notifier != nil {
		title := fmt.Sprintf("📋 %s · %s 日志（%d 字节）",
			escapeHTML(cust.Name), escapeHTML(req.Service), len(req.Content))
		if len(req.Content) <= 3500 {
			text := title + "\n\n<pre>" + escapeHTML(req.Content) + "</pre>"
			_ = h.notifier.NotifyAdmin(text)
		} else {
			// 文件名净化：避免客户名里有 / 等字符
			safeName := strings.ReplaceAll(cust.Name, "/", "_")
			safeName = strings.ReplaceAll(safeName, "..", "_")
			_ = h.notifier.SendLogDocument(
				fmt.Sprintf("%s-%s-%d.log", safeName, req.Service, rec.ID),
				[]byte(req.Content),
			)
			_ = h.notifier.NotifyAdmin(title + "\n（日志较长，已发文件附件）")
		}
	}
	c.JSON(http.StatusOK, gin.H{"id": rec.ID})
}

// validServiceName 限制 service 名只能由 [a-zA-Z0-9._-] 组成
func validServiceName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// downloadURL 生成 TGfulibot 主程序在 GitHub Release 的标准下载链接
func (h *AgentHandler) downloadURL(version string) string {
	return fmt.Sprintf(
		"https://github.com/%s/releases/download/%s/tgfulibot-%s-linux-amd64.tar.gz",
		h.releaseRepo, version, version,
	)
}

// agentDownloadURL 生成 agent 自身在 GitHub Release 的下载链接
func (h *AgentHandler) agentDownloadURL(agentRepo, version string) string {
	return fmt.Sprintf(
		"https://github.com/%s/releases/download/%s/%s-%s-linux-amd64.tar.gz",
		agentRepo, version, agentBinaryName, version,
	)
}

func bearer(h string) string {
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func getCustomer(c *gin.Context) *model.Customer {
	v, _ := c.Get("customer")
	cust, _ := v.(*model.Customer)
	return cust
}
