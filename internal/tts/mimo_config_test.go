package tts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaskKey(t *testing.T) {
	// 用完全虚构的值，切勿在测试里放任何真实密钥前缀
	if got := MaskKey("sk-example-0123456789"); got != "********6789" {
		t.Errorf("MaskKey = %q", got)
	}
	if got := MaskKey("ab"); got != "**" {
		t.Errorf("短 Key 掩码 = %q", got)
	}
	if got := MaskKey(""); got != "" {
		t.Errorf("空 Key 掩码 = %q", got)
	}
}

func TestNormalizeMimoFillsDefaults(t *testing.T) {
	got := normalizeMimo(MimoFileConfig{APIKey: " sk-1 ", BaseURL: "https://api.xiaomimimo.com/v1/"})
	if got.APIKey != "sk-1" {
		t.Errorf("应去掉 Key 空白: %q", got.APIKey)
	}
	if got.BaseURL != "https://api.xiaomimimo.com/v1" {
		t.Errorf("应去掉末尾斜杠: %q", got.BaseURL)
	}
	// 未填字段用默认值补齐，避免落盘出半截配置
	def := DefaultMimoConfig()
	if got.Model != def.Model || got.Voice != def.Voice || got.Format != def.Format {
		t.Errorf("未填字段未补默认值: %+v", got)
	}
}

func TestMimoStateSaveRequiresKey(t *testing.T) {
	st := NewMimoState(MimoFileConfig{}, filepath.Join(t.TempDir(), "m.json"))
	if err := st.Save(MimoFileConfig{BaseURL: "https://a/v1"}); err == nil {
		t.Error("缺 Key 应报错")
	}
	if err := st.Save(MimoFileConfig{APIKey: "sk-1", BaseURL: "api.xiaomimimo.com"}); err == nil {
		t.Error("地址缺协议应报错")
	}
	if st.Configured() {
		t.Error("保存失败后不应变为已配置")
	}
}

func TestMimoStateSaveWritesFileWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mimo_config.json")
	st := NewMimoState(MimoFileConfig{}, path)

	if err := st.Save(MimoFileConfig{APIKey: "sk-secret-1234", Voice: "茉莉"}); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o, want 600（含密钥）", perm)
	}

	loaded, ok, err := LoadMimoFile(path)
	if err != nil || !ok {
		t.Fatalf("回读失败: ok=%v err=%v", ok, err)
	}
	if loaded.APIKey != "sk-secret-1234" || loaded.Voice != "茉莉" {
		t.Errorf("往返不一致: %+v", loaded)
	}
}

func TestMimoStateConfiguredAndMasked(t *testing.T) {
	st := NewMimoState(MimoFileConfig{}, "")
	if st.Configured() {
		t.Error("无 Key 不应算已配置")
	}
	st.Save(MimoFileConfig{APIKey: "sk-abcdefgh"})
	if !st.Configured() {
		t.Error("有 Key 应算已配置")
	}
	if got := st.MaskedKey(); got != "********efgh" {
		t.Errorf("MaskedKey = %q", got)
	}
}

func TestLoadMimoFileMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := LoadMimoFile(filepath.Join(dir, "nope.json")); ok || err != nil {
		t.Errorf("文件不存在应 ok=false err=nil: ok=%v err=%v", ok, err)
	}

	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte(`{"mimo":{}}`), 0o600)
	if _, ok, _ := LoadMimoFile(empty); ok {
		t.Error("空配置应 ok=false")
	}

	broken := filepath.Join(dir, "broken.json")
	os.WriteFile(broken, []byte(`{oops`), 0o600)
	if _, _, err := LoadMimoFile(broken); err == nil {
		t.Error("坏 JSON 应报错")
	}
}
