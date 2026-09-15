package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type LLMConfig struct {
	Endpoint string `json:"endpoint"` // "https://api.openai.com/v1"
	Model    string `json:"model"`    // "gpt-4o-mini"
	APIKey   string `json:"api_key"`
	//可自行添加更多
}

// Validate 校验配置是否可用，返回可直接展示给用户的中文错误。
func (c LLMConfig) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("请填写接口地址（base URL）")
	}
	if !strings.HasPrefix(c.Endpoint, "http://") && !strings.HasPrefix(c.Endpoint, "https://") {
		return errors.New("接口地址必须以 http:// 或 https:// 开头")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("请选择或填写模型名")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("请填写 API Key")
	}
	return nil
}

// IsConfigured 判断配置是否完整到可以发起请求（不校验 Key 是否真实有效）。
func (c LLMConfig) IsConfigured() bool {
	c = c.Normalize()
	return c.Endpoint != "" && c.Model != "" && c.APIKey != ""
}

// MaskedKey 返回脱敏后的 Key：只保留末 4 位，用于回显给界面。
func (c LLMConfig) MaskedKey() string {
	k := strings.TrimSpace(c.APIKey)
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return strings.Repeat("*", len(k))
	}
	return strings.Repeat("*", 8) + k[len(k)-4:]
}

// Normalize 去掉首尾空白与末尾斜杠，避免拼出 //chat/completions。
func (c LLMConfig) Normalize() LLMConfig {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Model = strings.TrimSpace(c.Model)
	c.APIKey = strings.TrimSpace(c.APIKey)
	return c
}

// AnalysisResult LLM 对一条消息的分析结果
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

// Runtime 可运行时替换的 LLM 配置。
// 生产链路（Analyze）与调试台共用，改完对后续消息立即生效，无需重启。
type Runtime struct {
	mu  sync.RWMutex
	cfg LLMConfig
}

// NewRuntime 创建配置容器。
func NewRuntime(cfg LLMConfig) *Runtime {
	return &Runtime{cfg: cfg.Normalize()}
}

// Get 返回当前配置的副本（并发安全）。
func (r *Runtime) Get() LLMConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// Set 原子替换配置。
func (r *Runtime) Set(cfg LLMConfig) {
	r.mu.Lock()
	r.cfg = cfg.Normalize()
	r.mu.Unlock()
}

// CheckWritable 预检配置路径是否可写：在目标目录创建并删除临时文件。
// 用于启动阶段提前发现「目录只读/不存在且无法创建」的问题，避免用户点了保存才发现。
func CheckWritable(path string) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("无法创建目录 %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".writecheck-*")
	if err != nil {
		return fmt.Errorf("目录 %s 不可写: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// Save 把 LLM 配置写入 JSON 文件（原子写 + 0600，文件含密钥）。
func Save(path string, cfg LLMConfig) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建配置目录失败: %w", err)
		}
	}
	data, err := json.MarshalIndent(struct {
		LLM LLMConfig `json:"llm"`
	}{LLM: cfg}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return nil
}

// Load 读取持久化的 LLM 配置；文件不存在时返回 ok=false。
func Load(path string) (LLMConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LLMConfig{}, false, nil
		}
		return LLMConfig{}, false, fmt.Errorf("读取配置失败: %w", err)
	}
	var f struct {
		LLM LLMConfig `json:"llm"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return LLMConfig{}, false, fmt.Errorf("解析配置失败: %w", err)
	}
	cfg := f.LLM.Normalize()
	if cfg.Endpoint == "" && cfg.Model == "" && cfg.APIKey == "" {
		return LLMConfig{}, false, nil
	}
	return cfg, true, nil
}

// Analyze 调 LLM 分析一条用户消息。永远不返回 nil result值，解析失败时返回安全默认值。
// 后续会可能会使用备份LLM中心，目前为猜想
func Analyze(ctx context.Context, userMsg, userName string, cfg LLMConfig) (*AnalysisResult, error) {
	return AnalyzeWithHistory(ctx, userMsg, userName, cfg, nil, "")
}

func AnalyzeWithHistory(ctx context.Context, userMsg, userName string, cfg LLMConfig, history []map[string]string, persona string) (*AnalysisResult, error) {
	systemPrompt := loadPrompt()
	if persona != "" {
		systemPrompt += "\n\n当前角色与语气设定（仍遵守上述 JSON 格式）：\n" + persona
	}
	messages := []map[string]string{{"role": "system", "content": systemPrompt}}
	messages = append(messages, history...)
	messages = append(messages, map[string]string{"role": "user", "content": fmt.Sprintf("[用户:%s] %s", userName, userMsg)})
	content, err := chat(ctx, cfg, messages, 1024)
	if err != nil {
		return safeDefault(), err
	}

	var result AnalysisResult
	if err := json.Unmarshal([]byte(stripMarkdownFence(content)), &result); err != nil {
		return safeDefault(), fmt.Errorf("JSON 解析失败: %w", err)
	}
	result.ReplyText = CleanSpeech(result.ReplyText)
	switch result.Emotion {
	case "joy", "sadness", "anger", "fear", "surprise", "smirk", "neutral", "disgust":
	default:
		result.Emotion = "neutral"
	}
	if result.Intensity < 0 {
		result.Intensity = 0
	}
	if result.Intensity > 1 {
		result.Intensity = 1
	}
	return &result, nil
}

var speechTags = regexp.MustCompile(`(?i)\[(?:joy|sadness|anger|fear|surprise|smirk|neutral|disgust)\]`)

// CleanSpeech strips only supported control tags; ordinary bracketed text stays.
func CleanSpeech(s string) string { return strings.TrimSpace(speechTags.ReplaceAllString(s, "")) }

// TestResult 调试台「测试连接」的结果。
type TestResult struct {
	OK        bool    `json:"ok"`
	LatencyMS int64   `json:"latency_ms"`
	Model     string  `json:"model"`
	Reply     string  `json:"reply"`
	Usage     string  `json:"usage,omitempty"`
	Error     string  `json:"error,omitempty"`
	HTTPCode  int     `json:"http_code,omitempty"`
	Endpoint  string  `json:"endpoint"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
}

// TestConnection 用给定配置发一次最小请求，验证 endpoint / key / model 是否可用。
func TestConnection(ctx context.Context, cfg LLMConfig) TestResult {
	cfg = cfg.Normalize()
	res := TestResult{Endpoint: cfg.Endpoint, Model: cfg.Model}

	if err := cfg.Validate(); err != nil {
		res.Error = err.Error()
		return res
	}

	start := time.Now()
	content, err := chat(ctx, cfg, []map[string]string{
		{"role": "system", "content": "你是连通性测试助手，只回复两个字：可用"},
		{"role": "user", "content": "测试"},
	}, 16)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}

	res.OK = true
	res.Reply = strings.TrimSpace(content)
	if len([]rune(res.Reply)) > 120 {
		res.Reply = string([]rune(res.Reply)[:120]) + "…"
	}
	return res
}

// chat 发一次对话请求并返回助手文本。
func chat(ctx context.Context, cfg LLMConfig, messages []map[string]string, maxTokens int) (string, error) {
	content, _, err := chatFull(ctx, cfg, messages, maxTokens)
	return content, err
}

// chatFull 统一的 OpenAI 兼容 /chat/completions 调用。
// 服务端返回 error 字段或 choices 为空时都要给出可读错误，不能 panic 或误报 JSON 解析失败。
func chatFull(ctx context.Context, cfg LLMConfig, messages []map[string]string, maxTokens int) (string, string, error) {
	cfg = cfg.Normalize()
	if cfg.Endpoint == "" {
		return "", "", errors.New("未配置 LLM 接口地址")
	}

	body := map[string]any{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": 0.7,
		"max_tokens":  maxTokens,
	}
	reqBody, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("LLM 请求失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %w", err)
	}

	var parsed struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// 非 JSON 响应（网关 HTML 错误页等）：截断原文便于定位
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return "", "", fmt.Errorf("HTTP %d 响应非 JSON: %s", resp.StatusCode, snippet)
	}

	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", "", fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, pick(parsed.Error.Type, "接口报错"), parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if len(parsed.Choices) == 0 {
		return "", "", fmt.Errorf("HTTP %d 响应中没有 choices（模型名可能不受支持）: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return parsed.Choices[0].Message.Content, "", nil
}

func pick(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
