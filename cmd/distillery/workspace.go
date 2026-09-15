package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Agentropism/llm-vup-bridge/internal/debugui"
	"github.com/Agentropism/llm-vup-bridge/internal/llm"
	"github.com/Agentropism/llm-vup-bridge/internal/model"
	"github.com/Agentropism/llm-vup-bridge/internal/vtuber"
	"github.com/go-chi/chi/v5"
)

type studioSettings struct {
	Persona      string `json:"persona"`
	HistoryTurns int    `json:"history_turns"`
	OutputMode   string `json:"output_mode"`
	VTuberAddr   string `json:"vtuber_addr"`
}
type studio struct {
	mu            sync.RWMutex
	settings      studioSettings
	path          string
	conversations *llm.Conversations
}

func newStudio(path, addr string) (*studio, error) {
	s := &studio{path: path, conversations: llm.NewConversations(), settings: studioSettings{
		Persona:      "你是 Mili，机灵、自信、亲切的虚拟主播。自然接话，偶尔带笑意地轻轻吐槽，认真回答问题。通常一到三句，不强塞口癖，不贬低观众。",
		HistoryTurns: 8, OutputMode: "auto", VTuberAddr: addr,
	}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &s.settings); err != nil {
		return nil, err
	}
	if err = s.settings.validate(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s studioSettings) validate() error {
	if len([]rune(s.Persona)) > 4000 {
		return fmt.Errorf("角色设定最多 4000 字")
	}
	if s.HistoryTurns < 1 || s.HistoryTurns > 20 {
		return fmt.Errorf("上下文轮数必须为 1–20")
	}
	switch s.OutputMode {
	case "auto", "preview", "vtuber", "text":
	default:
		return fmt.Errorf("未知输出方式")
	}
	if s.VTuberAddr != "" {
		u, err := url.Parse(s.VTuberAddr)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("主项目地址必须是有效的 HTTP(S) 地址")
		}
	}
	if s.OutputMode == "vtuber" && s.VTuberAddr == "" {
		return fmt.Errorf("请填写主项目地址")
	}
	return nil
}
func (s *studio) get() studioSettings { s.mu.RLock(); defer s.mu.RUnlock(); return s.settings }
func (s *studio) save(v studioSettings) error {
	v.VTuberAddr = strings.TrimRight(strings.TrimSpace(v.VTuberAddr), "/")
	if err := v.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		data, _ := json.MarshalIndent(v, "", "  ")
		f, err := os.CreateTemp(filepath.Dir(s.path), ".studio-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.Write(data); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, s.path); err != nil {
			return err
		}
	}
	s.settings = v
	return nil
}
func conversationKey(e model.UnifiedEvent) string {
	scope := e.GroupID
	if scope == "" {
		scope = e.UserID
	}
	if scope == "" {
		scope = e.UserName
	}
	b, _ := json.Marshal([]string{e.Platform, scope})
	return string(b)
}

type processOptions struct {
	History      []map[string]string
	Persona      string
	Direct, Mute bool
}

// process shares the same context and output selection between live and debug.
func (a *app) process(ctx context.Context, e model.UnifiedEvent, direct, mute bool) (result ProcessResult) {
	if a.studio == nil {
		return processEvent(ctx, e, a.llmConfig(), a.ttsClient, a.vtuberClient, a.mimoClient, a.dispatcher, 0)
	}
	settings := a.studio.get()
	history, finish, err := a.studio.conversations.Begin(conversationKey(e), settings.HistoryTurns)
	if err != nil {
		return ProcessResult{SkipReason: err.Error()}
	}
	defer func() {
		reply := ""
		if ctx.Err() == nil && result.Responded && (direct || !result.TTSApplied || result.TTSSpoken) {
			reply = result.ReplyText
		}
		finish(fmt.Sprintf("[用户:%s] %s", e.UserName, e.Content), reply)
	}()
	tc, vc, mc := a.ttsClient, a.vtuberClient, a.mimoClient
	switch settings.OutputMode {
	case "auto":
		vc = nil
		if settings.VTuberAddr != "" {
			vc = vtuber.NewClient(settings.VTuberAddr)
		}
	case "preview":
		tc = nil
		vc = nil
	case "vtuber":
		vc = vtuber.NewAudioClient(settings.VTuberAddr)
	case "text":
		mute = true
	}
	if mute {
		tc = nil
		vc = nil
		mc = nil
	}
	return processEvent(ctx, e, a.llmConfig(), tc, vc, mc, a.dispatcher, boostFromContext(ctx), processOptions{History: history, Persona: settings.Persona, Direct: direct, Mute: mute})
}

type boostKey struct{}

func boostFromContext(ctx context.Context) float64 { v, _ := ctx.Value(boostKey{}).(float64); return v }

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求 JSON 无效或过大"})
		return false
	}
	return true
}
func (a *app) mountStudio(r chi.Router) {
	r.Get("/debug/studio.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(debugui.StudioJS())
	})
	r.Get("/debug/studio", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.studio.get()) })
	r.Post("/debug/studio", func(w http.ResponseWriter, r *http.Request) {
		var v studioSettings
		if !decodeBody(w, r, &v) {
			return
		}
		if err := v.validate(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := a.studio.save(v); err != nil {
			writeJSON(w, 500, map[string]string{"error": "保存失败，设置未改变：" + err.Error()})
			return
		}
		writeJSON(w, 200, a.studio.get())
	})
	r.Post("/debug/chat", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Session string `json:"session"`
			Text    string `json:"text"`
			User    string `json:"user"`
			Mute    bool   `json:"mute"`
		}
		if !decodeBody(w, r, &v) {
			return
		}
		if strings.TrimSpace(v.Text) == "" || len([]rune(v.Text)) > 2000 || v.Session == "" || len(v.Session) > 128 {
			writeJSON(w, 400, map[string]string{"error": "需要会话标识和 1–2000 字消息"})
			return
		}
		if v.User == "" {
			v.User = "测试观众"
		}
		e := model.UnifiedEvent{Platform: "debug-chat", GroupID: v.Session, UserName: v.User, Content: v.Text, MessageType: "text"}
		result := a.process(r.Context(), e, true, v.Mute)
		urls := []string{}
		for _, p := range result.AudioPaths {
			urls = append(urls, "/debug/audio/"+url.PathEscape(filepath.Base(p)))
		}
		writeJSON(w, 200, map[string]any{"result": result, "audio_urls": urls})
	})
	r.Post("/debug/chat/clear", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Session string `json:"session"`
		}
		if !decodeBody(w, r, &v) {
			return
		}
		if err := a.studio.conversations.Clear(conversationKey(model.UnifiedEvent{Platform: "debug-chat", GroupID: v.Session})); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	r.Get("/debug/integration", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.studio.get()
		if cfg.VTuberAddr == "" {
			writeJSON(w, 400, map[string]string{"error": "请先填写并保存主项目地址"})
			return
		}
		status, err := vtuber.NewClient(cfg.VTuberAddr).Health(r.Context())
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, status)
	})
}
