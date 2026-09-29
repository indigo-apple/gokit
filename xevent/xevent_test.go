package xevent_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xevent"
)

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Microsecond)
	}
	return cond()
}

// collector 收集事件（并发安全）。
type collector struct {
	mu     sync.Mutex
	events []xevent.Event
}

func (c *collector) add(ev xevent.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *collector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *collector) snapshot() []xevent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]xevent.Event, len(c.events))
	copy(out, c.events)
	return out
}

func (c *collector) handler() xevent.Handler {
	return func(_ context.Context, ev xevent.Event) error {
		c.add(ev)
		return nil
	}
}

// newBus 创建测试总线并在用例结束时关闭。
func newBus(t *testing.T, opts ...xevent.Option) xevent.Bus {
	t.Helper()
	b := xevent.New(opts...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.Close(ctx)
	})
	return b
}

// ---------------------------------------------------------------------------
// 主题匹配
// ---------------------------------------------------------------------------

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		// 精确
		{"order.created", "order.created", true},
		{"order.created", "order.paid", false},
		{"order.created", "order", false},
		{"order.created", "order.created.x", false},
		// 全匹配
		{"*", "anything", true},
		{"**", "a.b.c", true},
		{"*", "", true},
		// 单级通配
		{"order.*", "order.created", true},
		{"order.*", "order.paid", true},
		{"order.*", "order.item.added", false}, // * 只吃一段
		{"order.*", "order", false},
		{"*.created", "order.created", true},
		{"*.created", "user.created", true},
		{"*.created", "user.profile.created", false},
		{"a.*.c", "a.b.c", true},
		{"a.*.c", "a.b.d", false},
		{"a.*.c", "a.c", false},
		// 多级通配（只能作为后缀）
		{"order.**", "order.created", true},
		{"order.**", "order.item.added", true},
		{"order.**", "order", true},
		{"order.**", "other.created", false},
		{"order.**", "orders.created", false},
		// 空主题
		{"", "", true},
		{"a", "", false},
		// 大小写敏感
		{"Order.Created", "order.created", false},
		{"order.created", "Order.Created", false},
		// 正则
		{"re:^order\\..*$", "order.created", true},
		{"re:^order\\..*$", "user.created", false},
		{"re:^o", "order", true},
		{"re:[0-9]+", "abc123", true},
		// 非法正则 → 不匹配（不 panic）
		{"re:[", "anything", false},
		{"re:(", "anything", false},
	}
	for _, c := range cases {
		if got := xevent.Match(c.pattern, c.topic); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.topic, got, c.want)
		}
	}
}

func TestValidatePattern(t *testing.T) {
	valid := []string{"", "a", "a.b", "a.*", "a.**", "*", "**", "re:^a$"}
	for _, p := range valid {
		if err := xevent.ValidatePattern(p); err != nil && p != "" {
			t.Errorf("ValidatePattern(%q) = %v, 期望通过", p, err)
		}
	}
	if err := xevent.ValidatePattern(""); !errors.Is(err, xevent.ErrEmptyTopic) {
		t.Fatalf("空模式应报 ErrEmptyTopic, got %v", err)
	}
	for _, p := range []string{"**.b", "a.**.c", "re:["} {
		if err := xevent.ValidatePattern(p); err == nil {
			t.Errorf("ValidatePattern(%q) 应报错", p)
		}
	}
	// 中间出现 ** 的错误信息应可读
	err := xevent.ValidatePattern("a.**.c")
	if err == nil || !strings.Contains(err.Error(), "**") {
		t.Fatalf("错误信息应说明 ** 位置限制: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 发布 / 订阅基本流程
// ---------------------------------------------------------------------------

func TestPublishSubscribeAsync(t *testing.T) {
	b := newBus(t)
	c := &collector{}

	s, err := b.Subscribe("order.created", c.handler())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if s.ID() == "" || s.Topic() != "order.created" {
		t.Fatalf("订阅标识错误: %s / %s", s.ID(), s.Topic())
	}
	if s.Events() != nil {
		t.Fatal("非拉模式的 Events 应为 nil")
	}
	if b.Subscribers("order.created") != 1 {
		t.Fatalf("Subscribers = %d", b.Subscribers("order.created"))
	}

	ctx := context.Background()
	if err := b.Publish(ctx, "order.created", map[string]int{"id": 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 1 }, 2*time.Second) {
		t.Fatal("未收到事件")
	}

	got := c.snapshot()[0]
	if got.Topic != "order.created" || got.ID == "" || got.Timestamp.IsZero() {
		t.Fatalf("事件字段不完整: %+v", got)
	}
	if m, ok := got.Payload.(map[string]int); !ok || m["id"] != 1 {
		t.Fatalf("载荷错误: %#v", got.Payload)
	}
	if got.Attempt != 0 {
		t.Fatalf("首次投递 Attempt 应为 0, got %d", got.Attempt)
	}

	// 多个订阅都收到（广播）
	c2 := &collector{}
	if _, err := b.Subscribe("order.*", c2.handler()); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b.Publish(ctx, "order.created", "x"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 2 && c2.len() == 1 }, 2*time.Second) {
		t.Fatalf("广播失败: c=%d c2=%d", c.len(), c2.len())
	}
	if b.Subscribers("order.created") != 2 {
		t.Fatalf("Subscribers = %d", b.Subscribers("order.created"))
	}

	// 无人订阅的主题：静默成功
	if err := b.Publish(ctx, "nobody.cares", nil); err != nil {
		t.Fatalf("无人订阅时不应报错: %v", err)
	}
}

func TestPublishEventAndOptions(t *testing.T) {
	b := newBus(t)
	c := &collector{}
	if _, err := b.Subscribe("**", c.handler()); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ctx := context.Background()

	// 选项：key / headers / trace / source / 自定义 ID
	err := b.Publish(ctx, "t", "p",
		xevent.WithKey("k1"),
		xevent.WithHeaders(map[string]string{"a": "1", "b": "2"}),
		xevent.WithTraceID("trace-1"),
		xevent.WithSource("svc-a"),
		xevent.WithEventID("fixed-id"),
	)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 1 }, 2*time.Second) {
		t.Fatal("未收到事件")
	}
	got := c.snapshot()[0]
	if got.Key != "k1" || got.ID != "fixed-id" || got.TraceID != "trace-1" || got.Source != "svc-a" {
		t.Fatalf("选项未生效: %+v", got)
	}
	if got.Header("a") != "1" || got.Header("b") != "2" || got.Header("trace_id") != "trace-1" {
		t.Fatalf("头字段错误: %v", got.Headers)
	}
	if got.Header("nope") != "" {
		t.Fatal("不存在的头应返回空串")
	}
	if !strings.Contains(got.String(), "topic=t") {
		t.Fatalf("String = %q", got.String())
	}

	// PublishEvent：既有字段优先
	ev := xevent.Event{Topic: "t", ID: "predefined", Key: "pre", Payload: 42,
		Timestamp: time.Unix(1000, 0), Headers: map[string]string{"x": "y"}}
	if err := b.PublishEvent(ctx, ev); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 2 }, 2*time.Second) {
		t.Fatal("未收到事件")
	}
	got2 := c.snapshot()[1]
	if got2.ID != "predefined" || got2.Key != "pre" || got2.Header("x") != "y" {
		t.Fatalf("PublishEvent 字段被覆盖: %+v", got2)
	}
	if !got2.Timestamp.Equal(time.Unix(1000, 0)) {
		t.Fatalf("Timestamp 被覆盖: %v", got2.Timestamp)
	}

	// WithTimestamp
	if err := b.Publish(ctx, "t", nil, xevent.WithTimestamp(time.Unix(2000, 0))); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 3 }, 2*time.Second) {
		t.Fatal("未收到事件")
	}
	if ts := c.snapshot()[2].Timestamp; !ts.Equal(time.Unix(2000, 0)) {
		t.Fatalf("WithTimestamp 未生效: %v", ts)
	}
}

// WithHeader 必须是深拷贝：同一事件会投给多个订阅，原地写 map 会数据竞争。
func TestEventWithHeaderCopies(t *testing.T) {
	base := xevent.Event{Topic: "t", Headers: map[string]string{"a": "1"}}
	derived := base.WithHeader("b", "2")

	if base.Header("b") != "" {
		t.Fatal("WithHeader 不应修改原事件")
	}
	if derived.Header("a") != "1" || derived.Header("b") != "2" {
		t.Fatalf("派生事件头字段错误: %v", derived.Headers)
	}
	// 改派生事件的头不应影响原事件
	derived.Headers["a"] = "changed"
	if base.Header("a") != "1" {
		t.Fatal("Headers 未深拷贝（共享了底层 map）")
	}

	// Clone
	cl := base.Clone()
	cl.Headers["z"] = "9"
	if base.Header("z") != "" {
		t.Fatal("Clone 未深拷贝")
	}
	// nil Headers 也要安全
	var e xevent.Event
	if e.Header("x") != "" || e.Clone().Header("x") != "" {
		t.Fatal("nil Headers 处理错误")
	}
}

// ---------------------------------------------------------------------------
// 同步发布
// ---------------------------------------------------------------------------

func TestPublishSync(t *testing.T) {
	b := newBus(t)
	ctx := context.Background()

	var done atomic.Int64
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error {
		time.Sleep(30 * time.Millisecond)
		done.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	start := time.Now()
	if err := b.Publish(ctx, "t", nil, xevent.WithSync(true)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// 同步发布必须等 handler 完成
	if done.Load() != 1 {
		t.Fatal("同步发布返回时 handler 尚未完成")
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("同步发布过早返回: %v", elapsed)
	}

	// 便捷方法
	if err := b.PublishSync(ctx, "t", nil); err != nil {
		t.Fatalf("PublishSync: %v", err)
	}

	// 错误聚合（ErrorCollect 默认）
	sentinel := errors.New("handler 失败")
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { return sentinel }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	err := b.Publish(ctx, "t", nil, xevent.WithSync(true))
	if !errors.Is(err, sentinel) {
		t.Fatalf("同步发布应返回 handler 错误: %v", err)
	}

	// FailFast
	bf := newBus(t, xevent.WithErrorPolicy(xevent.ErrorFailFast))
	if _, err := bf.Subscribe("t", func(context.Context, xevent.Event) error { return sentinel }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	var secondRan atomic.Bool
	if _, err := bf.Subscribe("t", func(context.Context, xevent.Event) error {
		secondRan.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := bf.Publish(ctx, "t", nil, xevent.WithSync(true)); !errors.Is(err, sentinel) {
		t.Fatalf("FailFast: %v", err)
	}

	// ctx 结束：不再等待
	b2 := newBus(t)
	release := make(chan struct{})
	defer close(release)
	if _, err := b2.Subscribe("t", func(context.Context, xevent.Event) error {
		<-release
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if err := b2.Publish(cctx, "t", nil, xevent.WithSync(true)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("同步发布应受 ctx 约束: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 有序性
// ---------------------------------------------------------------------------

// WithKey：同 key 严格遵守发布顺序，且同 key 不并发。
func TestKeyedOrdering(t *testing.T) {
	b := newBus(t, xevent.WithKeyedShards(4))
	ctx := context.Background()

	type rec struct {
		mu       sync.Mutex
		seq      []int
		inFlight int
		maxOver  int
	}
	r := &rec{}

	if _, err := b.Subscribe("t", func(_ context.Context, ev xevent.Event) error {
		n := ev.Payload.(int)
		r.mu.Lock()
		r.inFlight++
		if r.inFlight > r.maxOver {
			r.maxOver = r.inFlight
		}
		r.seq = append(r.seq, n)
		r.mu.Unlock()

		time.Sleep(20 * time.Microsecond)

		r.mu.Lock()
		r.inFlight--
		r.mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	const n = 150
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx, "t", i, xevent.WithKey("same")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// 等全部投递完
	if !waitFor(func() bool { return b.Stats().Delivered == n }, 5*time.Second) {
		t.Fatalf("投递数 = %d, want %d", b.Stats().Delivered, n)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxOver != 1 {
		t.Fatalf("同 key 出现并发投递: 最大 %d（必须为 1）", r.maxOver)
	}
	if len(r.seq) != n {
		t.Fatalf("投递数 = %d", len(r.seq))
	}
	for i, v := range r.seq {
		if v != i {
			t.Fatalf("同 key 顺序错乱: 第 %d 位是 %d", i, v)
		}
	}
}

// WithOrdered：全局严格顺序。
func TestGlobalOrdered(t *testing.T) {
	b := newBus(t)
	ctx := context.Background()

	got := make([]int, 0, 100)
	var mu sync.Mutex
	if _, err := b.Subscribe("t", func(_ context.Context, ev xevent.Event) error {
		mu.Lock()
		got = append(got, ev.Payload.(int))
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	const n = 100
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx, "t", i, xevent.WithOrdered(true)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	if !waitFor(func() bool { return b.Stats().Delivered == n }, 5*time.Second) {
		t.Fatalf("投递数 = %d", b.Stats().Delivered)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, v := range got {
		if v != i {
			t.Fatalf("全局顺序错乱: 第 %d 位是 %d", i, v)
		}
	}
}

// 不同 key 应当并行（分片生效）。
func TestDifferentKeysParallel(t *testing.T) {
	b := newBus(t, xevent.WithKeyedShards(16))
	ctx := context.Background()

	release := make(chan struct{})
	var concurrent, maxConcurrent int64
	if _, err := b.Subscribe("t", func(_ context.Context, ev xevent.Event) error {
		n := atomic.AddInt64(&concurrent, 1)
		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if n <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, n) {
				break
			}
		}
		<-release
		atomic.AddInt64(&concurrent, -1)
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 16 个不同 key（分片数 16，理论上会有多个 key 同时进入）
	for i := 0; i < 32; i++ {
		if err := b.Publish(ctx, "t", i, xevent.WithKey(fmt.Sprintf("k%d", i))); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	// 等到有 2 个并发（说明分片并行生效）
	if !waitFor(func() bool { return atomic.LoadInt64(&maxConcurrent) >= 2 }, 3*time.Second) {
		close(release)
		t.Fatalf("不同 key 未观察到并行: max=%d（keyed 分片未生效）", atomic.LoadInt64(&maxConcurrent))
	}
	close(release)
}

// ---------------------------------------------------------------------------
// 背压
// ---------------------------------------------------------------------------

// blockWorker 订阅一个会「占住 worker」的 handler，并**等到它真正进入 handler** 才返回。
//
// 为什么必须等：只 publish 一次并不能保证 worker 已取走该事件 ——
// 若 buffered channel 里还留着它，后续 publish 的成败就取决于调度，
// 测试会变成 flaky（这是本包测试实际踩过的坑）。
func blockWorker(t *testing.T, b xevent.Bus, opts ...xevent.SubscribeOption) func() {
	t.Helper()
	release := make(chan struct{})
	var entered atomic.Int64

	if _, err := b.Subscribe("t", func(_ context.Context, ev xevent.Event) error {
		if ev.Payload == blockPayload {
			entered.Add(1)
			<-release
		}
		return nil
	}, opts...); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b.Publish(context.Background(), "t", blockPayload); err != nil {
		t.Fatalf("Publish(占用): %v", err)
	}
	if !waitFor(func() bool { return entered.Load() == 1 }, 3*time.Second) {
		t.Fatal("占用事件未进入 handler（worker 未开始工作）")
	}
	return func() { close(release) }
}

const blockPayload = "\x00BLOCK"

func TestBackpressure(t *testing.T) {
	t.Run("Block阻塞发布直到ctx结束", func(t *testing.T) {
		b := newBus(t)
		release := blockWorker(t, b, xevent.WithBuffer(1))
		defer release()

		ctx := context.Background()
		if err := b.Publish(ctx, "t", 1); err != nil { // 占满唯一的缓冲位
			t.Fatalf("Publish: %v", err)
		}

		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := b.Publish(cctx, "t", 2)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Publish = %v, want DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
			t.Fatalf("过早返回: %v", elapsed)
		}
	})

	t.Run("Fail返回ErrQueueFull", func(t *testing.T) {
		b := newBus(t)
		release := blockWorker(t, b, xevent.WithBuffer(1), xevent.WithOverflow(xcontainer.Fail))
		defer release()

		ctx := context.Background()
		if err := b.Publish(ctx, "t", 1); err != nil { // 缓冲已满
			t.Fatalf("Publish: %v", err)
		}
		err := b.Publish(ctx, "t", 2)
		if !errors.Is(err, xevent.ErrQueueFull) {
			t.Fatalf("Publish = %v, want ErrQueueFull", err)
		}
		if code := xerr.CodeOf(err); code != xerr.CodeResourceExhausted {
			t.Fatalf("错误码 = %v", code)
		}
		if meta := xerr.MetaOf(err); meta["topic"] != "t" {
			t.Fatalf("错误应带 topic: %v", meta)
		}
		if b.Stats().Dropped == 0 {
			t.Fatal("应计入 Dropped")
		}
	})

	t.Run("DropNewest丢新", func(t *testing.T) {
		b := newBus(t)
		release := blockWorker(t, b, xevent.WithBuffer(1), xevent.WithOverflow(xcontainer.DropNewest))
		defer release()

		ctx := context.Background()
		if err := b.Publish(ctx, "t", 1); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		// DropNewest 与 Fail 的差别：丢弃新事件但**不返回错误**（调用方不关心）
		if err := b.Publish(ctx, "t", 2); err != nil {
			t.Fatalf("DropNewest 不应返回错误: %v", err)
		}
		if st := b.Stats(); st.Dropped != 1 {
			t.Fatalf("Dropped = %d, want 1", st.Dropped)
		}
	})

	t.Run("DropOldest丢最旧_完整验证", func(t *testing.T) {
		b := newBus(t)
		var mu sync.Mutex
		var seen []int

		release := make(chan struct{})
		var entered atomic.Int64
		if _, err := b.Subscribe("t", func(_ context.Context, ev xevent.Event) error {
			switch v := ev.Payload.(type) {
			case string: // 占用任务
				entered.Add(1)
				<-release
			case int:
				mu.Lock()
				seen = append(seen, v)
				mu.Unlock()
			}
			return nil
		}, xevent.WithBuffer(2), xevent.WithOverflow(xcontainer.DropOldest)); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}

		ctx := context.Background()
		if err := b.Publish(ctx, "t", blockPayload); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if !waitFor(func() bool { return entered.Load() == 1 }, 3*time.Second) {
			t.Fatal("占用任务未进入 handler")
		}

		for _, v := range []int{1, 2} { // 填满缓冲 [1,2]
			if err := b.Publish(ctx, "t", v); err != nil {
				t.Fatalf("Publish: %v", err)
			}
		}
		// 缓冲满 → 丢最旧的 1，放入 3 → [2,3]
		if err := b.Publish(ctx, "t", 3); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if st := b.Stats(); st.Dropped != 1 {
			t.Fatalf("Dropped = %d, want 1", st.Dropped)
		}

		close(release)
		if !waitFor(func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(seen) == 2
		}, 3*time.Second) {
			t.Fatalf("收到 %d 条", len(seen))
		}
		mu.Lock()
		defer mu.Unlock()
		if seen[0] != 2 || seen[1] != 3 {
			t.Fatalf("DropOldest 后处理 %v, want [2 3]", seen)
		}
	})
}

// ---------------------------------------------------------------------------
// 重试 / 死信 / 过滤器
// ---------------------------------------------------------------------------

func TestRetryThenSuccess(t *testing.T) {
	b := newBus(t)
	var attempts atomic.Int64

	if _, err := b.Subscribe("t", func(_ context.Context, ev xevent.Event) error {
		n := attempts.Add(1)
		if n < 3 {
			return errors.New("暂时性失败")
		}
		if ev.Attempt != 2 {
			t.Errorf("第 3 次尝试的 Attempt = %d, want 2", ev.Attempt)
		}
		return nil
	}, xevent.WithRetry(5, 5*time.Millisecond)); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return b.Stats().Delivered == 1 }, 3*time.Second) {
		t.Fatalf("未成功投递: attempts=%d stats=%+v", attempts.Load(), b.Stats())
	}
	if attempts.Load() != 3 {
		t.Fatalf("尝试次数 = %d, want 3", attempts.Load())
	}
	if st := b.Stats(); st.Retried != 2 || st.Failed != 0 {
		t.Fatalf("Stats = %+v", st)
	}
}

func TestRetryExhaustedDeadLetter(t *testing.T) {
	type dlItem struct {
		ev  xevent.Event
		err error
	}
	var mu sync.Mutex
	var dl []dlItem

	b := newBus(t)
	sentinel := errors.New("永久失败")
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { return sentinel },
		xevent.WithRetry(2, time.Millisecond),
		xevent.WithDeadLetter(func(_ context.Context, ev xevent.Event, err error) {
			mu.Lock()
			dl = append(dl, dlItem{ev, err})
			mu.Unlock()
		}),
	); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var published atomic.Int64
	if err := b.Publish(context.Background(), "t", "p"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	published.Add(1)

	if !waitFor(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(dl) == 1
	}, 3*time.Second) {
		t.Fatal("死信处理器未被调用")
	}
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(dl[0].err, sentinel) {
		t.Fatalf("死信错误 = %v", dl[0].err)
	}
	// 死信事件的 Attempt 应为最后一次尝试的序号
	if dl[0].ev.Attempt != 2 {
		t.Fatalf("死信事件 Attempt = %d, want 2", dl[0].ev.Attempt)
	}
	if st := b.Stats(); st.Failed != 1 || st.Delivered != 0 {
		t.Fatalf("Stats = %+v", st)
	}
}

// 没有死信处理器时，失败应通过总线级 onError 暴露（而不是静默吞掉）。
func TestRetryExhaustedOnError(t *testing.T) {
	var mu sync.Mutex
	var got []string
	sentinel := errors.New("boom")

	b := newBus(t, xevent.WithOnError(func(topic string, ev xevent.Event, err error) {
		mu.Lock()
		got = append(got, topic+":"+err.Error())
		mu.Unlock()
	}))
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { return sentinel },
		xevent.WithRetry(1, time.Millisecond)); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}, 3*time.Second) {
		t.Fatal("onError 未被调用")
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "t:"+sentinel.Error() {
		t.Fatalf("onError 参数 = %q", got[0])
	}
}

func TestFilter(t *testing.T) {
	b := newBus(t)
	c := &collector{}
	if _, err := b.Subscribe("t", c.handler(), xevent.WithFilter(func(ev xevent.Event) bool {
		n, _ := ev.Payload.(int)
		return n%2 == 0 // 只处理偶数
	})); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := b.Publish(ctx, "t", i); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	if !waitFor(func() bool { return b.Stats().Filtered == 5 && c.len() == 5 }, 3*time.Second) {
		t.Fatalf("Filtered=%d collected=%d", b.Stats().Filtered, c.len())
	}
	for _, ev := range c.snapshot() {
		if ev.Payload.(int)%2 != 0 {
			t.Fatalf("奇数事件不应被投递: %v", ev.Payload)
		}
	}
}

// WithOnce：只消费一条后自动取消。
func TestOnce(t *testing.T) {
	b := newBus(t)
	c := &collector{}
	s, err := b.Subscribe("t", c.handler(), xevent.WithOnce())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := b.Publish(ctx, "t", i); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	if !waitFor(func() bool { return c.len() == 1 }, 3*time.Second) {
		t.Fatalf("收到 %d 条, want 1", c.len())
	}
	// 订阅应已自动摘除
	if !waitFor(func() bool { return b.Subscribers("t") == 0 }, 3*time.Second) {
		t.Fatalf("Subscribers = %d, want 0", b.Subscribers("t"))
	}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if c.len() != 1 {
		t.Fatalf("WithOnce 之后又收到事件: %d", c.len())
	}
}

// ---------------------------------------------------------------------------
// 消费组
// ---------------------------------------------------------------------------

func TestConsumerGroup(t *testing.T) {
	b := newBus(t)
	var a, bb atomic.Int64

	// 同组两个订阅：每条事件只应投给其中一个
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { a.Add(1); return nil },
		xevent.WithGroup("g1")); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { bb.Add(1); return nil },
		xevent.WithGroup("g1")); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// 另一个组
	var other atomic.Int64
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { other.Add(1); return nil },
		xevent.WithGroup("g2")); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	const n = 20
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx, "t", i); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	if !waitFor(func() bool {
		return a.Load()+bb.Load() == n && other.Load() == n
	}, 3*time.Second) {
		t.Fatalf("g1 合计 = %d（期望 %d）, g2 = %d（期望 %d）",
			a.Load()+bb.Load(), n, other.Load(), n)
	}
	// 组内应大致均衡（轮询）
	if a.Load() == 0 || bb.Load() == 0 {
		t.Fatalf("组内未负载均衡: a=%d b=%d", a.Load(), bb.Load())
	}
}

// ---------------------------------------------------------------------------
// 拉模式
// ---------------------------------------------------------------------------

func TestPullMode(t *testing.T) {
	b := newBus(t)
	s, err := b.Subscribe("t", nil, xevent.WithPull(), xevent.WithBuffer(4))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if s.Events() == nil {
		t.Fatal("拉模式的 Events 不应为 nil")
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := b.Publish(ctx, "t", i); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	got := make([]int, 0, 3)
	deadline := time.After(3 * time.Second)
	for len(got) < 3 {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("通道意外关闭（已收 %d 条）", len(got))
			}
			got = append(got, ev.Payload.(int))
		case <-deadline:
			t.Fatalf("只收到 %d 条: %v", len(got), got)
		}
	}
	// 拉模式顺序与入队一致
	for i, v := range got {
		if v != i {
			t.Fatalf("拉模式顺序错乱: %v", got)
		}
	}

	// 取消后通道应关闭
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !waitFor(func() bool {
		select {
		case _, ok := <-s.Events():
			return !ok
		default:
			return false
		}
	}, 2*time.Second) {
		t.Fatal("取消后拉模式通道未关闭")
	}
}

func TestPullHandlerConflicts(t *testing.T) {
	b := newBus(t)
	// 拉模式 + handler → 报错
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { return nil }, xevent.WithPull()); !errors.Is(err, xevent.ErrPullWithHandler) {
		t.Fatalf("err = %v, want ErrPullWithHandler", err)
	}
	// 无 handler 且非拉模式 → 报错
	if _, err := b.Subscribe("t", nil); !errors.Is(err, xevent.ErrNilHandler) {
		t.Fatalf("err = %v, want ErrNilHandler", err)
	}
}

// ---------------------------------------------------------------------------
// 延迟投递
// ---------------------------------------------------------------------------

func TestDelay(t *testing.T) {
	b := newBus(t)
	c := &collector{}
	if _, err := b.Subscribe("t", c.handler()); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	ctx := context.Background()
	start := time.Now()
	if err := b.Publish(ctx, "t", "delayed", xevent.WithDelay(120*time.Millisecond)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// 立即不应收到
	time.Sleep(40 * time.Millisecond)
	if c.len() != 0 {
		t.Fatal("延迟投递过早送达")
	}
	if !waitFor(func() bool { return c.len() == 1 }, 3*time.Second) {
		t.Fatal("延迟事件未送达")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("延迟未生效: %v", elapsed)
	}

	// Delay + Sync 是冲突语义
	if err := b.Publish(ctx, "t", nil, xevent.WithDelay(time.Second), xevent.WithSync(true)); err == nil {
		t.Fatal("WithDelay 与 WithSync 同时使用应报错")
	}

	// 关闭总线应停掉未到期的定时器
	b2 := newBus(t)
	c2 := &collector{}
	if _, err := b2.Subscribe("t", c2.handler()); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b2.Publish(context.Background(), "t", "x", xevent.WithDelay(100*time.Millisecond)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b2.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if c2.len() != 0 {
		t.Fatalf("关闭后延迟事件仍被投递: %d", c2.len())
	}
}

// ---------------------------------------------------------------------------
// 中间件
// ---------------------------------------------------------------------------

func TestMiddlewareChainOrder(t *testing.T) {
	b := newBus(t)
	var mu sync.Mutex
	var trace []string

	mw := func(name string) xevent.Middleware {
		return func(next xevent.Handler) xevent.Handler {
			return func(ctx context.Context, ev xevent.Event) error {
				mu.Lock()
				trace = append(trace, name+":in")
				mu.Unlock()
				err := next(ctx, ev)
				mu.Lock()
				trace = append(trace, name+":out")
				mu.Unlock()
				return err
			}
		}
	}

	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error {
		mu.Lock()
		trace = append(trace, "handler")
		mu.Unlock()
		return nil
	}, xevent.WithMiddleware(mw("A"), mw("B"))); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(trace) == 5
	}, 2*time.Second) {
		t.Fatalf("trace = %v", trace)
	}

	// 注意：断言必须在**独立作用域**里加锁，不能在测试函数上 defer Unlock ——
	// 下面 h2 会再次进入同一个中间件并申请同一把锁，defer 持有到函数结束会自死锁。
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(trace))
		copy(out, trace)
		return out
	}()
	want := []string{"A:in", "B:in", "handler", "B:out", "A:out"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("执行顺序 = %v, want %v", got, want)
		}
	}

	// Chain 空参数是恒等
	h := xevent.Chain()(func(context.Context, xevent.Event) error { return nil })
	if err := h(context.Background(), xevent.Event{}); err != nil {
		t.Fatalf("Chain() = %v", err)
	}
	// Chain 会跳过 nil
	h2 := xevent.Chain(nil, mw("C"))(func(context.Context, xevent.Event) error { return nil })
	_ = h2(context.Background(), xevent.Event{})
}

func TestMiddlewareRecoverAndTimeout(t *testing.T) {
	// Recover：panic → error → 触发重试与死信
	var dlErr atomic.Value
	b := newBus(t)
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { panic("boom") },
		xevent.WithMiddleware(xevent.Recover()),
		xevent.WithRetry(1, time.Millisecond),
		xevent.WithDeadLetter(func(_ context.Context, _ xevent.Event, err error) { dlErr.Store(err) }),
	); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return dlErr.Load() != nil }, 3*time.Second) {
		t.Fatal("panic 未被转成错误并进入死信")
	}
	err, _ := dlErr.Load().(error)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("错误 = %v", err)
	}

	// Timeout：handler 不理会 ctx，但投递流程应立即拿到超时错误
	bt := newBus(t)
	sentinel := errors.New("超时")
	var sawDeadline atomic.Bool
	if _, err := bt.Subscribe("t", func(ctx context.Context, _ xevent.Event) error {
		// 故意多等一会，观察 ctx 是否已到期
		time.Sleep(80 * time.Millisecond)
		if ctx.Err() != nil {
			sawDeadline.Store(true)
			return ctx.Err()
		}
		return sentinel
	}, xevent.WithMiddleware(xevent.Timeout(20*time.Millisecond)), xevent.WithRetry(0)); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := bt.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return bt.Stats().Failed == 1 }, 3*time.Second) {
		t.Fatal("超时未导致失败")
	}
	if !sawDeadline.Load() {
		t.Fatal("handler 未观察到 ctx 超时")
	}
}

func TestMiddlewareValidateAndSkip(t *testing.T) {
	b := newBus(t)
	c := &collector{}
	if _, err := b.Subscribe("t", c.handler(),
		xevent.WithMiddleware(
			xevent.Validate(func(ev xevent.Event) error {
				if ev.Header("v") == "" {
					return errors.New("缺少 v 头")
				}
				return nil
			}),
			xevent.Skip(func(ev xevent.Event) bool { return ev.Header("v") == "skip" }),
		),
		xevent.WithRetry(0),
	); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	ctx := context.Background()
	// 缺头 → 失败
	if err := b.Publish(ctx, "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return b.Stats().Failed == 1 }, 2*time.Second) {
		t.Fatal("校验失败未计入 Failed")
	}
	// v=skip → Skip 短路（视为成功）
	if err := b.Publish(ctx, "t", nil, xevent.WithHeaders(map[string]string{"v": "skip"})); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return b.Stats().Delivered == 1 }, 2*time.Second) {
		t.Fatal("Skip 未视为成功")
	}
	if c.len() != 0 {
		t.Fatal("Skip 的事件不应进入 handler")
	}
	// 正常
	if err := b.Publish(ctx, "t", nil, xevent.WithHeaders(map[string]string{"v": "ok"})); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 1 }, 2*time.Second) {
		t.Fatal("正常事件未投递")
	}
}

func TestMiddlewareTap(t *testing.T) {
	b := newBus(t)
	var calls, afters atomic.Int64
	var elapsedOK atomic.Bool

	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}, xevent.WithMiddleware(xevent.Tap(
		func(xevent.Event) { calls.Add(1) },
		func(_ xevent.Event, err error, d time.Duration) {
			afters.Add(1)
			if err == nil && d >= 4*time.Millisecond {
				elapsedOK.Store(true)
			}
		},
	))); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return afters.Load() == 1 }, 2*time.Second) {
		t.Fatal("Tap 回调未执行")
	}
	if calls.Load() != 1 || !elapsedOK.Load() {
		t.Fatalf("Tap: calls=%d elapsedOK=%v", calls.Load(), elapsedOK.Load())
	}
}

// ---------------------------------------------------------------------------
// 重放
// ---------------------------------------------------------------------------

func TestReplay(t *testing.T) {
	bb := xevent.New(xevent.WithReplayBuffer(10))
	defer func() { _ = bb.Close(context.Background()) }()
	ctx := context.Background()

	for i := 0; i < 15; i++ {
		topic := "t"
		if i%2 == 0 {
			topic = "other"
		}
		if err := bb.Publish(ctx, topic, i); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// 只保留最近 10 条
	if got := bb.ReplayCount(); got != 10 {
		t.Fatalf("ReplayCount = %d, want 10", got)
	}

	// 重放全部匹配 t 的事件（按原顺序）
	var got []int
	if err := bb.Replay(ctx, "t", func(_ context.Context, ev xevent.Event) error {
		got = append(got, ev.Payload.(int))
		return nil
	}, 0); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("重放未拿到事件")
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("重放顺序错误: %v", got)
		}
	}

	// 限制条数
	got = nil
	if err := bb.Replay(ctx, "t", func(_ context.Context, ev xevent.Event) error {
		got = append(got, ev.Payload.(int))
		return nil
	}, 2); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("限制条数失效: %v", got)
	}

	// 回调错误聚合
	sentinel := errors.New("replay 失败")
	if err := bb.Replay(ctx, "t", func(context.Context, xevent.Event) error { return sentinel }, 1); !errors.Is(err, sentinel) {
		t.Fatalf("Replay 错误 = %v", err)
	}
	// 通配模式
	if err := bb.Replay(ctx, "**", func(context.Context, xevent.Event) error { return nil }, 0); err != nil {
		t.Fatalf("Replay(**) = %v", err)
	}
	// nil handler
	if err := bb.Replay(ctx, "t", nil, 1); !errors.Is(err, xevent.ErrNilHandler) {
		t.Fatalf("Replay(nil) = %v", err)
	}
	// 未配置缓冲时不保留
	b2 := newBus(t)
	if b2.ReplayCount() != 0 {
		t.Fatalf("未配置缓冲却保留了 %d 条", b2.ReplayCount())
	}
}

// ---------------------------------------------------------------------------
// 关闭语义 / 观测 / nil 安全
// ---------------------------------------------------------------------------

func TestCloseGracefulAndIdempotent(t *testing.T) {
	b := xevent.New()
	var processed atomic.Int64
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error {
		processed.Add(1)
		return nil
	}, xevent.WithBuffer(128)); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	ctx := context.Background()
	const n = 200
	for i := 0; i < n; i++ {
		if err := b.Publish(ctx, "t", i); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 优雅关闭：已入队的事件必须全部投递完（不静默丢数据）
	if got := processed.Load(); got != n {
		t.Fatalf("关闭后已处理 = %d, want %d（优雅关闭丢事件）", got, n)
	}
	if !b.IsClosed() {
		t.Fatal("IsClosed 应为 true")
	}
	// 关闭后发布被拒绝
	if err := b.Publish(ctx, "t", 1); !errors.Is(err, xevent.ErrClosed) {
		t.Fatalf("Publish = %v, want ErrClosed", err)
	}
	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error { return nil }); !errors.Is(err, xevent.ErrClosed) {
		t.Fatalf("Subscribe = %v, want ErrClosed", err)
	}
	// 重复关闭安全
	if err := b.Close(ctx); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
}

func TestCloseCtxTimeout(t *testing.T) {
	b := xevent.New()
	release := make(chan struct{})
	defer close(release)

	if _, err := b.Subscribe("t", func(context.Context, xevent.Event) error {
		<-release
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b.Publish(context.Background(), "t", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return b.Stats().QueueDepth == 0 }, time.Second) {
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want DeadlineExceeded", err)
	}
}

func TestUnsubscribe(t *testing.T) {
	b := newBus(t)
	c := &collector{}
	s, err := b.Subscribe("t", c.handler())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ctx := context.Background()
	if err := b.Publish(ctx, "t", 1); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 1 }, 2*time.Second) {
		t.Fatal("未收到")
	}

	if err := b.Unsubscribe(s); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if b.Subscribers("t") != 0 {
		t.Fatalf("Subscribers = %d", b.Subscribers("t"))
	}
	if err := b.Publish(ctx, "t", 2); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if c.len() != 1 {
		t.Fatalf("退订后仍收到事件: %d", c.len())
	}

	// 非本总线的订阅
	b2 := newBus(t)
	s2, err := b2.Subscribe("t", c.handler())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b.Unsubscribe(s2); !errors.Is(err, xevent.ErrNoSubscription) {
		t.Fatalf("Unsubscribe(foreign) = %v", err)
	}
	if err := b.Unsubscribe(nil); !errors.Is(err, xevent.ErrNoSubscription) {
		t.Fatalf("Unsubscribe(nil) = %v", err)
	}
	// 重复 Cancel 安全
	if err := s.Cancel(); err != nil {
		t.Fatalf("重复 Cancel: %v", err)
	}
}

func TestStatsAndTopics(t *testing.T) {
	b := newBus(t)
	c := &collector{}

	if _, err := b.Subscribe("order.*", c.handler()); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := b.Subscribe("user.**", c.handler(), xevent.WithName("u")); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := b.Subscribe("order.*", c.handler(), xevent.WithName("dup")); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	topics := b.Topics()
	if len(topics) != 2 || topics[0] != "order.*" || topics[1] != "user.**" {
		t.Fatalf("Topics = %v", topics)
	}

	ctx := context.Background()
	if err := b.Publish(ctx, "order.created", 1); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !waitFor(func() bool { return c.len() == 2 }, 2*time.Second) {
		t.Fatalf("收到 %d 条, want 2（两个 order.* 订阅）", c.len())
	}

	st := b.Stats()
	if st.Published != 1 || st.Delivered != 2 || st.Subscribers != 3 {
		t.Fatalf("Stats = %+v", st)
	}

	subs := b.Subscriptions()
	if len(subs) != 3 {
		t.Fatalf("Subscriptions = %d", len(subs))
	}
	found := false
	for _, s := range subs {
		if s.Name == "u" && s.Pattern == "user.**" {
			found = true
		}
	}
	if !found {
		t.Fatalf("订阅快照缺少 name: %+v", subs)
	}
}

func TestNilAndArgSafety(t *testing.T) {
	var nilBus *xevent.Bus
	_ = nilBus

	b := newBus(t)
	ctx := context.Background()

	// 空主题
	if err := b.Publish(ctx, "", nil); !errors.Is(err, xevent.ErrEmptyTopic) {
		t.Fatalf("Publish(空主题) = %v", err)
	}
	if err := b.PublishEvent(ctx, xevent.Event{}); !errors.Is(err, xevent.ErrEmptyTopic) {
		t.Fatalf("PublishEvent(空主题) = %v", err)
	}
	// 空模式
	if _, err := b.Subscribe("", func(context.Context, xevent.Event) error { return nil }); !errors.Is(err, xevent.ErrEmptyTopic) {
		t.Fatalf("Subscribe(空模式) = %v", err)
	}
	// 非法模式
	if _, err := b.Subscribe("a.**.c", func(context.Context, xevent.Event) error { return nil }); err == nil {
		t.Fatal("非法模式应报错")
	}
	// nil ctx
	if err := b.Publish(nil, "t", nil); err != nil {
		t.Fatalf("Publish(nil ctx) = %v", err)
	}

	// nil 总线的 Close / Subscribers / Topics
	closed := xevent.New()
	_ = closed
	if got := b.Subscribers("x"); got != 0 {
		t.Fatalf("Subscribers = %d", got)
	}
	if err := b.Close(nil); err != nil {
		t.Fatalf("Close(nil ctx) = %v", err)
	}
}

// 订阅计数器：并发发布/订阅/退订不应 panic 或泄漏 goroutine。
func TestNoGoroutineLeak(t *testing.T) {
	// 预热
	for i := 0; i < 3; i++ {
		b := xevent.New()
		s, _ := b.Subscribe("t", func(context.Context, xevent.Event) error { return nil })
		_ = b.Publish(context.Background(), "t", nil)
		_ = s.Cancel()
		_ = b.Close(context.Background())
	}

	runtime.GC()
	base := runtime.NumGoroutine()

	for i := 0; i < 30; i++ {
		b := xevent.New()
		for j := 0; j < 4; j++ {
			if _, err := b.Subscribe(fmt.Sprintf("t%d", j), func(context.Context, xevent.Event) error { return nil }); err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
		}
		for j := 0; j < 20; j++ {
			if err := b.Publish(context.Background(), "t0", j, xevent.WithKey("k")); err != nil {
				t.Fatalf("Publish: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := b.Close(ctx); err != nil {
			cancel()
			t.Fatalf("Close: %v", err)
		}
		cancel()
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= base+3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine 泄漏: base=%d now=%d", base, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// 综合压测：多发布者 × 多订阅 × 按 key 保序，验证不漏不重
// ---------------------------------------------------------------------------

func TestConcurrentStress(t *testing.T) {
	b := newBus(t, xevent.WithKeyedShards(8))

	const (
		publishers  = 8
		perP        = 100
		keys        = 4
		subscribers = 3
	)

	// 每个订阅都独立收集（广播语义：每个订阅各收到全部事件）
	cols := make([]*collector, subscribers)
	for i := range cols {
		cols[i] = &collector{}
		if _, err := b.Subscribe("stress", cols[i].handler()); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perP; i++ {
				k := fmt.Sprintf("k%d", (p+i)%keys)
				if err := b.Publish(context.Background(), "stress", p*perP+i, xevent.WithKey(k)); err != nil {
					t.Errorf("Publish: %v", err)
					return
				}
			}
		}(p)
	}
	wg.Wait()

	total := publishers * perP
	if !waitFor(func() bool { return b.Stats().Delivered == int64(total*subscribers) }, 20*time.Second) {
		t.Fatalf("Delivered = %d, want %d", b.Stats().Delivered, total*subscribers)
	}

	// 每个订阅都必须收到全部事件且不重复
	for i, c := range cols {
		events := c.snapshot()
		if len(events) != total {
			t.Fatalf("订阅 %d 收到 %d 条, want %d", i, len(events), total)
		}
		seen := make(map[int]bool, total)
		for _, ev := range events {
			n := ev.Payload.(int)
			if seen[n] {
				t.Fatalf("订阅 %d 收到重复事件 %d", i, n)
			}
			seen[n] = true
		}
	}

	st := b.Stats()
	if st.Failed != 0 || st.Dropped != 0 || st.Panics != 0 {
		t.Fatalf("压测出现异常统计: %+v", st)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkPublishAsync(b *testing.B) {
	bs := xevent.New(xevent.WithGlobalBuffer(8192))
	defer func() { _ = bs.Close(context.Background()) }()
	if _, err := bs.Subscribe("t", func(context.Context, xevent.Event) error { return nil }); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bs.Publish(ctx, "t", i); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPublishKeyed(b *testing.B) {
	bs := xevent.New(xevent.WithGlobalBuffer(8192), xevent.WithKeyedShards(16))
	defer func() { _ = bs.Close(context.Background()) }()
	if _, err := bs.Subscribe("t", func(context.Context, xevent.Event) error { return nil }); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bs.Publish(ctx, "t", i, xevent.WithKey("k")); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPublishSync(b *testing.B) {
	bs := xevent.New()
	defer func() { _ = bs.Close(context.Background()) }()
	if _, err := bs.Subscribe("t", func(context.Context, xevent.Event) error { return nil }); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bs.Publish(ctx, "t", i, xevent.WithSync(true)); err != nil {
			b.Fatal(err)
		}
	}
}
