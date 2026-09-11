package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 配置文件的相对路径必须锚定到 config.json 所在目录（baseDir），
// 否则从不同工作目录启动会写到不同位置（表现为“保存了但找不到文件”）。
func TestResolveModelConfigPathAnchorsToBaseDir(t *testing.T) {
	got := resolveModelConfigPath("model_config.json", "/srv/vup")
	want := filepath.Join("/srv/vup", "model_config.json")
	if got != want {
		t.Errorf("相对路径未锚定到配置目录: got %q want %q", got, want)
	}
}

func TestResolveModelConfigPathKeepsAbsolute(t *testing.T) {
	got := resolveModelConfigPath("/etc/vup/models.json", "/srv/vup")
	if got != "/etc/vup/models.json" {
		t.Errorf("绝对路径应原样返回, got %q", got)
	}
}

func TestResolveModelConfigPathWithCwdBase(t *testing.T) {
	// 默认启动方式：config.json 在当前目录，baseDir="." → 解析为当前目录下的绝对路径
	got := resolveModelConfigPath("model_config.json", ".")
	if !filepath.IsAbs(got) {
		t.Fatalf("应解析为绝对路径, got %q", got)
	}
	if filepath.Base(got) != "model_config.json" {
		t.Errorf("文件名不对: %q", got)
	}
	if filepath.Dir(got) != absOf(t, ".") {
		t.Errorf("应位于当前目录: %q", got)
	}
}

func TestResolveModelConfigPathNested(t *testing.T) {
	got := resolveModelConfigPath("sub/models.json", "/srv/vup")
	want := filepath.Join("/srv/vup", "sub", "models.json")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// ── 配置文件定位 ──

func TestFindConfigExplicitMissingIsReported(t *testing.T) {
	// 显式指定时原样返回，found=false 由调用方决定是否致命
	got, found := findConfig("/tmp/definitely-not-here/config.json", true)
	if found {
		t.Error("不存在的文件 found 应为 false")
	}
	if got != "/tmp/definitely-not-here/config.json" {
		t.Errorf("应原样返回显式路径, got %q", got)
	}
}

func TestFindConfigFindsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{}`), 0o600)

	got, found := findConfig(path, false)
	if !found || got != path {
		t.Errorf("应找到已存在的文件: got=%q found=%v", got, found)
	}
}

func TestAbsOrSelfAndOrNone(t *testing.T) {
	if got := absOrSelf(""); got != "(未启用)" {
		t.Errorf("空路径展示 = %q", got)
	}
	if got := absOrSelf("config.json"); !filepath.IsAbs(got) {
		t.Errorf("相对路径应转为绝对路径: %q", got)
	}
	if got := orNone(""); got != "未启用" {
		t.Errorf("orNone(空) = %q", got)
	}
	if got := orNone("/tmp/cache"); got != "/tmp/cache" {
		t.Errorf("orNone(非空) = %q", got)
	}
}

func absOf(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// ── 配置加载诊断 ──
// 之前缺失/损坏的配置都会静默退化为内置默认值，症状表现为"功能坏了"（如试听 404），
// 而不是"配置错了"，因此这里把三种情况的诊断钉死。

func TestLoadConfigValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{
		"listen_addr": ":19999",
		"mimo": {"api_key": "sk-a", "voice": "苏打"},
		"llm": {"endpoint": "https://api.deepseek.com/v1", "model": "deepseek-chat"}
	}`), 0o600)

	cfg, diag := loadConfig(path)
	if !diag.Found || diag.ParseError != "" || diag.UsedDefault {
		t.Fatalf("有效配置不应有异常诊断: %+v", diag)
	}
	if cfg.ListenAddr != ":19999" {
		t.Errorf("ListenAddr = %q（文件内容未生效）", cfg.ListenAddr)
	}
	if cfg.Mimo.APIKey != "sk-a" || cfg.Mimo.Voice != "苏打" {
		t.Errorf("mimo 配置未生效: %+v", cfg.Mimo)
	}
	if cfg.LLM.Model != "deepseek-chat" {
		t.Errorf("llm 配置未生效: %+v", cfg.LLM)
	}
	// 未在文件中出现的字段应保留内置默认值，而不是被清零
	if cfg.Speech.CooldownSec != 5 {
		t.Errorf("未指定字段应保留默认值, CooldownSec = %d", cfg.Speech.CooldownSec)
	}
}

func TestLoadConfigMissingFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-there.json")
	cfg, diag := loadConfig(path)

	if diag.Found {
		t.Error("文件不存在时 Found 应为 false")
	}
	if diag.UsedDefault != true {
		t.Error("文件不存在时 UsedDefault 应为 true")
	}
	if diag.ParseError != "" {
		t.Errorf("文件不存在不应算作解析错误: %q", diag.ParseError)
	}
	if diag.Path == "" || !filepath.IsAbs(diag.Path) {
		t.Errorf("诊断里应给出绝对路径便于定位, got %q", diag.Path)
	}
	// 仍然返回可用的默认配置，由调用方决定是否致命
	if cfg.ListenAddr != ":9528" {
		t.Errorf("应回落到默认监听地址, got %q", cfg.ListenAddr)
	}
}

func TestLoadConfigBrokenJSONIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"listen_addr": ":1", oops}`), 0o600)

	_, diag := loadConfig(path)
	if diag.ParseError == "" {
		t.Fatal("坏 JSON 必须报告解析错误（此前被静默忽略）")
	}
	if diag.Found {
		t.Error("解析失败时不应标记 Found")
	}
	if !diag.UsedDefault {
		t.Error("解析失败时应标记使用了默认值")
	}
}

func TestLoadConfigEnvOverridesAreListed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"listen_addr": ":1"}`), 0o600)

	t.Setenv("LISTEN_ADDR", ":17001")
	t.Setenv("MIMO_API_KEY", "sk-env")
	t.Setenv("TTS_ADDR", "") // 空值不应算作覆盖

	cfg, diag := loadConfig(path)
	if cfg.ListenAddr != ":17001" {
		t.Errorf("环境变量未生效: %q", cfg.ListenAddr)
	}
	if cfg.Mimo.APIKey != "sk-env" {
		t.Errorf("MIMO_API_KEY 未生效: %q", cfg.Mimo.APIKey)
	}
	joined := ""
	for _, name := range diag.EnvApplied {
		joined += name + ","
	}
	if !strings.Contains(joined, "LISTEN_ADDR") || !strings.Contains(joined, "MIMO_API_KEY") {
		t.Errorf("已生效的环境变量未被列出: %v", diag.EnvApplied)
	}
	if strings.Contains(joined, "TTS_ADDR") {
		t.Errorf("空环境变量不应算作覆盖: %v", diag.EnvApplied)
	}
}
