package debugui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRingBufferKeepsNewestAndOrdersDesc(t *testing.T) {
	l := NewLog(3)
	for i := 1; i <= 5; i++ {
		l.Add(Entry{Platform: "test", MessageTyp: "text", Priority: i})
	}

	if l.Len() != 3 {
		t.Fatalf("Len() = %d, want 3（容量上限）", l.Len())
	}

	recent := l.Recent(0)
	if len(recent) != 3 {
		t.Fatalf("Recent(0) 返回 %d 条, want 3", len(recent))
	}
	// 最新在前：序号应为 5,4,3
	for i, want := range []int64{5, 4, 3} {
		if recent[i].Seq != want {
			t.Errorf("Recent()[%d].Seq = %d, want %d（最新在前）", i, recent[i].Seq, want)
		}
	}
	// 只保留最新的三条：priority 3/4/5
	if recent[2].Priority != 3 {
		t.Errorf("最旧一条 priority = %d, want 3（1、2 已被覆盖）", recent[2].Priority)
	}
}

func TestRecentLimit(t *testing.T) {
	l := NewLog(10)
	for i := 0; i < 6; i++ {
		l.Add(Entry{MessageTyp: "text"})
	}
	if got := len(l.Recent(2)); got != 2 {
		t.Fatalf("Recent(2) 返回 %d 条, want 2", got)
	}
	if got := len(l.Recent(100)); got != 6 {
		t.Fatalf("Recent(100) 返回 %d 条, want 6（不超过已有条数）", got)
	}
}

func TestSeqMonotonicAndAtFilled(t *testing.T) {
	l := NewLog(4)
	first := l.Add(Entry{MessageTyp: "gift"})
	second := l.Add(Entry{MessageTyp: "captain"})

	if second.Seq != first.Seq+1 {
		t.Fatalf("序号应递增: %d -> %d", first.Seq, second.Seq)
	}
	if first.At.IsZero() {
		t.Error("Add 应自动填充时间戳")
	}
}

func TestUpdateBackfillsResult(t *testing.T) {
	l := NewLog(4)
	e := l.Add(Entry{MessageTyp: "text", Reason: ReasonSkipped, Detail: "排队中"})

	l.Update(e.Seq, func(x *Entry) {
		x.Reason = ReasonSpoken
		x.Detail = ""
		x.CostMS = 42
	})

	got := l.Recent(1)[0]
	if got.Reason != ReasonSpoken {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonSpoken)
	}
	if got.CostMS != 42 {
		t.Errorf("CostMS = %d, want 42", got.CostMS)
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want 空", got.Detail)
	}
}

func TestUpdateUnknownSeqIsNoop(t *testing.T) {
	l := NewLog(2)
	e := l.Add(Entry{MessageTyp: "text", Reason: ReasonSkipped})
	l.Update(e.Seq+999, func(x *Entry) { x.Reason = ReasonTimeout })

	if got := l.Recent(1)[0].Reason; got != ReasonSkipped {
		t.Errorf("未知序号不应改动任何条目, Reason = %q", got)
	}
}

func TestDefaultCapacityWhenNonPositive(t *testing.T) {
	if l := NewLog(0); l.size != 200 {
		t.Errorf("NewLog(0).size = %d, want 200", l.size)
	}
	if l := NewLog(-5); l.size != 200 {
		t.Errorf("NewLog(-5).size = %d, want 200", l.size)
	}
}

func TestIndexPageEmbedded(t *testing.T) {
	page := IndexHTML()
	if len(page) == 0 {
		t.Fatal("调试页面为空：go:embed 未打包 index.html")
	}
	if got := string(page[:15]); got != "<!doctype html>" {
		t.Errorf("页面开头 = %q, want <!doctype html>", got)
	}
}

// ── 试听 ──

func TestSetAudioUsesBasenameAndURL(t *testing.T) {
	var e Entry
	e.SetAudio([]string{"/var/cache/mimo/cache/mimo_speech_123.mp3", ""})

	if len(e.Audio) != 1 || e.Audio[0] != "mimo_speech_123.mp3" {
		t.Fatalf("Audio = %v, want 只保留基名", e.Audio)
	}
	if len(e.AudioURLs) != 1 || e.AudioURLs[0] != "/debug/audio/mimo_speech_123.mp3" {
		t.Fatalf("AudioURLs = %v", e.AudioURLs)
	}
	// 再次设置应覆盖而不是累加
	e.SetAudio([]string{"/tmp/mimo_gift_thanks_456.mp3"})
	if len(e.Audio) != 1 || e.Audio[0] != "mimo_gift_thanks_456.mp3" {
		t.Fatalf("重复 SetAudio 未覆盖: %v", e.Audio)
	}
}

func TestSetAudioEmpty(t *testing.T) {
	e := Entry{Audio: []string{"old.mp3"}, AudioURLs: []string{"/debug/audio/old.mp3"}}
	e.SetAudio(nil)
	if len(e.Audio) != 0 || len(e.AudioURLs) != 0 {
		t.Fatalf("SetAudio(nil) 应清空, got %v / %v", e.Audio, e.AudioURLs)
	}
}

func TestMimeForAudio(t *testing.T) {
	cases := map[string]string{
		"a.mp3": "audio/mpeg", "a.MP3": "audio/mpeg", "a.wav": "audio/wav",
		"a.ogg": "audio/ogg", "a.m4a": "audio/mp4", "a.flac": "audio/flac",
		"a.bin": "application/octet-stream",
	}
	for name, want := range cases {
		if got := MimeForAudio(name); got != want {
			t.Errorf("MimeForAudio(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAudioHandlerServesClip(t *testing.T) {
	dir := t.TempDir()
	body := []byte("ID3fake-mp3-bytes")
	if err := os.WriteFile(filepath.Join(dir, "mimo_speech_1.mp3"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	h := AudioHandler(dir)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/debug/audio/mimo_speech_1.mp3", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", ct)
	}
	if got := rec.Body.String(); got != string(body) {
		t.Errorf("响应体不匹配: %q", got)
	}
}

func TestAudioHandlerRejectsTraversalAndBadNames(t *testing.T) {
	dir := t.TempDir()
	// 缓存目录外的“机密文件”
	secret := filepath.Join(filepath.Dir(dir), "secret.mp3")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(secret)

	h := AudioHandler(dir)
	for _, path := range []string{
		"/debug/audio/../secret.mp3",
		"/debug/audio/%2e%2e%2fsecret.mp3",
		"/debug/audio/sub/mimo_x.mp3",
		"/debug/audio/notmimo_1.mp3",
		"/debug/audio/",
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s 状态码 = %d, want 400", path, rec.Code)
		}
	}
}

func TestAudioHandlerMissingFileAndDisabled(t *testing.T) {
	h := AudioHandler(t.TempDir())
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/debug/audio/mimo_missing_1.mp3", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在的文件状态码 = %d, want 404", rec.Code)
	}

	// 未配置 Mimo 时 audioDir 为空，试听应明确不可用
	disabled := AudioHandler("")
	rec = httptest.NewRecorder()
	disabled(rec, httptest.NewRequest(http.MethodGet, "/debug/audio/mimo_x_1.mp3", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未启用时状态码 = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "未配置 Mimo") {
		t.Errorf("未启用时提示不明确: %q", rec.Body.String())
	}
}

// 支持 Range 请求，浏览器才能拖动进度条试听。
func TestAudioHandlerSupportsRange(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789")
	if err := os.WriteFile(filepath.Join(dir, "mimo_test_9.mp3"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/debug/audio/mimo_test_9.mp3", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	AudioHandler(dir)(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("状态码 = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "2345" {
		t.Errorf("Range 响应体 = %q, want 2345", got)
	}
}
