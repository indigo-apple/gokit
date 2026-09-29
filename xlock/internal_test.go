// 本文件是 xlock 的**包内**测试，用于覆盖不适合从外部观察的内核细节：
// entry 的所有权交接、等待者摘除、goroutine id 解析的防御分支等。
package xlock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// entry 内核
// ---------------------------------------------------------------------------

func TestEntryWriteLockExclusive(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()

	waited, err := e.lockWrite(ctx)
	if err != nil || waited {
		t.Fatalf("首次获取: waited=%v err=%v", waited, err)
	}
	// 已持有 → TryLock 必须失败
	if e.tryLockWrite() {
		t.Fatal("已持有时 tryLockWrite 应失败")
	}
	if e.tryLockRead() {
		t.Fatal("写锁持有时 tryLockRead 应失败")
	}
	e.unlockWrite()

	if !e.tryLockWrite() {
		t.Fatal("释放后应可获取")
	}
	e.unlockWrite()
}

func TestEntryReadLockSharedAndExclusiveVsWrite(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		waited, err := e.lockRead(ctx)
		if err != nil || waited {
			t.Fatalf("第 %d 个读者: waited=%v err=%v", i, waited, err)
		}
	}
	if w, r, _, _ := e.snapshot(); w || r != 3 {
		t.Fatalf("snapshot = (w=%v r=%d), want (false, 3)", w, r)
	}
	if e.tryLockWrite() {
		t.Fatal("有读者时 tryLockWrite 应失败")
	}

	e.unlockRead()
	e.unlockRead()
	if e.tryLockWrite() {
		t.Fatal("仍有 1 个读者时 tryLockWrite 应失败")
	}
	e.unlockRead()
	if !e.tryLockWrite() {
		t.Fatal("读者清空后 tryLockWrite 应成功")
	}
	e.unlockWrite()
}

// 写者按 FIFO 队列顺序交接所有权（防止写者饥饿，也保证可预期的公平性）。
func TestEntryFIFOHandoff(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()

	// 主 goroutine 先持有
	if _, err := e.lockWrite(ctx); err != nil {
		t.Fatalf("lockWrite: %v", err)
	}

	const n = 4
	order := make(chan int, n)
	var wg sync.WaitGroup

	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := e.lockWrite(ctx); err != nil {
				t.Errorf("写者 %d: %v", i, err)
				return
			}
			order <- i
			e.unlockWrite()
		}(i)

		// 逐个启动并确认已入队，从而让「队列顺序」确定可预期
		if !waitCond(func() bool {
			_, _, waiting, _ := e.snapshot()
			return waiting == i
		}, 2*time.Second) {
			t.Fatalf("写者 %d 未进入等待队列", i)
		}
	}

	// 释放首锁 → 依次交接
	e.unlockWrite()
	wg.Wait()
	close(order)

	var got []int
	for v := range order {
		got = append(got, v)
	}
	if len(got) != n {
		t.Fatalf("获取顺序长度 = %d, want %d (%v)", len(got), n, got)
	}
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("FIFO 顺序被破坏: %v", got)
		}
	}
	if w, r, wq, rq := e.snapshot(); w || r != 0 || wq != 0 || rq != 0 {
		t.Fatalf("结束后状态不干净: (w=%v r=%d wq=%d rq=%d)", w, r, wq, rq)
	}
	if !e.tryLockWrite() {
		t.Fatal("全部释放后锁应可用")
	}
	e.unlockWrite()
}

// 写者优先：写者排队期间，读者无法插队。
func TestEntryWriterPreferenceBlocksNewReaders(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()

	if _, err := e.lockRead(ctx); err != nil {
		t.Fatalf("lockRead: %v", err)
	}

	// 启动一个写者（会排队）
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		if _, err := e.lockWrite(ctx); err != nil {
			t.Errorf("lockWrite: %v", err)
			return
		}
		e.unlockWrite()
	}()

	if !waitCond(func() bool {
		_, _, waiting, _ := e.snapshot()
		return waiting == 1
	}, 2*time.Second) {
		t.Fatal("写者未入队")
	}

	// 写者在排队 → 新读者必须失败
	if e.tryLockRead() {
		t.Fatal("有写者排队时 tryLockRead 应失败（否则写者会被饿死）")
	}
	// 新读者以 ctx 获取也必须超时
	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := e.lockRead(cctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("新读者错误 = %v, want DeadlineExceeded", err)
	}

	e.unlockRead()
	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("最后一个读者释放后写者未被唤醒")
	}
}

// 直接验证「取消与唤醒竞争」时不会泄漏锁：
// 等待者已被交接（从队列摘除 -> 必然失败），必须主动归还所有权。
func TestEntryCancelAfterHandoffMustRelease(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()

	if _, err := e.lockWrite(ctx); err != nil {
		t.Fatalf("lockWrite: %v", err)
	}

	// 手工模拟「一个已经登记的等待者」
	ch := make(chan struct{})
	e.mu.Lock()
	e.wqueue = append(e.wqueue, ch)
	e.mu.Unlock()

	// 释放 → 所有权交接给该等待者（ch 被 close，writing 仍为 true）
	e.unlockWrite()

	// 此时等待者若 ctx 已取消，它尝试摘除自己必然失败
	e.mu.Lock()
	removed := e.removeWaiterLocked(ch)
	e.mu.Unlock()
	if removed {
		t.Fatal("已被交接的等待者不应还能从队列摘除")
	}

	// 关键：这个分支必须归还锁，否则锁永久泄漏
	e.unlockWrite()

	if !e.tryLockWrite() {
		t.Fatal("锁被泄漏：取消与唤醒竞争后未归还所有权")
	}
	e.unlockWrite()
}

func TestEntryRemoveWaiter(t *testing.T) {
	e := newEntry(nil)
	a, b := make(chan struct{}), make(chan struct{})
	e.mu.Lock()
	e.wqueue = append(e.wqueue, a, b)
	e.mu.Unlock()

	e.mu.Lock()
	if !e.removeWaiterLocked(a) {
		t.Fatal("摘除 a 应成功")
	}
	if e.removeWaiterLocked(a) {
		t.Fatal("重复摘除 a 应失败")
	}
	e.mu.Unlock()

	if _, _, waiting, _ := e.snapshot(); waiting != 1 {
		t.Fatalf("剩余等待者 = %d, want 1", waiting)
	}
	e.mu.Lock()
	if !e.removeWaiterLocked(b) {
		t.Fatal("摘除 b 应成功")
	}
	e.mu.Unlock()
	if _, _, waiting, _ := e.snapshot(); waiting != 0 {
		t.Fatalf("剩余等待者 = %d, want 0", waiting)
	}
}

// 写锁取消：未被交接时应能干净地摘除自己，且后续可正常获取。
func TestEntryWriteCtxCancelCleansQueue(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()
	if _, err := e.lockWrite(ctx); err != nil {
		t.Fatalf("lockWrite: %v", err)
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	waited, err := e.lockWrite(cctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, want DeadlineExceeded", err)
	}
	if !waited {
		t.Fatal("取消路径应标记为 waited")
	}
	if _, _, waiting, _ := e.snapshot(); waiting != 0 {
		t.Fatalf("取消后队列未清理: %d", waiting)
	}
	e.unlockWrite()
}

// 读者取消也要清理等待计数与队列（读者不进 wqueue，但 rwait 必须归零）。
func TestEntryReadCtxCancelCleansWaiters(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()
	if _, err := e.lockWrite(ctx); err != nil {
		t.Fatalf("lockWrite: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			if _, err := e.lockRead(cctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("错误 = %v", err)
			}
		}()
	}
	wg.Wait()

	if _, _, _, rwait := e.snapshot(); rwait != 0 {
		t.Fatalf("取消后等待读者数 = %d, want 0", rwait)
	}
	e.unlockWrite()

	// 全部取消后，锁应能立刻以读锁获取
	if !e.tryLockRead() {
		t.Fatal("读者取消后应可获取读锁")
	}
	e.unlockRead()
}

// 并发压力：混合读写与取消，结束后状态必须干净（配 -race 验证）。
func TestEntryConcurrentStress(t *testing.T) {
	e := newEntry(nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := 0; g < 24; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				switch (g + i) % 5 {
				case 0, 1:
					if _, err := e.lockWrite(ctx); err == nil {
						e.unlockWrite()
					}
				case 2, 3:
					if _, err := e.lockRead(ctx); err == nil {
						e.unlockRead()
					}
				default:
					cctx, cancel := context.WithTimeout(ctx, time.Millisecond)
					if _, err := e.lockWrite(cctx); err == nil {
						e.unlockWrite()
					}
					cancel()
				}
			}
		}(g)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("并发压力测试超时（疑似死锁）")
	}

	if w, r, wq, rq := e.snapshot(); w || r != 0 || wq != 0 || rq != 0 {
		t.Fatalf("结束后状态不干净: (w=%v r=%d wq=%d rq=%d)", w, r, wq, rq)
	}
	if !e.tryLockWrite() {
		t.Fatal("压力测试后锁应可用")
	}
}

// waitCond 是本文件内的小工具（避免依赖 xlock_test 包的 helper）。
func waitCond(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Microsecond)
	}
	return cond()
}

// ---------------------------------------------------------------------------
// goroutine id 解析
// ---------------------------------------------------------------------------

func TestParseGoroutineID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want uint64
		ok   bool
	}{
		{"标准格式", "goroutine 1 [running]:\nmain.main()", 1, true},
		{"大 id", "goroutine 1234567 [chan receive]:", 1234567, true},
		{"零 id", "goroutine 0 [idle]:", 0, true}, // 0 是合法解析结果
		{"空前缀", "", 0, false},
		{"前缀不完整", "goroutine", 0, false},
		{"前缀不匹配", "g0routine 1 [running]:", 0, false},
		{"前缀后无数字", "goroutine abc [running]:", 0, false},
		{"前缀后直接中括号", "goroutine  [running]:", 0, false},
		{"乱码", "\x00\x01\x02", 0, false},
		{"只有空格", "         ", 0, false},
		{"id 溢出阈值", "goroutine 9999999999999 [running]:", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseGoroutineID([]byte(c.in))
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (in=%q)", ok, c.ok, c.in)
			}
			if ok && got != c.want {
				t.Fatalf("id = %d, want %d", got, c.want)
			}
		})
	}
}

func TestU64ToA(t *testing.T) {
	cases := map[uint64]string{
		0: "0", 1: "1", 9: "9", 10: "10", 123: "123",
		18446744073709551615: "18446744073709551615",
	}
	for in, want := range cases {
		if got := u64toa(in); got != want {
			t.Fatalf("u64toa(%d) = %q, want %q", in, got, want)
		}
	}
}

// 无法取得 goroutine id 时必须返回明确错误（而不是死锁或静默降级）。
func TestReentrantNoGoroutineID(t *testing.T) {
	orig := goroutineIDFn
	defer func() { goroutineIDFn = orig }()
	goroutineIDFn = func() (uint64, bool) { return 0, false }

	if id, ok := GoroutineID(); ok || id != 0 {
		t.Fatalf("GoroutineID = (%d,%v), want (0,false)", id, ok)
	}
	if got := Owner(); got != "" {
		t.Fatalf("Owner = %q, want 空串", got)
	}

	l := NewReentrant()
	if _, err := l.Lock("k"); !errors.Is(err, ErrNoGoroutineID) {
		t.Fatalf("Lock 错误 = %v, want ErrNoGoroutineID", err)
	}
	if _, ok := l.TryLock("k"); ok {
		t.Fatal("TryLock 应返回 false")
	}
	if _, err := l.LockCtx(context.Background(), "k"); !errors.Is(err, ErrNoGoroutineID) {
		t.Fatalf("LockCtx 错误 = %v", err)
	}
	if _, err := l.Lock("k"); err == nil {
		t.Fatal("WaitTimeout 路径也应报错")
	}
	// 显式 owner 不受影响
	un, err := l.LockFor("explicit", "k")
	if err != nil {
		t.Fatalf("LockFor 不应受影响: %v", err)
	}
	un()
}

// ---------------------------------------------------------------------------
// 其它内部工具
// ---------------------------------------------------------------------------

func TestDedupeSorted(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{}, nil},
		{[]string{"b", "a"}, []string{"a", "b"}},
		{[]string{"a", "a", "a"}, []string{"a"}},
		{[]string{"c", "a", "b", "a"}, []string{"a", "b", "c"}},
		{[]string{""}, []string{""}},                          // 空串是合法 key
		{[]string{"x", "", "x", "a"}, []string{"", "a", "x"}}, // 空串排最前
	}
	for _, c := range cases {
		got := dedupeSorted(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("dedupeSorted(%v) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("dedupeSorted(%v) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

func TestCoreWaitCtx(t *testing.T) {
	// 未配置超时：返回 Background 且 cancel 是安全空操作
	c := core{}
	ctx, cancel := c.waitCtx()
	if ctx == nil || ctx.Done() != nil {
		t.Fatal("未配置超时应返回 Background（Done 为 nil）")
	}
	cancel()

	// 配置了超时
	c2 := core{cfg: config{waitTimeout: 10 * time.Millisecond}}
	ctx2, cancel2 := c2.waitCtx()
	defer cancel2()
	if ctx2.Done() == nil {
		t.Fatal("配置超时后 Done 不应为 nil")
	}
	select {
	case <-ctx2.Done():
	case <-time.After(time.Second):
		t.Fatal("超时未生效")
	}
}

func TestStatsObserveHold(t *testing.T) {
	var s stats
	s.observeHold(5 * time.Millisecond)
	s.observeHold(9 * time.Millisecond)
	s.observeHold(1 * time.Millisecond)

	snap := s.snapshot(3)
	if snap.Keys != 3 {
		t.Fatalf("Keys = %d", snap.Keys)
	}
	if snap.HoldTotal != 15*time.Millisecond {
		t.Fatalf("HoldTotal = %v, want 15ms", snap.HoldTotal)
	}
	if snap.HoldMax != 9*time.Millisecond {
		t.Fatalf("HoldMax = %v, want 9ms", snap.HoldMax)
	}
}

func TestCoreAcquireNilCtx(t *testing.T) {
	// 传 nil ctx 必须被安全地视为 Background
	l := NewKeyed()
	un, err := l.acquire(l, nil, "k", true, false)
	if err != nil {
		t.Fatalf("acquire(nil ctx): %v", err)
	}
	un()
	if l.ActiveKeys() != 0 {
		t.Fatalf("残留条目: %d", l.ActiveKeys())
	}
}

func TestEntryNilStats(t *testing.T) {
	// st 为 nil 时 entry 必须仍能正常工作（不落统计）
	e := newEntry(nil)
	ctx := context.Background()
	if _, err := e.lockWrite(ctx); err != nil {
		t.Fatalf("lockWrite: %v", err)
	}
	e.unlockWrite()
	if !e.tryLockWrite() {
		t.Fatal("tryLockWrite 应成功")
	}
	e.unlockWrite()
}
