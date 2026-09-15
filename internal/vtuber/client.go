package vtuber

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client 调用 LLM-Vup (Open-LLM-VTuber) /inject 端点的 HTTP 客户端
type Client struct {
	baseURL      string
	http         *http.Client
	ForwardAudio bool
}

// SpeakRequest 注入给 VTuber 前端的回复
type SpeakRequest struct {
	Text        string  `json:"text"`
	Emotion     string  `json:"emotion,omitempty"`
	Intensity   float64 `json:"intensity"`
	AudioBase64 string  `json:"audio_base64,omitempty"`
	AudioFormat string  `json:"audio_format,omitempty"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		// /inject 会等 TTS 合成完才返回，长文本可能较慢，超时给宽一些
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

func NewAudioClient(baseURL string) *Client { c := NewClient(baseURL); c.ForwardAudio = true; return c }

func (c *Client) SpeakAudio(ctx context.Context, req SpeakRequest, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > 24<<20 {
		return fmt.Errorf("生成音频超过 24 MB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	req.AudioBase64 = base64.StdEncoding.EncodeToString(data)
	req.AudioFormat = strings.TrimPrefix(filepath.Ext(path), ".")
	return c.Speak(ctx, req)
}

func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/inject/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接主项目失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("主项目返回 HTTP %d，请通过 bridge/serve.py 或 scripts/start.sh 启动", resp.StatusCode)
	}
	var status map[string]any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&status); err != nil {
		return nil, fmt.Errorf("未找到桥接接口，请使用桥接启动器启动主项目")
	}
	if status["bridge"] != "llm-vup-bridge" {
		return nil, fmt.Errorf("目标没有提供兼容的桥接接口")
	}
	return status, nil
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("vtuber: inject HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
