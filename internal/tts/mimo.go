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
	APIKey     string // sk-xxxxx
	BaseURL    string // https://api.xiaomimimo.com/v1
	Model      string // mimo-v2.5-tts
	Voice      string // 冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean/mimo_default
	Format     string // mp3 / wav
	CacheDir   string // 缓存音频文件目录
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

// EmotionProfile 情绪对应的 Mimo 参数
type EmotionProfile struct {
	Context string  // 给 TTS 模型的表演指令
	Speed   float64 // 语速
	MimoEmo string  // Mimo emotion 参数 (angry/happy/sad/fearful/surprised)
}

// ShortBoost 短句增强配置
type ShortBoost struct {
	Append string  // 尾缀文本
	Speed  float64 // 增强后的语速
}

// 情绪 → Mimo 参数映射
var emotionProfiles = map[string]EmotionProfile{
	"anger":    {Context: "用超愤怒暴躁的语气说——大声吼出来！非常生气！", Speed: 1.35, MimoEmo: "angry"},
	"joy":      {Context: "用超级开心兴奋的语气说——得意洋洋笑出声！开心到飞起！", Speed: 1.25, MimoEmo: "happy"},
	"sadness":  {Context: "用委屈带哭腔的语气说——声音颤抖，慢一点，楚楚可怜快要哭了", Speed: 0.75, MimoEmo: "sad"},
	"fear":     {Context: "用害怕慌张的语气说——倒吸一口凉气，声音发抖，语速很快！", Speed: 1.4, MimoEmo: "fearful"},
	"surprise": {Context: "用惊讶不可置信的语气说——眼睛瞪大，声音上扬，不敢相信！", Speed: 1.3, MimoEmo: "surprised"},
	"smirk":    {Context: "用嘲讽冷笑、阴阳怪气的语气说——毒舌又可爱，'呵，愚蠢的人类'", Speed: 1.08, MimoEmo: "happy"},
	"disgust":  {Context: "用嫌弃厌恶的语气说——皱着眉头，满满的鄙夷和不耐烦", Speed: 1.15, MimoEmo: "angry"},
	"neutral":  {Context: "用自然放松的口语语气说——像在直播间闲聊一样随意轻快", Speed: 1.0, MimoEmo: "default"},
}

// 短句（≤6字）增强映射
var shortBoosts = map[string]ShortBoost{
	"anger":    {Append: "！！你这个——算了！！", Speed: 1.45},
	"joy":      {Append: "哈哈～美滋滋～", Speed: 1.3},
	"sadness":  {Append: "……呜……", Speed: 0.7},
	"fear":     {Append: "吓死我了！！", Speed: 1.5},
	"surprise": {Append: "！！什么？！真的假的？！", Speed: 1.4},
	"smirk":    {Append: "哼哼～知道就好～", Speed: 1.12},
	"disgust":  {Append: "啧，无语了。", Speed: 1.2},
	"neutral":  {Append: "嗯。", Speed: 1.0},
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
		emoTag: regexp.MustCompile(`\[(\w+)\]`),
	}
}

// ParseEmotion 从文本中提取情绪标签并返回对应的 Mimo 参数
func (m *MimoClient) ParseEmotion(text string) (context string, speed float64, mimoEmotion string, boostText string) {
	clean := m.emoTag.ReplaceAllString(text, "")
	clean = strings.TrimRight(clean, "….,!?！？\n\r\t ")
	isShort := len([]rune(clean)) <= 6

	matches := m.emoTag.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		p := emotionProfiles["neutral"]
		return p.Context, p.Speed, p.MimoEmo, ""
	}

	tag := strings.ToLower(matches[len(matches)-1][1])
	p, ok := emotionProfiles[tag]
	if !ok {
		p = emotionProfiles["neutral"]
	}

	if isShort {
		if b, ok := shortBoosts[tag]; ok {
			return p.Context, b.Speed, p.MimoEmo, " " + b.Append
		}
	}

	return p.Context, p.Speed, p.MimoEmo, ""
}

// GenerateAudio 生成音频文件，返回文件路径。
func (m *MimoClient) GenerateAudio(ctx context.Context, text, filePrefix string) (string, error) {
	return m.generateAudio(ctx, text, filePrefix, 0)
}

// GenerateAudioBoost 生成音频文件；queueBoost>0 时在情绪基准语速上额外加速（积压加速）。
func (m *MimoClient) GenerateAudioBoost(ctx context.Context, text, filePrefix string, queueBoost float64) (string, error) {
	return m.generateAudio(ctx, text, filePrefix, queueBoost)
}

func (m *MimoClient) generateAudio(ctx context.Context, text, filePrefix string, queueBoost float64) (string, error) {
	emoCtx, speed, mimoEmo, boostText := m.ParseEmotion(text)
	ttsText := text
	if boostText != "" {
		ttsText = strings.TrimSpace(text + boostText)
	}
	speed += queueBoost

	audioParams := map[string]any{
		"voice":  m.cfg.Voice,
		"format": m.cfg.Format,
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

	filename := fmt.Sprintf("mimo_%s_%d.%s", filePrefix, time.Now().UnixNano(), m.cfg.Format)
	filePath := filepath.Join(m.cfg.CacheDir, filename)
	if err := os.WriteFile(filePath, audioBytes, 0644); err != nil {
		return "", fmt.Errorf("mimo: write file: %w", err)
	}

	return filePath, nil
}

func roundSpeed(s float64) float64 {
	return float64(int(s*100+0.5)) / 100
}
