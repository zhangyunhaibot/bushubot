// Package bot 是给 agent 用客户 Bot Token 发消息的极薄封装。
//
// 注意: 我们不在 agent 里跑 getUpdates / 监听命令——
// 因为同一个 Bot Token 被两个进程长轮询会冲突，业务消息会丢。
// 客户 Bot 由 tgfulibot 主程序独占接消息；agent 只用 SendMessage 推通知。
package bot

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Handler struct {
	bot     *tgbotapi.BotAPI
	ownerID int64
}

func New(token string, ownerID int64) (*Handler, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("初始化客户 Bot 失败: %w", err)
	}
	return &Handler{bot: api, ownerID: ownerID}, nil
}

// SendNotification 用客户 Bot Token 调 SendMessage 推送给客户本人。
// 注意：这里走的是 HTTP API，跟 tgfulibot 的 getUpdates 不冲突。
func (h *Handler) SendNotification(text string) error {
	if h.ownerID == 0 || text == "" {
		return nil
	}
	_, err := h.bot.Send(tgbotapi.NewMessage(h.ownerID, text))
	return err
}
