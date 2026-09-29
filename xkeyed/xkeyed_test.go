package xkeyed_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xkeyed"
)

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func waitUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Microsecond)
	}
	return cond()
}

// orderRecorder 记录每个 key 上的执行顺序，并检测同 key 是否发生并发重叠。
type orderRecorder struct {
	mu       sync.Mutex
	seq      map[string][]int
	inFlight map[string]int
	maxOver  map[string]int
}

func newOrderRecorder() *orderRecorder {
	return &orderRecorder{seq: map[string][]int{}, inFlight: map[string]int{}, maxOver: map[string]int{}}
}

func (r *orderRecorder) enter(key string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight[key]++
	if r.inFlight[key] > r.maxOver[key] {
		r.maxOver[key] = r.inFlight[key]
	}
	r.seq[key] = append(r.seq[key], n)
}

func (r *orderRecorder) leave(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight[key]--
}

func (r *orderRecorder) snapshot(key string) (seq []int, maxOverlap int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.seq[key]))
	copy(out, r.seq[key])
	return out, r.maxOver[key]
}

func (r *orderRecorder) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seq))
	for k := range r.seq {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// 核心保证：同 key 严格串行且保序
// ---------------------------------------------------------------------------

func TestSameKeySerialAndOrdered(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(16))
	defer p.Close()

	rec := newOrderRecorder()
	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 每个任务都先进入临界区，再 sleep 一小会以「放大」并发重叠的概率
			err := p.Do(context.Background(), "same-key", func(context.Context) error {
				rec.enter("same-key", i)
				time.Sleep(20 * time.Microsecond)
				rec.leave("same-key")
				return nil
			})
			if err != nil {
				t.Errorf("Do(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	seq, maxOverlap := rec.snapshot("same-key")
	if maxOverlap != 1 {
		t.Fatalf("同 key 出现并发重叠: 最大 %d（必须为 1）", maxOverlap)
	}
	if len(seq) != n {
		t.Fatalf("执行次数 = %d, want %d", len(seq), n)
	}
	// 提交是并发无序的，因此这里只能断言「恰好覆盖 0..n-1 各一次」
	seen := make([]bool, n)
	for _, v := range seq {
		if v < 0 || v >= n {
			t.Fatalf("越界序号 %d", v)
		}
		if seen[v] {
			t.Fatalf("序号 %d 被执行了两次（同 key 任务丢失或重复）", v)
		}
		seen[v] = true
	}
}

// 顺序保证：**单个 goroutine 顺序提交**时，执行顺序必须与提交顺序完全一致。
func TestSameKeyExecutionOrderMatchesSubmission(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(4))
	defer p.Close()

	rec := newOrderRecorder()
	for i := 0; i < 300; i++ {
		i := i
		if err := p.Submit(context.Background(), "k", func(context.Context) error {
			rec.enter("k", i)
			rec.leave("k")
			return nil
		}); err != nil {
			t.Fatalf("Submit(%d): %v", i, err)
		}
	}
	if err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	seq, maxOverlap := rec.snapshot("k")
	if maxOverlap != 1 {
		t.Fatalf("并发重叠 = %d", maxOverlap)
	}
	if len(seq) != 300 {
		t.Fatalf("执行次数 = %d", len(seq))
	}
	for i, v := range seq {
		if v != i {
			t.Fatalf("顺序错乱: 第 %d 位是 %d（期望 %d）", i, v, i)
		}
	}
}

// 即使两个 key 撞到同一分片（假冲突），**各自的相对顺序**也必须保持。
func TestFakeConflictKeepsPerKeyOrder(t *testing.T) {
	// 分片数为 1 → 所有 key 必然同分片（最强的假冲突场景）
	p := xkeyed.New(xkeyed.WithShards(1))
	defer p.Close()

	rec := newOrderRecorder()
	const perKey = 100
	keys := []string{"a", "b", "c"}

	for i := 0; i < perKey; i++ {
		for _, k := range keys {
			k, i := k, i
			if err := p.Submit(context.Background(), k, func(context.Context) error {
				rec.enter(k, i)
				rec.leave(k)
				return nil
			}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
		}
	}
	if err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	for _, k := range keys {
		seq, maxOverlap := rec.snapshot(k)
		if maxOverlap != 1 {
			t.Fatalf("key %s 并发重叠 = %d", k, maxOverlap)
		}
		if len(seq) != perKey {
			t.Fatalf("key %s 执行次数 = %d", k, len(seq))
		}
		for i, v := range seq {
			if v != i {
				t.Fatalf("key %s 顺序错乱: 第 %d 位是 %d", k, i, v)
			}
		}
	}
}

// 不同 key 落在不同分片时应当并行（否则分片就白设计了）。
func TestDifferentKeysRunInParallel(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(64))
	defer p.Close()

	// 找 8 个落在不同分片的 key
	keys := make([]string, 0, 8)
	used := map[int]bool{}
	for i := 0; len(keys) < 8 && i < 10000; i++ {
		k := fmt.Sprintf("key-%d", i)
		s := xkeyed.ShardOf(k, 64)
		if used[s] {
			continue
		}
		used[s] = true
		keys = append(keys, k)
	}
	if len(keys) < 8 {
		t.Fatal("未找到足够多的不同分片 key")
	}

	start := make(chan struct{})
	var concurrent, maxConcurrent int64
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_ = p.Do(context.Background(), k, func(context.Context) error {
				n := atomic.AddInt64(&concurrent, 1)
				for {
					old := atomic.LoadInt64(&maxConcurrent)
					if n <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, n) {
						break
					}
				}
				<-start // 等所有分片都进入临界区
				atomic.AddInt64(&concurrent, -1)
				return nil
			})
		}(k)
	}

	// 给所有分片一点时间启动，然后放行
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&concurrent) < 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(start)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并行任务未完成")
	}

	if got := atomic.LoadInt64(&maxConcurrent); got < 2 {
		t.Fatalf("不同 key 未观察到并行: 最大并发 = %d（分片未生效）", got)
	}
}

// ---------------------------------------------------------------------------
// Do / DoValue / Submit
// ---------------------------------------------------------------------------

func TestDoAndDoValue(t *testing.T) {
	p := xkeyed.New()
	defer p.Close()
	ctx := context.Background()

	// Do 透传任务错误
	sentinel := errors.New("业务失败")
	if err := p.Do(ctx, "k", func(context.Context) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Do = %v, want sentinel", err)
	}
	// Do 成功
	if err := p.Do(ctx, "k", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	// ctx 会传给任务
	if err := p.Do(ctx, "k", func(c context.Context) error {
		if c != ctx {
			return errors.New("ctx 未传递")
		}
		return nil
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	// nil ctx 视为 Background
	if err := p.Do(nil, "k", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do(nil ctx): %v", err)
	}

	// DoValue
	v, err := xkeyed.DoValue(ctx, p, "k", func(context.Context) (string, error) { return "ok", nil })
	if err != nil || v != "ok" {
		t.Fatalf("DoValue = %q, %v", v, err)
	}
	if _, err := xkeyed.DoValue(ctx, p, "k", func(context.Context) (int, error) { return 7, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("DoValue 错误 = %v", err)
	}
	// 错误路径不应返回残值
	if got, err := xkeyed.DoValue[int](ctx, p, "k", func(context.Context) (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) || got != 0 {
		t.Fatalf("DoValue = %d, %v", got, err)
	}

	// nil fn / nil Pipe
	if err := p.Do(ctx, "k", nil); !errors.Is(err, xkeyed.ErrNilFunc) {
		t.Fatalf("Do(nil fn) = %v", err)
	}
	if err := p.Submit(ctx, "k", nil); !errors.Is(err, xkeyed.ErrNilFunc) {
		t.Fatalf("Submit(nil fn) = %v", err)
	}
	var nilPipe *xkeyed.Pipe
	if err := nilPipe.Do(ctx, "k", func(context.Context) error { return nil }); !errors.Is(err, xkeyed.ErrClosed) {
		t.Fatalf("nil Do = %v", err)
	}
	if err := nilPipe.Submit(ctx, "k", func(context.Context) error { return nil }); !errors.Is(err, xkeyed.ErrClosed) {
		t.Fatalf("nil Submit = %v", err)
	}
	if _, err := xkeyed.DoValue[int](ctx, nil, "k", func(context.Context) (int, error) { return 1, nil }); !errors.Is(err, xkeyed.ErrClosed) {
		t.Fatalf("nil DoValue = %v", err)
	}
	if err := nilPipe.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	if !nilPipe.IsClosed() || nilPipe.Shards() != 0 || nilPipe.Pending() != 0 {
		t.Fatal("nil 零值方法错误")
	}
	if st := nilPipe.Stats(); st != (xkeyed.Stats{}) {
		t.Fatalf("nil Stats = %+v", st)
	}
	if err := nilPipe.Wait(ctx); err != nil {
		t.Fatalf("nil Wait = %v", err)
	}
}

// Submit 不等待结果，因此任务错误只能通过 WithOnError 暴露。
func TestSubmitErrorViaCallback(t *testing.T) {
	type item struct {
		key string
		err error
	}
	var mu sync.Mutex
	var got []item

	p := xkeyed.New(xkeyed.WithOnError(func(key string, err error) {
		mu.Lock()
		got = append(got, item{key, err})
		mu.Unlock()
	}))
	defer p.Close()

	sentinel := errors.New("异步失败")
	ctx := context.Background()
	if err := p.Submit(ctx, "a", func(context.Context) error { return sentinel }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := p.Submit(ctx, "b", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !waitUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}, time.Second) {
		t.Fatalf("onError 调用次数 = %d, want 1", len(got))
	}
	if got[0].key != "a" || !errors.Is(got[0].err, sentinel) {
		t.Fatalf("onError 参数 = %+v", got[0])
	}
	if st := p.Stats(); st.Failed != 1 || st.Completed != 1 {
		t.Fatalf("Stats = %+v", st)
	}
}

// ---------------------------------------------------------------------------
// panic 保护：worker 绝不能因为业务 panic 而退出
// ---------------------------------------------------------------------------

func TestPanicDoesNotKillWorker(t *testing.T) {
	var panics []any
	var mu sync.Mutex

	p := xkeyed.New(
		xkeyed.WithShards(1),
		xkeyed.WithOnPanic(func(key string, r any) {
			mu.Lock()
			panics = append(panics, r)
			mu.Unlock()
		}),
	)
	defer p.Close()
	ctx := context.Background()

	// panic
	err := p.Do(ctx, "k", func(context.Context) error { panic("boom") })
	if err == nil {
		t.Fatal("panic 任务应返回错误")
	}
	// worker 必须存活：后续任务照常执行
	if err := p.Do(ctx, "k", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("panic 后 worker 已死: %v", err)
	}
	// 同 key 顺序不受影响
	var order []int
	for i := 0; i < 5; i++ {
		i := i
		if err := p.Submit(ctx, "k", func(context.Context) error { order = append(order, i); return nil }); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(order) != 5 {
		t.Fatalf("order = %v", order)
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("panic 后顺序错乱: %v", order)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(panics) != 1 || panics[0] != "boom" {
		t.Fatalf("onPanic = %v", panics)
	}
	if st := p.Stats(); st.Panics != 1 {
		t.Fatalf("Stats.Panics = %d", st.Panics)
	}
}

// ---------------------------------------------------------------------------
// 背压与溢出策略
// ---------------------------------------------------------------------------

// blockedPipe 创建一个「worker 已被占住」的管道，用于确定性地测试队列溢出。
//
// 关键：必须等 Stats().Running == 1（任务真正开始执行）再往队列里塞，
// 否则 worker 可能还没启动，队列里的任务会被立刻消费掉，测试变得不确定。
func blockedPipe(t *testing.T, opts ...xkeyed.Option) (*xkeyed.Pipe, func()) {
	t.Helper()
	block := make(chan struct{})
	p := xkeyed.New(opts...)
	if err := p.Submit(context.Background(), "k", func(context.Context) error {
		<-block
		return nil
	}); err != nil {
		t.Fatalf("占用任务提交失败: %v", err)
	}
	if !waitUntil(func() bool { return p.Stats().Running == 1 }, 2*time.Second) {
		t.Fatalf("占用任务未开始执行")
	}
	return p, func() { close(block) }
}

func TestOverflowPolicies(t *testing.T) {
	noop := func(context.Context) error { return nil }

	t.Run("DropNewest拒绝新任务", func(t *testing.T) {
		p, release := blockedPipe(t,
			xkeyed.WithShards(1), xkeyed.WithQueueSize(1), xkeyed.WithOverflow(xcontainer.DropNewest))
		defer func() { release(); p.Close() }()

		ctx := context.Background()
		if err := p.Submit(ctx, "k", noop); err != nil { // 队列 [1]
			t.Fatalf("Submit: %v", err)
		}
		if err := p.Submit(ctx, "k", noop); !errors.Is(err, xkeyed.ErrQueueFull) { // 队列满 → 拒绝
			t.Fatalf("Submit = %v, want ErrQueueFull", err)
		}
		if st := p.Stats(); st.Dropped != 1 {
			t.Fatalf("Dropped = %d, want 1", st.Dropped)
		}
	})

	t.Run("Fail拒绝新任务", func(t *testing.T) {
		p, release := blockedPipe(t,
			xkeyed.WithShards(1), xkeyed.WithQueueSize(1), xkeyed.WithOverflow(xcontainer.Fail))
		defer func() { release(); p.Close() }()

		ctx := context.Background()
		if err := p.Submit(ctx, "k", noop); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		err := p.Submit(ctx, "k", noop)
		if !errors.Is(err, xkeyed.ErrQueueFull) {
			t.Fatalf("Submit = %v, want ErrQueueFull", err)
		}
		// 错误必须带可判定的错误码（便于业务区分「背压」与「管道关闭」）
		if code := xerr.CodeOf(err); code != xerr.CodeResourceExhausted {
			t.Fatalf("错误码 = %v", code)
		}
	})

	t.Run("DropOldest丢最旧", func(t *testing.T) {
		var mu sync.Mutex
		var ran []int

		p, release := blockedPipe(t,
			xkeyed.WithShards(1), xkeyed.WithQueueSize(2), xkeyed.WithOverflow(xcontainer.DropOldest))
		ctx := context.Background()

		rec := func(n int) func(context.Context) error {
			return func(context.Context) error {
				mu.Lock()
				ran = append(ran, n)
				mu.Unlock()
				return nil
			}
		}
		if err := p.Submit(ctx, "k", rec(1)); err != nil { // 队列 [1]
			t.Fatalf("Submit: %v", err)
		}
		if err := p.Submit(ctx, "k", rec(2)); err != nil { // 队列 [1,2]
			t.Fatalf("Submit: %v", err)
		}
		if err := p.Submit(ctx, "k", rec(3)); err != nil { // 满 → 丢 1 → 队列 [2,3]
			t.Fatalf("Submit: %v", err)
		}
		if st := p.Stats(); st.Dropped != 1 {
			t.Fatalf("Dropped = %d, want 1", st.Dropped)
		}

		release()
		if err := p.Wait(ctx); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		_ = p.Close()

		mu.Lock()
		defer mu.Unlock()
		if len(ran) != 2 || ran[0] != 2 || ran[1] != 3 {
			t.Fatalf("DropOldest 后执行 %v, want [2 3]", ran)
		}
	})

	t.Run("Block超时放弃", func(t *testing.T) {
		p, release := blockedPipe(t, xkeyed.WithShards(1), xkeyed.WithQueueSize(1))
		defer func() { release(); p.Close() }()

		if err := p.Submit(context.Background(), "k", noop); err != nil { // 队列满
			t.Fatalf("Submit: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := p.Submit(ctx, "k", noop)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Submit = %v, want DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
			t.Fatalf("过早返回: %v", elapsed)
		}
	})

	t.Run("Block在关闭时立刻返回", func(t *testing.T) {
		p, release := blockedPipe(t, xkeyed.WithShards(1), xkeyed.WithQueueSize(1))
		if err := p.Submit(context.Background(), "k", noop); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		errc := make(chan error, 1)
		go func() {
			// 队列满 → 阻塞在 Submit 上
			errc <- p.Submit(context.Background(), "k", noop)
		}()
		time.Sleep(30 * time.Millisecond)
		release()
		_ = p.Close()

		select {
		case err := <-errc:
			if !errors.Is(err, xkeyed.ErrClosed) {
				t.Fatalf("Submit = %v, want ErrClosed", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close 未唤醒阻塞中的 Submit")
		}
	})
}

// ---------------------------------------------------------------------------
// 关闭语义
// ---------------------------------------------------------------------------

func TestCloseDrainsAndRejects(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(4), xkeyed.WithQueueSize(64))
	ctx := context.Background()

	var processed int64
	for i := 0; i < 100; i++ {
		if err := p.Submit(ctx, fmt.Sprintf("k%d", i%4), func(context.Context) error {
			atomic.AddInt64(&processed, 1)
			return nil
		}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 优雅关闭必须把已入队的任务全部执行完（不丢数据）
	if got := atomic.LoadInt64(&processed); got != 100 {
		t.Fatalf("Close 后已处理 = %d, want 100（优雅关闭丢任务）", got)
	}
	if !p.IsClosed() {
		t.Fatal("IsClosed 应为 true")
	}
	// 关闭后拒绝新任务
	if err := p.Submit(ctx, "k", func(context.Context) error { return nil }); !errors.Is(err, xkeyed.ErrClosed) {
		t.Fatalf("Submit = %v, want ErrClosed", err)
	}
	if err := p.Do(ctx, "k", func(context.Context) error { return nil }); !errors.Is(err, xkeyed.ErrClosed) {
		t.Fatalf("Do = %v, want ErrClosed", err)
	}
	// 重复关闭安全
	if err := p.Close(); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
	if err := p.CloseCtx(ctx); err != nil {
		t.Fatalf("CloseCtx: %v", err)
	}
}

func TestCloseCtxTimeout(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(1), xkeyed.WithQueueSize(8))
	release := make(chan struct{})
	defer close(release)

	// 一个长时间运行的任务（不受 ctx 影响，模拟「卡住的任务」）
	if err := p.Submit(context.Background(), "k", func(context.Context) error {
		<-release
		return nil
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !waitUntil(func() bool { return p.Stats().Running == 1 }, time.Second) {
		t.Fatal("任务未开始执行")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.CloseCtx(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseCtx = %v, want DeadlineExceeded", err)
	}
}

func TestWaitTimeout(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(1))
	defer p.Close()
	release := make(chan struct{})
	defer close(release)

	if err := p.Submit(context.Background(), "k", func(context.Context) error {
		<-release
		return nil
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want DeadlineExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// 哈希与分片
// ---------------------------------------------------------------------------

func TestHashAndShard(t *testing.T) {
	for i := 0; i < 3; i++ {
		if xkeyed.HashKey("order:1") != xkeyed.HashKey("order:1") {
			t.Fatal("HashKey 必须可复现")
		}
	}
	if xkeyed.HashKey("a") == xkeyed.HashKey("b") {
		t.Fatal("不同 key 不应同哈希")
	}
	if xkeyed.HashKey("") == 0 {
		t.Fatal("空 key 也应有非零哈希")
	}

	for _, shards := range []int{1, 2, 8, 64} {
		for _, k := range []string{"", "a", "order:1"} {
			s := xkeyed.ShardOf(k, shards)
			if s < 0 || s >= shards {
				t.Fatalf("ShardOf(%q,%d) = %d 越界", k, shards, s)
			}
		}
	}
	if xkeyed.ShardOf("x", 0) != 0 || xkeyed.ShardOf("x", -5) != 0 {
		t.Fatal("shards<=0 应视为 1")
	}

	// 分布应大致均匀（避免哈希实现退化成常数）
	const shards = 16
	counts := make([]int, shards)
	for i := 0; i < 1600; i++ {
		counts[xkeyed.ShardOf(fmt.Sprintf("key-%d", i), shards)]++
	}
	for i, c := range counts {
		if c < 40 || c > 260 {
			t.Fatalf("分片 %d 分布异常: %d（1600 个 key / 16 片，期望约 100）", i, c)
		}
	}
}

func TestDefaultShardsAndOptions(t *testing.T) {
	p := xkeyed.New()
	defer p.Close()
	if p.Shards() < 8 {
		t.Fatalf("默认分片数 = %d，应 >= 8", p.Shards())
	}

	// shards<=0 使用默认；queueSize<=0 归一化
	p2 := xkeyed.New(xkeyed.WithShards(0), xkeyed.WithShards(-1), xkeyed.WithQueueSize(0))
	defer p2.Close()
	if p2.Shards() < 8 {
		t.Fatalf("分片数 = %d", p2.Shards())
	}
	if err := p2.Do(context.Background(), "k", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}

	p3 := xkeyed.New(xkeyed.WithShards(3))
	defer p3.Close()
	if p3.Shards() != 3 {
		t.Fatalf("Shards = %d", p3.Shards())
	}

	// 非法溢出策略应被归一化（不 panic）
	p4 := xkeyed.New(xkeyed.WithShards(1), xkeyed.WithQueueSize(1), xkeyed.WithOverflow(xcontainer.OverflowPolicy(99)))
	defer p4.Close()
	if err := p4.Do(context.Background(), "k", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 统计
// ---------------------------------------------------------------------------

func TestStats(t *testing.T) {
	p := xkeyed.New(xkeyed.WithShards(2), xkeyed.WithOnError(func(string, error) {}))
	ctx := context.Background()

	if st := p.Stats(); st.Submitted != 0 || st.Completed != 0 || st.Shards != 2 {
		t.Fatalf("初始 Stats = %+v", st)
	}

	_ = p.Do(ctx, "a", func(context.Context) error { return nil })
	_ = p.Do(ctx, "a", func(context.Context) error { return errors.New("x") })
	_ = p.Submit(ctx, "b", func(context.Context) error { panic("p") })
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	var st xkeyed.Stats
	if !waitUntil(func() bool {
		st = p.Stats()
		return st.Completed == 1 && st.Failed == 2 && st.Panics == 1 && st.Submitted == 3
	}, 2*time.Second) {
		t.Fatalf("Stats = %+v", st)
	}
	if st.Pending != 0 || st.Running != 0 {
		t.Fatalf("在途计数未归零: %+v", st)
	}
}

// ---------------------------------------------------------------------------
// 并发压力：多 goroutine × 多 key，全面验证「同 key 不并发 + 不漏不重」
// ---------------------------------------------------------------------------

func TestConcurrentStress(t *testing.T) {
	const (
		goroutines = 24
		keys       = 8
		perG       = 60
	)
	p := xkeyed.New(xkeyed.WithShards(4), xkeyed.WithQueueSize(256))
	defer p.Close()

	rec := newOrderRecorder()
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				k := fmt.Sprintf("k%d", (g+i)%keys)
				n := g*perG + i
				if err := p.Do(context.Background(), k, func(context.Context) error {
					rec.enter(k, n)
					time.Sleep(10 * time.Microsecond)
					rec.leave(k)
					return nil
				}); err != nil {
					t.Errorf("Do: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	total := 0
	for _, k := range rec.keys() {
		seq, maxOverlap := rec.snapshot(k)
		if maxOverlap != 1 {
			t.Fatalf("key %s 并发重叠 = %d（同 key 串行保证被破坏）", k, maxOverlap)
		}
		total += len(seq)
	}
	if total != goroutines*perG {
		t.Fatalf("总执行次数 = %d, want %d（有任务丢失或重复）", total, goroutines*perG)
	}
	if st := p.Stats(); st.Completed != int64(goroutines*perG) {
		t.Fatalf("Stats.Completed = %d", st.Completed)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkDoSameKey(b *testing.B) {
	p := xkeyed.New(xkeyed.WithShards(16))
	defer p.Close()
	ctx := context.Background()
	fn := func(context.Context) error { return nil }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.Do(ctx, "hot", fn); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoParallelKeys(b *testing.B) {
	p := xkeyed.New(xkeyed.WithShards(64), xkeyed.WithQueueSize(4096))
	defer p.Close()
	fn := func(context.Context) error { return nil }
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = p.Do(context.Background(), fmt.Sprintf("k%d", i%64), fn)
			i++
		}
	})
}

func BenchmarkSubmitAsync(b *testing.B) {
	p := xkeyed.New(xkeyed.WithShards(64), xkeyed.WithQueueSize(8192))
	defer p.Close()
	ctx := context.Background()
	fn := func(context.Context) error { return nil }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.Submit(ctx, fmt.Sprintf("k%d", i%64), fn); err != nil {
			b.Fatal(err)
		}
	}
	_ = p.Wait(ctx)
}
