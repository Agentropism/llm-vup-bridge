package provider

import (
	"strings"
	"testing"
)

func TestClassifyTTSEndpoints(t *testing.T) {
	// 用户实际踩到的坑：把 MiMo TTS 的地址填进了模型配置
	cases := []struct {
		endpoint string
		wantName string
	}{
		{"https://api.xiaomimimo.com/v1", "小米 MiMo TTS"},
		{"https://api.minimaxi.com/v1", "MiniMax 语音"},
		{"https://api.elevenlabs.io/v1", "ElevenLabs"},
		{"https://speech.platform.bing.com/tts", ""},
		{"http://localhost:9527/tts", ""},
	}
	for _, c := range cases {
		kind, name := ClassifyEndpoint(c.endpoint)
		if kind != KindTTS {
			t.Errorf("%s 应识别为 TTS, got %s", c.endpoint, kind)
		}
		if c.wantName != "" && name != c.wantName {
			t.Errorf("%s 服务名 = %q, want %q", c.endpoint, name, c.wantName)
		}
	}
}

func TestClassifyChatEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"https://api.deepseek.com/v1",
		"https://opencode.ai/zen/go/v1",
		"https://api.openai.com/v1",
	} {
		kind, name := ClassifyEndpoint(endpoint)
		if kind != KindChat {
			t.Errorf("%s 应识别为 chat, got %s", endpoint, kind)
		}
		if name == "" {
			t.Errorf("%s 应给出服务名", endpoint)
		}
	}
}

func TestClassifyUnknownAndEmpty(t *testing.T) {
	if kind, _ := ClassifyEndpoint(""); kind != KindUnknown {
		t.Errorf("空地址应为 unknown, got %s", kind)
	}
	if kind, _ := ClassifyEndpoint("http://127.0.0.1:11434/v1"); kind != KindUnknown {
		t.Errorf("本地 Ollama 应为 unknown（不能误判）, got %s", kind)
	}
	if kind, _ := ClassifyEndpoint("https://api.example.com/v1/embeddings"); kind != KindEmbedding {
		t.Errorf("embeddings 应单独分类, got %s", kind)
	}
}

func TestTTSMisconfigMessage(t *testing.T) {
	msg := TTSMisconfig("https://api.xiaomimimo.com/v1")
	if msg == "" {
		t.Fatal("TTS 地址必须给出纠正提示")
	}
	// 提示要同时说清「填到哪」和「该改成什么」
	for _, want := range []string{"TTS", "Mimo TTS 配置", "DeepSeek"} {
		if !strings.Contains(msg, want) {
			t.Errorf("提示缺少关键信息 %q: %s", want, msg)
		}
	}

	if got := TTSMisconfig("https://api.deepseek.com/v1"); got != "" {
		t.Errorf("正常对话接口不应报警: %s", got)
	}
	if got := TTSMisconfig("http://127.0.0.1:11434/v1"); got != "" {
		t.Errorf("本地自定义接口不应报警: %s", got)
	}
}
