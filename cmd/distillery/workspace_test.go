package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Agentropism/llm-vup-bridge/internal/dispatch"
	"github.com/Agentropism/llm-vup-bridge/internal/llm"
	"github.com/Agentropism/llm-vup-bridge/internal/model"
	"github.com/Agentropism/llm-vup-bridge/internal/tts"
	"github.com/Agentropism/llm-vup-bridge/internal/vtuber"
	"github.com/go-chi/chi/v5"
)

func TestStudioChatCarriesContextAndClear(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		calls++
		want := 2
		if calls == 2 {
			want = 4
		}
		if len(body.Messages) != want {
			t.Errorf("call %d: messages=%d want=%d", calls, len(body.Messages), want)
		}
		if calls == 2 && body.Messages[2]["content"] != "这就接上了。" {
			t.Errorf("wrong assistant history: %v", body.Messages)
		}
		if !strings.Contains(body.Messages[0]["content"], "测试角色设定") {
			t.Error("persona missing")
		}
		result := `{"intent_type":"chat","emotion":"joy","intensity":0.3,"reply_text":"[joy]这就接上了。","should_reply":true,"should_speak":true,"tts_enabled":true}`
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": result}}}})
	}))
	defer server.Close()
	s, err := newStudio(filepath.Join(t.TempDir(), "studio.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.get()
	cfg.Persona = "测试角色设定"
	cfg.OutputMode = "text"
	if err = s.save(cfg); err != nil {
		t.Fatal(err)
	}
	a := &app{studio: s, llmRt: llm.NewRuntime(llm.LLMConfig{Endpoint: server.URL, Model: "fake", APIKey: "test"}), dispatcher: dispatch.NewDispatcher(dispatch.SpeechWeight{})}
	router := chi.NewRouter()
	a.mountStudio(router)
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return r
	}
	for i := 0; i < 2; i++ {
		r := request("/debug/chat", `{"session":"s1","text":"继续说"}`)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "这就接上了。") {
			t.Fatalf("chat: %d %s", r.Code, r.Body.String())
		}
	}
	r := request("/debug/chat", `{"session":"s2","text":"另一间"}`)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	r = request("/debug/chat/clear", `{"session":"s1"}`)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	r = request("/debug/chat", `{"session":"s1","text":"新话题"}`)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = request("/debug/chat", `{"session":"s1","text":"  "}`); r.Code != 400 {
		t.Fatal("empty input accepted")
	}
}

func TestStudioPersistenceValidationAndRouting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "studio.json")
	s, err := newStudio(path, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.get()
	cfg.OutputMode = "text"
	cfg.HistoryTurns = 3
	if err = s.save(cfg); err != nil {
		t.Fatal(err)
	}
	restored, err := newStudio(path, "")
	if err != nil || restored.get() != cfg {
		t.Fatalf("restore failed: %v", err)
	}
	cfg.OutputMode = "vtuber"
	if err = s.save(cfg); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	if s.get().OutputMode != "text" {
		t.Fatal("invalid update changed runtime")
	}
	a := &app{studio: s, llmRt: llm.NewRuntime(llm.LLMConfig{})}
	result := a.process(context.Background(), model.UnifiedEvent{Platform: "test", Content: "花", UserName: "朋友", MessageType: "gift"}, true, false)
	if !result.Responded || result.TTSApplied {
		t.Fatalf("text mode called TTS: %+v", result)
	}
}

func TestMimoAudioForwardedWithoutSecondSynthesis(t *testing.T) {
	received := false
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body vtuber.SpeakRequest
		json.NewDecoder(r.Body).Decode(&body)
		data, err := base64.StdEncoding.DecodeString(body.AudioBase64)
		if err != nil || string(data) != "audio" || body.Text != "谢谢！" || body.Emotion != "joy" {
			t.Errorf("bad injection: %+v %v", body, err)
		}
		received = true
		w.Write([]byte(`{"status":"ok","clients":1}`))
	}))
	defer bridge.Close()
	mimo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"YXVkaW8="}}}]}`))
	}))
	defer mimo.Close()
	cfg := tts.DefaultMimoConfig()
	cfg.BaseURL = mimo.URL
	cfg.CacheDir = t.TempDir()
	mc := tts.NewMimoClient(cfg)
	if !sendTTS(context.Background(), nil, vtuber.NewAudioClient(bridge.URL), mc, "[joy]谢谢！", "joy", "test", 0.3, 0, nil) || !received {
		t.Fatal("audio not delivered")
	}
	files, _ := os.ReadDir(cfg.CacheDir)
	if len(files) != 1 {
		t.Fatal("generated audio not retained for preview")
	}
}
