package bot

import (
	"crypto/rsa"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bushubot-master/internal/license"
	"bushubot-master/internal/model"
	"bushubot-master/internal/store"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// releaseVersionRegex 校验版本号格式：v + 1~4 段数字（如 v0.49 / v1.10 / v1.10.3）
var releaseVersionRegex = regexp.MustCompile(`^v\d+(\.\d+){0,3}$`)

type Handler struct {
	bot         *tgbotapi.BotAPI
	store       *store.Store
	adminID     int64
	priv        *rsa.PrivateKey
	defaultDays int

	// 多步对话状态：chat_id → 当前对话
	convs   map[int64]*conversation
	convsMu sync.Mutex
}

func New(token string, adminID int64, s *store.Store, priv *rsa.PrivateKey, defaultDays int) (*Handler, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("初始化主 Bot 失败: %w", err)
	}
	return &Handler{
		bot: api, store: s, adminID: adminID,
		priv: priv, defaultDays: defaultDays,
		convs: make(map[int64]*conversation),
	}, nil
}

func (h *Handler) Run(stop <-chan struct{}) {
	uc := tgbotapi.NewUpdate(0)
	uc.Timeout = 30
	updates := h.bot.GetUpdatesChan(uc)
	log.Printf("master Bot 已就绪")

	for {
		select {
		case <-stop:
			h.bot.StopReceivingUpdates()
			return
		case u := <-updates:
			// 1) Callback 按钮点击
			if u.CallbackQuery != nil {
				if u.CallbackQuery.From.ID != h.adminID {
					h.ack(u.CallbackQuery.ID, "❌ 无权使用")
					continue
				}
				h.handleCallback(u.CallbackQuery)
				continue
			}
			// 2) 普通文本消息
			if u.Message == nil {
				continue
			}
			if u.Message.From.ID != h.adminID {
				h.reply(u.Message.Chat.ID, "❌ 你无权使用本 Bot")
				continue
			}
			// 优先：多步对话进行中
			if h.isInConversation(u.Message.Chat.ID) {
				h.advanceConversation(u.Message)
				continue
			}
			// 命令路由
			if u.Message.IsCommand() {
				h.dispatch(u.Message)
				continue
			}
			// 任意普通消息 → 显示主菜单
			h.sendMainMenu(u.Message.Chat.ID)
		}
	}
}

func (h *Handler) dispatch(m *tgbotapi.Message) {
	cmd := strings.ToLower(m.Command())
	args := strings.TrimSpace(m.CommandArguments())
	chat := m.Chat.ID

	switch cmd {
	case "start":
		h.sendMainMenu(chat)
	case "help":
		h.reply(chat,
			"📋 命令列表（也可以发任意消息打开按钮菜单）\n\n"+
				"<b>客户管理</b>\n"+
				"/list — 客户列表\n"+
				"/info <名字> — 查看客户\n"+
				"/add <名字>|<TG_ID>|<bot_token>[|<备注>] — 新增客户\n"+
				"/disable <名字> — 停用客户\n"+
				"/enable <名字> — 恢复客户\n\n"+
				"<b>远程运维（下次心跳生效，≤60s）</b>\n"+
				"/restart <名字> — 让客户服务器重启 tgfulibot\n"+
				"/update_now <名字> — 让客户立即拉最新版（不等下次心跳）\n\n"+
				"<b>版本管理</b>\n"+
				"/release <版本> [说明] — 发布 TGfulibot 新版本\n"+
				"/set_agent_version <版本> — 设置最新 agent 版本\n"+
				"/set_min_agent <版本> — 设置最低支持的 agent 版本（强制升级）\n"+
				"/version — 查看当前版本配置\n\n"+
				"<b>License</b>\n"+
				"/issue <名字> [天数] — 给已有客户重新签发 license\n"+
				"/pubkey — 打印公钥（拷贝到 TGfulibot 项目内嵌）\n\n"+
				"<b>通知</b>\n"+
				"/notify all|<名字> <消息> — 推送通知")

	case "list":
		h.cmdList(chat)
	case "info":
		h.cmdInfo(chat, args)
	case "add":
		h.cmdAdd(chat, args)
	case "release":
		h.cmdRelease(chat, args)
	case "notify":
		h.cmdNotify(chat, args)
	case "disable":
		h.cmdToggle(chat, args, false)
	case "enable":
		h.cmdToggle(chat, args, true)
	case "set_agent_version":
		h.cmdSetSetting(chat, model.SettingLatestAgentVersion, args, "✅ 最新 agent 版本已设为 ")
	case "set_min_agent":
		h.cmdSetSetting(chat, model.SettingMinSupportedAgentVersion, args, "✅ 最低支持 agent 版本已设为 ")
	case "version":
		h.cmdVersion(chat)
	case "issue":
		h.cmdIssue(chat, args)
	case "pubkey":
		h.cmdPubkey(chat)
	case "restart":
		h.cmdSendCommand(chat, args, "restart_service", "🔄 已下发重启指令给 ")
	case "update_now":
		h.cmdSendCommand(chat, args, "force_update", "⚡ 已下发强制更新指令给 ")
	default:
		h.reply(chat, "未知命令: /"+cmd)
	}
}

// ---------------- commands ----------------

func (h *Handler) cmdList(chat int64) {
	customers, err := h.store.ListCustomers()
	if err != nil {
		h.reply(chat, "查询失败: "+err.Error())
		return
	}
	if len(customers) == 0 {
		h.reply(chat, "暂无客户。用 /add 添加。")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👥 客户列表（共 %d 个）\n\n", len(customers)))
	for _, c := range customers {
		state := "🟢"
		if !c.Enabled {
			state = "🔴 停用"
		}
		hb := "从未心跳"
		if c.LastHeartbeatAt != nil {
			hb = c.LastHeartbeatAt.Format("01-02 15:04")
		}
		sb.WriteString(fmt.Sprintf("%s %s | v=%s | ip=%s | %s\n",
			state, htmlEsc(c.Name),
			htmlEsc(defaultStr(c.CurrentVersion, "?")),
			htmlEsc(defaultStr(c.ServerIP, "?")),
			hb))
	}
	h.reply(chat, sb.String())
}

func (h *Handler) cmdInfo(chat int64, name string) {
	if name == "" {
		h.reply(chat, "用法: /info <名字>")
		return
	}
	c, err := h.store.FindByName(name)
	if err != nil || c == nil {
		h.reply(chat, "找不到客户: "+htmlEsc(name))
		return
	}
	hb := "从未心跳"
	if c.LastHeartbeatAt != nil {
		hb = c.LastHeartbeatAt.Format("2006-01-02 15:04:05")
	}
	h.reply(chat, fmt.Sprintf(
		"📌 %s\n"+
			"• TG ID: %d\n"+
			"• license: %s\n"+
			"• 服务器 IP: %s\n"+
			"• 当前版本: %s\n"+
			"• 最后心跳: %s\n"+
			"• 状态: %s\n"+
			"• 备注: %s",
		htmlEsc(c.Name), c.TGUserID, htmlEsc(c.LicenseKey),
		htmlEsc(defaultStr(c.ServerIP, "?")),
		htmlEsc(defaultStr(c.CurrentVersion, "?")),
		hb, enabledLabel(c.Enabled), htmlEsc(defaultStr(c.Note, "-")),
	))
}

func (h *Handler) cmdAdd(chat int64, args string) {
	parts := strings.SplitN(args, "|", 4)
	if len(parts) < 3 {
		h.reply(chat, "用法: /add 名字|TG_ID|bot_token[|备注]")
		return
	}
	name := strings.TrimSpace(parts[0])
	tgID, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		h.reply(chat, "TG_ID 必须是数字")
		return
	}
	botToken := strings.TrimSpace(parts[2])
	note := ""
	if len(parts) == 4 {
		note = strings.TrimSpace(parts[3])
	}
	c, err := h.store.CreateCustomer(name, tgID, botToken, note)
	if err != nil {
		h.reply(chat, "创建失败: "+err.Error())
		return
	}

	// 自动签发默认天数的 license token
	tok, expires, err := h.signNewToken(c, h.defaultDays)
	if err != nil {
		h.reply(chat, "客户已创建，但签发 license 失败: "+err.Error())
		return
	}

	cloudInit := buildCloudInit(tok, botToken, tgID)
	sshCmd := buildSSHCommand(tok, botToken, tgID)

	h.reply(chat, fmt.Sprintf(
		"✅ 已创建客户 <b>%s</b>（有效期至 %s）\n\n"+
			"<b>方式 1 · Cloud-init</b>（创建新服务器时贴到\"用户数据\"栏）：\n"+
			"<pre>%s</pre>\n"+
			"<b>方式 2 · SSH 手工执行</b>（已有服务器，登录后粘贴）：\n"+
			"<pre>%s</pre>\n"+
			"<b>授权 token</b>（备查）：\n<code>%s</code>\n\n"+
			"💡 部署完成后约 5 分钟会自动收到上线通知。",
		htmlEsc(c.Name), expires.Format("2006-01-02"),
		htmlEsc(cloudInit), htmlEsc(sshCmd), htmlEsc(tok),
	))
}

// buildSSHCommand 生成给已有服务器 SSH 直接执行的命令
func buildSSHCommand(licenseToken, botToken string, adminID int64) string {
	return fmt.Sprintf(`curl -fsSL https://install.bushubot.example.com/install.sh | sudo bash -s -- \
  --license '%s' \
  --bot-token '%s' \
  --admin-id %d`, licenseToken, botToken, adminID)
}

// buildCloudInit 生成给云厂商"用户数据"栏粘贴的 cloud-init 脚本（仅多一行 shebang）
func buildCloudInit(licenseToken, botToken string, adminID int64) string {
	return "#!/bin/bash\n" + buildSSHCommand(licenseToken, botToken, adminID) + "\n"
}

// signNewToken 给客户签发新的 license token，并把 license_id 写回 customers.license_key 字段
// days 由调用方传入，避免改 h.defaultDays 这种共享状态做单次 override（非线程安全）
func (h *Handler) signNewToken(c *model.Customer, days int) (string, time.Time, error) {
	if h.priv == nil {
		return "", time.Time{}, fmt.Errorf("master 私钥未加载")
	}
	now := time.Now()
	expires := now.AddDate(0, 0, days)
	licenseID := "lic_" + c.LicenseKey[:16] + "_" + strconv.FormatInt(now.Unix(), 36)

	tok, err := license.Sign(license.Payload{
		LicenseID:    licenseID,
		CustomerID:   c.ID,
		CustomerName: c.Name,
		IssuedAt:     now,
		ExpiresAt:    expires,
	}, h.priv)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, expires, nil
}

func (h *Handler) cmdIssue(chat int64, args string) {
	parts := strings.Fields(args)
	if len(parts) < 1 {
		h.reply(chat, "用法: /issue <名字> [天数]")
		return
	}
	name := parts[0]
	days := h.defaultDays
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
			days = n
		}
	}

	c, err := h.store.FindByName(name)
	if err != nil || c == nil {
		h.reply(chat, "找不到客户: "+htmlEsc(name))
		return
	}

	tok, expires, err := h.signNewToken(c, days)
	if err != nil {
		h.reply(chat, "签发失败: "+err.Error())
		return
	}
	h.reply(chat, fmt.Sprintf(
		"✅ 已为 %s 重新签发 license（有效期至 %s，%d 天）\n\n"+
			"<b>新 token</b>:\n<code>%s</code>\n\n"+
			"客户需要在 config.json 中替换 license.token，然后重启 tgfulibot",
		htmlEsc(c.Name), expires.Format("2006-01-02"), days, htmlEsc(tok),
	))
}

func (h *Handler) cmdPubkey(chat int64) {
	if h.priv == nil {
		h.reply(chat, "master 私钥未加载")
		return
	}
	pem, err := license.PublicKeyPEM(&h.priv.PublicKey)
	if err != nil {
		h.reply(chat, "导出公钥失败: "+err.Error())
		return
	}
	h.reply(chat, fmt.Sprintf(
		"📋 master 公钥（请拷贝到 TGfulibot 项目，编译时内嵌）:\n\n<pre>%s</pre>",
		htmlEsc(string(pem)),
	))
}

func (h *Handler) cmdRelease(chat int64, args string) {
	if args == "" {
		h.reply(chat, "用法: /release <版本> [说明]")
		return
	}
	parts := strings.SplitN(args, " ", 2)
	version := strings.TrimSpace(parts[0])
	notes := ""
	if len(parts) == 2 {
		notes = strings.TrimSpace(parts[1])
	}
	if !releaseVersionRegex.MatchString(version) {
		h.reply(chat, "版本号格式不对，应形如 v0.49 / v1.10 / v1.10.3")
		return
	}
	if _, err := h.store.PublishRelease(version, notes); err != nil {
		h.reply(chat, "发布失败: "+err.Error())
		return
	}
	// 自动给所有客户排队一条更新通知
	msg := fmt.Sprintf("📦 新版本 %s 已发布", htmlEsc(version))
	if notes != "" {
		msg += "\n\n" + notes
	}
	_ = h.store.BroadcastNotification("update_available", version, msg)
	h.reply(chat, "✅ 已发布 "+htmlEsc(version)+"，所有客户将在下次心跳收到通知并自动更新")
}

func (h *Handler) cmdNotify(chat int64, args string) {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		h.reply(chat, "用法: /notify all|<名字> <消息>")
		return
	}
	target, msg := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if target == "all" {
		_ = h.store.BroadcastNotification("manual", "", msg)
		h.reply(chat, "✅ 已广播给所有启用客户")
		return
	}
	c, err := h.store.FindByName(target)
	if err != nil || c == nil {
		h.reply(chat, "找不到客户: "+htmlEsc(target))
		return
	}
	if err := h.store.NotifyCustomer(c.ID, "manual", msg); err != nil {
		h.reply(chat, "失败: "+err.Error())
		return
	}
	h.reply(chat, "✅ 已发送给 "+htmlEsc(target))
}

func (h *Handler) cmdSetSetting(chat int64, key, value, okPrefix string) {
	value = strings.TrimSpace(value)
	if value == "" {
		h.reply(chat, "用法: /"+key+" <版本号>，传空字符串可清除")
		return
	}
	if value == "-" || value == "clear" {
		value = ""
	}
	if err := h.store.SetSetting(key, value); err != nil {
		h.reply(chat, "失败: "+err.Error())
		return
	}
	h.reply(chat, okPrefix+htmlEsc(value))
}

func (h *Handler) cmdVersion(chat int64) {
	latestRel, _ := h.store.GetLatestRelease()
	latestAgent, _ := h.store.GetSetting(model.SettingLatestAgentVersion)
	minAgent, _ := h.store.GetSetting(model.SettingMinSupportedAgentVersion)
	repo, _ := h.store.GetSetting(model.SettingAgentReleaseRepo)

	tgVer := "(未发布)"
	if latestRel != nil {
		tgVer = latestRel.Version
	}
	h.reply(chat, fmt.Sprintf(
		"📦 当前版本配置\n\n"+
			"• TGfulibot 最新版: %s\n"+
			"• Agent 最新版:    %s\n"+
			"• Agent 最低支持版: %s\n"+
			"• Agent 仓库:      %s",
		tgVer,
		defaultStr(latestAgent, "(未设置)"),
		defaultStr(minAgent, "(不强制)"),
		defaultStr(repo, "(未设置)"),
	))
}

// cmdSendCommand 通过 notifications 表给某客户下发一条指令型通知
// agent 在下次心跳后拉到，根据 type 执行（重启服务 / 强制更新等）
func (h *Handler) cmdSendCommand(chat int64, name, typ, okPrefix string) {
	name = strings.TrimSpace(name)
	if name == "" {
		h.reply(chat, "用法: /restart|/update_now <客户名>")
		return
	}
	c, err := h.store.FindByName(name)
	if err != nil || c == nil {
		h.reply(chat, "找不到客户: "+htmlEsc(name))
		return
	}
	if !c.Enabled {
		h.reply(chat, "⚠️ 客户 "+htmlEsc(name)+" 已停用，先 /enable 再操作")
		return
	}
	if err := h.store.NotifyCustomer(c.ID, typ, ""); err != nil {
		h.reply(chat, "下发失败: "+err.Error())
		return
	}
	h.reply(chat, okPrefix+htmlEsc(name)+"（≤60 秒后生效，结果会上报到 agent_events）")
}

func (h *Handler) cmdToggle(chat int64, name string, enabled bool) {
	if name == "" {
		h.reply(chat, "请指定客户名")
		return
	}
	c, err := h.store.FindByName(name)
	if err != nil || c == nil {
		h.reply(chat, "找不到客户: "+htmlEsc(name))
		return
	}
	if err := h.store.SetEnabled(c.ID, enabled); err != nil {
		h.reply(chat, "失败: "+err.Error())
		return
	}
	if enabled {
		h.reply(chat, "✅ 已恢复 "+htmlEsc(name))
	} else {
		h.reply(chat, "🛑 已停用 "+htmlEsc(name)+"（下次心跳后客户服务会停止）")
	}
}

// ---------------- helpers ----------------

func (h *Handler) reply(chat int64, text string) {
	msg := tgbotapi.NewMessage(chat, text)
	msg.ParseMode = "HTML"
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("回复失败: %v", err)
	}
}

// NotifyAdmin 给 admin 主动发消息（alerter 等系统组件用）
func (h *Handler) NotifyAdmin(text string) error {
	if h.adminID == 0 {
		return nil
	}
	msg := tgbotapi.NewMessage(h.adminID, text)
	msg.ParseMode = "HTML"
	_, err := h.bot.Send(msg)
	return err
}

// SendLogDocument 给 admin 发一个 .log 附件（日志太长时用）
func (h *Handler) SendLogDocument(filename string, content []byte) error {
	if h.adminID == 0 {
		return nil
	}
	doc := tgbotapi.NewDocument(h.adminID, tgbotapi.FileBytes{
		Name:  filename,
		Bytes: content,
	})
	_, err := h.bot.Send(doc)
	return err
}

// SendPhotoToChat 把 PNG 字节发到指定 chat
func (h *Handler) SendPhotoToChat(chat int64, filename, caption string, png []byte) error {
	photo := tgbotapi.NewPhoto(chat, tgbotapi.FileBytes{Name: filename, Bytes: png})
	photo.Caption = caption
	photo.ParseMode = "HTML"
	_, err := h.bot.Send(photo)
	return err
}

// ack 回复 callback 按钮的"等待小气泡"
func (h *Handler) ack(callbackID, text string) {
	cb := tgbotapi.NewCallback(callbackID, text)
	if _, err := h.bot.Request(cb); err != nil {
		log.Printf("ack 失败: %v", err)
	}
}

// sendOrEdit 发送或编辑消息（菜单内导航时用 edit 避免刷屏）
func (h *Handler) sendOrEdit(chat int64, msgID int, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chat, msgID, text)
		edit.ParseMode = "HTML"
		if kb != nil {
			edit.ReplyMarkup = kb
		}
		if _, err := h.bot.Send(edit); err != nil {
			log.Printf("编辑消息失败: %v", err)
		}
		return
	}
	msg := tgbotapi.NewMessage(chat, text)
	msg.ParseMode = "HTML"
	if kb != nil {
		msg.ReplyMarkup = *kb
	}
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("发送菜单失败: %v", err)
	}
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func enabledLabel(b bool) string {
	if b {
		return "🟢 启用"
	}
	return "🔴 停用"
}

// htmlEsc 给 Telegram HTML parse mode 做转义。
// 所有写入 <b>%s</b>/<code>%s</code>/<pre>%s</pre> 的动态字段必须经过它，
// 否则字段里的 < & 会让消息发送失败或导致 HTML 注入显示异常。
func htmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
