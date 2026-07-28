package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type LLMConfig struct {
	Endpoint string // "https://api.openai.com/v1"
	Model    string // "gpt-4o-mini"
	APIKey   string
	//可自行添加更多
}

type AnalysisResult struct {
	IntentType  string  `json:"intent_type"`
	Emotion     string  `json:"emotion"`
	Intensity   float64 `json:"intensity"`
	ReplyText   string  `json:"reply_text"`
	ShouldSpeak bool    `json:"should_speak"`
	ShouldReply bool    `json:"should_reply"`
	TTSEnabled  bool    `json:"tts_enabled"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Analyze 调 LLM 分析一条用户消息。永远不返回 nil result值，解析失败时返回安全默认值。
//后续会可能会使用备份LLM中心，目前为猜想
func Analyze(ctx context.Context, userMsg, userName string, cfg LLMConfig) (*AnalysisResult, error) {
	systemPrompt := loadPrompt()

	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fmt.Sprintf("[用户:%s] %s", userName, userMsg)},
		},
		"temperature": 0.7,
		"max_tokens":  1024,
	}

	reqBody, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint+"/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return safeDefault(), fmt.Errorf("LLM 请求失败: %w", err)
	}
	defer resp.Body.Close()

	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return safeDefault(), fmt.Errorf("LLM 响应解析失败: %w", err)
	}

	content := llmResp.Choices[0].Message.Content
	content = stripMarkdownFence(content)

	var result AnalysisResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return safeDefault(), fmt.Errorf("JSON 解析失败: %w", err)
	}
	return &result, nil
}

func safeDefault() *AnalysisResult {
	return &AnalysisResult{IntentType: "chat", Emotion: "neutral"}
}

func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
