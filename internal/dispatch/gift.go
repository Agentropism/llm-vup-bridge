package dispatch

import (
	"fmt"

	"github.com/Agentropism/llm-vup-bridge/internal/model"
)

// GiftResponse 根据礼物类型生成感谢话术模板
type GiftResponse struct {
	Template string
	Priority Priority
}

// 礼物表达模板（按价值分级）
var giftTemplates = map[string][]string{
	"small": {
		"谢谢%s的小礼物！么么哒~",
		"收到%s的礼物啦，开心！",
		"感谢%s投喂~",
	},
	"medium": {
		"哇！%s送了个大礼物！太感谢了！",
		"%s好大方！谢谢你的礼物！",
		"收到%s的礼物了！爱你哟~",
	},
	"large": {
		"天呐！%s送了超级大礼！我都不知道说什么好了，太谢谢了！",
		"%s！！！这也太破费了！真的非常感谢！",
	},
	"sc": {
		"感谢%s的SC！让我看看写了什么~",
		"收到%s的SuperChat！太感谢支持了！",
	},
	"captain": {
		"欢迎%s上舰！以后就是一家人了！",
		"%s成为舰长了！感谢支持，我会继续努力的！",
	},
}

// BuildGiftReply 根据消息类型和内容构建礼物感谢文本
func BuildGiftReply(event model.UnifiedEvent) string {
	var category string
	switch event.MessageType {
	case "gift":
		category = classifyGift(event.Content)
	case "super_chat":
		category = "sc"
	case "captain":
		category = "captain"
	default:
		return ""
	}

	templates, ok := giftTemplates[category]
	if !ok || len(templates) == 0 {
		return fmt.Sprintf("谢谢%s的%s！", event.UserName, event.MessageType)
	}

	// 简单轮询选模板（用用户名 hash）
	idx := int(hashString(event.UserName)) % len(templates)
	return fmt.Sprintf(templates[idx], event.UserName)
}

// classifyGift 根据礼物内容判断价值等级
func classifyGift(content string) string {
	// 实际接入时根据礼物价格/名称判断
	// 这里用关键词简单分类
	highValue := []string{"火箭", "城堡", "星球", "嘉年华"}
	medValue := []string{"飞船", "摩天轮", "烟花", "告白"}

	for _, kw := range highValue {
		if contains(content, kw) {
			return "large"
		}
	}
	for _, kw := range medValue {
		if contains(content, kw) {
			return "medium"
		}
	}
	return "small"
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func hashString(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}
