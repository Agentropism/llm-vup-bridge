package llm

import (
	"errors"
	"sync"
	"time"
)

// Conversations keeps bounded, in-memory room context. A room is processed one
// turn at a time; independent rooms don't block one another during model calls.
type Conversations struct {
	mu    sync.Mutex
	rooms map[string]*room
}
type room struct {
	busy    bool
	last    time.Time
	history []map[string]string
}

func NewConversations() *Conversations { return &Conversations{rooms: make(map[string]*room)} }

// Begin returns a history snapshot and a completion callback. Commit only
// accepted replies; skipped turns and errors must not invent conversation history.
func (c *Conversations) Begin(key string, turns int) ([]map[string]string, func(string, string), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, r := range c.rooms {
		if !r.busy && now.Sub(r.last) > 30*time.Minute {
			delete(c.rooms, k)
		}
	}
	r := c.rooms[key]
	if r == nil {
		if len(c.rooms) >= 128 {
			oldest := ""
			var at time.Time
			for k, candidate := range c.rooms {
				if !candidate.busy && (oldest == "" || candidate.last.Before(at)) {
					oldest, at = k, candidate.last
				}
			}
			if oldest == "" {
				return nil, nil, errors.New("会话繁忙，请稍后重试")
			}
			delete(c.rooms, oldest)
		}
		r = &room{}
		c.rooms[key] = r
	}
	if r.busy {
		return nil, nil, errors.New("上一轮仍在处理中，请稍后重试")
	}
	r.busy = true
	if turns < 1 {
		turns = 1
	}
	if turns > 20 {
		turns = 20
	}
	limit := turns * 2
	if len(r.history) > limit {
		r.history = r.history[len(r.history)-limit:]
	}
	history := append([]map[string]string(nil), r.history...)
	var once sync.Once
	finish := func(user, reply string) {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if reply != "" {
				r.history = append(r.history, map[string]string{"role": "user", "content": clipRunes(user, 2000)}, map[string]string{"role": "assistant", "content": clipRunes(reply, 2000)})
			}
			if len(r.history) > limit {
				r.history = r.history[len(r.history)-limit:]
			}
			r.busy = false
			r.last = time.Now()
		})
	}
	return history, finish, nil
}

func (c *Conversations) Clear(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.rooms[key]; r != nil && r.busy {
		return errors.New("请等待当前回复结束后再清空")
	}
	delete(c.rooms, key)
	return nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
