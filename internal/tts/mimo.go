package tts

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
	"regexp"
	"strings"
	"time"
)

// MimoConfig MiMo TTS 配置
type MimoConfig struct {
	APIKey   string // sk-xxxxx
	BaseURL  string // https://api.xiaomimimo.com/v1
	Model    string // mimo-v2.5-tts
	Voice    string // 冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean/mimo_default
	Format   string // mp3 / wav
	CacheDir string // 缓存音频文件目录
}

// DefaultMimoConfig 默认 Mimo TTS 配置
func DefaultMimoConfig() MimoConfig {
	return MimoConfig{
		BaseURL:  "https://api.xiaomimimo.com/v1",
		Model:    "mimo-v2.5-tts",
		Voice:    "冰糖",
		Format:   "mp3",
		CacheDir: "cache",
	}
}

// Voices 已知可用音色（供调试台下拉选择；实际可用范围以 MiMo 服务端为准）。
var Voices = []string{"冰糖", "茉莉", "苏打", "白桦", "Mia", "Chloe", "Milo", "Dean"}

// EmotionProfile 情绪对应的 Mimo 参数
type EmotionProfile struct {
	Context string  // 给 TTS 模型的表演指令
	Speed   float64 // 语速
	MimoEmo string  // Mimo emotion 参数 (angry/happy/sad/fearful/surprised)
}

// 情绪只轻微改变表达，不强迫喊叫、哭腔或追加台词。
var emotionProfiles = map[string]EmotionProfile{
	"anger":    {Context: "认真、有一点不满，但保持克制，不喊叫。", Speed: 1.04, MimoEmo: "angry"},
	"joy":      {Context: "带一点笑意，轻松愉快地聊天，不夸张表演。", Speed: 1.04, MimoEmo: "happy"},
	"sadness":  {Context: "声音温和、稍低落，自然停顿，不刻意哭泣。", Speed: 0.95, MimoEmo: "sad"},
	"fear":     {Context: "略有担心，语气自然，不尖叫或喘气。", Speed: 1.02, MimoEmo: "fearful"},
	"surprise": {Context: "轻微惊喜，句尾自然上扬，不大声喊叫。", Speed: 1.04, MimoEmo: "surprised"},
	"smirk":    {Context: "带笑意地轻轻打趣，亲近俏皮，不冷笑或挖苦。", Speed: 1.02, MimoEmo: "happy"},
	"disgust":  {Context: "轻微无奈，平静表达，不用夸张厌恶的语调。", Speed: 0.98, MimoEmo: "default"},
	"neutral":  {Context: "自然放松地聊天，按语义连贯表达和停顿，不逐字念稿。", Speed: 1.0, MimoEmo: "default"},
}

// MimoClient MiMo TTS 客户端
type MimoClient struct {
	cfg    MimoConfig
	http   *http.Client
	emoTag *regexp.Regexp
}

// NewMimoClient 创建 MiMo TTS 客户端
func NewMimoClient(cfg MimoConfig) *MimoClient {
	if cfg.CacheDir == "" {
		cfg.CacheDir = "cache"
	}
	os.MkdirAll(cfg.CacheDir, 0755)
	return &MimoClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: 60 * time.Second},
		emoTag: regexp.MustCompile(`(?i)\[(joy|sadness|anger|fear|surprise|smirk|neutral|disgust)\]`),
	}
}

// ParseEmotion 从文本中提取情绪标签并返回对应的 Mimo 参数
func (m *MimoClient) ParseEmotion(text string) (context string, speed float64, mimoEmotion string, boostText string) {
	matches := m.emoTag.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		p := emotionProfiles["neutral"]
		return p.Context, p.Speed, p.MimoEmo, ""
	}

	tag := strings.ToLower(matches[0][1])
	p, ok := emotionProfiles[tag]
	if !ok {
		p = emotionProfiles["neutral"]
	}

	return p.Context, p.Speed, p.MimoEmo, ""
}

// GenerateAudio 生成音频文件，返回文件路径。
func (m *MimoClient) GenerateAudio(ctx context.Context, text, filePrefix string) (string, error) {
	return m.generateAudio(ctx, text, filePrefix, 0, MimoOptions{})
}

// GenerateAudioBoost 生成音频文件；queueBoost>0 时在情绪基准语速上额外加速（积压加速）。
func (m *MimoClient) GenerateAudioBoost(ctx context.Context, text, filePrefix string, queueBoost float64) (string, error) {
	return m.generateAudio(ctx, text, filePrefix, queueBoost, MimoOptions{})
}

// GenerateAudioWith 生成音频文件，并允许单次覆盖音色/格式/语速（试听用，不改动全局配置）。
func (m *MimoClient) GenerateAudioWith(ctx context.Context, text, filePrefix string, opts MimoOptions) (string, error) {
	return m.generateAudio(ctx, text, filePrefix, 0, opts)
}

// GenerateAudioExpressive preserves text while applying explicit emotion intensity.
func (m *MimoClient) GenerateAudioExpressive(ctx context.Context, text, prefix string, boost, intensity float64) (string, error) {
	return m.generateAudio(ctx, text, prefix, boost, MimoOptions{Intensity: &intensity})
}

// CacheDir 返回音频缓存目录（供调试台挂载试听端点）。
func (m *MimoClient) CacheDir() string { return m.cfg.CacheDir }

// Format 返回当前音频格式（mp3/wav）。
func (m *MimoClient) Format() string { return m.cfg.Format }

// Voice 返回当前音色。
func (m *MimoClient) Voice() string { return m.cfg.Voice }

// MimoOptions 单次生成的覆盖项；零值表示沿用全局配置。
type MimoOptions struct {
	Voice     string   // 覆盖音色
	Format    string   // 覆盖格式（mp3/wav）
	Speed     float64  // 直接指定语速（覆盖情绪推导值）
	Intensity *float64 // nil 使用轻微情绪；0 表示中性
}

func (m *MimoClient) generateAudio(ctx context.Context, text, filePrefix string, queueBoost float64, opts MimoOptions) (string, error) {
	emoCtx, speed, mimoEmo, _ := m.ParseEmotion(text)
	ttsText := strings.TrimSpace(m.emoTag.ReplaceAllString(text, ""))
	if ttsText == "" {
		return "", fmt.Errorf("mimo: 文本不能为空")
	}
	intensity := 0.35
	if opts.Intensity != nil {
		intensity = max(0, min(1, *opts.Intensity))
	}
	speed = 1 + (speed-1)*intensity
	if intensity == 0 {
		emoCtx = emotionProfiles["neutral"].Context
		mimoEmo = "default"
	}
	emoCtx += fmt.Sprintf(" 情绪强度 %.0f%%，只朗读提供的正文，保留标点停顿，不添加词句。", intensity*100)
	speed += queueBoost
	if opts.Speed > 0 {
		speed = opts.Speed
	}

	speed = max(0.8, min(1.2, speed))
	voice := m.cfg.Voice
	if opts.Voice != "" {
		voice = opts.Voice
	}
	format := m.cfg.Format
	if opts.Format != "" {
		format = opts.Format
	}

	audioParams := map[string]any{
		"voice":  voice,
		"format": format,
	}
	if mimoEmo != "" && mimoEmo != "default" {
		audioParams["emotion"] = mimoEmo
	}
	if speed != 0 && speed != 1.0 {
		audioParams["speed"] = roundSpeed(speed)
	}

	payload := map[string]any{
		"model": m.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": emoCtx},
			{"role": "assistant", "content": ttsText},
		},
		"modalities": []string{"audio"},
		"audio":      audioParams,
		"stream":     false,
	}

	body, _ := json.Marshal(payload)
	url := strings.TrimRight(m.cfg.BaseURL, "/") + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("mimo: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("mimo: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("mimo: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	var result struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Audio struct {
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("mimo: decode response: %w", err)
	}

	if result.Error.Message != "" {
		return "", fmt.Errorf("mimo: API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("mimo: no audio data in response")
	}

	audioData := result.Choices[0].Message.Audio.Data
	if audioData == "" {
		return "", fmt.Errorf("mimo: empty audio data")
	}

	audioBytes, err := base64.StdEncoding.DecodeString(audioData)
	if err != nil {
		return "", fmt.Errorf("mimo: base64 decode: %w", err)
	}

	filename := fmt.Sprintf("mimo_%s_%d.%s", filePrefix, time.Now().UnixNano(), format)
	filePath := filepath.Join(m.cfg.CacheDir, filename)
	if err := os.WriteFile(filePath, audioBytes, 0644); err != nil {
		return "", fmt.Errorf("mimo: write file: %w", err)
	}

	return filePath, nil
}

func roundSpeed(s float64) float64 {
	return float64(int(s*100+0.5)) / 100
}
