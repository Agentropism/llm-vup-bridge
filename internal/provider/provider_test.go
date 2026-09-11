package provider

import "testing"

func TestPresetsCoverRequestedProviders(t *testing.T) {
	// 需求明确要求支持 DeepSeek / OpenCode Go / OpenAI
	for _, key := range []string{"deepseek", "opencode-go", "openai"} {
		p, ok := ByKey(key)
		if !ok {
			t.Fatalf("缺少预设 %q", key)
		}
		if p.Name == "" || p.Endpoint == "" || len(p.Models) == 0 {
			t.Errorf("预设 %q 字段不完整: %+v", key, p)
		}
		if p.KeyHint == "" {
			t.Errorf("预设 %q 缺少 API Key 获取提示", key)
		}
	}
}

func TestPresetEndpoints(t *testing.T) {
	// 端点写错会导致 404/连接失败，这里锁死约定值
	want := map[string]string{
		"deepseek":    "https://api.deepseek.com/v1",
		"opencode-go": "https://opencode.ai/zen/go/v1",
		"openai":      "https://api.openai.com/v1",
	}
	for key, endpoint := range want {
		p, ok := ByKey(key)
		if !ok {
			t.Fatalf("缺少预设 %q", key)
		}
		if p.Endpoint != endpoint {
			t.Errorf("%s endpoint = %q, want %q", key, p.Endpoint, endpoint)
		}
	}
}

func TestByKeyUnknown(t *testing.T) {
	if _, ok := ByKey("nope"); ok {
		t.Error("未知 key 不应命中预设")
	}
}

func TestMatchEndpoint(t *testing.T) {
	if got := MatchEndpoint("https://api.deepseek.com/v1"); got != "deepseek" {
		t.Errorf("MatchEndpoint(deepseek) = %q", got)
	}
	if got := MatchEndpoint("https://opencode.ai/zen/go/v1"); got != "opencode-go" {
		t.Errorf("MatchEndpoint(opencode-go) = %q", got)
	}
	if got := MatchEndpoint("http://127.0.0.1:11434/v1"); got != "" {
		t.Errorf("自定义端点应返回空串, got %q", got)
	}
}

func TestDefaultModelIsFirst(t *testing.T) {
	// 界面默认选中 Models[0]，因此顺序有意义：应是最稳妥的通用模型
	if p, _ := ByKey("deepseek"); p.Models[0] != "deepseek-chat" {
		t.Errorf("deepseek 默认模型 = %q, want deepseek-chat", p.Models[0])
	}
	if p, _ := ByKey("openai"); p.Models[0] != "gpt-4o-mini" {
		t.Errorf("openai 默认模型 = %q, want gpt-4o-mini", p.Models[0])
	}
	if p, _ := ByKey("opencode-go"); p.Models[0] != "deepseek-v4-flash" {
		t.Errorf("opencode-go 默认模型 = %q, want deepseek-v4-flash", p.Models[0])
	}
}
