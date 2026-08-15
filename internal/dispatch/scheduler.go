// Package dispatch 发言调度：优先级队列、单消费者串行播报、打断与超时丢弃。
package dispatch

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// SpeakTask 一次发言任务。调度器按优先级消费：高优先级插队，串行播报不重叠，超时直接丢弃。
type SpeakTask struct {
	// Priority 越高越先消费。
	Priority Priority
	// Interruptible 是否可被更高优先级任务打断。
	// SC 感谢/礼物/舰长等系统事件不可打断（见 CanInterrupt）。
	Interruptible bool
	// Deadline 消费截止时间；轮到消费时已过期则直接丢弃。
	Deadline time.Time

	// Speak 串行执行播报，返回是否成功出声。
	// 任务被打断时 ctx 会被取消，Speak 应尽快返回；boost 为积压加速量（0=不加速）。
	Speak func(ctx context.Context, boost float64) bool
	// Stop 打断当前播报的回调（后端相关，可空）；打断发生时由调度器调用。
	Stop func()
	// OnDrop 任务被丢弃时的回调（超时 / 队列满淘汰 / 调度器关闭）。
	OnDrop func(reason string)
}

// SchedulerOptions 调度器配置。
type SchedulerOptions struct {
	QueueCap          int     // 队列容量上限，0=不限；队列满时淘汰优先级最低的排队任务
	BacklogThreshold  int     // 积压阈值：排队任务数达到该值时给可打断任务加速，0=禁用
	BacklogSpeedBoost float64 // 积压加速量（Mimo 为 speed 增量，synapse 为 rate 百分比增量）
}

// Scheduler 单消费者优先级调度器。
// 所有播报串行执行，保证 TTS 输出按序播放、互不重叠；
// 新提交的更高优先级任务可打断当前可打断的播报。
type Scheduler struct {
	mu  sync.Mutex
	h   taskHeap
	seq int64 // 提交序号：同优先级先到先得

	queueCap          int
	backlogThreshold  int
	backlogSpeedBoost float64

	current *SpeakTask
	cancel  context.CancelFunc // 当前任务的取消函数

	wakeCh chan struct{}
	stopCh chan struct{}
	wg     sync.WaitGroup
	closed bool
}

// NewScheduler 创建调度器；需调用 Start 启动消费协程。
func NewScheduler(opts SchedulerOptions) *Scheduler {
	return &Scheduler{
		queueCap:          opts.QueueCap,
		backlogThreshold:  opts.BacklogThreshold,
		backlogSpeedBoost: opts.BacklogSpeedBoost,
		wakeCh:            make(chan struct{}, 1),
		stopCh:            make(chan struct{}),
	}
}

// Start 启动消费协程。
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.run()
}

// Close 停止消费：打断当前任务并等待消费协程退出。可安全重复调用。
func (s *Scheduler) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	close(s.stopCh)
	s.wg.Wait()
}

// Submit 提交发言任务，可在任意协程调用。
// 队列已满时淘汰优先级最低的排队任务；新任务优先级不高于被淘汰者时直接丢弃新任务。
// 若当前播报可被打断且新任务优先级更高，则打断当前播报。
func (s *Scheduler) Submit(task *SpeakTask) {
	if task == nil {
		return
	}

	var dropNew    func() // 在锁外执行的「丢弃新任务」回调
	var dropOld    func() // 在锁外执行的「被淘汰任务」回调
	var stopFn     func() // 在锁外执行的后端停止回调
	interrupt := false

	s.mu.Lock()
	if s.closed {
		dropNew = func() { task.OnDrop("scheduler closed") }
	} else {
		// 队列满：淘汰优先级最低的排队任务；新任务优先级不高于被淘汰者时丢弃新任务
		if s.queueCap > 0 && s.h.Len() >= s.queueCap {
			if lowest := s.h.lowest(); lowest == nil || task.Priority <= lowest.task.Priority {
				dropNew = func() { task.OnDrop("queue full") }
			} else {
				evicted := lowest.task
				heap.Remove(&s.h, lowest.index)
				dropOld = func() { evicted.OnDrop("queue full evicted") }
			}
		}

		if dropNew == nil {
			// 打断当前播报：仅当当前任务可被打断且新任务优先级严格更高
			interrupt = s.current != nil &&
				s.current.Interruptible &&
				task.Priority > s.current.Priority
			if interrupt {
				if s.cancel != nil {
					s.cancel()
				}
				stopFn = s.current.Stop
			}

			s.seq++
			heap.Push(&s.h, &taskNode{task: task, seq: s.seq})
			select {
			case s.wakeCh <- struct{}{}:
			default:
			}
		}
	}
	s.mu.Unlock()

	if dropNew != nil {
		dropNew()
		return
	}
	if dropOld != nil {
		dropOld()
	}
	if interrupt && stopFn != nil {
		stopFn()
	}
}

// run 消费循环：取最高优先级任务串行执行。
func (s *Scheduler) run() {
	defer s.wg.Done()
	for {
		task := s.waitTask()
		if task == nil {
			return
		}

		// 积压加速：仅对可打断的普通消息加速，系统事件保持原速
		boost := float64(0)
		s.mu.Lock()
		if s.backlogThreshold > 0 && s.h.Len() >= s.backlogThreshold && task.Interruptible {
			boost = s.backlogSpeedBoost
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.current = task
		s.cancel = cancel
		s.mu.Unlock()

		task.Speak(ctx, boost)

		s.mu.Lock()
		s.current = nil
		s.cancel = nil
		cancel()
		s.mu.Unlock()
	}
}

// waitTask 阻塞等待下一个可消费任务；等待期间过期任务直接丢弃。
func (s *Scheduler) waitTask() *SpeakTask {
	for {
		s.mu.Lock()
		if s.h.Len() > 0 {
			node := heap.Pop(&s.h).(*taskNode)
			s.mu.Unlock()
			if time.Now().After(node.task.Deadline) {
				node.task.OnDrop("timeout")
				continue
			}
			return node.task
		}
		s.mu.Unlock()

		select {
		case <-s.stopCh:
			return nil
		case <-s.wakeCh:
		}
	}
}

// TTLFor 返回消息类型的消费时限：礼物/SC/舰长用 ttlGift，普通消息用 ttlText。
func TTLFor(p Priority, ttlText, ttlGift time.Duration) time.Duration {
	if p >= PriorityGift {
		return ttlGift
	}
	return ttlText
}

// CanInterrupt 返回该优先级对应的播报是否可被更高优先级打断。
// 礼物/SC/舰长致谢等系统事件不可打断，普通 LLM 互动可被打断。
func CanInterrupt(p Priority) bool {
	return p < PriorityGift
}

// ---- 优先级堆 ----

type taskNode struct {
	task  *SpeakTask
	seq   int64
	index int
}

type taskHeap []*taskNode

func (h taskHeap) Len() int { return len(h) }

// Less：优先级高在前；同优先级先提交者在前。
func (h taskHeap) Less(i, j int) bool {
	if h[i].task.Priority != h[j].task.Priority {
		return h[i].task.Priority > h[j].task.Priority
	}
	return h[i].seq < h[j].seq
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x any) {
	node := x.(*taskNode)
	node.index = len(*h)
	*h = append(*h, node)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	node := old[n-1]
	old[n-1] = nil
	node.index = -1
	*h = old[:n-1]
	return node
}

// lowest 返回优先级最低的排队节点（队列满淘汰用），队列为空返回 nil。
func (h taskHeap) lowest() *taskNode {
	var lowest *taskNode
	for _, n := range h {
		if lowest == nil || h.Less(lowest.index, n.index) {
			lowest = n
		}
	}
	return lowest
}
