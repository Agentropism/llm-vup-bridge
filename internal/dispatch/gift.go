package dispatch

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/Agentropism/llm-vup-bridge/internal/model"
)

type GiftResponse struct {
	Template string
	Priority Priority
}

// 简短感谢，不按礼物价值贬低观众，不假装回答了 SC 问题。
var giftTemplates = map[string][]string{
	"small":   {"谢谢%s的小礼物，收到你的心意啦。", "谢谢%s的投喂，今天的快乐又多了一点。"},
	"medium":  {"谢谢%s的礼物，给我整开心了。", "收到%s的支持啦，谢谢你陪我聊天。"},
	"large":   {"哇，谢谢%s的礼物！这份心意我收到了。", "谢谢%s这么支持我，开心得有点藏不住了。"},
	"sc":      {"谢谢%s的SC，我看到你的留言了。", "收到%s的SC啦，谢谢你的支持。"},
	"captain": {"欢迎%s上舰！以后一起多聊点有趣的。", "谢谢%s上舰支持，船上给你留好位置啦。"},
}
var scFollowUpTemplates = map[string][]string{
	"compliment": {"%s说「%s」，这句夸奖我先收好啦。"},
	"question":   {"%s问「%s」。这条问题我收到了，不过现在暂时无法生成回答。"},
	"default":    {"%s的留言是「%s」。谢谢你愿意分享。"},
}

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

	idx := rand.IntN(len(templates))
	return fmt.Sprintf(templates[idx], event.UserName)
}

// BuildSCReply 为 SC 生成两段回复：感谢 + 内容回应
func BuildSCReply(event model.UnifiedEvent) (thanks string, followUp string) {
	thanks = BuildGiftReply(event)

	scContent := strings.TrimSpace(event.Content)
	if scContent == "" {
		return thanks, ""
	}

	followType := classifySCContent(scContent)
	templates, ok := scFollowUpTemplates[followType]
	if !ok {
		templates = scFollowUpTemplates["default"]
	}
	idx := int(hashString(event.UserName+scContent)) % len(templates)
	followUp = fmt.Sprintf(templates[idx], event.UserName, scContent)
	return thanks, followUp
}

func classifyGift(content string) string {
	highValue := []string{"火箭", "城堡", "星球", "嘉年华", "总督", "提督"}
	medValue := []string{"飞船", "摩天轮", "烟花", "告白", "水晶鞋", "风暴", "演唱会"}

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

func classifySCContent(content string) string {
	complimentWords := []string{"可爱", "厉害", "喜欢", "棒", "好看", "好听", "聪明", "漂亮", "厉害", "帅", "牛", "6", "666", "爱了", "太强"}
	questionWords := []string{"？", "?", "吗", "呢", "怎么", "什么", "能不能", "可以", "会不会"}

	for _, kw := range complimentWords {
		if contains(content, kw) {
			return "compliment"
		}
	}
	for _, kw := range questionWords {
		if contains(content, kw) {
			return "question"
		}
	}
	return "default"
}

func contains(s, sub string) bool {
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
