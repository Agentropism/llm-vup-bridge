package dispatch

import (
	"math/rand"
	"sync"
	"time"
)

// Priority 消息优先级
type Priority int

const (
	PriorityLow      Priority = 0 // 普通弹幕
	PriorityNormal   Priority = 1 // 闲聊/问候
	PriorityHigh     Priority = 2 // 提问/@
	PriorityGift     Priority = 3 // 礼物
	PrioritySC       Priority = 4 // SuperChat
	PriorityCaptain  Priority = 5 // 舰长
)

// MessageType → Priority 映射
var messagePriority = map[string]Priority{
	"text":       PriorityNormal,
	"gift":       PriorityGift,
	"super_chat": PrioritySC,
	"captain":    PriorityCaptain,
}

func PriorityFor(messageType string) Priority {
	if p, ok := messagePriority[messageType]; ok {
		return p
	}
	return PriorityLow
}

// SpeechWeight 情景发言权重配置
type SpeechWeight struct {
	// 各 intent_type 的回复概率 (0.0~1.0)
	ReplyChance map[string]float64

	// 全局冷却：两次回复之间的最小间隔
	Cooldown time.Duration

	// 礼物/SC 无视冷却
	GiftBypassCooldown bool
}

func DefaultSpeechWeight() SpeechWeight {
	return SpeechWeight{
		ReplyChance: map[string]float64{
			"chat":        0.6,  // 闲聊 60% 概率回复
			"greeting":    0.8,  // 问候 80%
			"question":    0.95, // 提问几乎必回
			"command":     0.9,  // 指令 90%
			"gift_thanks": 1.0,  // 礼物必回
		},
		Cooldown:           5 * time.Second,
		GiftBypassCooldown: true,
	}
}

// Dispatcher 发言调度器：决定是否回复、何时回复
type Dispatcher struct {
	mu     sync.Mutex
	weight SpeechWeight
	lastReply time.Time
	rng    *rand.Rand
}

func NewDispatcher(w SpeechWeight) *Dispatcher {
	return &Dispatcher{
		weight: w,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// ShouldRespond 判断是否应该回复
// 返回 true 表示本次应该发言
func (d *Dispatcher) ShouldRespond(intentType string, priority Priority) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// 高优先级（礼物/SC/舰长）：无视冷却，必回
	if priority >= PriorityGift {
		if d.weight.GiftBypassCooldown {
			d.lastReply = now
			return true
		}
	}

	// 冷却检查
	if now.Sub(d.lastReply) < d.weight.Cooldown {
		// 冷却中，只有高优先级能打断
		if priority < PriorityHigh {
			return false
		}
	}

	// 概率判定
	chance, ok := d.weight.ReplyChance[intentType]
	if !ok {
		chance = 0.5 // 未知类型默认 50%
	}

	if d.rng.Float64() < chance {
		d.lastReply = now
		return true
	}
	return false
}

// ForceRespond 强制标记为已回复（外部决定回复时调用）
func (d *Dispatcher) ForceRespond() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReply = time.Now()
}
