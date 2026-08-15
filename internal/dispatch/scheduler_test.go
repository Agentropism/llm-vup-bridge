package dispatch

import (
	"context"
	"testing"
	"time"
)

// 占住消费者的阻塞任务：直到 gate 关闭或 ctx 取消才结束。
func holderTask(gate <-chan struct{}, started chan<- struct{}) *SpeakTask {
	return &SpeakTask{
		Priority:      PrioritySC,
		Interruptible: false,
		Deadline:      time.Now().Add(30 * time.Second),
		Speak: func(ctx context.Context, _ float64) bool {
			if started != nil {
				close(started)
			}
			select {
			case <-ctx.Done():
				return false
			case <-gate:
				return true
			}
		},
	}
}

func newTask(p Priority, speak func(context.Context, float64) bool) *SpeakTask {
	return &SpeakTask{
		Priority:      p,
		Interruptible: CanInterrupt(p),
		Deadline:      time.Now().Add(10 * time.Second),
		Speak:         speak,
	}
}

// 高优先级先消费，低优先级后消费。
func TestSchedulerPriorityOrder(t *testing.T) {
	s := NewScheduler(SchedulerOptions{})
	s.Start()
	defer s.Close()

	gate := make(chan struct{})
	started := make(chan struct{})
	s.Submit(holderTask(gate, started))
	<-started // 消费者被占住，后续任务全部排队

	executed := make(chan Priority, 3)
	mk := func(p Priority) *SpeakTask {
		return newTask(p, func(context.Context, float64) bool {
			executed <- p
			return true
		})
	}
	s.Submit(mk(PriorityNormal))
	s.Submit(mk(PriorityHigh))
	s.Submit(mk(PriorityLow))
	close(gate)

	got := []Priority{<-executed, <-executed, <-executed}
	want := []Priority{PriorityHigh, PriorityNormal, PriorityLow}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("消费顺序错误: got=%v want=%v", got, want)
		}
	}
}

// 过期任务直接丢弃，不执行播报。
func TestSchedulerTimeoutDrop(t *testing.T) {
	s := NewScheduler(SchedulerOptions{})
	s.Start()
	defer s.Close()

	dropped := make(chan string, 1)
	called := make(chan struct{}, 1)
	s.Submit(&SpeakTask{
		Priority:      PriorityLow,
		Interruptible: true,
		Deadline:      time.Now().Add(-time.Second), // 已过期
		Speak: func(context.Context, float64) bool {
			called <- struct{}{}
			return true
		},
		OnDrop: func(reason string) { dropped <- reason },
	})

	select {
	case reason := <-dropped:
		if reason != "timeout" {
			t.Fatalf("丢弃原因错误: got=%q want=%q", reason, "timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("过期任务未被丢弃")
	}
	select {
	case <-called:
		t.Fatal("过期任务不应执行播报")
	case <-time.After(100 * time.Millisecond):
	}
}

// 高优先级任务打断当前可打断的播报：ctx 取消 + Stop 回调。
func TestSchedulerInterruptLower(t *testing.T) {
	s := NewScheduler(SchedulerOptions{})
	s.Start()
	defer s.Close()

	started := make(chan struct{})
	cancelled := make(chan struct{})
	stopped := make(chan struct{})

	low := newTask(PriorityNormal, func(ctx context.Context, _ float64) bool {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return false
	})
	low.Stop = func() { close(stopped) }
	s.Submit(low)
	<-started

	highRan := make(chan struct{})
	high := newTask(PrioritySC, func(context.Context, float64) bool {
		close(highRan)
		return true
	})
	s.Submit(high)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("未调用 Stop 打断当前播报")
	}
	select {
	case <-highRan:
	case <-time.After(2 * time.Second):
		t.Fatal("高优先级任务未执行")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("被打断任务的 ctx 未被取消")
	}
}

// 不可打断任务执行期间，后续任务等待其完成。
func TestSchedulerNoInterruptUninterruptible(t *testing.T) {
	s := NewScheduler(SchedulerOptions{})
	s.Start()
	defer s.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})

	first := newTask(PrioritySC, func(ctx context.Context, _ float64) bool {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			t.Error("不可打断任务不应被取消")
			return false
		}
		close(firstDone)
		return true
	})
	s.Submit(first)
	<-started

	secondRan := make(chan struct{})
	s.Submit(newTask(PriorityCaptain, func(context.Context, float64) bool {
		close(secondRan)
		return true
	}))

	// 第一个任务执行期间，第二个任务不应运行
	select {
	case <-secondRan:
		t.Fatal("不可打断任务执行期间，后续任务不应运行")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	<-firstDone
	select {
	case <-secondRan:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个任务结束后，第二个任务未执行")
	}
}

// 队列满：淘汰优先级最低的排队任务；新任务优先级更低时直接丢弃新任务。
func TestSchedulerQueueFullEvict(t *testing.T) {
	s := NewScheduler(SchedulerOptions{QueueCap: 2})
	s.Start()
	defer s.Close()

	gate := make(chan struct{})
	started := make(chan struct{})
	s.Submit(holderTask(gate, started))
	<-started // 消费者被占住

	ran := make(chan Priority, 4)
	mk := func(p Priority) *SpeakTask {
		return newTask(p, func(context.Context, float64) bool {
			ran <- p
			return true
		})
	}

	evicted := make(chan string, 1)
	droppedNew := make(chan string, 1)

	low := mk(PriorityLow)
	low.OnDrop = func(reason string) { evicted <- reason }
	s.Submit(low) // 队列 [low]
	s.Submit(mk(PriorityNormal))

	// 队列满：[low, normal]，提交 high 应淘汰 low
	s.Submit(mk(PriorityHigh))

	// 队列仍满：[normal, high]，提交 low2（优先级不高于 normal）应直接丢弃新任务
	low2 := mk(PriorityLow)
	low2.OnDrop = func(reason string) { droppedNew <- reason }
	s.Submit(low2)

	close(gate)

	select {
	case reason := <-evicted:
		if reason != "queue full evicted" {
			t.Fatalf("淘汰原因错误: got=%q", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("队列满时未淘汰低优先级任务")
	}
	select {
	case reason := <-droppedNew:
		if reason != "queue full" {
			t.Fatalf("丢弃原因错误: got=%q", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("新任务优先级过低时未直接丢弃")
	}

	// 剩下的任务按优先级消费：high → normal
	got := []Priority{<-ran, <-ran}
	want := []Priority{PriorityHigh, PriorityNormal}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("消费顺序错误: got=%v want=%v", got, want)
		}
	}
}

// 积压超过阈值时给可打断任务注入加速量，系统事件不加速。
func TestSchedulerBacklogBoost(t *testing.T) {
	s := NewScheduler(SchedulerOptions{
		BacklogThreshold:  1,
		BacklogSpeedBoost: 0.15,
	})
	s.Start()
	defer s.Close()

	gate := make(chan struct{})
	started := make(chan struct{})
	s.Submit(holderTask(gate, started))
	<-started

	boostCh := make(chan float64, 3)
	text := func() *SpeakTask {
		return newTask(PriorityNormal, func(_ context.Context, boost float64) bool {
			boostCh <- boost
			return true
		})
	}
	sc := newTask(PrioritySC, func(_ context.Context, boost float64) bool {
		boostCh <- boost
		return true
	})

	// 执行顺序：sc → text1 → text2
	// sc 执行时队列 [text1, text2] 已积压，但系统事件不加速 → 0
	// text1 执行时队列 [text2] 积压 → 0.15
	// text2 执行时队列已空 → 0
	s.Submit(text())
	s.Submit(text())
	s.Submit(sc)
	close(gate)

	got := []float64{<-boostCh, <-boostCh, <-boostCh}
	want := []float64{0, 0.15, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("积压加速错误: got=%v want=%v", got, want)
		}
	}
}

func TestTTLForAndCanInterrupt(t *testing.T) {
	text, gift := 10*time.Second, 60*time.Second
	if got := TTLFor(PriorityNormal, text, gift); got != text {
		t.Fatalf("普通消息 TTL 错误: got=%v want=%v", got, text)
	}
	if got := TTLFor(PrioritySC, text, gift); got != gift {
		t.Fatalf("礼物/SC TTL 错误: got=%v want=%v", got, gift)
	}
	if !CanInterrupt(PriorityNormal) {
		t.Fatal("普通消息应可被打断")
	}
	if CanInterrupt(PriorityGift) {
		t.Fatal("礼物致谢不应可被打断")
	}
}
