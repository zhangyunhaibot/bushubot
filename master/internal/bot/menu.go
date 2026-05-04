package bot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"bushubot-master/internal/model"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// customerOfflineThreshold 与 alerter.offlineThreshold 保持一致 (40 min)
	// 心跳间隔默认 20 min, 超过 2 倍间隔没心跳就标 🔴
	customerOfflineThreshold = 40 * time.Minute
	// customersPerPage 客户列表每页按钮数 (5 行 × 2 列)
	customersPerPage = 10
)

// customerHealthEmoji 客户健康状态符号: 🟢 在线, 🔴 已停用 / 心跳超时
func customerHealthEmoji(c *model.Customer) string {
	if !c.Enabled {
		return "🔴"
	}
	if c.LastHeartbeatAt == nil {
		return "🔴"
	}
	if time.Since(*c.LastHeartbeatAt) > customerOfflineThreshold {
		return "🔴"
	}
	return "🟢"
}

// customerListLabel 列表按钮 label, 例: "🟢 测试王 v0.51"
func customerListLabel(c *model.Customer) string {
	ver := c.CurrentVersion
	if ver == "" {
		ver = "?"
	}
	return customerHealthEmoji(c) + " " + c.Name + " " + ver
}

// callback_data 命名约定（< 64 字节）：
//   menu:main / menu:customers / menu:version / menu:license / menu:notify
//   cust:<id>:detail / cust:<id>:restart / cust:<id>:update / cust:<id>:disable / cust:<id>:enable / cust:<id>:reissue
//   add:start
//   release:start
//   pubkey
//   notify:all / notify:single
//   set_agent / set_min_agent
//   conv:cancel

func btn(text, data string) tgbotapi.InlineKeyboardButton {
	return tgbotapi.NewInlineKeyboardButtonData(text, data)
}

func row(b ...tgbotapi.InlineKeyboardButton) []tgbotapi.InlineKeyboardButton {
	return b
}

// ---------------- 主菜单 ----------------

func mainMenuKB() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		row(btn("👥 客户管理", "menu:customers"), btn("📦 版本管理", "menu:version")),
		row(btn("🔑 License", "menu:license"), btn("📢 发通知", "menu:notify")),
	)
}

func (h *Handler) sendMainMenu(chat int64) {
	h.sendOrEdit(chat, 0, "📋 <b>bushubot 主控台</b>\n\n请选择操作：", ptrKB(mainMenuKB()))
}

func (h *Handler) editToMainMenu(chat int64, msgID int) {
	h.sendOrEdit(chat, msgID, "📋 <b>bushubot 主控台</b>\n\n请选择操作：", ptrKB(mainMenuKB()))
}

// ---------------- 客户列表 ----------------

func (h *Handler) sendCustomerListMenu(chat int64, msgID int) {
	h.sendCustomerListPage(chat, msgID, 0)
}

// sendCustomerListPage 渲染客户列表第 page 页 (0-based)
// 设计:
//   - 异常 (🔴) 客户排前面, 然后按名字字典序
//   - 每页 customersPerPage 个按钮, 2 列布局, label = "🟢 测试王 v0.51"
//   - 客户超 1 页时显示翻页按钮; 1 页能装下时不显示
//   - 顶部文字仅展示总数和异常数 (🟢 X · 🔴 Y), 不再列每个客户细节 (避免客户多时刷屏)
func (h *Handler) sendCustomerListPage(chat int64, msgID int, page int) {
	customers, err := h.store.ListCustomers()
	if err != nil {
		h.reply(chat, "查询失败: "+err.Error())
		return
	}

	// 异常优先排序: 🔴 排前; 同状态内按名字字典序
	sort.SliceStable(customers, func(i, j int) bool {
		ei := customerHealthEmoji(&customers[i])
		ej := customerHealthEmoji(&customers[j])
		if ei != ej {
			return ei == "🔴" // 🔴 排前
		}
		return customers[i].Name < customers[j].Name
	})

	// 统计在线 / 异常
	online, offline := 0, 0
	for i := range customers {
		if customerHealthEmoji(&customers[i]) == "🟢" {
			online++
		} else {
			offline++
		}
	}

	total := len(customers)
	totalPages := 1
	if total > 0 {
		totalPages = (total + customersPerPage - 1) / customersPerPage
	}
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	text := fmt.Sprintf("👥 <b>客户列表</b>\n共 %d 个 · 🟢 %d 在线 · 🔴 %d 异常",
		total, online, offline)
	if total == 0 {
		text += "\n\n暂无客户。"
	} else if totalPages > 1 {
		text += fmt.Sprintf("\n第 %d / %d 页", page+1, totalPages)
	}

	// 当前页客户切片
	start := page * customersPerPage
	end := start + customersPerPage
	if end > total {
		end = total
	}
	pageCustomers := customers[start:end]

	rows := [][]tgbotapi.InlineKeyboardButton{}
	// 一行 2 个按钮, label 较长 (含名字 + 版本号), 2 列更耐看
	cur := []tgbotapi.InlineKeyboardButton{}
	for i := range pageCustomers {
		c := &pageCustomers[i]
		cur = append(cur, btn(customerListLabel(c), "cust:"+strconv.Itoa(int(c.ID))+":detail"))
		if len(cur) == 2 {
			rows = append(rows, cur)
			cur = []tgbotapi.InlineKeyboardButton{}
		}
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}

	// 翻页控件 (仅多页时显示)
	if totalPages > 1 {
		var pager []tgbotapi.InlineKeyboardButton
		if page > 0 {
			pager = append(pager, btn("⬅️ 上页", "custpg:"+strconv.Itoa(page-1)))
		}
		if page < totalPages-1 {
			pager = append(pager, btn("➡️ 下页", "custpg:"+strconv.Itoa(page+1)))
		}
		if len(pager) > 0 {
			rows = append(rows, pager)
		}
	}

	rows = append(rows, row(btn("➕ 添加客户", "add:start"), btn("🔙 返回", "menu:main")))

	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	h.sendOrEdit(chat, msgID, text, &kb)
}

// ---------------- 单客户操作 ----------------

func (h *Handler) sendCustomerDetail(chat int64, msgID int, c *model.Customer) {
	hb := "从未心跳"
	if c.LastHeartbeatAt != nil {
		hb = c.LastHeartbeatAt.Format("01-02 15:04:05")
	}
	// 状态口径与客户列表一致: 看 enabled + 是否心跳超时
	state := "🟢 在线"
	if !c.Enabled {
		state = "🔴 已停用"
	} else if c.LastHeartbeatAt == nil || time.Since(*c.LastHeartbeatAt) > customerOfflineThreshold {
		state = "🔴 离线"
	}
	text := fmt.Sprintf(
		"📌 <b>%s</b>\n"+
			"━━━━━━━━━━━━\n"+
			"• TG ID: <code>%d</code>\n"+
			"• 服务器 IP: %s\n"+
			"• 当前版本: %s\n"+
			"• Agent 版本: %s\n"+
			"• 最后心跳: %s\n"+
			"• 状态: %s\n"+
			"• 备注: %s\n"+
			"━━━━━━━━━━━━\n%s",
		htmlEsc(c.Name), c.TGUserID,
		htmlEsc(emptyDash(c.ServerIP)),
		htmlEsc(emptyDash(c.CurrentVersion)),
		htmlEsc(emptyDash(c.AgentVersion)),
		hb, state, htmlEsc(emptyDash(c.Note)),
		formatMetrics(c),
	)

	id := strconv.Itoa(int(c.ID))
	// 启用是恢复操作风险低, 直接执行; 停用 / 重启 / 更新 / 重签都加二次确认
	togglelabel := "🔴 停用"
	togglekey := "askDisable"
	if !c.Enabled {
		togglelabel = "🟢 启用"
		togglekey = "enable"
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(
		row(
			btn("🔄 重启服务", "cust:"+id+":askRestart"),
			btn("⚡ 立即更新", "cust:"+id+":askUpdate"),
		),
		row(
			btn("📋 查看日志", "cust:"+id+":logs"),
			btn("📈 历史曲线", "cust:"+id+":chart"),
		),
		row(
			btn("🔁 重签 License", "cust:"+id+":askReissue"),
			btn("📢 单独通知", "cust:"+id+":notify"),
		),
		row(
			btn(togglelabel, "cust:"+id+":"+togglekey),
			btn("🔙 客户列表", "menu:customers"),
		),
	)
	h.sendOrEdit(chat, msgID, text, &kb)
}

// ---------------- 日志服务/行数 二级菜单 ----------------

func (h *Handler) sendLogServiceMenu(chat int64, msgID int, customerID uint, name string) {
	id := strconv.Itoa(int(customerID))
	text := fmt.Sprintf("📋 <b>%s · 选择要拉取的服务</b>", htmlEsc(name))
	kb := tgbotapi.NewInlineKeyboardMarkup(
		row(
			btn("tgfulibot", "log:"+id+":tgfulibot.service"),
			btn("bushubot-agent", "log:"+id+":bushubot-agent.service"),
		),
		row(
			btn("postgresql", "log:"+id+":postgresql.service"),
			btn("redis-server", "log:"+id+":redis-server.service"),
		),
		row(btn("🔙 返回客户", "cust:"+id+":detail")),
	)
	h.sendOrEdit(chat, msgID, text, &kb)
}

func (h *Handler) sendLogLinesMenu(chat int64, msgID int, customerID uint, service string) {
	id := strconv.Itoa(int(customerID))
	text := fmt.Sprintf("📋 <b>%s</b>\n\n选择拉取的行数：", htmlEsc(service))
	kb := tgbotapi.NewInlineKeyboardMarkup(
		row(
			btn("最近 100 条", "logn:"+id+":"+service+":100"),
			btn("最近 500 条", "logn:"+id+":"+service+":500"),
		),
		row(btn("🔙 返回服务列表", "cust:"+id+":logs")),
	)
	h.sendOrEdit(chat, msgID, text, &kb)
}

// formatMetrics 输出资源指标段。心跳里没带就显示 "暂无指标"
func formatMetrics(c *model.Customer) string {
	if c.MemTotalMB == 0 && c.DiskTotalGB == 0 && c.CPUCount == 0 {
		return "📊 <i>暂无资源指标（agent 心跳后会上报）</i>"
	}
	memPct, diskPct := 0, 0
	if c.MemTotalMB > 0 {
		memPct = c.MemUsedMB * 100 / c.MemTotalMB
	}
	if c.DiskTotalGB > 0 {
		diskPct = c.DiskUsedGB * 100 / c.DiskTotalGB
	}

	loadStr := "?"
	loadHint := ""
	if c.CPUCount > 0 {
		loadStr = fmt.Sprintf("%.2f / %d", c.Load1m, c.CPUCount)
		ratio := c.Load1m / float64(c.CPUCount)
		switch {
		case ratio >= 1.0:
			loadHint = " 🔴"
		case ratio >= 0.7:
			loadHint = " 🟡"
		default:
			loadHint = " 🟢"
		}
	}

	uptime := "?"
	if c.UptimeSeconds > 0 {
		uptime = formatUptime(c.UptimeSeconds)
	}

	return fmt.Sprintf(
		"📊 <b>资源指标</b>\n"+
			"• 内存: %d MB / %d MB (%d%%) %s\n"+
			"• 磁盘: %d GB / %d GB (%d%%) %s\n"+
			"• 负载: %s%s\n"+
			"• 系统已运行: %s",
		c.MemUsedMB, c.MemTotalMB, memPct, pctEmoji(memPct),
		c.DiskUsedGB, c.DiskTotalGB, diskPct, pctEmoji(diskPct),
		loadStr, loadHint, uptime,
	)
}

func pctEmoji(pct int) string {
	switch {
	case pct >= 90:
		return "🔴"
	case pct >= 75:
		return "🟡"
	default:
		return "🟢"
	}
}

func formatUptime(secs int64) string {
	if secs <= 0 {
		return "?"
	}
	d := secs / 86400
	h := (secs % 86400) / 3600
	m := (secs % 3600) / 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// ---------------- 版本管理 ----------------

func (h *Handler) sendVersionMenu(chat int64, msgID int) {
	latestRel, _ := h.store.GetLatestRelease()
	latestAgent, _ := h.store.GetSetting(model.SettingLatestAgentVersion)
	minAgent, _ := h.store.GetSetting(model.SettingMinSupportedAgentVersion)

	tgVer := "(未发布)"
	if latestRel != nil {
		tgVer = latestRel.Version
	}
	text := fmt.Sprintf(
		"📦 <b>版本管理</b>\n"+
			"━━━━━━━━━━━━\n"+
			"• TGfulibot 最新版: <code>%s</code>\n"+
			"• Agent 最新版:    <code>%s</code>\n"+
			"• Agent 最低支持版: <code>%s</code>",
		htmlEsc(tgVer),
		htmlEsc(defaultStr(latestAgent, "(未设置)")),
		htmlEsc(defaultStr(minAgent, "(不强制)")),
	)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		row(btn("🚀 发布 TGfulibot 新版本", "release:start")),
		row(
			btn("📦 设置 Agent 最新版", "set_agent"),
			btn("⛔ 设置 Agent 最低版", "set_min_agent"),
		),
		row(btn("🔙 返回", "menu:main")),
	)
	h.sendOrEdit(chat, msgID, text, &kb)
}

// ---------------- License 菜单 ----------------

func (h *Handler) sendLicenseMenu(chat int64, msgID int) {
	text := "🔑 <b>License 管理</b>\n\n选择操作："
	kb := tgbotapi.NewInlineKeyboardMarkup(
		row(btn("📋 导出公钥", "pubkey")),
		row(btn("🔙 返回", "menu:main")),
	)
	h.sendOrEdit(chat, msgID, text, &kb)
}

// ---------------- 通知 / 广播 菜单 ----------------

func (h *Handler) sendNotifyMenu(chat int64, msgID int) {
	text := "📢 <b>广播 & 通知</b>\n\n选择操作："
	kb := tgbotapi.NewInlineKeyboardMarkup(
		row(btn("🛠 维护通知", "bcast:tpl:maintenance"), btn("✅ 维护结束", "bcast:tpl:maintenance_done")),
		row(btn("🚀 重大版本预告", "bcast:tpl:version_preview"), btn("⚠️ 升级失败告警", "bcast:tpl:upgrade_failed")),
		row(btn("✏️ 自定义文本", "bcast:tpl:custom")),
		row(btn("👤 单个客户", "menu:customers"), btn("📜 广播历史", "bcast:history")),
		row(btn("🔙 返回", "menu:main")),
	)
	h.sendOrEdit(chat, msgID, text, &kb)
}

// ---------------- 广播历史菜单 ----------------

func (h *Handler) sendBroadcastHistory(chat int64, msgID int) {
	list, err := h.store.RecentBroadcasts(10)
	if err != nil {
		h.reply(chat, "查询历史失败: "+err.Error())
		return
	}
	if len(list) == 0 {
		text := "📜 <b>广播历史</b>\n\n暂无记录。"
		kb := tgbotapi.NewInlineKeyboardMarkup(row(btn("🔙 返回", "menu:notify")))
		h.sendOrEdit(chat, msgID, text, &kb)
		return
	}

	var sb strings.Builder
	sb.WriteString("📜 <b>广播历史</b>（最近 10 条）\n\n")
	for _, b := range list {
		title := b.Title
		if title == "" {
			title = firstLine(b.Content, 40)
		}
		sb.WriteString(fmt.Sprintf(
			"• <b>%s</b>\n  <i>%s · %s · 推送 %d 客户 · %s</i>\n\n",
			htmlEsc(title),
			b.CreatedAt.Format("01-02 15:04"),
			templateLabel(b.Template),
			b.TargetCount,
			sentByLabel(b.SentBy),
		))
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(row(btn("🔙 返回", "menu:notify")))
	h.sendOrEdit(chat, msgID, sb.String(), &kb)
}

// ---------------- helpers ----------------

func firstLine(s string, max int) string {
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

func templateLabel(tpl string) string {
	switch tpl {
	case "maintenance":
		return "维护通知"
	case "maintenance_done":
		return "维护结束"
	case "version_preview":
		return "版本预告"
	case "upgrade_failed":
		return "升级失败"
	case "custom":
		return "自定义"
	case "auto_alert":
		return "自动告警"
	default:
		return tpl
	}
}

func sentByLabel(by string) string {
	if by == "system_alerter" {
		return "系统自动"
	}
	return "管理员"
}

// ---------------- helpers ----------------

func ptrKB(kb tgbotapi.InlineKeyboardMarkup) *tgbotapi.InlineKeyboardMarkup {
	return &kb
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
