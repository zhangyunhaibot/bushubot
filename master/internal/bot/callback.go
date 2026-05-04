package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"bushubot-master/internal/model"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleCallback 是按钮点击的总入口
func (h *Handler) handleCallback(cb *tgbotapi.CallbackQuery) {
	chat := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	data := cb.Data

	// callback 必须先 ack 一下，否则 Telegram 会一直转圈
	defer h.ack(cb.ID, "")

	switch {
	case data == "menu:main":
		h.editToMainMenu(chat, msgID)

	case data == "menu:customers":
		h.sendCustomerListMenu(chat, msgID)

	case data == "menu:version":
		h.sendVersionMenu(chat, msgID)

	case data == "menu:license":
		h.sendLicenseMenu(chat, msgID)

	case data == "menu:notify":
		h.sendNotifyMenu(chat, msgID)

	case data == "pubkey":
		h.cmdPubkey(chat)
		// 回到 license 菜单
		h.sendLicenseMenu(chat, 0)

	case data == "add:start":
		h.startAddCustomer(chat, msgID)

	case data == "release:start":
		h.startReleaseVersion(chat, msgID)

	case data == "set_agent":
		h.startSetAgentVersion(chat, msgID, false)

	case data == "set_min_agent":
		h.startSetAgentVersion(chat, msgID, true)

	case data == "notify:all":
		h.startNotifyAll(chat, msgID)

	case strings.HasPrefix(data, "bcast:tpl:"):
		template := strings.TrimPrefix(data, "bcast:tpl:")
		h.startBroadcastTemplate(chat, msgID, template)

	case data == "bcast:confirm":
		h.confirmBroadcast(chat)

	case data == "bcast:edit":
		h.editBroadcast(chat)

	case data == "bcast:cancel":
		h.endConversation(chat)
		h.sendNotifyMenu(chat, msgID)

	case data == "bcast:history":
		h.sendBroadcastHistory(chat, msgID)

	case data == "conv:cancel":
		h.cancelConversation(chat, msgID)

	case strings.HasPrefix(data, "custpg:"):
		// custpg:<page> 客户列表翻页
		pageStr := strings.TrimPrefix(data, "custpg:")
		page, err := strconv.Atoi(pageStr)
		if err != nil {
			page = 0
		}
		h.sendCustomerListPage(chat, msgID, page)

	case strings.HasPrefix(data, "cust:"):
		h.handleCustomerAction(chat, msgID, data)

	case strings.HasPrefix(data, "log:"):
		// log:<cust_id>:<service>  → 进入行数选择
		h.handleLogServicePick(chat, msgID, data)

	case strings.HasPrefix(data, "logn:"):
		// logn:<cust_id>:<service>:<lines>  → 真正下发指令
		h.handleLogLinesPick(chat, msgID, data)

	default:
		h.reply(chat, "未知操作: "+data)
	}
}

func (h *Handler) handleLogServicePick(chat int64, msgID int, data string) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 3 {
		return
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	service := parts[2]
	c, _ := h.store.FindByID(uint(id))
	if c == nil {
		h.reply(chat, "找不到客户")
		return
	}
	h.sendLogLinesMenu(chat, msgID, c.ID, service)
}

func (h *Handler) handleLogLinesPick(chat int64, msgID int, data string) {
	parts := strings.SplitN(data, ":", 4)
	if len(parts) < 4 {
		return
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	service := parts[2]
	lines := parts[3] // 直接当字符串拼到 message
	c, _ := h.store.FindByID(uint(id))
	if c == nil {
		h.reply(chat, "找不到客户")
		return
	}
	if !c.Enabled {
		h.reply(chat, "⚠️ 客户已停用")
		return
	}
	// agent 收到 message=service|lines，自动解析
	_ = h.store.NotifyCustomer(c.ID, "fetch_logs", service+"|"+lines)
	h.sendOrEdit(chat, msgID,
		fmt.Sprintf("📋 已下发：拉取 <b>%s · %s</b> 最近 %s 行\n\n约 ≤60s 推送到这里。",
			htmlEsc(c.Name), htmlEsc(service), lines),
		ptrKB(tgbotapi.NewInlineKeyboardMarkup(
			row(btn("🔙 返回客户", "cust:"+strconv.Itoa(int(c.ID))+":detail")),
		)),
	)
}

// handleCustomerAction 处理 cust:<id>:<action> 类的回调
func (h *Handler) handleCustomerAction(chat int64, msgID int, data string) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 {
		h.reply(chat, "操作格式错误")
		return
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		h.reply(chat, "客户 ID 错误")
		return
	}
	action := parts[2]

	c, err := h.store.FindByID(uint(id))
	if err != nil || c == nil {
		h.reply(chat, "找不到客户")
		return
	}

	switch action {
	case "detail":
		h.sendCustomerDetail(chat, msgID, c)

	case "askRestart":
		h.sendConfirmPage(chat, msgID, c, "restart", "🔄 重启 tgfulibot 服务",
			"客户业务会短暂中断，约 60s 内自动恢复")

	case "askUpdate":
		h.sendConfirmPage(chat, msgID, c, "update", "⚡ 立即强制更新",
			"客户会立刻拉新版 tgfulibot 并重启，业务会短暂中断")

	case "askDisable":
		h.sendConfirmPage(chat, msgID, c, "disable", "🛑 停用客户",
			"客户下次心跳后会立刻 stop tgfulibot 服务，业务停止")

	case "askReissue":
		h.sendConfirmPage(chat, msgID, c, "reissue", "🔁 重签 License",
			"会签发一个新 token, 旧 token 仍可用直到过期; 客户需要替换 config.json 重启")

	case "restart":
		if !c.Enabled {
			h.reply(chat, "⚠️ 客户已停用，先启用再重启")
			return
		}
		_ = h.store.NotifyCustomer(c.ID, "restart_service", "")
		h.reply(chat, "🔄 已下发重启指令给 <b>"+htmlEsc(c.Name)+"</b>（≤60s 生效）")
		// 刷新详情
		c2, _ := h.store.FindByID(c.ID)
		if c2 != nil {
			h.sendCustomerDetail(chat, 0, c2)
		}

	case "update":
		if !c.Enabled {
			h.reply(chat, "⚠️ 客户已停用")
			return
		}
		_ = h.store.NotifyCustomer(c.ID, "force_update", "")
		h.reply(chat, "⚡ 已下发强制更新指令给 <b>"+htmlEsc(c.Name)+"</b>（≤60s 生效）")
		c2, _ := h.store.FindByID(c.ID)
		if c2 != nil {
			h.sendCustomerDetail(chat, 0, c2)
		}

	case "disable":
		if err := h.store.SetEnabled(c.ID, false); err != nil {
			h.reply(chat, "失败: "+err.Error())
			return
		}
		h.reply(chat, "🛑 已停用 <b>"+htmlEsc(c.Name)+"</b>（下次心跳后客户服务会停止）")
		c2, _ := h.store.FindByID(c.ID)
		if c2 != nil {
			h.sendCustomerDetail(chat, msgID, c2)
		}

	case "enable":
		if err := h.store.SetEnabled(c.ID, true); err != nil {
			h.reply(chat, "失败: "+err.Error())
			return
		}
		h.reply(chat, "✅ 已恢复 <b>"+htmlEsc(c.Name)+"</b>")
		c2, _ := h.store.FindByID(c.ID)
		if c2 != nil {
			h.sendCustomerDetail(chat, msgID, c2)
		}

	case "reissue":
		tok, expires, err := h.signNewToken(c, h.defaultDays)
		if err != nil {
			h.reply(chat, "签发失败: "+err.Error())
			return
		}
		h.reply(chat, "🔁 已为 <b>"+htmlEsc(c.Name)+"</b> 重签 license（有效期至 "+expires.Format("2006-01-02")+"）\n\n<b>新 token</b>:\n<code>"+htmlEsc(tok)+"</code>\n\n客户需要在 config.json 替换 license.token 后重启 tgfulibot。")

	case "notify":
		h.startNotifyOne(chat, msgID, c.ID, c.Name)

	case "logs":
		if !c.Enabled {
			h.reply(chat, "⚠️ 客户已停用")
			return
		}
		h.sendLogServiceMenu(chat, msgID, c.ID, c.Name)

	case "chart":
		// 拉过去 24h 的快照，画图发出去
		since := time.Now().Add(-24 * time.Hour)
		snaps, err := h.store.GetSnapshots(c.ID, since, 0)
		if err != nil {
			h.reply(chat, "查询失败: "+err.Error())
			return
		}
		png, err := renderMetricsChart(c.Name, snaps, 24)
		if err != nil {
			h.reply(chat, "📈 "+err.Error())
			return
		}
		caption := fmt.Sprintf("📈 <b>%s</b> · 资源历史 (24h, %d 个采样点)", htmlEsc(c.Name), len(snaps))
		filename := safeFilename(c.Name) + "-24h.png"
		if err := h.SendPhotoToChat(chat, filename, caption, png); err != nil {
			h.reply(chat, "发图失败: "+err.Error())
		}

	default:
		h.reply(chat, "未知客户操作: "+action)
	}
}

// safeFilename 把字符串里的危险字符替换为 _，最多 50 字节
// 避免客户名里的 /、\、..、空格 让 Telegram 拒收附件
func safeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	s = strings.ReplaceAll(s, " ", "_")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "customer"
	}
	return s
}

// sendConfirmPage 显示一个二次确认页, 防止误点击触发危险操作
// confirmAction 是确认按钮回调时的 action 名 (例如 "restart"), 取消固定回详情页
func (h *Handler) sendConfirmPage(chat int64, msgID int, c *model.Customer, confirmAction, title, hint string) {
	id := strconv.Itoa(int(c.ID))
	text := fmt.Sprintf("⚠️ <b>确认操作</b>\n\n%s · 客户 <b>%s</b>\n\nℹ️ %s",
		title, htmlEsc(c.Name), hint)
	kb := tgbotapi.NewInlineKeyboardMarkup(row(
		btn("✅ 确认", "cust:"+id+":"+confirmAction),
		btn("❌ 取消", "cust:"+id+":detail"),
	))
	h.sendOrEdit(chat, msgID, text, ptrKB(kb))
}
