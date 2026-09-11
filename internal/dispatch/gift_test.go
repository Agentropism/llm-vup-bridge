package dispatch

import (
	"strings"
	"testing"

	"github.com/Agentropism/llm-vup-bridge/internal/model"
)

// 模板占位符必须与 fmt.Sprintf 传入的参数个数一致，
// 否则回复里会混入 %!(EXTRA string=...)（SC 默认模板曾有此问题）。
func TestGiftTemplatesPlaceholderCount(t *testing.T) {
	for category, templates := range giftTemplates {
		want := 1 // 礼物/SC/舰长模板只用 %s 填用户名
		for i, tpl := range templates {
			if got := strings.Count(tpl, "%s"); got != want {
				t.Errorf("giftTemplates[%q][%d] 有 %d 个 %%s, want %d: %s", category, i, got, want, tpl)
			}
		}
	}
	for category, templates := range scFollowUpTemplates {
		want := 2 // SC 跟进模板用 %s 填用户名 + SC 内容
		for i, tpl := range templates {
			if got := strings.Count(tpl, "%s"); got != want {
				t.Errorf("scFollowUpTemplates[%q][%d] 有 %d 个 %%s, want %d: %s", category, i, got, want, tpl)
			}
		}
	}
}

func TestBuildSCReplyNoFormatArtifacts(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"default 类内容", "随便说点什么"},
		{"夸赞类内容", "主播好厉害"},
		{"提问类内容", "这是什么歌"},
		{"空内容", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			event := model.UnifiedEvent{
				Platform: "bilibili", UserName: "老板B",
				Content: c.content, MessageType: "super_chat",
			}
			thanks, followUp := BuildSCReply(event)

			if thanks == "" {
				t.Error("感谢语不应为空")
			}
			for _, part := range []string{thanks, followUp} {
				if strings.Contains(part, "%!") {
					t.Errorf("回复含 fmt 错误标记: %s", part)
				}
			}
			if c.content != "" && followUp == "" {
				t.Error("非空 SC 内容应产生跟进回复")
			}
			if c.content != "" && !strings.Contains(followUp, c.content) {
				t.Errorf("跟进回复应包含 SC 内容 %q，实际: %s", c.content, followUp)
			}
		})
	}
}

func TestBuildGiftReplyTiers(t *testing.T) {
	// 高价值礼物走 large 模板，且必须插入用户名
	reply := BuildGiftReply(model.UnifiedEvent{
		UserName: "老板A", Content: "火箭", MessageType: "gift",
	})
	if reply == "" {
		t.Fatal("礼物回复不应为空")
	}
	if !strings.Contains(reply, "老板A") {
		t.Errorf("礼物回复应包含用户名，实际: %s", reply)
	}
	if strings.Contains(reply, "%!") {
		t.Errorf("礼物回复含 fmt 错误标记: %s", reply)
	}

	// 未知类型返回空串，由上层记为 "empty gift reply"
	if got := BuildGiftReply(model.UnifiedEvent{MessageType: "text"}); got != "" {
		t.Errorf("非礼物类型应返回空串, got %q", got)
	}
}
