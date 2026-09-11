package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Agentropism/llm-vup-bridge/internal/debugui"
	"github.com/Agentropism/llm-vup-bridge/internal/dispatch"
	"github.com/Agentropism/llm-vup-bridge/internal/emotion"
	"github.com/Agentropism/llm-vup-bridge/internal/llm"
	"github.com/Agentropism/llm-vup-bridge/internal/model"
	"github.com/Agentropism/llm-vup-bridge/internal/provider"
	"github.com/Agentropism/llm-vup-bridge/internal/tts"
	"github.com/Agentropism/llm-vup-bridge/internal/vtuber"

	"github.com/go-chi/chi/v5"
)

type AppConfig struct {
	ListenAddr string `json:"listen_addr"`
	TTSAddr    string `json:"tts_addr"`
	VTuberAddr string `json:"vtuber_addr"`
	// Mimo TTS 直连配置（非空时优先使用，绕过 LLM-Vup 的 TTS 引擎）
	Mimo struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Voice   string `json:"voice"`
		Format  string `json:"format"`
		// CacheDir 音频缓存目录；空则用 config.json 同级的 cache/
		CacheDir string `json:"cache_dir"`
	} `json:"mimo"`
	LLM struct {
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
	} `json:"llm"`
	Speech struct {
		CooldownSec int                `json:"cooldown_sec"`
		ReplyChance map[string]float64 `json:"reply_chance"`
		GiftBypass  bool               `json:"gift_bypass_cooldown"`
		// 优先级队列相关（见 internal/dispatch/scheduler.go）
		QueueTTLTextSec   int     `json:"ttl_text_sec"`        // 普通消息排队时限（秒），超时丢弃
		QueueTTLGiftSec   int     `json:"ttl_gift_sec"`        // 礼物/SC/舰长排队时限（秒），超时丢弃
		QueueMaxSize      int     `json:"queue_max_size"`      // 队列容量，0=不限；满时淘汰最低优先级
		BacklogThreshold  int     `json:"backlog_threshold"`   // 积压加速阈值：排队数达到该值时加速，0=禁用
		BacklogSpeedBoost float64 `json:"backlog_speed_boost"` // 积压加速量（Mimo speed 增量 / synapse rate 百分比增量）
	} `json:"speech"`
}

// configDiag 记录配置加载过程中发生的事，供启动横幅与调试台展示。
// 之前的实现把「文件不存在」和「JSON 解析失败」都静默降级为全默认配置，
// 表现为外部症状（Mimo 未配置→无音频→试听 404）而不是配置错误，极难定位。
type configDiag struct {
	Path        string   `json:"path"`         // 实际读取的路径
	Found       bool     `json:"found"`        // 文件是否存在
	ParseError  string   `json:"parse_error"`  // JSON 解析错误
	EnvApplied  []string `json:"env_applied"`  // 实际生效的环境变量覆盖
	UsedDefault bool     `json:"used_default"` // 是否整体退化为内置默认值
}

// loadConfig 读取配置并返回诊断信息。调用方负责把诊断打出来并决定是否致命。
// 缺失/损坏的配置一律以「响亮」的方式暴露，绝不静默降级。
func loadConfig(path string) (AppConfig, configDiag) {
	cfg := AppConfig{ListenAddr: ":9528", TTSAddr: "http://localhost:9527", VTuberAddr: "http://localhost:12393"}
	cfg.LLM.Endpoint = "https://api.openai.com/v1"
	cfg.LLM.Model = "gpt-4o-mini"
	cfg.Mimo.Model = "mimo-v2.5-tts"
	cfg.Mimo.Voice = "冰糖"
	cfg.Mimo.Format = "mp3"
	cfg.Mimo.BaseURL = "https://api.xiaomimimo.com/v1"
	cfg.Speech.CooldownSec = 5
	cfg.Speech.GiftBypass = true
	cfg.Speech.ReplyChance = map[string]float64{
		"chat": 0.6, "greeting": 0.8, "question": 0.95,
		"command": 0.9, "gift_thanks": 1.0,
	}
	cfg.Speech.QueueTTLTextSec = 10
	cfg.Speech.QueueTTLGiftSec = 60
	cfg.Speech.QueueMaxSize = 64
	cfg.Speech.BacklogThreshold = 3
	cfg.Speech.BacklogSpeedBoost = 0.15

	diag := configDiag{Path: absOrSelf(path), UsedDefault: true}

	switch data, err := os.ReadFile(path); {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			// 坏 JSON 是最隐蔽的一种：文件明明存在，配置却完全没生效
			diag.ParseError = err.Error()
		} else {
			diag.Found = true
			diag.UsedDefault = false
		}
	case os.IsNotExist(err):
		// Found 保持 false，由调用方决定是否致命
	default:
		diag.ParseError = err.Error()
	}

	applyEnv := func(name string, dst *string) {
		if v := os.Getenv(name); v != "" {
			*dst = v
			diag.EnvApplied = append(diag.EnvApplied, name)
		}
	}
	applyEnv("LLM_ENDPOINT", &cfg.LLM.Endpoint)
	applyEnv("LLM_MODEL", &cfg.LLM.Model)
	applyEnv("LLM_API_KEY", &cfg.LLM.APIKey)
	applyEnv("MIMO_API_KEY", &cfg.Mimo.APIKey)
	applyEnv("MIMO_VOICE", &cfg.Mimo.Voice)
	applyEnv("TTS_ADDR", &cfg.TTSAddr)
	applyEnv("VTUBER_ADDR", &cfg.VTuberAddr)
	applyEnv("LISTEN_ADDR", &cfg.ListenAddr)

	return cfg, diag
}

// reportConfigDiag 把配置诊断打成显眼的日志。
func reportConfigDiag(diag configDiag, explicit bool) {
	switch {
	case diag.ParseError != "" && !diag.Found:
		log.Printf("[config] ✖ 读取 %s 失败: %s", diag.Path, diag.ParseError)
	case diag.ParseError != "":
		log.Printf("[config] ✖ %s 的 JSON 解析失败: %s", diag.Path, diag.ParseError)
		log.Printf("[config]   配置【完全没有生效】，若无环境变量覆盖，用的就是内置默认值")
		log.Printf("[config]   常见原因：多余的逗号、缺失的引号、JSON 里写了注释。校验：python3 -m json.tool %s", diag.Path)
	case !diag.Found && explicit:
		log.Printf("[config] ✖ 你显式指定的配置文件不存在: %s", diag.Path)
	case !diag.Found:
		log.Printf("[config] ⚠ 未找到 %s，将使用内置默认值（Mimo 未配置 → 无音频缓存 → 试听不可用）", diag.Path)
		log.Printf("[config]   把 config.json 放在该路径下，或用 --config /绝对/路径/config.json 指定")
	}
	if len(diag.EnvApplied) > 0 {
		log.Printf("[config] 环境变量覆盖已生效: %s", strings.Join(diag.EnvApplied, ", "))
	}
}

// findConfig 定位配置文件：显式指定则用之；否则依次尝试
// 当前目录、可执行文件所在目录、上级目录（兼容从 bin/ 启动）。
// 这样"只把 model_config.json 放在仓库根"这类布局也能被找到。
func findConfig(explicit string, explicitSet bool) (string, bool) {
	exists := func(p string) bool {
		if p == "" {
			return false
		}
		info, err := os.Stat(p)
		return err == nil && !info.IsDir()
	}

	// 显式指定：路径原样使用（便于报错时展示用户写的路径），found 反映真实存在性
	if explicitSet {
		return explicit, exists(explicit)
	}

	candidates := []string{explicit}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "config.json"),
			filepath.Join(dir, "..", "config.json"))
	}
	for _, c := range candidates {
		if exists(c) {
			return c, true
		}
	}
	return explicit, false
}

// absOrSelf 返回绝对路径，失败时原样返回（仅用于日志展示）。
func absOrSelf(path string) string {
	if path == "" {
		return "(未启用)"
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// orNone 把空字符串显示为「未启用」。
func orNone(s string) string {
	if s == "" {
		return "未启用"
	}
	return s
}

// resolveModelConfigPath 把配置文件的相对路径解析为绝对路径。
// 绝对路径原样返回；相对路径以 baseDir（通常是 config.json 所在目录）为基准，
// 保证与启动时的工作目录无关。
func resolveModelConfigPath(modelPath, baseDir string) string {
	if filepath.IsAbs(modelPath) {
		return modelPath
	}
	if baseDir == "" {
		baseDir = "."
	}
	abs, err := filepath.Abs(filepath.Join(baseDir, modelPath))
	if err != nil {
		return modelPath
	}
	return abs
}

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	modelCfgPath := flag.String("model-config", "model_config.json",
		"LLM 模型配置持久化路径（相对路径以 config.json 所在目录为基准；含密钥，0600）")
	flag.Parse()

	explicitConfig := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicitConfig = true
		}
	})

	// 定位 config.json（显式 > 当前目录 > 可执行文件目录 > 上级目录）
	resolvedCfgPath, configFound := findConfig(*cfgPath, explicitConfig)
	if !configFound {
		resolvedCfgPath = *cfgPath
	}
	configPath := absOrSelf(resolvedCfgPath)
	configDir := filepath.Dir(configPath)

	cfg, diag := loadConfig(resolvedCfgPath)
	reportConfigDiag(diag, explicitConfig)

	// 配置有问题就拒绝启动：继续跑只会让人以为是"功能坏了"（详见 reportConfigDiag 的说明）
	if diag.ParseError != "" {
		log.Fatalf("[config] 拒绝以默认值启动")
	}
	if explicitConfig && !diag.Found {
		log.Fatalf("[config] 拒绝以默认值启动")
	}
	if !diag.Found {
		log.Printf("[config] 继续以内置默认值启动；调试台可随时配置模型与 Mimo，无需手写 config.json")
	}

	ttsClient := tts.NewClient(cfg.TTSAddr)

	// 模型配置与 Mimo 配置的落盘位置：相对路径锚定到 config.json 所在目录，
	// 这样从任意工作目录启动都会写回同一处，避免"保存了但找不到文件"。
	*modelCfgPath = resolveModelConfigPath(*modelCfgPath, configDir)
	mimoCfgPath := resolveModelConfigPath("mimo_config.json", configDir)

	// mimoconfig.json 优先级：文件 > config.json 的 mimo 段
	mimoFile, hasMimoFile, mimoErr := tts.LoadMimoFile(mimoCfgPath)
	if mimoErr != nil {
		log.Printf("[mimo] 读取 %s 失败: %v", mimoCfgPath, mimoErr)
	}
	if hasMimoFile {
		cfg.Mimo.APIKey = mimoFile.APIKey
		cfg.Mimo.BaseURL = mimoFile.BaseURL
		cfg.Mimo.Model = mimoFile.Model
		cfg.Mimo.Voice = mimoFile.Voice
		cfg.Mimo.Format = mimoFile.Format
		log.Printf("[mimo] 已加载试听配置 | voice=%s | format=%s | 来源: %s",
			mimoFile.Voice, mimoFile.Format, mimoCfgPath)
	}

	var vtuberClient *vtuber.Client
	if cfg.VTuberAddr != "" {
		vtuberClient = vtuber.NewClient(cfg.VTuberAddr)
	}

	var mimoClient *tts.MimoClient
	var audioDir string
	// 即使本次没配 Key，也保留状态与落盘路径，便于在调试台里补填后重启生效
	mimoState := tts.NewMimoState(tts.MimoFileConfig{}, mimoCfgPath)
	if cfg.Mimo.APIKey != "" {
		// 音频缓存与模型配置一样锚定到 config.json 所在目录（可用 mimo.cache_dir 覆盖），
		// 这样换工作目录启动时，试听端点仍能找到之前的音频。
		cacheDir := cfg.Mimo.CacheDir
		if cacheDir == "" {
			cacheDir = "cache"
		}
		audioDir = resolveModelConfigPath(cacheDir, configDir)
		mimoCfg := tts.MimoConfig{
			APIKey:   cfg.Mimo.APIKey,
			BaseURL:  cfg.Mimo.BaseURL,
			Model:    cfg.Mimo.Model,
			Voice:    cfg.Mimo.Voice,
			Format:   cfg.Mimo.Format,
			CacheDir: audioDir,
		}
		mimoClient = tts.NewMimoClient(mimoCfg)
		audioDir = mimoClient.CacheDir()
		mimoState = tts.NewMimoState(tts.MimoFileConfig{
			APIKey:  cfg.Mimo.APIKey,
			BaseURL: cfg.Mimo.BaseURL,
			Model:   cfg.Mimo.Model,
			Voice:   cfg.Mimo.Voice,
			Format:  cfg.Mimo.Format,
		}, mimoCfgPath)
		log.Printf("[mimo] 直连 MiMo TTS | voice=%s | model=%s | 音频缓存: %s",
			cfg.Mimo.Voice, cfg.Mimo.Model, audioDir)
	} else {
		log.Printf("[mimo] ⚠ 未配置 mimo.api_key：不会生成音频，试听不可用（可在调试台 /debug 的「Mimo TTS 试听」里填写）")
	}

	llmCfg := llm.LLMConfig{
		Endpoint: cfg.LLM.Endpoint,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
	}
	// 调试台保存的模型配置优先于 config.json 与环境变量，便于运行时切换服务商
	if saved, ok, err := llm.Load(*modelCfgPath); err != nil {
		log.Printf("[llm] 读取 %s 失败，沿用 config.json/环境变量: %v", *modelCfgPath, err)
	} else if ok {
		llmCfg = saved
		log.Printf("[llm] 已加载持久化模型配置 | %s (%s) | 来源: %s", saved.Model, saved.Endpoint, *modelCfgPath)
	}
	// 提前探测落盘能力：路径不可写时在启动阶段就提醒，而不是等用户点保存才发现
	if err := llm.CheckWritable(*modelCfgPath); err != nil {
		log.Printf("[llm] ⚠ 模型配置路径不可写，调试台的模型切换将无法持久化: %v", err)
	}

	sw := dispatch.SpeechWeight{
		ReplyChance:        cfg.Speech.ReplyChance,
		Cooldown:           time.Duration(cfg.Speech.CooldownSec) * time.Second,
		GiftBypassCooldown: cfg.Speech.GiftBypass,
	}
	dispatcher := dispatch.NewDispatcher(sw)

	// 优先级调度器：单消费者串行播报（TTS 不重叠），按优先级消费、超时丢弃、积压加速
	ttlText := time.Duration(cfg.Speech.QueueTTLTextSec) * time.Second
	ttlGift := time.Duration(cfg.Speech.QueueTTLGiftSec) * time.Second
	sched := dispatch.NewScheduler(dispatch.SchedulerOptions{
		QueueCap:          cfg.Speech.QueueMaxSize,
		BacklogThreshold:  cfg.Speech.BacklogThreshold,
		BacklogSpeedBoost: cfg.Speech.BacklogSpeedBoost,
	})
	sched.Start()
	defer sched.Close()

	// TTS 实际生效路径：Mimo 直连 > VTuber 注入 > synapse-tts
	ttsPath := "synapse-tts"
	if vtuberClient != nil {
		ttsPath = "vtuber-inject"
	}
	if mimoClient != nil {
		ttsPath = "mimo-direct"
	}

	app := &app{
		cfg:             cfg,
		ttsClient:       ttsClient,
		vtuberClient:    vtuberClient,
		mimoClient:      mimoClient,
		llmRt:           llm.NewRuntime(llmCfg),
		modelConfigPath: *modelCfgPath,
		dispatcher:      dispatcher,
		sched:           sched,
		ttlText:         ttlText,
		ttlGift:         ttlGift,
		ttsPath:         ttsPath,
		audioDir:        audioDir,
		mimoState:       mimoState,
		configDiag:      diag,
		log:             debugui.NewLog(200),
	}

	r := chi.NewRouter()

	r.Post("/event", func(w http.ResponseWriter, req *http.Request) {
		var event model.UnifiedEvent
		if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		seq, accepted := app.enqueue(event)
		if !accepted {
			writeJSON(w, http.StatusOK, map[string]any{"status": "dropped", "seq": seq})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})

	r.Post("/test", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Text  string `json:"text"`
			User  string `json:"user"`
			Type  string `json:"type"`
			Emoji string `json:"emotion"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		if body.User == "" {
			body.User = "测试用户"
		}
		if body.Type == "" {
			body.Type = "text"
		}
		if body.Emoji == "" {
			body.Emoji = "smirk"
		}

		event := model.UnifiedEvent{
			Platform:    "test",
			UserName:    body.User,
			Content:     body.Text,
			MessageType: body.Type,
		}
		// 测试端点不走优先级队列：同步处理并返回结果
		result := processEvent(req.Context(), event, app.llmConfig(), ttsClient, vtuberClient, mimoClient, dispatcher, 0)
		writeJSON(w, http.StatusOK, result)
	})

	// Mimo TTS 直连测试端点（支持单次覆盖音色/格式/语速，便于试听不同配置）
	r.Post("/tts/mimo", func(w http.ResponseWriter, req *http.Request) {
		if mimoClient == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mimo not configured"})
			return
		}
		var body struct {
			Text    string  `json:"text"`
			Emotion string  `json:"emotion"`
			Speed   float64 `json:"speed"`
			Voice   string  `json:"voice"`
			Format  string  `json:"format"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		if body.Text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty text"})
			return
		}
		// 如果指定了情绪，在文本前加标签
		tagged := body.Text
		if body.Emotion != "" {
			tagged = "[" + body.Emotion + "] " + body.Text
		}
		opts := tts.MimoOptions{Voice: body.Voice, Format: body.Format, Speed: body.Speed}
		filePath, err := mimoClient.GenerateAudioWith(req.Context(), tagged, "test", opts)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		name := filepath.Base(filePath)
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"file":   filePath,
			"audio":  name,
			"url":    "/debug/audio/" + url.PathEscape(name),
		})
	})

	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// 试听：按文件名播放 cache/ 下的音频（独立于 LLM-Vup，仅需 distillery 自身）
	r.Get("/debug/audio/*", debugui.AudioHandler(app.audioDir))

	// ── 模型配置（运行时切换服务商，立即生效并落盘）──
	r.Get("/debug/providers", func(w http.ResponseWriter, req *http.Request) {
		cfg := app.llmConfig()
		debugui.WriteJSON(w, http.StatusOK, map[string]any{
			"providers": provider.Presets,
			"current":   modelConfigView(cfg, app.modelConfigPath),
		})
	})
	r.Post("/debug/model", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Provider string `json:"provider"`
			Endpoint string `json:"endpoint"`
			Model    string `json:"model"`
			APIKey   string `json:"api_key"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		cfg := llm.LLMConfig{Endpoint: body.Endpoint, Model: body.Model, APIKey: body.APIKey}
		key := strings.TrimSpace(body.APIKey)
		switch {
		case key == "":
			debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{
				"error": "请填写 API Key（界面只回显掩码，出于安全不回传明文）",
			})
			return
		case key == keepKeySentinel:
			// 显式选择「沿用已保存的 Key」
			cfg.APIKey = app.llmConfig().APIKey
			if cfg.APIKey == "" {
				debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{
					"error": "没有可沿用的 API Key，请先填写",
				})
				return
			}
		}
		// 先校验再生效：校验不过就什么都不改；落盘失败则已生效但要如实提示
		if err := cfg.Validate(); err != nil {
			debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		persistErr := app.activateLLMConfig(cfg)
		log.Printf("[llm] 模型配置已更新 | %s (%s) | provider=%s",
			cfg.Model, cfg.Endpoint, pickProvider(body.Provider, cfg.Endpoint))

		resp := map[string]any{
			"status":  "ok",
			"current": modelConfigView(app.llmConfig(), app.modelConfigPath),
		}
		if persistErr != nil {
			resp["warning"] = "已立即生效，但写入 " + app.modelConfigPath + " 失败：" +
				persistErr.Error() + "（重启后会丢失，请确认该路径可写）"
		}
		debugui.WriteJSON(w, http.StatusOK, resp)
	})
	// 测试连接：用当前生效配置发一次最小请求，验证 endpoint / key / model
	r.Post("/debug/model/test", func(w http.ResponseWriter, req *http.Request) {
		cfg := app.llmConfig()
		ctx, cancel := context.WithTimeout(req.Context(), 40*time.Second)
		defer cancel()
		result := llm.TestConnection(ctx, cfg)
		debugui.WriteJSON(w, http.StatusOK, result)
	})

	// ── Mimo TTS 试听配置（写 mimo_config.json，保存后需重启生效）──
	r.Get("/debug/mimo", func(w http.ResponseWriter, req *http.Request) {
		debugui.WriteJSON(w, http.StatusOK, app.mimoView())
	})
	r.Post("/debug/mimo", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			APIKey  string `json:"api_key"`
			BaseURL string `json:"base_url"`
			Model   string `json:"model"`
			Voice   string `json:"voice"`
			Format  string `json:"format"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		// Key 留空且已配置过 → 沿用（界面只回显掩码）
		if strings.TrimSpace(body.APIKey) == "" && app.mimoState.Configured() {
			body.APIKey = app.mimoState.Get().APIKey
		}
		in := tts.MimoFileConfig{
			APIKey: body.APIKey, BaseURL: body.BaseURL,
			Model: body.Model, Voice: body.Voice, Format: body.Format,
		}
		if err := in.ValidateForSave(); err != nil {
			debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		persistErr := app.mimoState.Save(in)
		log.Printf("[mimo] 试听配置已更新 | voice=%s | format=%s | 文件=%s",
			app.mimoState.Get().Voice, app.mimoState.Get().Format, app.mimoState.Path())

		resp := map[string]any{
			"status": "ok",
			"mimo":   app.mimoView(),
			// MimoClient 在启动时构造，保存后需要重启才会用于生成音频
			"restart_required": true,
			"restart_hint": "已保存到 " + app.mimoState.Path() +
				"。请重启 distillery（Mimo 客户端在启动时初始化），之后事件就会产出可试听的音频。",
		}
		if persistErr != nil {
			resp["warning"] = "已更新内存配置，但写入 " + app.mimoState.Path() + " 失败：" +
				persistErr.Error() + "（重启后需要重新填写）"
		}
		debugui.WriteJSON(w, http.StatusOK, resp)
	})

	// ── 调试台（只读页面 + 与生产端点同构的写入）──
	r.Get("/debug", func(w http.ResponseWriter, req *http.Request) {
		debugui.WriteHTML(w)
	})
	r.Get("/debug/config", func(w http.ResponseWriter, req *http.Request) {
		debugui.WriteJSON(w, http.StatusOK, app.status())
	})
	r.Get("/debug/log", func(w http.ResponseWriter, req *http.Request) {
		n := 50
		if v := req.URL.Query().Get("n"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				n = parsed
			}
		}
		debugui.WriteJSON(w, http.StatusOK, map[string]any{
			"total":   app.log.Len(),
			"entries": app.log.Recent(n),
		})
	})
	// 等价 POST /event，额外返回 seq / accepted，便于页面把结果与队列任务对上
	r.Post("/debug/queue", func(w http.ResponseWriter, req *http.Request) {
		var event model.UnifiedEvent
		if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
			debugui.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		seq, accepted := app.enqueue(event)
		debugui.WriteJSON(w, http.StatusOK, map[string]any{
			"status":   map[bool]string{true: "accepted", false: "dropped"}[accepted],
			"accepted": accepted,
			"seq":      seq,
			"priority": int(dispatch.PriorityFor(event.MessageType)),
		})
	})

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: r}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("[distillery] 正在关闭...")
		srv.Close()
	}()

	vtuberInfo := cfg.VTuberAddr
	if vtuberInfo == "" {
		vtuberInfo = "disabled"
	}
	t := "synapse"
	if mimoClient != nil {
		t = "mimo"
	}
	log.Printf("[distillery] 启动 | 监听: %s | VTuber: %s | TTS: %s | LLM: %s (%s) | 冷却: %ds | 队列: cap=%d ttl=%ds/%ds 积压加速=%d@%+.2f",
		cfg.ListenAddr, vtuberInfo, t, app.llmConfig().Endpoint, app.llmConfig().Model, cfg.Speech.CooldownSec,
		cfg.Speech.QueueMaxSize, cfg.Speech.QueueTTLTextSec, cfg.Speech.QueueTTLGiftSec,
		cfg.Speech.BacklogThreshold, cfg.Speech.BacklogSpeedBoost)
	log.Printf("[distillery] 配置文件: %s | 模型配置保存到: %s | 音频缓存: %s | 调试台: http://localhost%s/debug",
		absOrSelf(*cfgPath), absOrSelf(app.modelConfigPath), orNone(audioDir), cfg.ListenAddr)

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("[distillery] 服务异常: %v", err)
	}
}

type ProcessResult struct {
	Responded  bool   `json:"responded"`
	ReplyText  string `json:"reply_text,omitempty"`
	SpokenText string `json:"spoken_text,omitempty"` // 实际送去 TTS 的文本（可能带情绪标签）
	Emotion    string `json:"emotion,omitempty"`
	IntentType string `json:"intent_type,omitempty"`
	TTSSpoken  bool   `json:"tts_spoken"`
	TTSApplied bool   `json:"tts_applied"` // 是否真的调用了 TTS（false=按策略不发声）
	SkipReason string `json:"skip_reason,omitempty"`
	// 以下两项仅用于调试台：音频文件路径（可试听）与 TTS 失败原因
	AudioPaths []string `json:"audio_paths,omitempty"`
	TTSError   string   `json:"tts_error,omitempty"`
}

// app 持有运行期依赖，供各 HTTP 处理器与调试台共用。
type app struct {
	cfg          AppConfig
	ttsClient    *tts.Client
	vtuberClient *vtuber.Client
	mimoClient   *tts.MimoClient
	llmRt        *llm.Runtime
	// modelConfigPath 是调试台保存模型配置的文件；空则只改内存不落盘
	modelConfigPath string
	dispatcher      *dispatch.Dispatcher
	sched           *dispatch.Scheduler
	ttlText         time.Duration
	ttlGift         time.Duration
	ttsPath         string
	audioDir        string // 音频缓存目录（试听端点根目录），空=试听不可用
	mimoState       *tts.MimoState
	configDiag      configDiag
	log             *debugui.Log
}

// llmConfig 读取当前生效的 LLM 配置。
func (a *app) llmConfig() llm.LLMConfig { return a.llmRt.Get() }

// activateLLMConfig 立即切换生效配置，并尝试落盘。
// 调用方应先 Validate；本函数不做校验，返回的 error 只表示落盘失败
// （此时配置已生效，但重启后会回到旧值）。
func (a *app) activateLLMConfig(cfg llm.LLMConfig) error {
	cfg = cfg.Normalize()
	a.llmRt.Set(cfg)
	if err := llm.Save(a.modelConfigPath, cfg); err != nil {
		log.Printf("[llm] 配置已生效但落盘失败 (%s): %v", a.modelConfigPath, err)
		return err
	}
	log.Printf("[llm] 配置已生效并写入 %s", a.modelConfigPath)
	return nil
}

// enqueue 把事件提交给优先级调度器，并登记调试记录。
// 返回调试记录序号与是否成功进入队列（false 表示提交阶段即被丢弃，如队列满）。
func (a *app) enqueue(event model.UnifiedEvent) (int64, bool) {
	priority := dispatch.PriorityFor(event.MessageType)
	entry := debugui.Entry{
		Platform:   event.Platform,
		User:       event.UserName,
		MessageTyp: event.MessageType,
		Content:    event.Content,
		Priority:   int(priority),
		PriorityZh: priorityLabel(priority),
		Queued:     true,
		Reason:     debugui.ReasonSkipped,
		Detail:     "排队中",
	}
	recorded := a.log.Add(entry)

	task := buildSpeakTask(event, a.llmConfig(), a.ttsClient, a.vtuberClient, a.mimoClient, a.dispatcher, a.ttlText, a.ttlGift)
	accepted := false
	task.Submitted = func(ok bool) { accepted = ok }
	task.OnDrop = func(reason string) {
		log.Printf("[distillery] [%s] %s 丢弃 (%s)", event.Platform, event.UserName, reason)
		a.log.Update(recorded.Seq, func(e *debugui.Entry) {
			e.Queued = false
			e.Reason = dropReason(reason)
			e.Detail = reason
		})
	}
	task.Speak = func(ctx context.Context, boost float64) bool {
		start := time.Now()
		result := processEvent(ctx, event, a.llmConfig(), a.ttsClient, a.vtuberClient, a.mimoClient, a.dispatcher, boost)
		logResult(ctx, event, result, boost)
		a.log.Update(recorded.Seq, func(e *debugui.Entry) {
			e.Boost = boost
			e.WaitMS = start.Sub(recorded.At).Milliseconds()
			e.CostMS = time.Since(start).Milliseconds()
			e.ReplyText = result.ReplyText
			e.SpokenText = result.SpokenText
			e.Emotion = result.Emotion
			e.IntentType = result.IntentType
			e.SetAudio(result.AudioPaths)
			e.TTSError = result.TTSError
			e.Reason, e.Detail = outcomeOf(ctx, result)
			// 完全没有配置发声后端时，明确说明而不是误报 TTS 失败
			if !a.hasTTSBackend() && e.Reason == debugui.ReasonTTSError {
				e.Reason = debugui.ReasonSkipped
				e.Detail = "未配置任何 TTS 后端（仅生成文本回复）"
			}
		})
		return result.TTSSpoken
	}

	a.sched.Submit(task)
	if !accepted {
		// Submit 内部的 Submitted 回调已同步执行；若仍未 accepted 说明提交被拒
		a.log.Update(recorded.Seq, func(e *debugui.Entry) {
			if e.Detail == "排队中" {
				e.Reason = debugui.ReasonQueueFull
				e.Detail = "提交被拒（队列满）"
			}
		})
	}
	return recorded.Seq, accepted
}

// keepKeySentinel 是 /debug/model 的约定值：API Key 传它表示沿用已保存的 Key。
// 用哨兵值而不是空串，避免切换服务商时空 Key 误用上一家的 Key。
const keepKeySentinel = "__keep__"

// modelConfigView 生成回显给界面的模型配置（API Key 只给掩码与是否已设置）。
func modelConfigView(cfg llm.LLMConfig, persistPath string) map[string]any {
	view := map[string]any{
		"provider":       pickProvider("", cfg.Endpoint),
		"endpoint":       cfg.Endpoint,
		"model":          cfg.Model,
		"api_key_masked": cfg.MaskedKey(),
		"api_key_set":    cfg.APIKey != "",
		"configured":     cfg.IsConfigured(),
		"persist_path":   persistPath,
	}
	// 把 TTS 接口填进模型配置是最容易犯的错：形状一样但用途完全不同
	if warn := provider.TTSMisconfig(cfg.Endpoint); warn != "" {
		view["misconfig"] = warn
		if kind, name := provider.ClassifyEndpoint(cfg.Endpoint); kind == provider.KindTTS {
			view["endpoint_kind"] = string(kind)
			view["endpoint_kind_name"] = name
		}
	} else if kind, _ := provider.ClassifyEndpoint(cfg.Endpoint); kind == provider.KindEmbedding {
		view["misconfig"] = "这是一个向量化（embeddings）接口，不是对话模型接口，无法用于意图分析。"
	}
	return view
}

// pickProvider 优先用界面传入的 provider，其次按 endpoint 反查，最后视为自定义。
func pickProvider(hint, endpoint string) string {
	if _, ok := provider.ByKey(hint); ok {
		return hint
	}
	if key := provider.MatchEndpoint(endpoint); key != "" {
		return key
	}
	return "custom"
}

// mimoView 生成回显给界面的 Mimo 试听配置（Key 只给掩码）。
func (a *app) mimoView() map[string]any {
	cur := a.mimoState.Get()
	active := a.mimoClient != nil
	view := map[string]any{
		"api_key_masked": a.mimoState.MaskedKey(),
		"api_key_set":    a.mimoState.Configured(),
		"base_url":       cur.BaseURL,
		"model":          cur.Model,
		"voice":          cur.Voice,
		"format":         cur.Format,
		"voices":         tts.Voices,
		"persist_path":   a.mimoState.Path(),
		// active 表示当前进程是否真的能用（需重启才能把保存的配置变成 active）
		"active":           active,
		"restart_required": a.mimoState.Configured() && !active,
		"audio_available":  a.audioDir != "",
	}
	if !active {
		view["why"] = "Mimo 客户端在启动时初始化；填好 Key 并重启后才会产出音频"
	}
	return view
}

// status 汇总当前生效配置（密钥仅返回是否已配置），供调试页面展示。
func (a *app) status() map[string]any {
	llmCfg := a.llmConfig()
	return map[string]any{
		"listen_addr":       a.cfg.ListenAddr,
		"tts_path":          a.ttsPath,
		"tts_addr":          a.cfg.TTSAddr,
		"vtuber_addr":       a.cfg.VTuberAddr,
		"mimo_configured":   a.mimoClient != nil,
		"mimo_voice":        a.cfg.Mimo.Voice,
		"mimo_model":        a.cfg.Mimo.Model,
		"mimo_format":       a.cfg.Mimo.Format,
		"mimo_voices":       tts.Voices,
		"audio_dir":         a.audioDir,
		"audio_available":   a.audioDir != "",
		"llm_endpoint":      llmCfg.Endpoint,
		"llm_model":         llmCfg.Model,
		"llm_api_key_set":   llmCfg.APIKey != "",
		"llm_provider":      pickProvider("", llmCfg.Endpoint),
		"llm_configured":    llmCfg.IsConfigured(),
		"model_config_path": a.modelConfigPath,
		// 配置加载诊断：调试台据此提示"配置根本没生效"
		"config_path":         a.configDiag.Path,
		"config_found":        a.configDiag.Found,
		"config_parse_error":  a.configDiag.ParseError,
		"config_env_applied":  a.configDiag.EnvApplied,
		"config_used_default": a.configDiag.UsedDefault,
		"speech":              a.cfg.Speech,
		"prompt_cache_note":   "prompt 由 internal/llm/prompts.go 全局缓存，改文件需重启",
		"queue": map[string]any{
			"max_size":            a.cfg.Speech.QueueMaxSize,
			"ttl_text_sec":        a.cfg.Speech.QueueTTLTextSec,
			"ttl_gift_sec":        a.cfg.Speech.QueueTTLGiftSec,
			"backlog_threshold":   a.cfg.Speech.BacklogThreshold,
			"backlog_speed_boost": a.cfg.Speech.BacklogSpeedBoost,
		},
	}
}

// priorityLabel 优先级的中文说明。
func priorityLabel(p dispatch.Priority) string {
	switch p {
	case dispatch.PriorityNormal:
		return "普通弹幕"
	case dispatch.PriorityHigh:
		return "高（提问/@）"
	case dispatch.PriorityGift:
		return "礼物"
	case dispatch.PrioritySC:
		return "SuperChat"
	case dispatch.PriorityCaptain:
		return "舰长"
	default:
		return "低（未知类型）"
	}
}

// dropReason 把调度器的丢弃原因映射为调试记录的结果枚举。
func dropReason(reason string) debugui.Reason {
	switch reason {
	case "timeout":
		return debugui.ReasonTimeout
	case "queue full", "queue full evicted":
		return debugui.ReasonQueueFull
	case "scheduler closed":
		return debugui.ReasonClosed
	default:
		return debugui.ReasonSkipped
	}
}

// hasTTSBackend 判断是否存在任何可用的发声后端。
// 三者皆无时（例如只想起 distillery 调试页面与模板回复），不应把结果记为 TTS 失败。
func (a *app) hasTTSBackend() bool {
	return a.mimoClient != nil || a.vtuberClient != nil || a.cfg.TTSAddr != ""
}

// outcomeOf 根据处理结果判定最终结果枚举。
func outcomeOf(ctx context.Context, r ProcessResult) (debugui.Reason, string) {
	if ctx.Err() != nil {
		return debugui.ReasonInterrupt, "播报中被打断"
	}
	if r.Responded {
		switch {
		case r.TTSApplied && r.TTSSpoken:
			return debugui.ReasonSpoken, ""
		case r.TTSApplied:
			detail := "已生成回复但 TTS 未出声"
			if r.TTSError != "" {
				detail = r.TTSError
			}
			return debugui.ReasonTTSError, detail
		default:
			return debugui.ReasonSkipped, "仅生成回复（策略未要求发声）"
		}
	}
	switch {
	case r.SkipReason == "llm error" || strings.HasPrefix(r.SkipReason, "llm error:"):
		return debugui.ReasonLLMError, r.SkipReason
	case r.SkipReason == "interrupted":
		return debugui.ReasonInterrupt, r.SkipReason
	default:
		return debugui.ReasonSkipped, r.SkipReason
	}
}

// buildSpeakTask 把一个统一事件包装成发言任务，提交给优先级调度器。
// Speak / OnDrop 由调用方按需覆盖（见 app.enqueue），这里给出默认实现。
func buildSpeakTask(event model.UnifiedEvent, llmCfg llm.LLMConfig, ttsClient *tts.Client, vtuberClient *vtuber.Client, mimoClient *tts.MimoClient, dispatcher *dispatch.Dispatcher, ttlText, ttlGift time.Duration) *dispatch.SpeakTask {
	priority := dispatch.PriorityFor(event.MessageType)
	return &dispatch.SpeakTask{
		Priority:      priority,
		Interruptible: dispatch.CanInterrupt(priority),
		Deadline:      time.Now().Add(dispatch.TTLFor(priority, ttlText, ttlGift)),
		Speak: func(ctx context.Context, boost float64) bool {
			result := processEvent(ctx, event, llmCfg, ttsClient, vtuberClient, mimoClient, dispatcher, boost)
			logResult(ctx, event, result, boost)
			return result.TTSSpoken
		},
		Stop: buildStopFn(mimoClient, vtuberClient, ttsClient),
	}
}

// buildStopFn 构造打断当前播报的回调。
// 仅 synapse-tts 回退路径有停止端点；Mimo/注入路径依赖 ctx 取消在途请求（远端播放无中断通道，已知限制）。
func buildStopFn(mimoClient *tts.MimoClient, vtuberClient *vtuber.Client, ttsClient *tts.Client) func() {
	if mimoClient != nil || vtuberClient != nil || ttsClient == nil {
		return nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := ttsClient.Stop(ctx); err != nil {
			log.Printf("[distillery] 打断当前播报失败: %v", err)
		}
	}
}

func logResult(ctx context.Context, event model.UnifiedEvent, result ProcessResult, boost float64) {
	if ctx.Err() != nil {
		log.Printf("[distillery] [%s] %s → 被打断 (intent=%s)", event.Platform, event.UserName, result.IntentType)
		return
	}
	if result.Responded {
		boostInfo := ""
		if boost > 0 {
			boostInfo = fmt.Sprintf(" | boost=%+.2f", boost)
		}
		log.Printf("[distillery] [%s] %s → %s | emotion=%s | tts=%v%s | %.40s",
			event.Platform, event.UserName, result.IntentType, result.Emotion, result.TTSSpoken, boostInfo, result.ReplyText)
	} else {
		log.Printf("[distillery] [%s] %s → 跳过 (%s)", event.Platform, event.UserName, result.SkipReason)
	}
}

func processEvent(ctx context.Context, event model.UnifiedEvent, llmCfg llm.LLMConfig, ttsClient *tts.Client, vtuberClient *vtuber.Client, mimoClient *tts.MimoClient, dispatcher *dispatch.Dispatcher, boost float64) ProcessResult {
	var trace ttsTrace
	priority := dispatch.PriorityFor(event.MessageType)

	// ── 礼物/SC/舰长：直接生成感谢，不走 LLM ──
	if event.MessageType == "gift" || event.MessageType == "super_chat" || event.MessageType == "captain" {
		if !dispatcher.ShouldRespond("gift_thanks", priority) {
			return ProcessResult{SkipReason: "cooldown"}
		}

		if event.MessageType == "super_chat" && event.Content != "" {
			thanks, followUp := dispatch.BuildSCReply(event)
			spoken := sendTTS(ctx, ttsClient, vtuberClient, mimoClient, thanks, "joy", "sc_thanks", 0.9, boost, trace.report)
			if followUp != "" {
				// 两段式 SC 回复间隔；等待期间被打断则提前结束
				select {
				case <-ctx.Done():
					r := ProcessResult{
						Responded: true, ReplyText: thanks, SpokenText: thanks,
						Emotion: "joy", IntentType: "sc_reply", TTSSpoken: spoken,
						TTSApplied: true, SkipReason: "interrupted",
					}
					trace.apply(&r)
					return r
				case <-time.After(1500 * time.Millisecond):
				}
				sendTTS(ctx, ttsClient, vtuberClient, mimoClient, followUp, "smirk", "sc_followup", 0.7, boost, trace.report)
			}
			fullText := thanks
			if followUp != "" {
				fullText += " | " + followUp
			}
			r := ProcessResult{
				Responded: true, ReplyText: fullText,
				SpokenText: thanks + " … " + followUp,
				Emotion:    "joy", IntentType: "sc_reply", TTSSpoken: spoken, TTSApplied: true,
			}
			trace.apply(&r)
			return r
		}

		replyText := dispatch.BuildGiftReply(event)
		if replyText == "" {
			return ProcessResult{SkipReason: "empty gift reply"}
		}

		emo := "joy"
		if event.MessageType == "captain" {
			emo = "surprise"
		}
		spoken := sendTTS(ctx, ttsClient, vtuberClient, mimoClient, replyText, emo, "gift_thanks", 0.9, boost, trace.report)
		r := ProcessResult{
			Responded: true, ReplyText: replyText, SpokenText: replyText,
			Emotion: emo, IntentType: "gift_thanks", TTSSpoken: spoken, TTSApplied: true,
		}
		trace.apply(&r)
		return r
	}

	// ── 普通消息：走 LLM 分析 ──
	if !llmCfg.IsConfigured() {
		// 未配置模型时不必等 30s 超时：直接如实说明
		return ProcessResult{SkipReason: "未配置 LLM 模型（普通消息需先在调试台配置模型）"}
	}
	analysis, err := llm.Analyze(ctx, event.Content, event.UserName, llmCfg)
	if err != nil {
		if ctx.Err() != nil {
			return ProcessResult{SkipReason: "interrupted"}
		}
		log.Printf("[distillery] LLM 分析失败: %v", err)
		return ProcessResult{SkipReason: "llm error: " + err.Error()}
	}

	if !dispatcher.ShouldRespond(analysis.IntentType, priority) {
		return ProcessResult{
			SkipReason: "weight/cooldown", IntentType: analysis.IntentType, Emotion: analysis.Emotion,
		}
	}

	if !analysis.ShouldReply || analysis.ReplyText == "" {
		return ProcessResult{
			SkipReason: "no reply needed", IntentType: analysis.IntentType, Emotion: analysis.Emotion,
		}
	}

	if !analysis.ShouldSpeak || !analysis.TTSEnabled {
		// 生成回复但按策略不发声：不算失败
		return ProcessResult{
			Responded: true, ReplyText: analysis.ReplyText,
			Emotion: analysis.Emotion, IntentType: analysis.IntentType,
			TTSSpoken: true, TTSApplied: false,
		}
	}

	spoken := sendTTS(ctx, ttsClient, vtuberClient, mimoClient, analysis.ReplyText, analysis.Emotion, "llm_reply", analysis.Intensity, boost, trace.report)

	r := ProcessResult{
		Responded: true, ReplyText: analysis.ReplyText, SpokenText: analysis.ReplyText,
		Emotion: analysis.Emotion, IntentType: analysis.IntentType,
		TTSSpoken: spoken, TTSApplied: true,
	}
	trace.apply(&r)
	return r
}

// ttsTrace 收集一次事件处理里各段 TTS 的结果，用于调试台展示与试听。
type ttsTrace struct {
	audio []string // 生成的音频文件路径（相对进程工作目录）
	errs  []string // TTS 失败原因
}

func (t *ttsTrace) report(file, errMsg string) {
	if file != "" {
		t.audio = append(t.audio, file)
	}
	if errMsg != "" {
		t.errs = append(t.errs, errMsg)
	}
}

func (t *ttsTrace) apply(r *ProcessResult) {
	r.AudioPaths = t.audio
	if len(t.errs) > 0 {
		r.TTSError = strings.Join(t.errs, " | ")
	}
}

// sendTTS 按 Mimo → VTuber 注入 → synapse-tts 的优先级发声。
// prefix 用作音频文件名前缀（便于调试台区分来源）；report 可空，用于回传音频文件路径或失败原因（试听用）。
func sendTTS(ctx context.Context, client *tts.Client, vtuberClient *vtuber.Client, mimoClient *tts.MimoClient, text, emo, prefix string, intensity, boost float64, report func(file, errMsg string)) bool {
	if report == nil {
		report = func(string, string) {}
	}

	// 优先 Mimo 直连（绕过 LLM-Vup 的 TTS 引擎）
	if mimoClient != nil {
		tagged := text
		if emo != "" {
			tagged = "[" + emo + "] " + text
		}
		filePath, err := mimoClient.GenerateAudioBoost(ctx, tagged, prefix, boost)
		if err != nil {
			log.Printf("[distillery] Mimo TTS 失败: %v", err)
			report("", err.Error())
			return false
		}
		log.Printf("[distillery] Mimo TTS 生成: %s", filePath)
		report(filePath, "")
		return true
	}

	// 其次 VTuber /inject 注入（注入协议无语速通道，积压加速不生效）
	if vtuberClient != nil {
		err := vtuberClient.Speak(ctx, vtuber.SpeakRequest{
			Text:      text,
			Emotion:   emo,
			Intensity: intensity,
		})
		if err != nil {
			log.Printf("[distillery] VTuber 注入失败: %v", err)
			report("", err.Error())
			return false
		}
		// 注入路径无本地音频文件，前端自行播放
		report("", "")
		return true
	}

	// 回退 synapse-tts
	params := emotion.Modulate(emo, intensity)
	if boost > 0 {
		params = emotion.BoostRate(params, boost)
	}
	req := tts.SpeakRequest{
		Text:   text,
		Rate:   params.Rate,
		Volume: params.Volume,
		Pitch:  params.Pitch,
	}
	if err := client.SpeakWithOpts(ctx, req); err != nil {
		log.Printf("[distillery] TTS 播放失败: %v", err)
		report("", err.Error())
		return false
	}
	// synapse-tts 由服务端直接播放，distillery 不持有音频文件
	report("", "")
	return true
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
