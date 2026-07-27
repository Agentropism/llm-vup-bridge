package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Agentropism/llm-vup-bridge/internal/dispatch"
	"github.com/Agentropism/llm-vup-bridge/internal/emotion"
	"github.com/Agentropism/llm-vup-bridge/internal/llm"
	"github.com/Agentropism/llm-vup-bridge/internal/model"
	"github.com/Agentropism/llm-vup-bridge/internal/tts"
	"github.com/Agentropism/llm-vup-bridge/internal/vtuber"

	"github.com/go-chi/chi/v5"
)

type AppConfig struct {
	ListenAddr string `json:"listen_addr"`
	TTSAddr    string `json:"tts_addr"`
	// VTuber 前端 (LLM-Vup) 地址，非空时回复注入到 Live2D 形象播报；
	// 置空字符串则回退到 synapse-tts 本地放音
	VTuberAddr string `json:"vtuber_addr"`
	LLM        struct {
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
	} `json:"llm"`
	Speech struct {
		CooldownSec    int                `json:"cooldown_sec"`
		ReplyChance    map[string]float64 `json:"reply_chance"`
		GiftBypass     bool               `json:"gift_bypass_cooldown"`
	} `json:"speech"`
}

func loadConfig(path string) AppConfig {
	cfg := AppConfig{ListenAddr: ":9528", TTSAddr: "http://localhost:9527", VTuberAddr: "http://localhost:12393"}
	cfg.LLM.Endpoint = "https://api.openai.com/v1"
	cfg.LLM.Model = "gpt-4o-mini"
	cfg.Speech.CooldownSec = 5
	cfg.Speech.GiftBypass = true
	cfg.Speech.ReplyChance = map[string]float64{
		"chat": 0.6, "greeting": 0.8, "question": 0.95,
		"command": 0.9, "gift_thanks": 1.0,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[config] 未找到 %s，使用默认配置", path)
	} else {
		json.Unmarshal(data, &cfg)
	}

	if v := os.Getenv("LLM_ENDPOINT"); v != "" {
		cfg.LLM.Endpoint = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("TTS_ADDR"); v != "" {
		cfg.TTSAddr = v
	}
	if v := os.Getenv("VTUBER_ADDR"); v != "" {
		cfg.VTuberAddr = v
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	return cfg
}

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	cfg := loadConfig(*cfgPath)
	ttsClient := tts.NewClient(cfg.TTSAddr)

	// VTuberAddr 非空时启用 VTuber 前端播报，否则回退 synapse-tts 本地放音
	var vtuberClient *vtuber.Client
	if cfg.VTuberAddr != "" {
		vtuberClient = vtuber.NewClient(cfg.VTuberAddr)
	}

	llmCfg := llm.LLMConfig{
		Endpoint: cfg.LLM.Endpoint,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
	}

	// 发言调度器
	sw := dispatch.SpeechWeight{
		ReplyChance:        cfg.Speech.ReplyChance,
		Cooldown:           time.Duration(cfg.Speech.CooldownSec) * time.Second,
		GiftBypassCooldown: cfg.Speech.GiftBypass,
	}
	dispatcher := dispatch.NewDispatcher(sw)

	r := chi.NewRouter()

	r.Post("/event", func(w http.ResponseWriter, req *http.Request) {
		var event model.UnifiedEvent
		if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		go handleEvent(req.Context(), event, llmCfg, ttsClient, vtuberClient, dispatcher)
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})

	r.Post("/test", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Text string `json:"text"`
			User string `json:"user"`
			Type string `json:"type"` // text, gift, super_chat, captain
		}
		json.NewDecoder(req.Body).Decode(&body)
		if body.User == "" {
			body.User = "测试用户"
		}
		if body.Type == "" {
			body.Type = "text"
		}

		event := model.UnifiedEvent{
			Platform:    "test",
			UserName:    body.User,
			Content:     body.Text,
			MessageType: body.Type,
		}

		result := processEvent(req.Context(), event, llmCfg, ttsClient, vtuberClient, dispatcher)
		writeJSON(w, http.StatusOK, result)
	})

	r.Post("/tts/stop", func(w http.ResponseWriter, req *http.Request) {
		ttsClient.Stop(req.Context())
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
	})

	r.Post("/tts/skip", func(w http.ResponseWriter, req *http.Request) {
		ttsClient.Skip(req.Context())
		writeJSON(w, http.StatusOK, map[string]string{"status": "skipped"})
	})

	r.Get("/tts/status", func(w http.ResponseWriter, req *http.Request) {
		st, err := ttsClient.Status(req.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)
	})

	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	log.Printf("[distillery] 启动 | 监听: %s | VTuber: %s | TTS: %s | LLM: %s (%s) | 冷却: %ds",
		cfg.ListenAddr, vtuberInfo, cfg.TTSAddr, cfg.LLM.Endpoint, cfg.LLM.Model, cfg.Speech.CooldownSec)

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("[distillery] 服务异常: %v", err)
	}
}

type ProcessResult struct {
	Responded  bool   `json:"responded"`
	ReplyText  string `json:"reply_text,omitempty"`
	Emotion    string `json:"emotion,omitempty"`
	IntentType string `json:"intent_type,omitempty"`
	TTSSpoken  bool   `json:"tts_spoken"`
	SkipReason string `json:"skip_reason,omitempty"`
}

func handleEvent(ctx context.Context, event model.UnifiedEvent, llmCfg llm.LLMConfig, ttsClient *tts.Client, vtuberClient *vtuber.Client, dispatcher *dispatch.Dispatcher) {
	result := processEvent(ctx, event, llmCfg, ttsClient, vtuberClient, dispatcher)
	if result.Responded {
		log.Printf("[distillery] [%s] %s → %s | emotion=%s | tts=%v | %.40s",
			event.Platform, event.UserName, result.IntentType, result.Emotion, result.TTSSpoken, result.ReplyText)
	} else {
		log.Printf("[distillery] [%s] %s → 跳过 (%s)", event.Platform, event.UserName, result.SkipReason)
	}
}

func processEvent(ctx context.Context, event model.UnifiedEvent, llmCfg llm.LLMConfig, ttsClient *tts.Client, vtuberClient *vtuber.Client, dispatcher *dispatch.Dispatcher) ProcessResult {
	priority := dispatch.PriorityFor(event.MessageType)

	// ── 礼物/SC/舰长：直接生成感谢，不走 LLM ──
	if event.MessageType == "gift" || event.MessageType == "super_chat" || event.MessageType == "captain" {
		replyText := dispatch.BuildGiftReply(event)
		if replyText == "" {
			return ProcessResult{SkipReason: "empty gift reply"}
		}

		// 礼物必回，但仍检查调度器（冷却由 GiftBypass 控制）
		if !dispatcher.ShouldRespond("gift_thanks", priority) {
			return ProcessResult{SkipReason: "cooldown"}
		}

		spoken := sendTTS(ctx, ttsClient, vtuberClient, replyText, "joy", 0.9)
		return ProcessResult{
			Responded: true, ReplyText: replyText,
			Emotion: "joy", IntentType: "gift_thanks", TTSSpoken: spoken,
		}
	}

	// ── 普通消息：走 LLM 分析 ──
	analysis, err := llm.Analyze(ctx, event.Content, event.UserName, llmCfg)
	if err != nil {
		log.Printf("[distillery] LLM 分析失败: %v", err)
		return ProcessResult{SkipReason: "llm error"}
	}

	// 发言权重判定
	if !dispatcher.ShouldRespond(analysis.IntentType, priority) {
		return ProcessResult{
			SkipReason: "weight/cooldown", IntentType: analysis.IntentType, Emotion: analysis.Emotion,
		}
	}

	// 不需要回复
	if !analysis.ShouldReply || analysis.ReplyText == "" {
		return ProcessResult{
			SkipReason: "no reply needed", IntentType: analysis.IntentType, Emotion: analysis.Emotion,
		}
	}

	// TTS：should_speak + tts_enabled 都满足才出声
	spoken := false
	if analysis.ShouldSpeak && analysis.TTSEnabled {
		spoken = sendTTS(ctx, ttsClient, vtuberClient, analysis.ReplyText, analysis.Emotion, analysis.Intensity)
	}

	return ProcessResult{
		Responded: true, ReplyText: analysis.ReplyText,
		Emotion: analysis.Emotion, IntentType: analysis.IntentType, TTSSpoken: spoken,
	}
}

// sendTTS 发送回复播报：优先注入 VTuber 前端（Live2D 形象出声+表情），
// 未配置 VTuber 时回退到 synapse-tts 本地放音（带情绪调节参数）
func sendTTS(ctx context.Context, client *tts.Client, vtuberClient *vtuber.Client, text, emo string, intensity float64) bool {
	if vtuberClient != nil {
		err := vtuberClient.Speak(ctx, vtuber.SpeakRequest{
			Text:      text,
			Emotion:   emo,
			Intensity: intensity,
		})
		if err != nil {
			log.Printf("[distillery] VTuber 注入失败: %v", err)
			return false
		}
		return true
	}

	params := emotion.Modulate(emo, intensity)

	err := client.SpeakWithOpts(ctx, tts.SpeakRequest{
		Text:   text,
		Rate:   params.Rate,
		Pitch:  params.Pitch,
		Volume: params.Volume,
	})
	if err != nil {
		log.Printf("[distillery] TTS 发送失败: %v", err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
