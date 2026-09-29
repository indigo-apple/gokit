package xqueue_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xcontainer/xqueue"
	"github.com/zhangwenxing/gokit/xerr"
)

func eqInts(t *testing.T, name string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// SliceQueue
// ---------------------------------------------------------------------------

func TestSliceQueueFIFO(t *testing.T) {
	q := xqueue.New[int]()
	q.Push(1)
	q.PushAll(2, 3)

	if v, ok := q.Peek(); !ok || v != 1 {
		t.Fatalf("Peek = %d,%v", v, ok)
	}
	eqInts(t, "Values", q.Values(), []int{1, 2, 3})
	if q.Len() != 3 {
		t.Fatalf("Len = %d", q.Len())
	}

	for _, want := range []int{1, 2, 3} {
		got, ok := q.Pop()
		if !ok || got != want {
			t.Fatalf("Pop = %d,%v, want %d", got, ok, want)
		}
	}
	if !q.IsEmpty() {
		t.Fatal("应已清空")
	}
	if v, ok := q.Peek(); ok || v != 0 {
		t.Fatalf("空队列 Peek = %d,%v", v, ok)
	}
	if v, ok := q.Pop(); ok || v != 0 {
		t.Fatalf("空队列 Pop = %d,%v", v, ok)
	}
}

// 这是 SliceQueue 最关键的测试：head 索引 + 惰性压缩必须保证 FIFO 顺序不错乱。
func TestSliceQueueFIFOAcrossCompaction(t *testing.T) {
	q := xqueue.New[int]()
	want := 0
	pushed := 0

	// 反复「批量入队 137 → 批量出队 100」，跨越多次 head 压缩阈值
	for round := 0; round < 30; round++ {
		for i := 0; i < 137; i++ {
			q.Push(pushed)
			pushed++
		}
		for i := 0; i < 100; i++ {
			got, ok := q.Pop()
			if !ok {
				t.Fatalf("第 %d 轮第 %d 次 Pop 意外为空", round, i)
			}
			if got != want {
				t.Fatalf("FIFO 错乱: got %d, want %d (round=%d i=%d)", got, want, round, i)
			}
			want++
		}
	}

	// 剩余元素也必须按序
	rest := q.Drain()
	for i, v := range rest {
		if v != want+i {
			t.Fatalf("剩余元素错乱: [%d]=%d, want %d", i, v, want+i)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("Drain 后 Len = %d", q.Len())
	}
}

func TestSliceQueueClearAndReuse(t *testing.T) {
	q := xqueue.New[int]()
	q.PushAll(1, 2, 3)
	q.Pop()
	if q.Len() != 2 {
		t.Fatalf("Len = %d", q.Len())
	}
	q.Clear()
	if !q.IsEmpty() || q.Values() != nil || q.Len() != 0 {
		t.Fatal("Clear 后应为空")
	}
	// Clear 后 head 归零，必须能正常复用
	q.Push(9)
	eqInts(t, "Clear 后复用", q.Values(), []int{9})
	if v, ok := q.Pop(); !ok || v != 9 {
		t.Fatalf("Pop = %d,%v", v, ok)
	}
}

func TestSliceQueueForEachAndClone(t *testing.T) {
	q := xqueue.New[int]()
	q.PushAll(1, 2, 3)
	q.Pop() // 队首变 2

	var seen []int
	q.ForEach(func(v int) { seen = append(seen, v) })
	eqInts(t, "ForEach", seen, []int{2, 3})
	if q.Len() != 2 {
		t.Fatal("ForEach 不应消耗队列")
	}

	c := q.Clone()
	c.Push(4)
	if q.Len() != 2 {
		t.Fatal("Clone 应独立")
	}
	eqInts(t, "Clone", c.Values(), []int{2, 3, 4})
}

func TestSliceQueueNilAndEmptySafety(t *testing.T) {
	var q *xqueue.SliceQueue[int]
	if q.Push(1) {
		t.Fatal("nil Push 应为 false")
	}
	q.PushAll(1, 2)
	if v, ok := q.Pop(); ok || v != 0 {
		t.Fatalf("nil Pop = %d,%v", v, ok)
	}
	if v, ok := q.Peek(); ok || v != 0 {
		t.Fatalf("nil Peek = %d,%v", v, ok)
	}
	if q.Len() != 0 || !q.IsEmpty() || q.Values() != nil || q.Drain() != nil {
		t.Fatal("nil 零值方法错误")
	}
	q.Clear()
	q.ForEach(func(int) { t.Fatal("nil 不应回调") })
	if q.Clone() == nil {
		t.Fatal("nil Clone 应返回空队列")
	}

	// 空队列（非 nil）同样安全
	e := xqueue.New[string]()
	e.ForEach(func(string) { t.Fatal("空队列不应回调") })
	if e.Values() != nil || e.Drain() != nil {
		t.Fatal("空队列应返回 nil")
	}
}

// ---------------------------------------------------------------------------
// Ring
// ---------------------------------------------------------------------------

func TestRingBasicAndWraparound(t *testing.T) {
	q := xqueue.NewRing[int](4)
	if q.Cap() != 4 || !q.IsEmpty() || q.IsFull() {
		t.Fatal("初始状态错误")
	}

	for i := 1; i <= 4; i++ {
		if !q.Push(i) {
			t.Fatalf("Push(%d) 失败", i)
		}
	}
	if !q.IsFull() {
		t.Fatal("应为满")
	}
	if q.Push(5) {
		t.Fatal("满队列 Push 应返回 false")
	}
	eqInts(t, "满队列 Values", q.Values(), []int{1, 2, 3, 4})

	// 出队 1 个再入队 1 个：触发绕环
	if v, ok := q.Pop(); !ok || v != 1 {
		t.Fatalf("Pop = %d,%v", v, ok)
	}
	if !q.Push(5) {
		t.Fatal("腾出空间后 Push 应成功")
	}
	eqInts(t, "绕环后 Values", q.Values(), []int{2, 3, 4, 5})
	if v, ok := q.Peek(); !ok || v != 2 {
		t.Fatalf("Peek = %d,%v", v, ok)
	}
}

// 长时间「一进一出」压测绕环逻辑的 head/count 取模正确性。
func TestRingLongRun(t *testing.T) {
	const capN = 5
	q := xqueue.NewRing[int](capN)
	want := 0
	for i := 0; i < 5000; i++ {
		if !q.Push(i) {
			t.Fatalf("第 %d 次 Push 失败 (Len=%d)", i, q.Len())
		}
		if q.Len() == capN {
			got, ok := q.Pop()
			if !ok || got != want {
				t.Fatalf("got %d,%v want %d", got, ok, want)
			}
			want++
		}
	}
	// 稳态：每轮「满即弹」，循环结束时队列长度恒为 capN-1
	if q.Len() != capN-1 {
		t.Fatalf("Len = %d, want %d", q.Len(), capN-1)
	}
	// 剩余元素必须紧接 want 连续递增（任何绕环错位都会在这里暴露）
	total := want
	for i := 0; i < capN-1; i++ {
		got, ok := q.Pop()
		if !ok || got != want {
			t.Fatalf("剩余 got %d,%v want %d", got, ok, want)
		}
		want++
		total++
	}
	// 所有压入的值都应被弹出过一次
	if total != 5000 {
		t.Fatalf("总弹出数 = %d, want 5000", total)
	}
	if !q.IsEmpty() {
		t.Fatal("应已空")
	}
}

func TestRingMisc(t *testing.T) {
	q := xqueue.NewRing[string](2)
	q.Push("a")
	q.Push("b")

	var seen []string
	q.ForEach(func(v string) { seen = append(seen, v) })
	if len(seen) != 2 || seen[0] != "a" || seen[1] != "b" {
		t.Fatalf("ForEach = %v", seen)
	}

	q.Clear()
	if !q.IsEmpty() || q.Values() != nil {
		t.Fatal("Clear 后应为空")
	}
	if v, ok := q.Pop(); ok || v != "" {
		t.Fatalf("空 Pop = %q,%v", v, ok)
	}
	if v, ok := q.Peek(); ok || v != "" {
		t.Fatalf("空 Peek = %q,%v", v, ok)
	}
	q.ForEach(func(string) { t.Fatal("空队列不应回调") })

	// capacity <= 0 归一化为 1
	z := xqueue.NewRing[int](0)
	if z.Cap() != 1 {
		t.Fatalf("Cap = %d, want 1", z.Cap())
	}
	if !z.Push(1) || z.Push(2) {
		t.Fatal("容量 1 的队列行为错误")
	}

	// nil 接收者
	var n *xqueue.Ring[int]
	if n.Push(1) {
		t.Fatal("nil Push 应为 false")
	}
	if v, ok := n.Pop(); ok || v != 0 {
		t.Fatalf("nil Pop = %d,%v", v, ok)
	}
	if v, ok := n.Peek(); ok || v != 0 {
		t.Fatalf("nil Peek = %d,%v", v, ok)
	}
	if n.Len() != 0 || n.Cap() != 0 || !n.IsEmpty() || !n.IsFull() || n.Values() != nil {
		t.Fatal("nil 零值方法错误")
	}
	n.Clear()
	n.ForEach(func(int) { t.Fatal("nil 不应回调") })
}

// ---------------------------------------------------------------------------
// Bounded
// ---------------------------------------------------------------------------

func TestBoundedPolicies(t *testing.T) {
	t.Run("DropNewest", func(t *testing.T) {
		q := xqueue.NewBounded[int](3, xcontainer.DropNewest)
		for i := 1; i <= 5; i++ {
			if err := q.Push(i); err != nil {
				t.Fatalf("Push: %v", err)
			}
		}
		eqInts(t, "Values", q.Drain(), []int{1, 2, 3})
		if q.Dropped() != 2 {
			t.Fatalf("Dropped = %d, want 2", q.Dropped())
		}
	})

	t.Run("DropOldest", func(t *testing.T) {
		q := xqueue.NewBounded[int](3, xcontainer.DropOldest)
		for i := 1; i <= 5; i++ {
			if err := q.Push(i); err != nil {
				t.Fatalf("Push: %v", err)
			}
		}
		eqInts(t, "Values", q.Drain(), []int{3, 4, 5})
		if q.Dropped() != 2 {
			t.Fatalf("Dropped = %d, want 2", q.Dropped())
		}
	})

	t.Run("Fail", func(t *testing.T) {
		q := xqueue.NewBounded[int](2, xcontainer.Fail)
		q.Push(1)
		q.Push(2)
		err := q.Push(3)
		if !errors.Is(err, xcontainer.ErrFull) {
			t.Fatalf("Push = %v, want ErrFull", err)
		}
		if xerr.CodeOf(err) != xerr.CodeResourceExhausted {
			t.Fatalf("错误码 = %v", xerr.CodeOf(err))
		}
		eqInts(t, "失败不影响已入队", q.Drain(), []int{1, 2})
	})

	t.Run("Block 非阻塞 Push 返回 ErrWouldBlock", func(t *testing.T) {
		q := xqueue.NewBounded[int](2, xcontainer.Block)
		q.Push(1)
		q.Push(2)
		err := q.Push(3)
		if !errors.Is(err, xcontainer.ErrWouldBlock) {
			t.Fatalf("Push = %v, want ErrWouldBlock", err)
		}
	})

	t.Run("非法策略归一化 + capacity<=0", func(t *testing.T) {
		q := xqueue.NewBounded[int](0, xcontainer.OverflowPolicy(-7))
		if q.Cap() != 1 {
			t.Fatalf("Cap = %d, want 1", q.Cap())
		}
		if q.Policy() != xcontainer.DropNewest {
			t.Fatalf("Policy = %v", q.Policy())
		}
	})
}

func TestBoundedCloseSemantics(t *testing.T) {
	q := xqueue.NewBounded[int](4, xcontainer.Block)
	if err := q.Push(1); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := q.Push(2); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if q.IsClosed() {
		t.Fatal("不应已关闭")
	}

	q.Close()
	if !q.IsClosed() {
		t.Fatal("应已关闭")
	}
	q.Close() // 重复关闭必须安全

	// 关闭后 Push 返回 ErrClosed
	if err := q.Push(3); !errors.Is(err, xcontainer.ErrClosed) {
		t.Fatalf("关闭后 Push = %v, want ErrClosed", err)
	}
	if err := q.PushCtx(context.Background(), 3); !errors.Is(err, xcontainer.ErrClosed) {
		t.Fatalf("关闭后 PushCtx = %v, want ErrClosed", err)
	}

	// 已入队的元素仍可被完整消费（关键：Close 不丢数据）
	eqInts(t, "关闭后仍可排空", q.Drain(), []int{1, 2})

	// 排空后 Pop = false；PopCtx 返回 ErrClosed
	if v, ok := q.Pop(); ok || v != 0 {
		t.Fatalf("排空后 Pop = %d,%v", v, ok)
	}
	v, ok, err := q.PopCtx(context.Background())
	if ok || v != 0 || !errors.Is(err, xcontainer.ErrClosed) {
		t.Fatalf("排空后 PopCtx = %d,%v,%v", v, ok, err)
	}
}

// 关键并发回归：多生产者 + 关闭，绝不能出现 "send on closed channel" panic。
func TestBoundedCloseUnderConcurrentProducers(t *testing.T) {
	for round := 0; round < 50; round++ {
		q := xqueue.NewBounded[int](8, xcontainer.DropOldest)
		var wg sync.WaitGroup
		for p := 0; p < 8; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					// 关闭后返回 ErrClosed 是预期行为，其它错误才是问题
					if err := q.Push(p*200 + i); err != nil && !errors.Is(err, xcontainer.ErrClosed) {
						t.Errorf("Push: %v", err)
						return
					}
				}
			}(p)
		}
		// 与生产者并发关闭
		//
		// ⚠️ 注意：Go 1.21 的 for 循环变量是「每循环一份」而非「每轮一份」
		// （1.22 才改变语义），因此必须把 round 作为参数传入闭包，
		// 否则 goroutine 读 round 会与主 goroutine 的 round++ 构成数据竞争。
		delay := time.Duration(round%3) * time.Millisecond
		go func(d time.Duration) {
			time.Sleep(d)
			q.Close()
		}(delay)
		wg.Wait()
		q.Close()
	}
}

func TestBoundedNoLossWithBlockPolicy(t *testing.T) {
	const producers, perProducer, capacity = 8, 300, 16
	total := producers * perProducer

	q := xqueue.NewBounded[int](capacity, xcontainer.Block)
	ctx := context.Background()

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		base := p * perProducer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				if err := q.PushCtx(ctx, base+i); err != nil {
					t.Errorf("PushCtx: %v", err)
					return
				}
			}
		}()
	}

	got := make([]int, 0, total)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for len(got) < total {
			v, ok, err := q.PopCtx(ctx)
			if err != nil {
				t.Errorf("PopCtx: %v", err)
				return
			}
			if !ok {
				return
			}
			got = append(got, v)
		}
	}()

	wg.Wait()
	<-done

	if len(got) != total {
		t.Fatalf("收到 %d 个元素, want %d", len(got), total)
	}
	seen := make([]bool, total)
	for _, v := range got {
		if v < 0 || v >= total {
			t.Fatalf("越界值 %d", v)
		}
		if seen[v] {
			t.Fatalf("值 %d 重复", v)
		}
		seen[v] = true
	}
	if q.Len() != 0 {
		t.Fatalf("消费完后队列应空, Len = %d", q.Len())
	}
	if q.Dropped() != 0 {
		t.Fatalf("Block 策略不应丢数据, Dropped = %d", q.Dropped())
	}
}

func TestBoundedPushCtxCancel(t *testing.T) {
	q := xqueue.NewBounded[int](1, xcontainer.Block)
	if err := q.PushCtx(context.Background(), 1); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}

	// 队列已满：PushCtx 应被 ctx 取消
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := q.PushCtx(ctx, 2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PushCtx = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("PushCtx 过早返回: %v", elapsed)
	}

	// 主动取消
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := q.PushCtx(ctx2, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("PushCtx = %v, want Canceled", err)
	}

	// ctx 为 nil 视为 Background：队列有空间时应成功
	q2 := xqueue.NewBounded[int](2, xcontainer.Block)
	if err := q2.PushCtx(nil, 1); err != nil {
		t.Fatalf("PushCtx(nil) = %v", err)
	}
}

func TestBoundedPopCtxBlockingAndCancel(t *testing.T) {
	q := xqueue.NewBounded[int](4, xcontainer.Block)

	// 阻塞等待生产者
	got := make(chan int, 1)
	go func() {
		v, ok, err := q.PopCtx(context.Background())
		if err != nil || !ok {
			t.Errorf("PopCtx = %d,%v,%v", v, ok, err)
			return
		}
		got <- v
	}()

	time.Sleep(30 * time.Millisecond) // 让消费者先阻塞
	if err := q.PushCtx(context.Background(), 42); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	select {
	case v := <-got:
		if v != 42 {
			t.Fatalf("PopCtx = %d, want 42", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PopCtx 未被唤醒")
	}

	// 空队列 + ctx 超时
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, ok, err := q.PopCtx(ctx)
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PopCtx = %v,%v, want DeadlineExceeded", ok, err)
	}

	// nil ctx 也应安全（此处队列非空，立即返回）
	q.Push(7)
	v, ok, err := q.PopCtx(nil)
	if !ok || err != nil || v != 7 {
		t.Fatalf("PopCtx(nil) = %d,%v,%v", v, ok, err)
	}

	// nil 接收者
	var n *xqueue.Bounded[int]
	if _, ok, err := n.PopCtx(context.Background()); ok || !errors.Is(err, xcontainer.ErrClosed) {
		t.Fatalf("nil PopCtx = %v,%v", ok, err)
	}
	if err := n.Push(1); err != nil {
		t.Fatalf("nil Push = %v", err)
	}
	if _, ok := n.Pop(); ok {
		t.Fatal("nil Pop 应为 false")
	}
	if n.Len() != 0 || n.Cap() != 0 || !n.IsEmpty() || !n.IsFull() || n.Dropped() != 0 || !n.IsClosed() {
		t.Fatal("nil 零值方法错误")
	}
	n.Close()
	if !n.IsClosed() {
		t.Fatal("nil Close 后应视为已关闭")
	}
	if n.Policy() != xcontainer.DropNewest {
		t.Fatal("nil Policy 应为 DropNewest")
	}
	if n.Drain() != nil {
		t.Fatal("nil Drain 应为 nil")
	}
}

// 关闭后 PopCtx 应返回 ErrClosed 而不是永久阻塞。
func TestBoundedPopCtxUnblocksOnClose(t *testing.T) {
	q := xqueue.NewBounded[int](4, xcontainer.Block)
	errc := make(chan error, 1)
	go func() {
		_, _, err := q.PopCtx(context.Background())
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	q.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, xcontainer.ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未唤醒阻塞中的 PopCtx")
	}
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

func TestSyncQueueBasic(t *testing.T) {
	q := xqueue.NewSync[int]()
	q.Push(1)
	q.Push(2)
	if q.Len() != 2 {
		t.Fatalf("Len = %d", q.Len())
	}
	if v, ok := q.Peek(); !ok || v != 1 {
		t.Fatalf("Peek = %d,%v", v, ok)
	}
	eqInts(t, "Values", q.Values(), []int{1, 2})
	drained := q.Drain()
	eqInts(t, "Drain", drained, []int{1, 2})
	if !q.IsEmpty() {
		t.Fatal("Drain 后应为空")
	}
	if v, ok := q.Pop(); ok || v != 0 {
		t.Fatalf("空 Pop = %d,%v", v, ok)
	}
	q.Push(3)
	q.Clear()
	if !q.IsEmpty() || q.Values() != nil {
		t.Fatal("Clear 后应为空")
	}

	// nil 接收者
	var n *xqueue.Sync[int]
	if n.Push(1) {
		t.Fatal("nil Push 应为 false")
	}
	if v, ok := n.Pop(); ok || v != 0 {
		t.Fatalf("nil Pop = %d,%v", v, ok)
	}
	if v, ok := n.Peek(); ok || v != 0 {
		t.Fatalf("nil Peek = %d,%v", v, ok)
	}
	if n.Len() != 0 || !n.IsEmpty() || n.Values() != nil || n.Drain() != nil {
		t.Fatal("nil 零值方法错误")
	}
	n.Clear()
}

func TestSyncQueueNoLoss(t *testing.T) {
	const producers, perProducer = 8, 500
	total := producers * perProducer

	q := xqueue.NewSync[int]()
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		base := p * perProducer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				q.Push(base + i)
			}
		}()
	}
	wg.Wait()

	if q.Len() != total {
		t.Fatalf("Len = %d, want %d", q.Len(), total)
	}

	var mu sync.Mutex
	seen := make([]int, total)
	var wg2 sync.WaitGroup
	for c := 0; c < 8; c++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			for {
				v, ok := q.Pop()
				if !ok {
					return
				}
				mu.Lock()
				seen[v]++
				mu.Unlock()
			}
		}()
	}
	wg2.Wait()

	for v, n := range seen {
		if n != 1 {
			t.Fatalf("值 %d 出现 %d 次", v, n)
		}
	}
}

// ---------------------------------------------------------------------------
// 真实场景：有界队列 + 优雅关闭（关闭后把缓冲内容落盘）
// ---------------------------------------------------------------------------

func TestRealisticGracefulShutdown(t *testing.T) {
	q := xqueue.NewBounded[int](32, xcontainer.Block)
	ctx := context.Background()

	var produced []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := q.PushCtx(ctx, p*100+i); err != nil {
					return
				}
			}
		}(p)
	}

	// 消费者
	var consumed []int
	var cwg sync.WaitGroup
	cwg.Add(1)
	go func() {
		defer cwg.Done()
		for {
			v, ok, err := q.PopCtx(ctx)
			if err != nil {
				return // ErrClosed
			}
			if !ok {
				return
			}
			consumed = append(consumed, v)
		}
	}()

	wg.Wait()
	q.Close() // 生产者已完成，关闭队列让消费者退出
	cwg.Wait()

	// 关闭后 Drain 应拿到消费者退出瞬间可能残留的元素
	rest := q.Drain()
	mu.Lock()
	produced = append(produced, consumed...)
	produced = append(produced, rest...)
	mu.Unlock()

	if len(produced) != 400 {
		t.Fatalf("总处理量 = %d, want 400 (consumer=%d drain=%d)", len(produced), len(consumed), len(rest))
	}
	seen := make([]bool, 400)
	for _, v := range produced {
		if seen[v] {
			t.Fatalf("值 %d 重复", v)
		}
		seen[v] = true
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkSliceQueuePushPop(b *testing.B) {
	q := xqueue.New[int](1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Push(i)
		q.Pop()
	}
}

func BenchmarkRingPushPop(b *testing.B) {
	q := xqueue.NewRing[int](1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Push(i)
		q.Pop()
	}
}

func BenchmarkBoundedDropOldest(b *testing.B) {
	q := xqueue.NewBounded[int](64, xcontainer.DropOldest)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = q.Push(i)
	}
}

func BenchmarkSyncQueuePushPop(b *testing.B) {
	q := xqueue.NewSync[int]()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Push(i)
		q.Pop()
	}
}
