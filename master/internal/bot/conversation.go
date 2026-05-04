// 多步对话状态机：用于"添加客户/发布版本/输入通知文本"等需要分步收集信息的流程
package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// conversation 描述一次进行中的多步对话
type conversation struct {
	flow      string         // add_customer / release_version / set_agent / set_min_agent / notify_all / notify_one
	step      string         // 当前步骤标识
	data      map[string]any // 已收集的数据
	updatedAt time.Time
}

// 取消按钮的标准 InlineKeyboard
var cancelKB = tgbotapi.NewInlineKeyboardMarkup(
	tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "conv:cancel"),
	),
)

// ---------------- 核心调度 ----------------

func (h *Handler) isInConversation(chat int64) bool {
	h.convsMu.Lock()
	defer h.convsMu.Unlock()
	c, ok := h.convs[chat]
	if !ok {
		return false
	}
	// 超过 10 分钟无操作 → 自动清理
	if time.Since(c.updatedAt) > 10*time.Minute {
		delete(h.convs, chat)
		return false
	}
	return true
}

func (h *Handler) startConversation(chat int64, flow, step string) *conversation {
	h.convsMu.Lock()
	defer h.convsMu.Unlock()
	c := &conversation{flow: flow, step: step, data: map[string]any{}, updatedAt: time.Now()}
	h.convs[chat] = c
	return c
}

func (h *Handler) endConversation(chat int64) {
	h.convsMu.Lock()
	defer h.convsMu.Unlock()
	delete(h.convs, chat)
}

func (h *Handler) cancelConversation(chat int64, msgID int) {
	h.endConversation(chat)
	h.sendOrEdit(chat, msgID, "已取消。", ptrKB(mainMenuKB()))
}

// advanceConversation 在收到一条普通消息时推进当前对话
func (h *Handler) advanceConversation(m *tgbotapi.Message) {
	chat := m.Chat.ID
	text := strings.TrimSpace(m.Text)

	h.convsMu.Lock()
	c, ok := h.convs[chat]
	if ok {
		c.updatedAt = time.Now()
	}
	h.convsMu.Unlock()
	if !ok {
		return
	}

	switch c.flow {
	case "add_customer":
		h.advanceAddCustomer(chat, c, text)
	case "release_version":
		h.advanceReleaseVersion(chat, c, text)
	case "set_agent", "set_min_agent":
		h.advanceSetAgent(chat, c, text)
	case "notify_all":
		h.advanceNotifyAll(chat, c, text)
	case "notify_one":
		h.advanceNotifyOne(chat, c, text)
	case "bcast_maintenance":
		h.advanceMaintenance(chat, c, text)
	case "bcast_version_preview":
		h.advanceVersionPreview(chat, c, text)
	case "bcast_upgrade_failed":
		h.advanceUpgradeFailed(chat, c, text)
	case "bcast_custom":
		h.advanceCustomBroadcast(chat, c, text)
	case "broadcast_preview":
		// 预览态只接受按钮点击，文本输入忽略
		h.reply(chat, "请点【✅ 确认发送】/【✏️ 修改】/【❌ 取消】")
	default:
		h.endConversation(chat)
	}
}

// ================== flow: add_customer ==================
//
// 步骤: name → tg_id → bot_token → note → done

func (h *Handler) startAddCustomer(chat int64, msgID int) {
	h.startConversation(chat, "add_customer", "name")
	h.sendOrEdit(chat, msgID, "➕ <b>添加新客户 · 步骤 1/4</b>\n\n请发送客户名（你给客户起的别名，例如 老王）：", &cancelKB)
}

func (h *Handler) advanceAddCustomer(chat int64, c *conversation, text string) {
	switch c.step {
	case "name":
		if text == "" {
			h.reply(chat, "客户名不能为空，请重新输入：")
			return
		}
		c.data["name"] = text
		c.step = "tg_id"
		h.sendOrEdit(chat, 0, "➕ <b>添加客户 · 步骤 2/4</b>\n\n请发送客户的 Telegram User ID（数字）：", &cancelKB)

	case "tg_id":
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			h.reply(chat, "TG ID 必须是纯数字，请重新输入：")
			return
		}
		c.data["tg_id"] = id
		c.step = "bot_token"
		h.sendOrEdit(chat, 0, "➕ <b>添加客户 · 步骤 3/4</b>\n\n请发送客户自己创建的 Bot Token：", &cancelKB)

	case "bot_token":
		if !strings.Contains(text, ":") {
			h.reply(chat, "Bot Token 看起来不对（应该形如 <code>123456:ABC...</code>），请重新输入：")
			return
		}
		c.data["bot_token"] = text
		c.step = "note"
		h.sendOrEdit(chat, 0, "➕ <b>添加客户 · 步骤 4/4</b>\n\n请输入备注（例如\"月费/3 月\"），不需要发 -：", &cancelKB)

	case "note":
		note := text
		if note == "-" {
			note = ""
		}
		// 创建客户 + 签发 token
		name := c.data["name"].(string)
		tgID := c.data["tg_id"].(int64)
		botToken := c.data["bot_token"].(string)

		newC, err := h.store.CreateCustomer(name, tgID, botToken, note)
		if err != nil {
			h.reply(chat, "❌ 创建失败: "+err.Error())
			h.endConversation(chat)
			return
		}
		tok, expires, err := h.signNewToken(newC, h.defaultDays)
		if err != nil {
			h.reply(chat, "客户已创建，但签发 license 失败: "+err.Error())
			h.endConversation(chat)
			return
		}
		cloudInit := buildCloudInit(tok, botToken, tgID)
		sshCmd := buildSSHCommand(tok, botToken, tgID)
		h.reply(chat, fmt.Sprintf(
			"✅ 已创建客户 <b>%s</b>（有效期至 %s）\n\n"+
				"<b>方式 1 · Cloud-init</b>（创建新服务器时贴）：\n<pre>%s</pre>\n"+
				"<b>方式 2 · SSH 手工执行</b>（已有服务器）：\n<pre>%s</pre>\n"+
				"💡 部署完成后约 5 分钟会自动收到上线通知。",
			htmlEsc(newC.Name), expires.Format("2006-01-02"),
			htmlEsc(cloudInit), htmlEsc(sshCmd),
		))
		h.endConversation(chat)
		h.sendMainMenu(chat)
	}
}

// ================== flow: release_version ==================
//
// 步骤: version → notes → done

func (h *Handler) startReleaseVersion(chat int64, msgID int) {
	h.startConversation(chat, "release_version", "version")
	h.sendOrEdit(chat, msgID, "🚀 <b>发布 TGfulibot 新版本 · 步骤 1/2</b>\n\n请发送版本号（例如 v0.14）：", &cancelKB)
}

func (h *Handler) advanceReleaseVersion(chat int64, c *conversation, text string) {
	switch c.step {
	case "version":
		if !strings.HasPrefix(text, "v") {
			h.reply(chat, "版本号必须以 v 开头，例如 v0.14。请重新输入：")
			return
		}
		c.data["version"] = text
		c.step = "notes"
		h.sendOrEdit(chat, 0, "🚀 <b>发布 · 步骤 2/2</b>\n\n请输入更新说明（多行也行），不需要发 -：", &cancelKB)

	case "notes":
		notes := text
		if notes == "-" {
			notes = ""
		}
		version := c.data["version"].(string)
		if _, err := h.store.PublishRelease(version, notes); err != nil {
			h.reply(chat, "❌ 发布失败: "+err.Error())
			h.endConversation(chat)
			return
		}
		msg := fmt.Sprintf("📦 新版本 %s 已发布", version)
		if notes != "" {
			msg += "\n\n" + notes
		}
		_ = h.store.BroadcastNotification("update_info", version, msg)
		h.reply(chat, "✅ 已发布 <b>"+version+"</b>，所有客户将在下次心跳收到通知并自动更新")
		h.endConversation(chat)
		h.sendMainMenu(chat)
	}
}

// ================== flow: set_agent / set_min_agent ==================

func (h *Handler) startSetAgentVersion(chat int64, msgID int, isMin bool) {
	flow := "set_agent"
	hint := "📦 <b>设置 Agent 最新版</b>\n\n请输入版本号（例如 agent-v0.2），输入 - 清除："
	if isMin {
		flow = "set_min_agent"
		hint = "⛔ <b>设置 Agent 最低支持版</b>\n\n输入低于此版本的客户会被强制升级。例如 agent-v0.2，输入 - 清除："
	}
	h.startConversation(chat, flow, "value")
	h.sendOrEdit(chat, msgID, hint, &cancelKB)
}

func (h *Handler) advanceSetAgent(chat int64, c *conversation, text string) {
	value := text
	if value == "-" {
		value = ""
	}
	key := "latest_agent_version"
	label := "最新 agent 版本"
	if c.flow == "set_min_agent" {
		key = "min_supported_agent_version"
		label = "最低支持 agent 版本"
	}
	if err := h.store.SetSetting(key, value); err != nil {
		h.reply(chat, "❌ 失败: "+err.Error())
		h.endConversation(chat)
		return
	}
	if value == "" {
		h.reply(chat, "✅ 已清除 "+label)
	} else {
		h.reply(chat, "✅ "+label+" 已设为 "+value)
	}
	h.endConversation(chat)
	h.sendVersionMenu(chat, 0)
}

// ================== flow: notify_all ==================

func (h *Handler) startNotifyAll(chat int64, msgID int) {
	h.startConversation(chat, "notify_all", "message")
	h.sendOrEdit(chat, msgID, "📡 <b>全员广播</b>\n\n请输入要发给所有启用客户的消息：", &cancelKB)
}

func (h *Handler) advanceNotifyAll(chat int64, c *conversation, text string) {
	if text == "" {
		h.reply(chat, "消息不能为空")
		return
	}
	_ = h.store.BroadcastNotification("manual", "", text)
	h.reply(chat, "✅ 已加入广播队列，所有启用客户的 agent 心跳后会推送给客户")
	h.endConversation(chat)
	h.sendMainMenu(chat)
}

// ================== flow: notify_one ==================

func (h *Handler) startNotifyOne(chat int64, msgID int, customerID uint, name string) {
	c := h.startConversation(chat, "notify_one", "message")
	c.data["customer_id"] = customerID
	c.data["name"] = name
	h.sendOrEdit(chat, msgID, "👤 <b>通知 "+name+"</b>\n\n请输入要发给该客户的消息：", &cancelKB)
}

func (h *Handler) advanceNotifyOne(chat int64, c *conversation, text string) {
	if text == "" {
		h.reply(chat, "消息不能为空")
		return
	}
	id := c.data["customer_id"].(uint)
	name := c.data["name"].(string)
	if err := h.store.NotifyCustomer(id, "manual", text); err != nil {
		h.reply(chat, "❌ 失败: "+err.Error())
		h.endConversation(chat)
		return
	}
	h.reply(chat, "✅ 已发送给 "+name+"（≤60s 推送）")
	h.endConversation(chat)
}
