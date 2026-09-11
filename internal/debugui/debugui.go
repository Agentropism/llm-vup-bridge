// Package debugui 提供 distillery 的调试页面与调试数据端点。
//
// 页面是编译进二进制的单个 HTML（go:embed），无需前端构建步骤、无外部依赖。
// 调试端点为只读 + 与生产端点同构的写入，不改变既有行为：
//
//	GET  /debug         调试页面
//	GET  /debug/config  当前生效配置（脱敏）
//	GET  /debug/log     最近事件处理记录（内存环形缓冲）
//	POST /debug/queue   提交事件到优先级队列（等价 POST /event，额外返回是否入队）
package debugui

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var files embed.FS

// IndexHTML 返回调试页面内容。
func IndexHTML() []byte {
	data, err := files.ReadFile("index.html")
	if err != nil {
		return []byte("<!doctype html><meta charset=utf-8>调试页面缺失: " + err.Error())
	}
	return data
}

// Reason 任务未播报的原因（与 dispatch 的丢弃/跳过语义对齐）。
type Reason string

const (
	ReasonSpoken    Reason = "spoken"     // 已发声
	ReasonSkipped   Reason = "skipped"    // 进入处理但未回复（概率/冷却/无回复必要）
	ReasonTTSError  Reason = "tts_error"  // 走到 TTS 但发声失败
	ReasonLLMError  Reason = "llm_error"  // LLM 分析失败
	ReasonTimeout   Reason = "timeout"    // 排队超时被丢弃
	ReasonQueueFull Reason = "queue_full" // 队列满被丢弃
	ReasonEvicted   Reason = "evicted"    // 队列满时被淘汰
	ReasonClosed    Reason = "closed"     // 调度器已关闭
	ReasonInterrupt Reason = "interrupted"
)

// Entry 一次事件处理的调试记录。
type Entry struct {
	Seq        int64     `json:"seq"`
	At         time.Time `json:"at"`
	Platform   string    `json:"platform"`
	User       string    `json:"user"`
	MessageTyp string    `json:"message_type"`
	Content    string    `json:"content"`
	Priority   int       `json:"priority"`
	PriorityZh string    `json:"priority_zh"`
	Queued     bool      `json:"queued"`               // false 表示提交阶段就被丢弃
	Reason     Reason    `json:"reason"`               // 最终结果
	Detail     string    `json:"detail,omitempty"`     // 补充说明（跳过原因等）
	Boost      float64   `json:"boost,omitempty"`      // 积压加速量
	WaitMS     int64     `json:"wait_ms"`              // 排队等待耗时
	CostMS     int64     `json:"cost_ms"`              // 处理耗时
	ReplyText  string    `json:"reply_text,omitempty"` // LLM/模板生成的回复
	SpokenText string    `json:"spoken_text,omitempty"`
	Emotion    string    `json:"emotion,omitempty"`
	IntentType string    `json:"intent_type,omitempty"`
	// Audio 是本次事件生成的音频文件名（可经 GET /debug/audio/{name} 试听）；
	// AudioURLs 为对应的完整 URL，便于页面直接播放。
	Audio     []string `json:"audio,omitempty"`
	AudioURLs []string `json:"audio_urls,omitempty"`
	TTSError  string   `json:"tts_error,omitempty"`
}

// SetAudio 记录本次事件生成的音频文件（保留基名，避免泄露进程路径）。
func (e *Entry) SetAudio(paths []string) {
	e.Audio = e.Audio[:0]
	e.AudioURLs = e.AudioURLs[:0]
	for _, p := range paths {
		if p == "" {
			continue
		}
		name := filepath.Base(p)
		e.Audio = append(e.Audio, name)
		e.AudioURLs = append(e.AudioURLs, "/debug/audio/"+url.PathEscape(name))
	}
}

// MimeForAudio 按扩展名推断音频 Content-Type（试听端点用）。
func MimeForAudio(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".m4a", ".aac":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	default:
		return "application/octet-stream"
	}
}

// Log 事件处理记录的内存环形缓冲，供调试页面读取。
type Log struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	size int
	seq  int64
}

// NewLog 创建容量为 size 的环形缓冲；size <= 0 时取默认值 200。
func NewLog(size int) *Log {
	if size <= 0 {
		size = 200
	}
	return &Log{buf: make([]Entry, 0, size), size: size}
}

// Add 记录一条流水，返回带序号的条目。
func (l *Log) Add(e Entry) Entry {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	e.Seq = l.seq
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if len(l.buf) < l.size {
		l.buf = append(l.buf, e)
	} else {
		l.buf[l.next] = e
		l.next = (l.next + 1) % l.size
	}
	return e
}

// Update 按序号覆盖已有条目（处理完成后回填结果）。
func (l *Log) Update(seq int64, fn func(*Entry)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.buf {
		if l.buf[i].Seq == seq {
			fn(&l.buf[i])
			return
		}
	}
}

// Recent 返回最近 n 条记录，按时间倒序（最新在前）。
func (l *Log) Recent(n int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]Entry, 0, len(l.buf))
	if l.next == 0 {
		out = append(out, l.buf...)
	} else {
		out = append(out, l.buf[l.next:]...)
		out = append(out, l.buf[:l.next]...)
	}
	// 倒序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Len 返回当前记录条数。
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buf)
}

// WriteJSON 以 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// WriteHTML 以 HTML 响应（禁用缓存，便于改页面后直接刷新）。
func WriteHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(IndexHTML())
}

// AudioHandler 返回试听端点处理器：从 dir 下按文件名提供音频文件。
// 只接受单层文件名（拒绝任何路径分隔符与 ".."，避免目录穿越），
// 并按扩展名设置 Content-Type；http.ServeContent 负责 Range 请求，便于拖动进度条。
func AudioHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dir == "" {
			http.Error(w, "音频缓存未启用（未配置 Mimo）", http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/debug/audio/")
		if name == "" || name == r.URL.Path {
			http.Error(w, "缺少文件名", http.StatusBadRequest)
			return
		}
		if unescaped, err := url.PathUnescape(name); err == nil {
			name = unescaped
		}
		if strings.ContainsAny(name, `/\`) || name == ".." || name == "." ||
			!strings.HasPrefix(filepath.Base(name), "mimo_") {
			http.Error(w, "非法音频文件名", http.StatusBadRequest)
			return
		}

		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			http.Error(w, "音频文件不存在或已被清理", http.StatusNotFound)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.Error(w, "音频文件不可读", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", MimeForAudio(name))
		http.ServeContent(w, r, name, info.ModTime(), f)
	}
}
