package vtuber

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client 调用 LLM-Vup (Open-LLM-VTuber) /inject 端点的 HTTP 客户端
type Client struct {
	baseURL string
	http    *http.Client
}

// SpeakRequest 注入给 VTuber 前端的回复
type SpeakRequest struct {
	Text      string  `json:"text"`
	Emotion   string  `json:"emotion,omitempty"`
	Intensity float64 `json:"intensity"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		// /inject 会等 TTS 合成完才返回，长文本可能较慢，超时给宽一些
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Speak 将文本注入 VTuber 前端播报（音频合成、口型、表情由 LLM-Vup 完成）
func (c *Client) Speak(ctx context.Context, req SpeakRequest) error {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/inject", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vtuber: build request failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("vtuber: speak request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vtuber: inject returned status %d", resp.StatusCode)
	}
	return nil
}
