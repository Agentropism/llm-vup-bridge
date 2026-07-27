package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client 调用 synapse-tts 服务的 HTTP 客户端
type Client struct {
	baseURL string
	http    *http.Client
}

type SpeakRequest struct {
	Text   string `json:"text"`
	Voice  string `json:"voice,omitempty"`
	Rate   string `json:"rate,omitempty"`
	Volume string `json:"volume,omitempty"`
	Pitch  string `json:"pitch,omitempty"`
}

type StatusResponse struct {
	Speaking bool   `json:"speaking"`
	Pending  int    `json:"pending"`
	Engine   string `json:"engine"`
	Player   string `json:"player"`
	Voice    string `json:"voice"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Speak 将文本加入 TTS 播放队列（非阻塞）
func (c *Client) Speak(ctx context.Context, text string) error {
	return c.SpeakWithOpts(ctx, SpeakRequest{Text: text})
}

// SpeakWithOpts 带参数加入播放队列
func (c *Client) SpeakWithOpts(ctx context.Context, req SpeakRequest) error {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/speak", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("tts: build request failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("tts: speak request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tts: speak returned status %d", resp.StatusCode)
	}
	return nil
}

// Stop 停止当前播放并清空队列
func (c *Client) Stop(ctx context.Context) error {
	return c.post(ctx, "/stop")
}

// Skip 跳过当前播放
func (c *Client) Skip(ctx context.Context) error {
	return c.post(ctx, "/skip")
}

// Status 查询 TTS 服务状态
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var st StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
