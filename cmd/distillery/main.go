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
	VTuberAddr string `json:"vtuber_addr"`
	// Mimo TTS 直连配置（非空时优先使用，绕过 LLM-Vup 的 TTS 引擎）
	Mimo struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Voice   string `json:"voice"`
		Format  string `json:"format"`
	} `json:"mimo"`
	LLM struct {
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
	if v := os.Getenv("MIMO_API_KEY"); v != "" {
		cfg.Mimo.APIKey = v
	}
	if v := os.Getenv("MIMO_VOICE"); v != "" {
		cfg.Mimo.Voice = v
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

	var vtuberClient *vtuber.Client
	if cfg.VTuberAddr != "" {
		vtuberClient = vtuber.NewClient(cfg.VTuberAddr)
	}

	var mimoClient *tts.MimoClient
	if cfg.Mimo.APIKey != "" {
		mimoCfg := tts.MimoConfig{
			APIKey:   cfg.Mimo.APIKey,
			BaseURL:  cfg.Mimo.BaseURL,
			Model:    cfg.Mimo.Model,
			Voice:    cfg.Mimo.Voice,
			Format:   cfg.Mimo.Format,
			CacheDir: "cache",
		}
		mimoClient = tts.NewMimoClient(mimoCfg)
		log.Printf("[mimo] 直连 MiMo TTS | voice=%s | model=%s", cfg.Mimo.Voice, cfg.Mimo.Model)
	}

	llmCfg := llm.LLMConfig{
		Endpoint: cfg.LLM.Endpoint,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
	}

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
		go handleEvent(req.Context(), event, llmCfg, ttsClient, vtuberClient, mimoClient, dispatcher)
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
		result := processEvent(req.Context(), event, llmCfg, ttsClient, vtuberClient, mimoClient, dispatcher)
		writeJSON(w, http.StatusOK, result)
	})

	// Mimo TTS 直连测试端点
	r.Post("/tts/mimo", func(w http.ResponseWriter, req *http.Request) {
		if mimoClient == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mimo not configured"})
			return
		}
		var body struct {
			Text    string `json:"text"`
			Emotion string `json:"emotion"`
			Speed   float64 `json:"speed"`
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
		filePath, err := mimoClient.GenerateAudio(req.Context(), tagged, "test")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "file": filePath})
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
	t := "synapse"
	if mimoClient != nil {
		t = "mimo"
	}
	log.Printf("[distillery] 启动 | 监听: %s | VTuber: %s | TTS: %s | LLM: %s (%s) | 冷却: %ds",
		cfg.ListenAddr, vtuberInfo, t, cfg.LLM.Endpoint, cfg.LLM.Model, cfg.Speech.CooldownSec)

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

func handleEvent(ctx context.Context, event model.UnifiedEvent, llmCfg llm.LLMConfig, ttsClient *tts.Client, vtuberClient *vtuber.Client, mimoClient *tts.MimoClient, dispatcher *dispatch.Dispatcher) {
	result := processEvent(ctx, event, llmCfg, ttsClient, vtuberClient, mimoClient, dispatcher)
	if result.Responded {
		log.Printf("[distillery] [%s] %s → %s | emotion=%s | tts=%v | %.40s",
			event.Platform, event.UserName, result.IntentType, result.Emotion, result.TTSSpoken, result.ReplyText)
	} else {
		log.Printf("[distillery] [%s] %s → 跳过 (%s)", event.Platform, event.UserName, result.SkipReason)
	}
}

func processEvent(ctx context.Context, event model.UnifiedEvent, llmCfg llm.LLMConfig, ttsClient *tts.Client, vtuberClient *vtuber.Client, mimoClient *tts.MimoClient, dispatcher *dispatch.Dispatcher) ProcessResult {
	priority := dispatch.PriorityFor(event.MessageType)

	// ── 礼物/SC/舰长：直接生成感谢，不走 LLM ──
	if event.MessageType == "gift" || event.MessageType == "super_chat" || event.MessageType == "captain" {
		if !dispatcher.ShouldRespond("gift_thanks", priority) {
			return ProcessResult{SkipReason: "cooldown"}
		}

		if event.MessageType == "super_chat" && event.Content != "" {
			thanks, followUp := dispatch.BuildSCReply(event)
			spoken := sendTTS(ctx, ttsClient, vtuberClient, mimoClient, thanks, "joy", 0.9)
			if followUp != "" {
				time.Sleep(1500 * time.Millisecond)
				sendTTS(ctx, ttsClient, vtuberClient, mimoClient, followUp, "smirk", 0.7)
			}
			fullText := thanks
			if followUp != "" {
				fullText += " | " + followUp
			}
			return ProcessResult{
				Responded: true, ReplyText: fullText,
				Emotion: "joy", IntentType: "sc_reply", TTSSpoken: spoken,
			}
		}

		replyText := dispatch.BuildGiftReply(event)
		if replyText == "" {
			return ProcessResult{SkipReason: "empty gift reply"}
		}

		emo := "joy"
		if event.MessageType == "captain" {
			emo = "surprise"
		}
		spoken := sendTTS(ctx, ttsClient, vtuberClient, mimoClient, replyText, emo, 0.9)
		return ProcessResult{
			Responded: true, ReplyText: replyText,
			Emotion: emo, IntentType: "gift_thanks", TTSSpoken: spoken,
		}
	}

	// ── 普通消息：走 LLM 分析 ──
	analysis, err := llm.Analyze(ctx, event.Content, event.UserName, llmCfg)
	if err != nil {
		log.Printf("[distillery] LLM 分析失败: %v", err)
		return ProcessResult{SkipReason: "llm error"}
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

	spoken := false
	if analysis.ShouldSpeak && analysis.TTSEnabled {
		spoken = sendTTS(ctx, ttsClient, vtuberClient, mimoClient, analysis.ReplyText, analysis.Emotion, analysis.Intensity)
	}

	return ProcessResult{
		Responded: true, ReplyText: analysis.ReplyText,
		Emotion: analysis.Emotion, IntentType: analysis.IntentType, TTSSpoken: spoken,
	}
}

func sendTTS(ctx context.Context, client *tts.Client, vtuberClient *vtuber.Client, mimoClient *tts.MimoClient, text, emo string, intensity float64) bool {
	// 优先 Mimo 直连（绕过 LLM-Vup 的 TTS 引擎）
	if mimoClient != nil {
		tagged := text
		if emo != "" {
			tagged = "[" + emo + "] " + text
		}
		filePath, err := mimoClient.GenerateAudio(ctx, tagged, "speech")
		if err != nil {
			log.Printf("[distillery] Mimo TTS 失败: %v", err)
			return false
		}
		log.Printf("[distillery] Mimo TTS 生成: %s (%d bytes)", filePath, 0) // simplified
		return true
	}

	// 其次 VTuber /inject 注入
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

	// 回退 synapse-tts
	params := emotion.Modulate(emo, intensity)
	req := tts.SpeakRequest{
		Text:   text,
		Rate:   params.Rate,
		Volume: params.Volume,
		Pitch:  params.Pitch,
	}
	if err := client.SpeakWithOpts(ctx, req); err != nil {
		log.Printf("[distillery] TTS 播放失败: %v", err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
