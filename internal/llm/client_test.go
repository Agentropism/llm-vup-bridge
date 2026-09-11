package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	valid := LLMConfig{Endpoint: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKey: "sk-x"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("完整配置不应报错: %v", err)
	}

	cases := []struct {
		name string
		cfg  LLMConfig
		want string
	}{
		{"缺 endpoint", LLMConfig{Model: "m", APIKey: "k"}, "接口地址"},
		{"endpoint 缺协议", LLMConfig{Endpoint: "api.deepseek.com", Model: "m", APIKey: "k"}, "http"},
		{"缺 model", LLMConfig{Endpoint: "https://a/v1", APIKey: "k"}, "模型"},
		{"缺 key", LLMConfig{Endpoint: "https://a/v1", Model: "m"}, "API Key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if err == nil {
				t.Fatal("应当报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息 %q 未包含 %q", err.Error(), c.want)
			}
		})
	}
}

func TestMaskedKeyNeverLeaksFullKey(t *testing.T) {
	cfg := LLMConfig{APIKey: "sk-1234567890abcdef"}
	masked := cfg.MaskedKey()
	if strings.Contains(masked, "1234567890") {
		t.Errorf("掩码泄露了 Key 主体: %q", masked)
	}
	if !strings.HasSuffix(masked, "cdef") {
		t.Errorf("掩码应保留末 4 位便于核对: %q", masked)
	}
	if got := (LLMConfig{APIKey: "ab"}).MaskedKey(); got != "**" {
		t.Errorf("短 Key 掩码 = %q, want **", got)
	}
	if got := (LLMConfig{}).MaskedKey(); got != "" {
		t.Errorf("空 Key 掩码 = %q, want 空", got)
	}
}

func TestNormalize(t *testing.T) {
	got := LLMConfig{Endpoint: "  https://api.openai.com/v1///  ", Model: " gpt-4o ", APIKey: " sk-1 "}.Normalize()
	if got.Endpoint != "https://api.openai.com/v1" {
		t.Errorf("Endpoint = %q（应去掉空白与末尾斜杠）", got.Endpoint)
	}
	if got.Model != "gpt-4o" || got.APIKey != "sk-1" {
		t.Errorf("去空白失败: %+v", got)
	}
}

func TestIsConfigured(t *testing.T) {
	if (LLMConfig{Endpoint: "https://a/v1", Model: "m"}).IsConfigured() {
		t.Error("缺 Key 不应算已配置")
	}
	if !(LLMConfig{Endpoint: "https://a/v1", Model: "m", APIKey: "k"}).IsConfigured() {
		t.Error("完整配置应算已配置")
	}
}

func TestRuntimeGetSetIsolatesCallers(t *testing.T) {
	rt := NewRuntime(LLMConfig{Endpoint: "https://a/v1", Model: "m1", APIKey: "k"})

	got := rt.Get()
	got.Model = "mutated" // 改动副本不应影响内部状态
	if rt.Get().Model != "m1" {
		t.Fatal("Get 返回的副本被外部修改后污染了内部配置")
	}

	rt.Set(LLMConfig{Endpoint: "https://b/v1/", Model: "m2", APIKey: "k2"})
	if rt.Get().Model != "m2" || rt.Get().Endpoint != "https://b/v1" {
		t.Fatalf("Set 未生效或未 Normalize: %+v", rt.Get())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model_config.json")
	cfg := LLMConfig{Endpoint: "https://opencode.ai/zen/go/v1", Model: "deepseek-v4-flash", APIKey: "sk-secret"}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 文件含密钥，权限必须是 0600
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("配置文件权限 = %o, want 600（含密钥）", perm)
	}

	loaded, ok, err := Load(path)
	if err != nil || !ok {
		t.Fatalf("Load 失败: ok=%v err=%v", ok, err)
	}
	if loaded != cfg {
		t.Errorf("往返不一致: %+v != %+v", loaded, cfg)
	}

	// 落盘结构应可直接被运维阅读/手改
	raw, _ := os.ReadFile(path)
	var shape struct {
		LLM struct {
			Endpoint string `json:"endpoint"`
			Model    string `json:"model"`
			APIKey   string `json:"api_key"`
		} `json:"llm"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("落盘 JSON 结构不符: %v", err)
	}
	if shape.LLM.Model != "deepseek-v4-flash" {
		t.Errorf("落盘字段 model = %q", shape.LLM.Model)
	}
}

func TestLoadMissingOrEmptyFile(t *testing.T) {
	dir := t.TempDir()

	if _, ok, err := Load(filepath.Join(dir, "nope.json")); ok || err != nil {
		t.Errorf("文件不存在应返回 ok=false, err=nil: ok=%v err=%v", ok, err)
	}

	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte(`{"llm":{}}`), 0o600)
	if _, ok, err := Load(empty); ok || err != nil {
		t.Errorf("空配置应返回 ok=false: ok=%v err=%v", ok, err)
	}

	broken := filepath.Join(dir, "broken.json")
	os.WriteFile(broken, []byte(`{oops`), 0o600)
	if _, _, err := Load(broken); err == nil {
		t.Error("损坏的 JSON 应当报错，而不是静默沿用")
	}
}

func TestSaveEmptyPathIsNoop(t *testing.T) {
	if err := Save("", LLMConfig{Model: "m"}); err != nil {
		t.Errorf("空路径应跳过落盘, got %v", err)
	}
}

func TestCheckWritable(t *testing.T) {
	// 不存在的目录可以被创建 → 视为可写
	nested := filepath.Join(t.TempDir(), "a", "b", "model_config.json")
	if err := CheckWritable(nested); err != nil {
		t.Errorf("应能创建上级目录: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(nested)); err != nil {
		t.Errorf("上级目录未被创建: %v", err)
	}

	// 空路径直接跳过
	if err := CheckWritable(""); err != nil {
		t.Errorf("空路径应跳过检查: %v", err)
	}

	// 只读目录 → 必须报错，且不得留下临时文件
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })

	if err := CheckWritable(filepath.Join(ro, "model_config.json")); err == nil {
		t.Error("只读目录应当报错，否则用户点保存才发现")
	}
	entries, _ := os.ReadDir(ro)
	if len(entries) != 0 {
		t.Errorf("预检不应留下残留文件: %v", entries)
	}
}

// 不同服务商返回的错误体不同：都要给出可读错误，且不能 panic（曾因空 choices 崩过）。
func TestChatFullErrorHandling(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"标准 error 字段", 401, `{"error":{"message":"Invalid API key","type":"auth_error"}}`, "Invalid API key"},
		{"空 choices", 200, `{"choices":[]}`, "choices"},
		{"非 JSON 网关错误页", 502, `<html>bad gateway</html>`, "非 JSON"},
		{"无 error 字段的错误码", 404, `{"detail":"Not Found"}`, "HTTP 404"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer srv.Close()

			_, err := chat(context.Background(), LLMConfig{
				Endpoint: srv.URL, Model: "m", APIKey: "k",
			}, []map[string]string{{"role": "user", "content": "hi"}}, 16)

			if err == nil {
				t.Fatal("应当返回错误")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("错误 %q 未包含 %q", err.Error(), c.wantSub)
			}
		})
	}
}

func TestChatFullSuccess(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Write([]byte(`{"choices":[{"message":{"content":"可用"}}]}`))
	}))
	defer srv.Close()

	out, err := chat(context.Background(), LLMConfig{
		Endpoint: srv.URL + "/", Model: "m", APIKey: "sk-test",
	}, []map[string]string{{"role": "user", "content": "hi"}}, 16)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if out != "可用" {
		t.Errorf("content = %q", out)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("请求路径 = %q（末尾斜杠应被 Normalize 去掉）", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestTestConnectionValidatesBeforeRequest(t *testing.T) {
	res := TestConnection(context.Background(), LLMConfig{Endpoint: "https://a/v1", Model: "m"})
	if res.OK {
		t.Error("缺 Key 不应判定为连通")
	}
	if !strings.Contains(res.Error, "API Key") {
		t.Errorf("错误信息应提示 Key: %q", res.Error)
	}
}

func TestTestConnectionSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"可用"}}]}`))
	}))
	defer srv.Close()

	res := TestConnection(context.Background(), LLMConfig{Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if !res.OK {
		t.Fatalf("应连通: %+v", res)
	}
	if res.Reply != "可用" {
		t.Errorf("Reply = %q", res.Reply)
	}
	if res.Endpoint != srv.URL || res.Model != "m" {
		t.Errorf("回显字段不对: %+v", res)
	}
}
