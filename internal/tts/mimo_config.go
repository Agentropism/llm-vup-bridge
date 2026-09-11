package tts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MimoFileConfig 是 mimo_config.json 的落盘结构：只存试听（TTS）相关字段。
// 独立成文件是为了让"只想试听"的人不必手写完整的 config.json。
type MimoFileConfig struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
	Voice   string `json:"voice,omitempty"`
	Format  string `json:"format,omitempty"`
}

type mimoFileEnvelope struct {
	Mimo MimoFileConfig `json:"mimo"`
}

// MimoState 可运行时读写的 Mimo 配置（进程内）。
// 注意：MimoClient 在启动时构造，因此保存后需要重启才真正用于生成音频；
// 本类型用于「保存 → 落盘 → 提示重启」的流程。
type MimoState struct {
	mu   sync.RWMutex
	cfg  MimoFileConfig
	path string // 落盘路径，空=只改内存
}

// NewMimoState 创建状态容器。
func NewMimoState(cfg MimoFileConfig, path string) *MimoState {
	return &MimoState{cfg: normalizeMimo(cfg), path: path}
}

// Get 返回副本。
func (s *MimoState) Get() MimoFileConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Path 返回落盘路径。
func (s *MimoState) Path() string { return s.path }

// Configured 是否已具备生成音频的最小条件（Key 非空）。
func (s *MimoState) Configured() bool { return s.Get().APIKey != "" }

// MaskedKey 只保留末 4 位。
func (s *MimoState) MaskedKey() string { return MaskKey(s.Get().APIKey) }

// ValidateForSave 保存前校验，返回可直接展示的中文错误。
func (c MimoFileConfig) ValidateForSave() error {
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("请填写 Mimo API Key")
	}
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		base = DefaultMimoConfig().BaseURL
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return fmt.Errorf("接口地址必须以 http:// 或 https:// 开头")
	}
	return nil
}

// MaskKey 脱敏：只留末 4 位。
func MaskKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return strings.Repeat("*", len(k))
	}
	return strings.Repeat("*", 8) + k[len(k)-4:]
}

// Save 校验并落盘，同时更新内存值。返回落盘错误（内存值仍会更新）。
func (s *MimoState) Save(in MimoFileConfig) error {
	if err := in.ValidateForSave(); err != nil {
		return err
	}
	cfg := normalizeMimo(in)

	s.mu.Lock()
	s.cfg = cfg
	path := s.path
	s.mu.Unlock()

	if path == "" {
		return nil
	}
	return saveMimoFile(path, cfg)
}

func normalizeMimo(c MimoFileConfig) MimoFileConfig {
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Model = strings.TrimSpace(c.Model)
	c.Voice = strings.TrimSpace(c.Voice)
	c.Format = strings.TrimSpace(c.Format)
	if c.BaseURL == "" {
		c.BaseURL = DefaultMimoConfig().BaseURL
	}
	if c.Model == "" {
		c.Model = DefaultMimoConfig().Model
	}
	if c.Voice == "" {
		c.Voice = DefaultMimoConfig().Voice
	}
	if c.Format == "" {
		c.Format = DefaultMimoConfig().Format
	}
	return c
}

func saveMimoFile(path string, cfg MimoFileConfig) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建配置目录失败: %w", err)
		}
	}
	data, err := json.MarshalIndent(mimoFileEnvelope{Mimo: cfg}, "", "  ")
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

// LoadMimoFile 读取 mimo_config.json；不存在时 ok=false。
func LoadMimoFile(path string) (MimoFileConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MimoFileConfig{}, false, nil
		}
		return MimoFileConfig{}, false, fmt.Errorf("读取配置失败: %w", err)
	}
	var env mimoFileEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return MimoFileConfig{}, false, fmt.Errorf("解析配置失败: %w", err)
	}
	if env.Mimo.APIKey == "" {
		return MimoFileConfig{}, false, nil
	}
	return normalizeMimo(env.Mimo), true, nil
}
