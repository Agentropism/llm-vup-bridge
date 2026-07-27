package llm

import (
	"os"
	"path/filepath"
	"runtime"
)

var cachedPrompt string

func loadPrompt() string {
	if cachedPrompt != "" {
		return cachedPrompt
	}

	// 尝试从可执行文件同级的 prompts/ 目录读取
	candidates := []string{
		"prompts/intent_analysis.txt",
		filepath.Join(getProjectRoot(), "prompts", "intent_analysis.txt"),
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			cachedPrompt = string(data)
			return cachedPrompt
		}
	}

	return `你是一个VTube（虚拟主播）的行为分析器。分析用户消息，只返回JSON。
{"intent_type":"chat","emotion":"neutral","intensity":0.5,"reply_text":"","should_speak":false,"should_reply":false,"tts_enabled":false}`
}

func getProjectRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// internal/llm/prompts.go → 上两级是项目根
	return filepath.Join(filepath.Dir(filename), "..", "..")
}
