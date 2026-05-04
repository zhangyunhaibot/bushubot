// Package alerter 是升级失败自动告警的后台 goroutine。
//
// 工作流程:
//   每隔 interval 扫一次 agent_events 表，找最近 lookback 内的失败事件
//   按客户聚合 → 推送给 admin（通过 bot.Handler.NotifyAdmin）
//   已告警事件标 alerted=true，不再重复告警
package alerter

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"bushubot-master/internal/model"
	"bushubot-master/internal/store"
)

type Notifier interface {
	NotifyAdmin(text string) error
}

type Alerter struct {
	store    *store.Store
	notifier Notifier
	interval time.Duration
	lookback time.Duration

	// 规则告警去重: key="<rule>:<customer_id>" → 上次告警时间
	ruleAlertMu   sync.Mutex
	ruleLastAlert map[string]time.Time
}

// bucket 提到包级，让 formatAlert 能引用
type bucket struct {
	count   int
	latest  model.AgentEvent
	ids     []uint
	samples []string
}

func New(s *store.Store, n Notifier) *Alerter {
	return &Alerter{
		store:         s,
		notifier:      n,
		interval:      5 * time.Minute,
		lookback:      30 * time.Minute,
		ruleLastAlert: make(map[string]time.Time),
	}
}

func (a *Alerter) Run(stop <-chan struct{}) {
	tick := time.NewTicker(a.interval)
	defer tick.Stop()

	log.Printf("alerter 已启动 (扫描间隔 %s, 回溯窗口 %s)", a.interval, a.lookback)
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			a.scanAndAlert()
			a.scanRules()
		}
	}
}

// ---------------- 阈值规则告警 ----------------

const (
	ruleDiskHigh = "disk_high"
	ruleMemHigh  = "mem_high"
	ruleOffline  = "offline"

	thresholdDiskPct = 90 // 磁盘超 90% 告警
	thresholdMemPct  = 90 // 内存超 90% 告警
	// agent 心跳间隔默认 20 min, 这里给 2 倍容错: 40 min 没心跳才算掉线
	offlineThreshold  = 40 * time.Minute
	ruleAlertCooldown = 1 * time.Hour
)

func (a *Alerter) scanRules() {
	customers, err := a.store.ListCustomers()
	if err != nil {
		log.Printf("alerter 列客户失败: %v", err)
		return
	}
	now := time.Now()
	for _, c := range customers {
		if !c.Enabled {
			continue
		}
		// 1. 掉线
		if c.LastHeartbeatAt != nil {
			if now.Sub(*c.LastHeartbeatAt) > offlineThreshold {
				a.maybeAlert(ruleOffline, c.ID, fmt.Sprintf(
					"📡 <b>客户掉线告警</b>\n\n客户: <b>%s</b>\nIP: %s\n最后心跳: %s（已 %s 未上报）",
					escapeHTML(c.Name), escapeHTML(dashIfEmpty(c.ServerIP)),
					c.LastHeartbeatAt.Format("01-02 15:04:05"),
					formatDur(now.Sub(*c.LastHeartbeatAt)),
				))
			}
		}
		// 2. 磁盘
		if c.DiskTotalGB > 0 {
			pct := c.DiskUsedGB * 100 / c.DiskTotalGB
			if pct >= thresholdDiskPct {
				a.maybeAlert(ruleDiskHigh, c.ID, fmt.Sprintf(
					"💾 <b>磁盘告警</b>\n\n客户: <b>%s</b>\n已用: %d GB / %d GB (%d%%)\n建议在主 Bot 中查看详情或登录服务器清理",
					escapeHTML(c.Name), c.DiskUsedGB, c.DiskTotalGB, pct,
				))
			}
		}
		// 3. 内存
		if c.MemTotalMB > 0 {
			pct := c.MemUsedMB * 100 / c.MemTotalMB
			if pct >= thresholdMemPct {
				a.maybeAlert(ruleMemHigh, c.ID, fmt.Sprintf(
					"🧠 <b>内存告警</b>\n\n客户: <b>%s</b>\n已用: %d MB / %d MB (%d%%)\n建议: 重启服务可能能释放内存",
					escapeHTML(c.Name), c.MemUsedMB, c.MemTotalMB, pct,
				))
			}
		}
	}
}

// maybeAlert 同一规则同一客户在 cooldown 内不重复告警
func (a *Alerter) maybeAlert(rule string, customerID uint, text string) {
	key := fmt.Sprintf("%s:%d", rule, customerID)
	a.ruleAlertMu.Lock()
	last, ok := a.ruleLastAlert[key]
	if ok && time.Since(last) < ruleAlertCooldown {
		a.ruleAlertMu.Unlock()
		return
	}
	a.ruleLastAlert[key] = time.Now()
	a.ruleAlertMu.Unlock()

	if err := a.notifier.NotifyAdmin(text); err != nil {
		log.Printf("alerter 推送规则告警失败 (%s): %v", key, err)
	}
}

func formatDur(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
	return fmt.Sprintf("%.1f 天", d.Hours()/24)
}

func (a *Alerter) scanAndAlert() {
	since := time.Now().Add(-a.lookback)
	events, err := a.store.PendingFailureEvents(since)
	if err != nil {
		log.Printf("alerter 查询失败: %v", err)
		return
	}
	if len(events) == 0 {
		return
	}

	// 按 customer_id + event_type 聚合
	type key struct {
		customerID uint
		eventType  string
	}
	groups := map[key]*bucket{}
	for _, e := range events {
		k := key{e.CustomerID, e.EventType}
		b, ok := groups[k]
		if !ok {
			b = &bucket{}
			groups[k] = b
		}
		b.count++
		b.latest = e
		b.ids = append(b.ids, e.ID)
		if len(b.samples) < 3 && e.ErrorMessage != "" {
			b.samples = append(b.samples, e.ErrorMessage)
		}
	}

	// 给每组发一条告警
	var allIDs []uint
	for k, b := range groups {
		text := a.formatAlert(k.customerID, k.eventType, b)
		if err := a.notifier.NotifyAdmin(text); err != nil {
			log.Printf("alerter 推送 admin 失败: %v", err)
			continue
		}
		allIDs = append(allIDs, b.ids...)
	}

	if err := a.store.MarkEventsAlerted(allIDs); err != nil {
		log.Printf("alerter 标记 alerted 失败: %v", err)
	} else {
		log.Printf("alerter: 推送了 %d 组告警，标记 %d 条事件", len(groups), len(allIDs))
	}
}

func (a *Alerter) formatAlert(customerID uint, eventType string, b *bucket) string {
	cust, _ := a.store.FindByID(customerID)
	name := fmt.Sprintf("#%d", customerID)
	if cust != nil {
		name = cust.Name
	}

	emoji := "🚨"
	label := eventType
	switch eventType {
	case "update_failed":
		label = "TGfulibot 升级失败"
	case "agent_self_update_failed":
		label = "Agent 自我更新失败"
	case "restart_failed":
		emoji = "⚠️"
		label = "重启服务失败"
	}

	var samples string
	if len(b.samples) > 0 {
		samples = "\n\n<b>最近错误:</b>\n<code>" +
			escapeHTML(strings.Join(b.samples, "\n— ")) + "</code>"
	}

	return fmt.Sprintf(
		"%s <b>升级异常告警</b>\n\n"+
			"客户: <b>%s</b>\n"+
			"问题: %s（%d 次）\n"+
			"版本: %s\n"+
			"最近发生: %s%s\n\n"+
			"建议: 在主 Bot 中查看客户详情或手动 /update_now 重试",
		emoji, escapeHTML(name), label, b.count,
		dashIfEmpty(b.latest.Version),
		b.latest.CreatedAt.Format("01-02 15:04:05"),
		samples,
	)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
