package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestMimoPreservesShortTextAndUsesExplicitEmotion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Audio map[string]any `json:"audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if got := body.Messages[1].Content; got != "谢谢！" {
			t.Errorf("TTS rewrote text: %q", got)
		}
		if body.Audio["emotion"] != "happy" {
			t.Errorf("first explicit emotion lost: %v", body.Audio)
		}
		if speed, ok := body.Audio["speed"].(float64); ok && (speed < 0.8 || speed > 1.2) {
			t.Errorf("speed outside natural range: %v", speed)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"YXVkaW8="}}}]}`))
	}))
	defer server.Close()
	cfg := DefaultMimoConfig()
	cfg.BaseURL = server.URL
	cfg.CacheDir = t.TempDir()
	client := NewMimoClient(cfg)
	path, err := client.GenerateAudioExpressive(context.Background(), "[joy]谢谢！[anger]", "test", 0, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "audio" {
		t.Fatalf("audio cache: %q %v", data, err)
	}
	_, _, _, appendText := client.ParseEmotion("[joy]好")
	if appendText != "" {
		t.Fatal("short phrase got extra dialogue")
	}
}
