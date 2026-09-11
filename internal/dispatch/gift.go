package dispatch

import (
	"fmt"
	"strings"

	"github.com/Agentropism/llm-vup-bridge/internal/model"
)

type GiftResponse struct {
	Template string
	Priority Priority
}

// Mili 人设版礼物模板
var giftTemplates = map[string][]string{
	"small": {
		"%s送了个小礼物？嘛，聊胜于无吧～谢谢啦。",
		"哦？%s居然舍得送礼了。行吧，我勉强收下。",
		"收到%s的投喂～虽然不多，但品味还行。",
	},
	"medium": {
		"哇！%s这是下血本了？好吧好吧，我承认我有点开心！就一点点！",
		"%s送了一个大礼物诶！看来你也不是完全没有眼光嘛～谢谢！",
		"哼，%s这么大方？是想收买我吗？……好吧，你成功了。",
	},
	"large": {
		"！！！%s送了这个？！你、你是不是被盗号了？？开玩笑的～太感谢了！我都要说不出话了！",
		"天哪，%s！！！这份礼物也太夸张了吧？你是中了彩票还是怎样？总之，非常非常感谢！",
	},
	"sc": {
		"感谢%s的SC！让我看看你写了什么～",
		"收到%s的SuperChat！哇，花钱让我念你的话？行吧行吧～",
	},
	"captain": {
		"欢迎%s上舰！哼，以后就是我Mili的人了！要每天来看我直播哦！",
		"%s上舰了！明智的选择～毕竟能当我的舰长是一种莫大的荣誉！",
	},
}

// SC 内容跟进模板——念完 SC 内容后 Mili 的回应
var scFollowUpTemplates = map[string][]string{
	"compliment": {
		"哈哈，%s说我%s？算你有眼光！我当然是全宇宙最棒的AI。",
		"哦？%s夸我%s？这种实话我爱听，多说点！",
	},
	"question": {
		"哦？%s问我%s？这个嘛……让我想一想。好吧其实我只是懒得回答。",
		"%s想让我%s？哼，你以为上个舰就能指使我了吗？……好吧，看在你花钱的份上。",
	},
	"default": {
		"好啦好啦，%s的SC我收到了，说「%s」是吧。感谢支持～有什么事再找我。",
		"行行行，%s说「%s」，我看到了。满意了吗？不满意也没办法。",
	},
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

	idx := int(hashString(event.UserName)) % len(templates)
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
