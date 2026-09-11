// Package provider 内置 LLM 服务商预设（endpoint + 常用模型），供调试台一键配置。
//
// 所有预设都走 OpenAI 兼容的 /chat/completions 协议，因此 distillery 无需按服务商分支。
package provider

// Preset 一个服务商预设。
type Preset struct {
	Key      string   `json:"key"`      // 内部标识
	Name     string   `json:"name"`     // 显示名
	Endpoint string   `json:"endpoint"` // OpenAI 兼容 base URL（不含 /chat/completions）
	Models   []string `json:"models"`   // 常用模型；可在界面上手工改成其它模型名
	KeyHint  string   `json:"key_hint"` // API Key 获取提示
	Note     string   `json:"note"`     // 补充说明
}

// Presets 内置预设列表（顺序即界面展示顺序）。
var Presets = []Preset{
	{
		Key:      "deepseek",
		Name:     "DeepSeek",
		Endpoint: "https://api.deepseek.com/v1",
		Models:   []string{"deepseek-chat", "deepseek-reasoner"},
		KeyHint:  "https://platform.deepseek.com/api_keys",
		Note:     "deepseek-chat 为通用对话，deepseek-reasoner 为推理模型（更慢、更贵）",
	},
	{
		Key:      "opencode-go",
		Name:     "OpenCode Go",
		Endpoint: "https://opencode.ai/zen/go/v1",
		Models: []string{
			"deepseek-v4-flash", "deepseek-v4-pro",
			"kimi-k2.7-code", "kimi-k2.6", "kimi-k2.5",
			"glm-5.2", "glm-5.1", "glm-5",
			"mimo-v2.5", "mimo-v2.5-pro", "mimo-v2-pro", "mimo-v2-omni",
			"hy3-preview",
		},
		KeyHint: "https://opencode.ai/auth",
		Note:    "订阅制（$10/月），与 OpenCode Zen 共用同一 API Key；模型列表见 GET /models",
	},
	{
		Key:      "openai",
		Name:     "OpenAI",
		Endpoint: "https://api.openai.com/v1",
		Models:   []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1-mini", "gpt-4.1", "o4-mini"},
		KeyHint:  "https://platform.openai.com/api-keys",
		Note:     "o 系列推理模型较慢，建议先用 gpt-4o-mini 跑通链路",
	},
}

// ByKey 按标识查找预设。
func ByKey(key string) (Preset, bool) {
	for _, p := range Presets {
		if p.Key == key {
			return p, true
		}
	}
	return Preset{}, false
}

// MatchEndpoint 反查 endpoint 对应的预设标识，未匹配返回空串（视为自定义）。
func MatchEndpoint(endpoint string) string {
	for _, p := range Presets {
		if p.Endpoint == endpoint {
			return p.Key
		}
	}
	return ""
}
