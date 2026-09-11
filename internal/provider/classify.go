package provider

import "strings"

// EndpointKind 接口用途分类。
type EndpointKind string

const (
	KindChat      EndpointKind = "chat"      // 对话/补全模型（LLM 分析用）
	KindTTS       EndpointKind = "tts"       // 语音合成（应配到 mimo.api_key）
	KindEmbedding EndpointKind = "embedding" // 向量化
	KindUnknown   EndpointKind = "unknown"   // 未识别
)

// ttsHosts 已知的语音合成服务域名。
// 把 TTS 接口填进"模型配置"是最容易犯的错：形状一样（都是 OpenAI 兼容 /v1），
// 但用途完全不同——用它做意图分析只会得到解析失败。
var ttsHosts = []struct {
	host string
	name string
}{
	{"xiaomimimo.com", "小米 MiMo TTS"},
	{"minimaxi.com", "MiniMax 语音"},
	{"minimax.io", "MiniMax 语音"},
	{"volces.com", "火山引擎语音"},
	{"xfyun.cn", "讯飞语音"},
	{"ttsonline", "TTS 在线服务"},
	{"speech.tencentcloudapi.com", "腾讯云语音"},
	{"azure.com", "Azure 语音"},
	{"elevenlabs.io", "ElevenLabs"},
	{"openai.com/audio", "OpenAI 音频"},
}

// ttsPathHints 路径中出现这些词基本可以判定是语音接口。
var ttsPathHints = []string{"/tts", "/audio/speech", "/speech", "/voice", "/synthesize"}

// ClassifyEndpoint 判断接口用途，并给出人类可读的服务名（未识别时为空）。
func ClassifyEndpoint(endpoint string) (EndpointKind, string) {
	e := strings.ToLower(strings.TrimSpace(endpoint))
	if e == "" {
		return KindUnknown, ""
	}

	for _, h := range ttsHosts {
		if strings.Contains(e, h.host) {
			return KindTTS, h.name
		}
	}
	for _, p := range ttsPathHints {
		if strings.Contains(e, p) {
			return KindTTS, ""
		}
	}
	if strings.Contains(e, "/embeddings") {
		return KindEmbedding, ""
	}

	if key := MatchEndpoint(strings.TrimSpace(endpoint)); key != "" {
		if preset, ok := ByKey(key); ok {
			return KindChat, preset.Name
		}
	}
	return KindUnknown, ""
}

// TTSMisconfig 当模型配置指向了语音接口时，返回面向用户的纠正说明。
// 返回空串表示没有发现问题。
func TTSMisconfig(endpoint string) string {
	kind, name := ClassifyEndpoint(endpoint)
	if kind != KindTTS {
		return ""
	}
	who := name
	if who == "" {
		who = "一个语音合成（TTS）接口"
	}
	return "这个地址（" + who + "）是语音合成接口，不是对话模型接口。" +
		"用它做意图分析会一直失败（模型不会返回要求的 JSON）。" +
		"如果这是给试听用的 TTS Key，请填到下方「Mimo TTS 配置」里；" +
		"模型配置请改选 DeepSeek / OpenCode Go / OpenAI。"
}
