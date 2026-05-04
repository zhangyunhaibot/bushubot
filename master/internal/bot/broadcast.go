package bot

import (
	"fmt"
	"strconv"
	"strings"

	"bushubot-master/internal/model"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// 广播预览/确认按钮
func broadcastPreviewKB() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		row(btn("✅ 确认发送", "bcast:confirm"), btn("✏️ 修改", "bcast:edit")),
		row(btn("❌ 取消", "bcast:cancel")),
	)
}

// 进入预览态：把渲染好的内容塞到 conversation.data["preview"]，等用户点确认
func (h *Handler) enterBroadcastPreview(chat int64, c *conversation, template, title, content string) {
	enabledCount := h.countEnabledCustomers()

	c.flow = "broadcast_preview"
	c.step = "preview"
	c.data["preview_template"] = template
	c.data["preview_title"] = title
	c.data["preview_content"] = content

	text := fmt.Sprintf(
		"📢 <b>即将广播给 %d 个启用客户</b>\n\n"+
			"━━━━━━━━━━━━\n%s\n━━━━━━━━━━━━\n\n"+
			"确认无误后点【✅ 确认发送】",
		enabledCount, htmlEsc(content),
	)
	kb := broadcastPreviewKB()
	h.sendOrEdit(chat, 0, text, &kb)
}

// confirmBroadcast 真正写入 broadcasts + notifications 表
func (h *Handler) confirmBroadcast(chat int64) {
	h.convsMu.Lock()
	c, ok := h.convs[chat]
	h.convsMu.Unlock()
	if !ok || c.flow != "broadcast_preview" {
		h.reply(chat, "没有待发送的广播")
		return
	}

	template, _ := c.data["preview_template"].(string)
	title, _ := c.data["preview_title"].(string)
	content, _ := c.data["preview_content"].(string)

	bc, err := h.store.BroadcastWithRecord(template, title, content, model.BroadcastSentByAdmin)
	if err != nil {
		h.reply(chat, "❌ 广播失败: "+err.Error())
		h.endConversation(chat)
		return
	}
	h.reply(chat, fmt.Sprintf(
		"✅ 已加入广播队列（%d 个客户，#%d）\n各客户 agent 心跳后会推送给客户",
		bc.TargetCount, bc.ID,
	))
	h.endConversation(chat)
	h.sendNotifyMenu(chat, 0)
}

// editBroadcast 重新进入对应模板的填写流程
func (h *Handler) editBroadcast(chat int64) {
	h.convsMu.Lock()
	c, ok := h.convs[chat]
	h.convsMu.Unlock()
	if !ok {
		h.startNotifyAll(chat, 0)
		return
	}
	template, _ := c.data["preview_template"].(string)
	h.endConversation(chat)
	h.startBroadcastTemplate(chat, 0, template)
}

// startBroadcastTemplate 模板入口：根据 template 决定走哪个对话流
func (h *Handler) startBroadcastTemplate(chat int64, msgID int, template string) {
	switch template {
	case model.BroadcastTplMaintenance:
		h.startConversation(chat, "bcast_maintenance", "duration")
		h.sendOrEdit(chat, msgID,
			"🛠 <b>维护通知 · 步骤 1/2</b>\n\n请输入预计停机时长（分钟，整数）：",
			&cancelKB)

	case model.BroadcastTplMaintenanceDone:
		// 无需输入，直接预览
		c := h.startConversation(chat, "broadcast_preview", "preview")
		title := "✅ 维护结束"
		content := "✅ 系统维护结束\n\n服务已全部恢复，所有功能可正常使用。\n\n感谢您的耐心等待 🙏"
		h.enterBroadcastPreview(chat, c, model.BroadcastTplMaintenanceDone, title, content)

	case model.BroadcastTplVersionPreview:
		h.startConversation(chat, "bcast_version_preview", "version")
		h.sendOrEdit(chat, msgID,
			"🚀 <b>重大版本预告 · 步骤 1/2</b>\n\n请输入版本号（例如 v0.14）：",
			&cancelKB)

	case model.BroadcastTplUpgradeFailed:
		h.startConversation(chat, "bcast_upgrade_failed", "version")
		h.sendOrEdit(chat, msgID,
			"⚠️ <b>升级失败告警 · 步骤 1/2</b>\n\n请输入受影响的版本号（例如 v0.14）：",
			&cancelKB)

	case model.BroadcastTplCustom:
		h.startConversation(chat, "bcast_custom", "content")
		h.sendOrEdit(chat, msgID,
			"✏️ <b>自定义广播</b>\n\n请输入完整文本（支持多行）：",
			&cancelKB)

	default:
		h.reply(chat, "未知模板: "+template)
	}
}

// ---------------- 各模板的对话推进 ----------------

func (h *Handler) advanceMaintenance(chat int64, c *conversation, text string) {
	switch c.step {
	case "duration":
		mins, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || mins <= 0 || mins > 1440 {
			h.reply(chat, "请输入 1-1440 之间的整数（分钟）：")
			return
		}
		c.data["duration"] = mins
		c.step = "reason"
		h.sendOrEdit(chat, 0,
			"🛠 <b>维护通知 · 步骤 2/2</b>\n\n请输入维护原因（例如\"升级到 v0.14\"），不写发 -：",
			&cancelKB)

	case "reason":
		reason := strings.TrimSpace(text)
		if reason == "-" {
			reason = "系统升级"
		}
		mins := c.data["duration"].(int)
		title := fmt.Sprintf("🛠 系统维护通知（%d 分钟）", mins)
		content := fmt.Sprintf(
			"🛠 系统维护通知\n\n"+
				"原因：%s\n"+
				"预计耗时：%d 分钟\n\n"+
				"维护期间 Bot 将暂时无响应，请稍候。\n———\n感谢理解 🙏",
			reason, mins,
		)
		h.enterBroadcastPreview(chat, c, model.BroadcastTplMaintenance, title, content)
	}
}

func (h *Handler) advanceVersionPreview(chat int64, c *conversation, text string) {
	switch c.step {
	case "version":
		ver := strings.TrimSpace(text)
		if !strings.HasPrefix(ver, "v") {
			h.reply(chat, "版本号必须以 v 开头：")
			return
		}
		c.data["version"] = ver
		c.step = "eta"
		h.sendOrEdit(chat, 0,
			"🚀 <b>重大版本预告 · 步骤 2/2</b>\n\n请输入预计发布时间（例如\"今晚 22:00\"）：",
			&cancelKB)

	case "eta":
		eta := strings.TrimSpace(text)
		if eta == "" {
			h.reply(chat, "时间不能为空：")
			return
		}
		ver := c.data["version"].(string)
		title := fmt.Sprintf("🚀 重大版本 %s 即将发布", ver)
		content := fmt.Sprintf(
			"🚀 重大版本预告\n\n"+
				"版本号：%s\n"+
				"预计发布：%s\n\n"+
				"届时系统会自动升级，无需操作。\n如遇异常会另行通知。",
			ver, eta,
		)
		h.enterBroadcastPreview(chat, c, model.BroadcastTplVersionPreview, title, content)
	}
}

func (h *Handler) advanceUpgradeFailed(chat int64, c *conversation, text string) {
	switch c.step {
	case "version":
		ver := strings.TrimSpace(text)
		if ver == "" {
			h.reply(chat, "版本号不能为空：")
			return
		}
		c.data["version"] = ver
		c.step = "summary"
		h.sendOrEdit(chat, 0,
			"⚠️ <b>升级失败告警 · 步骤 2/2</b>\n\n请简述问题（一句话）：",
			&cancelKB)

	case "summary":
		summary := strings.TrimSpace(text)
		if summary == "" {
			h.reply(chat, "问题描述不能为空：")
			return
		}
		ver := c.data["version"].(string)
		title := fmt.Sprintf("⚠️ %s 升级遇到问题", ver)
		content := fmt.Sprintf(
			"⚠️ 升级异常通知\n\n"+
				"受影响版本：%s\n"+
				"问题：%s\n\n"+
				"我们正在处理中，预计很快恢复。\n———\n如服务不可用，可手动重启或等待自动恢复。",
			ver, summary,
		)
		h.enterBroadcastPreview(chat, c, model.BroadcastTplUpgradeFailed, title, content)
	}
}

func (h *Handler) advanceCustomBroadcast(chat int64, c *conversation, text string) {
	if strings.TrimSpace(text) == "" {
		h.reply(chat, "内容不能为空：")
		return
	}
	title := firstLine(text, 50)
	h.enterBroadcastPreview(chat, c, model.BroadcastTplCustom, title, text)
}

// countEnabledCustomers 统计启用客户数（预览时显示用）
func (h *Handler) countEnabledCustomers() int {
	cs, err := h.store.ListCustomers()
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range cs {
		if c.Enabled {
			n++
		}
	}
	return n
}
