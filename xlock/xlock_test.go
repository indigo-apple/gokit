package xlock_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xlock"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

func mustLock(t *testing.T, l xlock.Locker, key string) xlock.UnlockFunc {
	t.Helper()
	un, err := l.Lock(key)
	if err != nil {
		t.Fatalf("Lock(%q) 出错: %v", key, err)
	}
	if un == nil {
		t.Fatalf("Lock(%q) 返回了 nil 释放函数", key)
	}
	return un
}

// waitUntil 轮询等待条件成立（避免用固定时长 sleep 造成偶发失败）。
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

// newLockers 返回全部满足 Locker 接口的实现，用于「同一套契约测试全部实现」。
func newLockers() []struct {
	name string
	impl func() xlock.Locker
} {
	return []struct {
		name string
		impl func() xlock.Locker
	}{
		{"Keyed", func() xlock.Locker { return xlock.NewKeyed() }},
		{"Sharded", func() xlock.Locker { return xlock.NewSharded(8) }},
	}
}

// ---------------------------------------------------------------------------
// 哈希与分片
// ---------------------------------------------------------------------------

func TestHashKeyDeterministic(t *testing.T) {
	// 必须可复现（刻意不用 hash/maphash，后者每次进程启动种子不同）
	for i := 0; i < 3; i++ {
		if xlock.HashKey("order:123") != xlock.HashKey("order:123") {
			t.Fatal("HashKey 必须稳定")
		}
	}
	if xlock.HashKey("a") == xlock.HashKey("b") {
		t.Fatal("不同 key 不应同哈希")
	}
	if xlock.HashKey("") == 0 {
		t.Fatal("空 key 也应有非零哈希")
	}
}

func TestShardOf(t *testing.T) {
	for _, shards := range []int{1, 2, 8, 32} {
		for _, k := range []string{"", "a", "order:1", "order:2"} {
			s := xlock.ShardOf(k, shards)
			if s < 0 || s >= shards {
				t.Fatalf("ShardOf(%q, %d) = %d 越界", k, shards, s)
			}
		}
	}
	// shards<=0 视为 1
	if got := xlock.ShardOf("x", 0); got != 0 {
		t.Fatalf("ShardOf(x, 0) = %d, want 0", got)
	}
	if got := xlock.ShardOf("x", -5); got != 0 {
		t.Fatalf("ShardOf(x, -5) = %d, want 0", got)
	}
	// 稳定性
	if xlock.ShardOf("order:123", 32) != xlock.ShardOf("order:123", 32) {
		t.Fatal("ShardOf 必须稳定")
	}
}

// ---------------------------------------------------------------------------
// 契约测试：所有 Locker 实现共享的行为
// ---------------------------------------------------------------------------

// 核心不变式：同 key 互斥。用极小的临界区 + 非原子计数暴露任何互斥失效。
func TestLockerMutualExclusion(t *testing.T) {
	for _, tc := range newLockers() {
		t.Run(tc.name, func(t *testing.T) {
			const goroutines, perG = 24, 200
			l := tc.impl()

			var counter int // 故意用非原子变量：只要能观察到丢失更新就说明互斥失效
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < perG; i++ {
						un, err := l.Lock("counter")
						if err != nil {
							t.Errorf("Lock 出错: %v", err)
							return
						}
						counter++ // 临界区
						un()
					}
				}()
			}
			wg.Wait()

			if counter != goroutines*perG {
				t.Fatalf("计数器 = %d, want %d（互斥失效或丢失更新）", counter, goroutines*perG)
			}
		})
	}
}

// 不同 key 互不阻塞。
func TestLockerDifferentKeysAreIndependent(t *testing.T) {
	for _, tc := range newLockers() {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.impl()
			unA := mustLock(t, l, "A")
			defer unA()

			unB, ok := l.TryLock("B")
			if !ok {
				t.Fatal("持有 A 时 B 应可获取")
			}
			unB()

			if _, ok := l.TryLock("A"); ok {
				t.Fatal("持有 A 时再次 TryLock(A) 不应成功")
			}
		})
	}
}

// 读锁可共享，且与写锁互斥。
func TestLockerReadLockShared(t *testing.T) {
	for _, tc := range newLockers() {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.impl()

			un1, err := l.RLock("k")
			if err != nil {
				t.Fatalf("RLock: %v", err)
			}
			un2, err := l.RLock("k")
			if err != nil {
				t.Fatalf("RLock: %v", err)
			}
			un3, err := l.RLock("k")
			if err != nil {
				t.Fatalf("RLock: %v", err)
			}

			// 有读者时写锁必须失败
			if _, ok := l.TryLock("k"); ok {
				t.Fatal("有活跃读者时写锁不应成功")
			}

			un1()
			un2()
			// 仍剩一个读者 → 写锁仍失败
			if _, ok := l.TryLock("k"); ok {
				t.Fatal("仍有读者时写锁不应成功")
			}
			un3()

			// RLockCtx 成功路径
			unC, err := l.RLockCtx(context.Background(), "k")
			if err != nil {
				t.Fatalf("RLockCtx: %v", err)
			}
			unC()
			// RLockCtx 已取消
			cctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := l.RLockCtx(cctx, "k"); !errors.Is(err, context.Canceled) {
				t.Fatalf("RLockCtx(已取消) = %v", err)
			}

			// 读者清空 → 写锁可获取
			unW, ok := l.TryLock("k")
			if !ok {
				t.Fatal("读者清空后写锁应可获取")
			}
			// 写锁持有期间读锁也失败
			if _, ok := l.TryRLock("k"); ok {
				t.Fatal("写锁持有期间读锁不应成功")
			}
			unW()
		})
	}
}

// 写者优先：有写者在排队时，新读者必须等待（防止写者饥饿）。
func TestLockerWriterPreference(t *testing.T) {
	for _, tc := range newLockers() {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.impl()

			unR, err := l.RLock("k")
			if err != nil {
				t.Fatalf("RLock: %v", err)
			}

			// 启动一个写者，它会排队等待
			got := make(chan struct{})
			go func() {
				un, err := l.Lock("k")
				if err != nil {
					t.Errorf("写者 Lock 出错: %v", err)
					return
				}
				close(got)
				un()
			}()

			// 等写者进入队列
			if !waitUntil(func() bool {
				_, _, waiting, _ := stateOf(l, "k")
				return waiting > 0
			}, time.Second) {
				t.Fatal("写者未进入等待队列")
			}

			// 写者排队时，新读者必须失败（写者优先）
			if _, ok := l.TryRLock("k"); ok {
				t.Fatal("有写者排队时新读者不应成功（会出现写者饥饿）")
			}

			unR()

			select {
			case <-got:
			case <-time.After(2 * time.Second):
				t.Fatal("读者释放后写者未被唤醒")
			}
		})
	}
}

// stateOf 取内部状态（ShardedLocker 没有 EntryState，用空值兜底）。
func stateOf(l xlock.Locker, key string) (writing bool, readers, waitingWriters, waitingReaders int) {
	type stateful interface {
		EntryState(string) (bool, int, int, int)
	}
	if s, ok := l.(stateful); ok {
		return s.EntryState(key)
	}
	return false, 0, 0, 0
}

// ctx 取消必须真正把自己从等待队列中摘除（这是相对 sync.Mutex 的核心优势）。
func TestLockerCtxCancelRemovesFromQueue(t *testing.T) {
	l := xlock.NewKeyed()
	un := mustLock(t, l, "k")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := l.LockCtx(ctx, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockCtx 错误 = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("过早返回: %v", elapsed)
	}

	// 关键断言：取消后等待队列已被清理，不会占着位置
	if _, _, waiting, _ := l.EntryState("k"); waiting != 0 {
		t.Fatalf("取消后仍有 %d 个等待写者（ctx 取消未清理队列）", waiting)
	}

	un()

	// 锁应立即可用
	un2, ok := l.TryLock("k")
	if !ok {
		t.Fatal("取消后锁应可正常获取")
	}
	un2()
}

// 主动取消（而非超时）
func TestLockerCtxCancelExplicit(t *testing.T) {
	l := xlock.NewKeyed()
	un := mustLock(t, l, "k")
	defer un()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.LockCtx(ctx, "k")
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("错误 = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 未被响应")
	}
	if _, _, waiting, _ := l.EntryState("k"); waiting != 0 {
		t.Fatalf("取消后队列未清理: %d", waiting)
	}
}

// 读锁取消
func TestLockerReadCtxCancel(t *testing.T) {
	l := xlock.NewKeyed()
	un := mustLock(t, l, "k") // 写锁持有 → 读者必须排队
	defer un()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := l.RLockCtx(ctx, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, want DeadlineExceeded", err)
	}
	if _, _, _, waitingReaders := l.EntryState("k"); waitingReaders != 0 {
		t.Fatalf("取消后仍有 %d 个等待读者", waitingReaders)
	}
}

// ---------------------------------------------------------------------------
// KeyedLocker 专项
// ---------------------------------------------------------------------------

// 引用计数：所有路径（含被取消的获取、失败的 TryLock）都必须归还条目。
func TestKeyedNoEntryLeak(t *testing.T) {
	l := xlock.NewKeyed()

	for i := 0; i < 1500; i++ {
		key := fmt.Sprintf("k:%d", i)

		u1 := mustLock(t, l, key)
		// 读锁必须用**另一个 key**：非重入锁在同 key 上重复获取（无论读写）都会阻塞
		rkey := key + ":r"
		u2, err := l.RLock(rkey)
		if err != nil {
			t.Fatalf("RLock %s: %v", rkey, err)
		}
		// 同 key 的 TryLock 必然失败，但也会走「取条目 → 归还」路径
		if _, ok := l.TryLock(key); ok {
			t.Fatalf("同 key TryLock 不应成功: %s", key)
		}

		// 带超时的获取同样要归还条目
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		if _, err := l.LockCtx(ctx, key); err == nil {
			t.Fatal("预期获取超时")
		}
		cancel()

		// 已取消的 ctx 必须快速失败（不能走「无人竞争」快速路径抢到锁）
		cctx, ccancel := context.WithCancel(context.Background())
		ccancel()
		if _, err := l.LockCtx(cctx, key); !errors.Is(err, context.Canceled) {
			t.Fatalf("已取消 ctx 错误 = %v, want context.Canceled", err)
		}

		u2()
		u1()
	}

	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("key 表泄漏: ActiveKeys = %d（应为 0）", n)
	}
	if snap := l.ActiveKeysSnapshot(); len(snap) != 0 {
		t.Fatalf("快照非空: %v", snap)
	}
}

// 反复申请/释放同一个 key，条目应始终只有一个。
func TestKeyedSameKeyReuse(t *testing.T) {
	l := xlock.NewKeyed()
	for i := 0; i < 500; i++ {
		un := mustLock(t, l, "hot")
		if n := l.ActiveKeys(); n != 1 {
			t.Fatalf("第 %d 轮 ActiveKeys = %d, want 1", i, n)
		}
		un()
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("ActiveKeys = %d, want 0", n)
	}
}

func TestKeyedMaxKeys(t *testing.T) {
	l := xlock.NewKeyed(xlock.WithMaxKeys(2))

	u1 := mustLock(t, l, "a")
	defer u1()
	u2 := mustLock(t, l, "b")
	defer u2()

	_, err := l.Lock("c")
	if !errors.Is(err, xlock.ErrTooManyKeys) {
		t.Fatalf("错误 = %v, want ErrTooManyKeys", err)
	}
	if xerr.CodeOf(err) != xerr.CodeResourceExhausted {
		t.Fatalf("错误码 = %v", xerr.CodeOf(err))
	}
	meta := xerr.MetaOf(err)
	if meta["key"] != "c" || meta["limit"] != 2 {
		t.Fatalf("元数据缺失: %v", meta)
	}

	// 已存在的 key 不受上限影响
	if _, ok := l.TryLock("a"); ok {
		t.Fatal("已被持有，不应成功")
	}
	u1()
	// 释放后腾出名额
	u3, ok := l.TryLock("c")
	if !ok {
		t.Fatal("腾出名额后应可获取 c")
	}
	u3()
}

func TestKeyedWaitTimeout(t *testing.T) {
	l := xlock.NewKeyed(xlock.WithWaitTimeout(60 * time.Millisecond))
	un := mustLock(t, l, "k")
	defer un()

	start := time.Now()
	_, err := l.Lock("k")
	if !errors.Is(err, xlock.ErrLockTimeout) {
		t.Fatalf("错误 = %v, want ErrLockTimeout", err)
	}
	if xerr.CodeOf(err) != xerr.CodeTimeout {
		t.Fatalf("错误码 = %v", xerr.CodeOf(err))
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("过早超时: %v", elapsed)
	}

	// RLock 同样受 WaitTimeout 约束
	if _, err := l.RLock("k"); !errors.Is(err, xlock.ErrLockTimeout) {
		t.Fatalf("RLock 错误 = %v, want ErrLockTimeout", err)
	}

	// 释放后条目应被回收（超时路径也要归还条目）
	un()
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("超时路径泄漏条目: %d", n)
	}
}

func TestKeyedUnlockIdempotent(t *testing.T) {
	l := xlock.NewKeyed()
	un := mustLock(t, l, "k")

	if err := un(); err != nil {
		t.Fatalf("首次释放返回错误: %v", err)
	}
	// 幂等：二次释放不 panic、不改变状态
	for i := 0; i < 3; i++ {
		if err := un(); err != nil {
			t.Fatalf("重复释放返回错误: %v", err)
		}
	}
	st := l.Stats()
	if st.InvalidUnlock != 3 {
		t.Fatalf("InvalidUnlock = %d, want 3", st.InvalidUnlock)
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("ActiveKeys = %d", n)
	}
	// 锁确实被释放了
	un2, ok := l.TryLock("k")
	if !ok {
		t.Fatal("锁未被释放")
	}
	un2()
}

func TestKeyedContentionCallback(t *testing.T) {
	type call struct {
		key    string
		waited time.Duration
	}
	var mu sync.Mutex
	var calls []call

	l := xlock.NewKeyed(xlock.WithOnContention(func(key string, waited time.Duration) {
		mu.Lock()
		calls = append(calls, call{key, waited})
		mu.Unlock()
	}))

	un := mustLock(t, l, "k")

	// 另一个 goroutine 等待并最终拿到锁
	done := make(chan struct{})
	go func() {
		defer close(done)
		u, err := l.Lock("k")
		if err != nil {
			t.Errorf("Lock: %v", err)
			return
		}
		u()
	}()

	time.Sleep(30 * time.Millisecond)
	un()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("竞争回调未被触发")
	}
	for _, c := range calls {
		if c.key != "k" {
			t.Fatalf("回调 key = %q", c.key)
		}
		if c.waited <= 0 {
			t.Fatalf("回调 waited = %v, 应为正", c.waited)
		}
	}
}

func TestKeyedContentionThreshold(t *testing.T) {
	var n atomic.Int64
	l := xlock.NewKeyed(
		xlock.WithOnContention(func(string, time.Duration) { n.Add(1) }),
		xlock.WithContentionThreshold(time.Hour), // 阈值极大 → 永不回调
	)
	un := mustLock(t, l, "k")
	done := make(chan struct{})
	go func() {
		defer close(done)
		u, err := l.Lock("k")
		if err != nil {
			t.Errorf("Lock: %v", err)
			return
		}
		u()
	}()
	time.Sleep(20 * time.Millisecond)
	un()
	<-done

	if n.Load() != 0 {
		t.Fatalf("阈值过滤失效，回调次数 = %d", n.Load())
	}
}

func TestKeyedStatsAndEntryState(t *testing.T) {
	l := xlock.NewKeyed()

	st := l.Stats()
	if st.Keys != 0 || st.WaitCount != 0 || st.HoldTotal != 0 {
		t.Fatalf("初始统计应为零: %+v", st)
	}

	un := mustLock(t, l, "k")
	if st := l.Stats(); st.Keys != 1 {
		t.Fatalf("Keys = %d, want 1", st.Keys)
	}
	if w, r, ww, rw := l.EntryState("k"); !w || r != 0 || ww != 0 || rw != 0 {
		t.Fatalf("EntryState = (%v,%d,%d,%d)", w, r, ww, rw)
	}
	// 不存在的 key
	if w, r, ww, rw := l.EntryState("nope"); w || r != 0 || ww != 0 || rw != 0 {
		t.Fatal("不存在的 key 状态应为零")
	}

	// 读锁必须用另一个 key：同 key 在写锁持有期间取读锁会自我阻塞（非重入锁的固有语义）
	un2, err := l.RLock("r")
	if err != nil {
		t.Fatalf("RLock: %v", err)
	}
	if w, r, _, _ := l.EntryState("r"); w || r != 1 {
		t.Fatalf("有 1 个读者时 EntryState(%q) = (%v,%d)", "r", w, r)
	}
	un2()

	time.Sleep(5 * time.Millisecond)
	un()

	st = l.Stats()
	if st.Keys != 0 {
		t.Fatalf("Keys = %d", st.Keys)
	}
	if st.HoldMax <= 0 || st.HoldTotal <= 0 {
		t.Fatalf("持有时长未记录: %+v", st)
	}
	if st.HoldMax > st.HoldTotal {
		t.Fatalf("HoldMax(%v) 不应大于 HoldTotal(%v)", st.HoldMax, st.HoldTotal)
	}
}

// 并发压力：大量 goroutine 混合 Lock/RLock/TryLock/取消，结束时不得残留条目、不得死锁。
func TestKeyedConcurrentStressNoLeak(t *testing.T) {
	l := xlock.NewKeyed()
	const (
		goroutines = 32
		iterations = 150
		keyCount   = 16
	)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("k%d", (g+i)%keyCount)
				switch i % 4 {
				case 0:
					if un, err := l.Lock(key); err == nil {
						un()
					}
				case 1:
					if un, err := l.RLock(key); err == nil {
						un()
					}
				case 2:
					if un, ok := l.TryLock(key); ok {
						un()
					}
				default:
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
					if un, err := l.LockCtx(ctx, key); err == nil {
						un()
					}
					cancel()
				}
			}
		}(g)
	}
	wg.Wait()

	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("压力测试后残留 %d 个条目: %v", n, l.ActiveKeysSnapshot())
	}
	if st := l.Stats(); st.InvalidUnlock != 0 {
		t.Fatalf("出现无效释放 %d 次（锁泄漏迹象）", st.InvalidUnlock)
	}
}

// 取消与唤醒竞争的高压测试：验证「被唤醒的同时 ctx 取消」不会丢锁。
func TestKeyedCancelWakeupRaceNoLockLeak(t *testing.T) {
	l := xlock.NewKeyed()
	const rounds = 400

	for r := 0; r < rounds; r++ {
		key := fmt.Sprintf("r%d", r%8)

		holder, err := l.Lock(key)
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}

		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// 极短超时：制造「唤醒信号与 ctx 超时几乎同时到达」的场景
				ctx, cancel := context.WithTimeout(context.Background(),
					time.Duration(i)*time.Millisecond)
				defer cancel()
				un, err := l.LockCtx(ctx, key)
				if err == nil {
					un()
				}
			}(i)
		}

		// 让等待者先入队，再释放
		time.Sleep(time.Duration(r%3) * 100 * time.Microsecond)
		holder()
		wg.Wait()
	}

	// 所有锁都必须已归还
	un, ok := l.TryLock("r0")
	if !ok {
		t.Fatal("锁泄漏：无法获取 r0")
	}
	un()

	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("残留 %d 个条目", n)
	}
	if st := l.Stats(); st.InvalidUnlock != 0 {
		t.Fatalf("InvalidUnlock = %d", st.InvalidUnlock)
	}
}

// 等待计数：发生过竞争的获取必须被统计。
func TestKeyedWaitStats(t *testing.T) {
	l := xlock.NewKeyed()
	un := mustLock(t, l, "k")

	done := make(chan struct{})
	go func() {
		defer close(done)
		u, err := l.Lock("k")
		if err != nil {
			t.Errorf("Lock: %v", err)
			return
		}
		u()
	}()
	time.Sleep(30 * time.Millisecond)
	un()
	<-done

	if st := l.Stats(); st.WaitCount < 1 || st.WaitTotal <= 0 {
		t.Fatalf("等待统计缺失: %+v", st)
	}
}

// ---------------------------------------------------------------------------
// ShardedLocker 专项
// ---------------------------------------------------------------------------

func TestShardedFixedTableSize(t *testing.T) {
	const shards = 4
	l := xlock.NewSharded(shards)
	if l.Shards() != shards {
		t.Fatalf("Shards = %d", l.Shards())
	}
	// 大量不同 key 之后锁表大小恒定（固定锁表，不会增长）
	for i := 0; i < 5000; i++ {
		un, err := l.Lock(fmt.Sprintf("key:%d", i))
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		un()
	}
	if st := l.Stats(); st.Keys != shards {
		t.Fatalf("锁表大小 = %d, want %d", st.Keys, shards)
	}
}

func TestShardedDefaultShards(t *testing.T) {
	l := xlock.NewSharded()
	if l.Shards() != 32 {
		t.Fatalf("默认分片数 = %d, want 32", l.Shards())
	}
	if got := xlock.NewSharded(0).Shards(); got != 32 {
		t.Fatalf("NewSharded(0) = %d, want 32", got)
	}
	if got := xlock.NewSharded(-3).Shards(); got != 32 {
		t.Fatalf("NewSharded(-3) = %d, want 32", got)
	}
	if got := xlock.NewSharded(1).Shards(); got != 1 {
		t.Fatalf("NewSharded(1) = %d", got)
	}
}

// 假冲突：落到同一分片的不同 key 会互相阻塞 —— 这是文档化的行为，锁进测试。
func TestShardedFakeConflict(t *testing.T) {
	const shards = 4
	l := xlock.NewSharded(shards)

	// 找两个落到同一分片的不同 key
	var k1, k2 string
	bucket := map[int]string{}
	for i := 0; i < 1000; i++ {
		k := fmt.Sprintf("k%d", i)
		s := xlock.ShardOf(k, shards)
		if prev, ok := bucket[s]; ok {
			k1, k2 = prev, k
			break
		}
		bucket[s] = k
	}
	if k1 == "" || k2 == "" {
		t.Fatal("未找到同分片的 key（哈希分布异常）")
	}
	if xlock.ShardOf(k1, shards) != xlock.ShardOf(k2, shards) {
		t.Fatal("前提错误：两 key 不在同一分片")
	}

	un1, err := l.Lock(k1)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// 同分片的另一个 key 会被阻塞（假冲突）
	if _, ok := l.TryLock(k2); ok {
		t.Fatal("同分片 key 应产生假冲突（文档化行为）")
	}
	un1()

	un2, ok := l.TryLock(k2)
	if !ok {
		t.Fatal("释放后应可获取")
	}
	un2()
}

func TestNilReceiverSafety(t *testing.T) {
	// nil 接收者不应 panic（与 xstack/xqueue 的约定一致）
	var l *xlock.ShardedLocker
	if l.Shards() != 0 {
		t.Fatal("nil Shards 应为 0")
	}
	if l.Stats().Keys != 0 {
		t.Fatal("nil Stats 应为零值")
	}
	if _, err := l.Lock("k"); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("nil Lock = %v, want ErrNilLocker", err)
	}
	if _, err := l.RLock("k"); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("nil RLock = %v", err)
	}
	if _, ok := l.TryLock("k"); ok {
		t.Fatal("nil TryLock 应为 false")
	}
	if _, ok := l.TryRLock("k"); ok {
		t.Fatal("nil TryRLock 应为 false")
	}
	if _, err := l.LockCtx(context.Background(), "k"); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("nil LockCtx = %v", err)
	}
	if _, err := l.RLockCtx(context.Background(), "k"); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("nil RLockCtx = %v", err)
	}

	var k *xlock.KeyedLocker
	if k.ActiveKeys() != 0 {
		t.Fatal("nil ActiveKeys 应为 0")
	}
	if _, err := k.Lock("k"); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("nil KeyedLocker Lock = %v", err)
	}
	if _, ok := k.TryLock("k"); ok {
		t.Fatal("nil KeyedLocker TryLock 应为 false")
	}

	var pool *xlock.ResourcePool
	if pool.Size() != 0 || pool.Available() != 0 || pool.InUse() != 0 || pool.MaxInUse() != 0 {
		t.Fatal("nil 资源池零值方法错误")
	}
	if !pool.IsClosed() || !pool.Closed() {
		t.Fatal("nil 资源池应视为已关闭")
	}
	if _, err := pool.Acquire(); !errors.Is(err, xlock.ErrPoolClosed) {
		t.Fatalf("nil Acquire = %v", err)
	}
	if _, ok := pool.TryAcquire(); ok {
		t.Fatal("nil TryAcquire 应为 false")
	}
	pool.Close()
	if st := pool.Stats(); st != (xlock.PoolStats{}) {
		t.Fatalf("nil Stats = %+v", st)
	}
}

// ---------------------------------------------------------------------------
// ReentrantLocker 专项
// ---------------------------------------------------------------------------

func TestGoroutineID(t *testing.T) {
	id, ok := xlock.GoroutineID()
	if !ok || id == 0 {
		t.Fatalf("GoroutineID = (%d, %v)，期望成功", id, ok)
	}
	// 同一 goroutine 内必须稳定
	id2, ok2 := xlock.GoroutineID()
	if !ok2 || id2 != id {
		t.Fatalf("同一 goroutine 内不稳定: %d vs %d", id, id2)
	}
	if owner := xlock.Owner(); owner == "" {
		t.Fatal("Owner 不应为空")
	}

	// 不同 goroutine 应得到不同 id
	ch := make(chan uint64, 1)
	go func() {
		got, _ := xlock.GoroutineID()
		ch <- got
	}()
	if other := <-ch; other == id {
		t.Fatalf("不同 goroutine 应有不同 id: %d", other)
	}
}

// 同一 goroutine 内重入成功，但**另一个 goroutine** 必须被阻塞。
func TestReentrantSameGoroutine(t *testing.T) {
	l := xlock.NewReentrant()

	u1, err := l.Lock("k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if d := l.Depth("k"); d != 1 {
		t.Fatalf("Depth = %d, want 1", d)
	}
	if l.Holder("k") == "" {
		t.Fatal("Holder 不应为空")
	}

	// 另一个 goroutine 尝试获取：必须被阻塞（不能因为「有 owner」就放行）
	acquired := make(chan struct{})
	go func() {
		u, err := l.Lock("k")
		if err != nil {
			t.Errorf("Lock: %v", err)
			return
		}
		close(acquired)
		u()
	}()

	select {
	case <-acquired:
		t.Fatal("持有期间其他 goroutine 不应获取成功")
	case <-time.After(80 * time.Millisecond):
		// 预期：被阻塞
	}

	u1()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("释放后等待者未被唤醒")
	}
	if d := l.Depth("k"); d != 0 {
		t.Fatalf("全部释放后 Depth = %d, want 0", d)
	}
}

// 真正的同 goroutine 重入（同一调用栈内加锁两次）。
func TestReentrantNestedSameGoroutine(t *testing.T) {
	l := xlock.NewReentrant()

	u1, err := l.Lock("k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// 同一 goroutine 内嵌套获取，必须立即成功
	u2, err := l.Lock("k")
	if err != nil {
		t.Fatalf("嵌套 Lock: %v", err)
	}
	u3, err := l.Lock("k")
	if err != nil {
		t.Fatalf("三层嵌套 Lock: %v", err)
	}
	if d := l.Depth("k"); d != 3 {
		t.Fatalf("Depth = %d, want 3", d)
	}

	// 跨 goroutine 仍然互斥：Depth 为 3 时其他 goroutine 拿不到
	other := make(chan bool, 1)
	go func() {
		_, ok := l.TryLock("k")
		other <- ok
	}()
	if ok := <-other; ok {
		t.Fatal("持有期间其他 goroutine 不应成功")
	}

	u3()
	u2()
	if d := l.Depth("k"); d != 1 {
		t.Fatalf("Depth = %d, want 1", d)
	}
	u1()
	if d := l.Depth("k"); d != 0 {
		t.Fatalf("Depth = %d, want 0", d)
	}
	if l.Depth("不存在的key") != 0 || l.Holder("不存在的key") != "" {
		t.Fatal("不存在的 key 应为零值")
	}
}

// 跨 goroutine 互斥（可重入锁不能破坏基本互斥）。
func TestReentrantCrossGoroutineMutualExclusion(t *testing.T) {
	l := xlock.NewReentrant()
	const goroutines, perG = 16, 200

	var counter int
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				un, err := l.Lock("c")
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				counter++
				un()
			}
		}()
	}
	wg.Wait()

	if counter != goroutines*perG {
		t.Fatalf("计数器 = %d, want %d", counter, goroutines*perG)
	}
}

func TestReentrantLockFor(t *testing.T) {
	l := xlock.NewReentrant()

	u1, err := l.LockFor("tx-1", "k")
	if err != nil {
		t.Fatalf("LockFor: %v", err)
	}
	if h := l.Holder("k"); h != "tx-1" {
		t.Fatalf("Holder = %q, want tx-1", h)
	}

	// 同一 owner 再次获取 → 重入
	u2, err := l.LockFor("tx-1", "k")
	if err != nil {
		t.Fatalf("LockFor 重入: %v", err)
	}
	if d := l.Depth("k"); d != 2 {
		t.Fatalf("Depth = %d, want 2", d)
	}

	// 不同 owner 从同一 goroutine 获取 → 必须阻塞（TryLock 失败）
	if _, ok := l.TryLock("k"); ok {
		t.Fatal("不同 owner 不应重入成功")
	}

	u2()
	u1()
	if d := l.Depth("k"); d != 0 {
		t.Fatalf("Depth = %d", d)
	}
}

func TestReentrantCtxCancel(t *testing.T) {
	l := xlock.NewReentrant()
	// 主 goroutine 持有
	un, err := l.LockFor("owner-main", "k")
	if err != nil {
		t.Fatalf("LockFor: %v", err)
	}
	defer un()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := l.LockForCtx(ctx, "owner-b", "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, want DeadlineExceeded", err)
	}
	// 同 owner 在 ctx 下仍应重入成功（用新 ctx，避免受上一个已超时的 ctx 影响）
	rctx, rcancel := context.WithTimeout(context.Background(), time.Second)
	defer rcancel()
	if u2, err := l.LockForCtx(rctx, "owner-main", "k"); err != nil {
		t.Fatalf("同 owner 重入失败: %v", err)
	} else {
		u2()
	}

	// WaitTimeout 路径
	l2 := xlock.NewReentrant(xlock.WithWaitTimeout(50 * time.Millisecond))
	h, err := l2.LockFor("a", "k")
	if err != nil {
		t.Fatalf("LockFor: %v", err)
	}
	defer h()
	if _, err := l2.LockFor("b", "k"); !errors.Is(err, xlock.ErrLockTimeout) {
		t.Fatalf("错误 = %v, want ErrLockTimeout", err)
	}

	// TryLock 成功路径
	l3 := xlock.NewReentrant()
	if un, ok := l3.TryLock("free"); !ok {
		t.Fatal("空闲锁 TryLock 应成功")
	} else {
		un()
	}
	// MaxKeys
	l4 := xlock.NewReentrant(xlock.WithMaxKeys(1))
	u4, err := l4.LockFor("o", "x")
	if err != nil {
		t.Fatalf("LockFor: %v", err)
	}
	if _, err := l4.LockFor("o", "y"); !errors.Is(err, xlock.ErrTooManyKeys) {
		t.Fatalf("错误 = %v, want ErrTooManyKeys", err)
	}
	u4()
	if st := l4.Stats(); st.Keys != 0 {
		t.Fatalf("Keys = %d", st.Keys)
	}
}

// 非持有者释放：必须幂等忽略而不是破坏状态。
func TestReentrantInvalidUnlock(t *testing.T) {
	l := xlock.NewReentrant()

	// 构造一个「别人的释放函数」：owner 不匹配
	un, err := l.LockFor("owner-1", "k")
	if err != nil {
		t.Fatalf("LockFor: %v", err)
	}

	// 直接构造一个未持有的释放回调来验证幂等（通过重复释放实现）
	un() // 第一次正常释放
	un() // 第二次：深度为 0 → 无效释放
	un()

	if st := l.Stats(); st.InvalidUnlock != 2 {
		t.Fatalf("InvalidUnlock = %d, want 2", st.InvalidUnlock)
	}
	// 状态必须干净
	if d := l.Depth("k"); d != 0 {
		t.Fatalf("Depth = %d", d)
	}
	un2, ok := l.TryLock("k")
	if !ok {
		t.Fatal("锁应可用")
	}
	un2()
}

// ---------------------------------------------------------------------------
// ResourcePool 专项
// ---------------------------------------------------------------------------

func TestResourcePoolCapacity(t *testing.T) {
	p := xlock.NewResourcePool(3, xlock.WithPoolName("db"))
	if p.Name() != "db" || p.Size() != 3 {
		t.Fatalf("name=%q size=%d", p.Name(), p.Size())
	}
	if p.Available() != 3 || p.InUse() != 0 {
		t.Fatalf("初始 available=%d inUse=%d", p.Available(), p.InUse())
	}

	u1, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	u2, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	u3, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if p.InUse() != 3 || p.Available() != 0 {
		t.Fatalf("满载状态: inUse=%d available=%d", p.InUse(), p.Available())
	}
	// 满载后 TryAcquire 失败
	if _, ok := p.TryAcquire(); ok {
		t.Fatal("满载时 TryAcquire 应失败")
	}

	u1()
	u2()
	u3()
	if p.InUse() != 0 || p.Available() != 3 {
		t.Fatalf("归还后: inUse=%d available=%d", p.InUse(), p.Available())
	}
	// 重复归还幂等
	u1()
	if p.InUse() != 0 || p.Available() != 3 {
		t.Fatalf("重复归还破坏了状态: inUse=%d available=%d", p.InUse(), p.Available())
	}
	if st := p.Stats(); st.Invalid != 1 {
		t.Fatalf("Invalid = %d, want 1", st.Invalid)
	}
	if p.MaxInUse() != 3 {
		t.Fatalf("MaxInUse = %d, want 3", p.MaxInUse())
	}
}

func TestResourcePoolBlockingAndCancel(t *testing.T) {
	p := xlock.NewResourcePool(1)
	u := func() {
		un, err := p.Acquire()
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer un()
	}
	_ = u

	hold, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 超时
	start := time.Now()
	if _, err := p.AcquireTimeout(60 * time.Millisecond); !errors.Is(err, xlock.ErrLockTimeout) {
		t.Fatalf("错误 = %v, want ErrLockTimeout", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("过早返回: %v", elapsed)
	}

	// ctx 取消
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := p.AcquireCtx(ctx)
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v, want context.Canceled", err)
	}

	// ctx 超时
	ctx2, cancel2 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel2()
	if _, err := p.AcquireCtx(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, want DeadlineExceeded", err)
	}

	// 阻塞等待：释放后应立即唤醒
	got := make(chan struct{})
	go func() {
		un, err := p.Acquire()
		if err != nil {
			t.Errorf("Acquire: %v", err)
			return
		}
		close(got)
		un()
	}()
	time.Sleep(30 * time.Millisecond)
	hold()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("释放后等待者未被唤醒")
	}

	// ctx 为 nil 等价于 Acquire
	if un, err := p.AcquireCtx(nil); err != nil {
		t.Fatalf("AcquireCtx(nil): %v", err)
	} else {
		un()
	}
}

func TestResourcePoolCloseSemantics(t *testing.T) {
	// 容量 1 且已被借出 → 后续 Acquire 必然是等待态
	p := xlock.NewResourcePool(1)
	u1, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 有等待者时关闭
	errc := make(chan error, 1)
	go func() {
		_, err := p.Acquire()
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)

	p.Close()
	if !p.IsClosed() || !p.Closed() {
		t.Fatal("应已关闭")
	}
	p.Close() // 重复关闭安全

	select {
	case err := <-errc:
		if !errors.Is(err, xlock.ErrPoolClosed) {
			t.Fatalf("等待者错误 = %v, want ErrPoolClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未唤醒等待者")
	}

	// 关闭后不能再借用
	if _, err := p.Acquire(); !errors.Is(err, xlock.ErrPoolClosed) {
		t.Fatalf("错误 = %v, want ErrPoolClosed", err)
	}
	if _, err := p.AcquireCtx(context.Background()); !errors.Is(err, xlock.ErrPoolClosed) {
		t.Fatalf("错误 = %v, want ErrPoolClosed", err)
	}
	if _, err := p.AcquireTimeout(time.Second); !errors.Is(err, xlock.ErrPoolClosed) {
		t.Fatalf("错误 = %v, want ErrPoolClosed", err)
	}
	if _, ok := p.TryAcquire(); ok {
		t.Fatal("关闭后 TryAcquire 应失败")
	}

	// 已借出的令牌仍可归还（Close 不丢数据）
	u1()
	if p.Available() != 1 {
		t.Fatalf("归还后 Available = %d, want 1", p.Available())
	}
	if p.InUse() != 0 {
		t.Fatalf("InUse = %d", p.InUse())
	}
}

func TestResourcePoolWith(t *testing.T) {
	p := xlock.NewResourcePool(2)
	ctx := context.Background()

	called := 0
	if err := p.With(ctx, func() error { called++; return nil }); err != nil {
		t.Fatalf("With: %v", err)
	}
	if called != 1 {
		t.Fatal("With 未执行 fn")
	}
	if p.InUse() != 0 {
		t.Fatalf("With 未归还令牌: %d", p.InUse())
	}

	// fn 返回错误要透传，且令牌仍归还
	sentinel := errors.New("业务错误")
	if err := p.With(ctx, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v", err)
	}
	if p.InUse() != 0 {
		t.Fatal("出错时未归还令牌")
	}

	// fn panic 也要归还
	func() {
		defer func() { _ = recover() }()
		_ = p.With(ctx, func() error { panic("boom") })
	}()
	if p.InUse() != 0 {
		t.Fatalf("panic 时未归还令牌: %d", p.InUse())
	}

	// fn 为 nil
	if err := p.With(ctx, nil); err == nil {
		t.Fatal("fn 为 nil 应报错")
	}

	// WithPoolValue
	v, err := xlock.WithPoolValue(p, ctx, func() (int, error) { return 42, nil })
	if err != nil || v != 42 {
		t.Fatalf("WithPoolValue = %d, %v", v, err)
	}
	if p.InUse() != 0 {
		t.Fatal("WithPoolValue 未归还令牌")
	}
	// 错误路径
	if _, err := xlock.WithPoolValue(p, ctx, func() (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v", err)
	}
	if _, err := xlock.WithPoolValue[int](nil, ctx, func() (int, error) { return 1, nil }); !errors.Is(err, xlock.ErrPoolClosed) {
		t.Fatalf("nil 池错误 = %v", err)
	}

	// 已关闭的池
	p.Close()
	if err := p.With(ctx, func() error { return nil }); !errors.Is(err, xlock.ErrPoolClosed) {
		t.Fatalf("关闭后 With 错误 = %v", err)
	}
}

func TestResourcePoolConcurrencyLimit(t *testing.T) {
	const limit = 4
	p := xlock.NewResourcePool(limit)

	var current, peak atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				un, err := p.Acquire()
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				n := current.Add(1)
				// 记录峰值并发
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(50 * time.Microsecond)
				current.Add(-1)
				un()
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("峰值并发 = %d, 超过上限 %d", got, limit)
	}
	if got := peak.Load(); got < limit {
		t.Fatalf("峰值并发 = %d, 未达到上限 %d（测试未真正施压）", got, limit)
	}
	if p.InUse() != 0 || p.Available() != limit {
		t.Fatalf("结束状态: inUse=%d available=%d", p.InUse(), p.Available())
	}
	st := p.Stats()
	if st.MaxInUse > limit {
		t.Fatalf("MaxInUse = %d 超过上限", st.MaxInUse)
	}
	if st.Acquired != 32*50 {
		t.Fatalf("Acquired = %d, want %d", st.Acquired, 32*50)
	}
}

func TestNewResourcePoolDefaults(t *testing.T) {
	for _, size := range []int{0, -1, -100} {
		p := xlock.NewResourcePool(size)
		if p.Size() != 1 {
			t.Fatalf("NewResourcePool(%d).Size() = %d, want 1", size, p.Size())
		}
	}
}

// ---------------------------------------------------------------------------
// With / WithR / WithValue / LockAll
// ---------------------------------------------------------------------------

func TestWithScope(t *testing.T) {
	l := xlock.NewKeyed()
	ctx := context.Background()

	called := 0
	if err := xlock.With(ctx, l, "k", func() error {
		called++
		// 作用域内锁被持有
		if _, ok := l.TryLock("k"); ok {
			t.Error("作用域内不应能再次获取")
		}
		return nil
	}); err != nil {
		t.Fatalf("With: %v", err)
	}
	if called != 1 {
		t.Fatal("fn 未执行")
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("With 后泄漏条目: %d", n)
	}

	// fn 错误透传
	sentinel := errors.New("业务失败")
	if err := xlock.With(ctx, l, "k", func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v", err)
	}

	// fn panic → 锁仍释放
	func() {
		defer func() { _ = recover() }()
		_ = xlock.With(ctx, l, "k", func() error { panic("boom") })
	}()
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("panic 后泄漏条目: %d", n)
	}
	un, ok := l.TryLock("k")
	if !ok {
		t.Fatal("panic 后锁未释放")
	}
	un()

	// nil locker / nil fn
	if err := xlock.With(ctx, nil, "k", func() error { return nil }); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("错误 = %v, want ErrNilLocker", err)
	}
	if err := xlock.With(ctx, l, "k", nil); err == nil {
		t.Fatal("nil fn 应报错")
	}

	// ctx 已取消 → 不执行 fn（即使锁空闲）
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	ran := false
	if err := xlock.With(cctx, l, "k", func() error { ran = true; return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v, want context.Canceled", err)
	}
	if ran {
		t.Fatal("ctx 已取消时不应执行 fn")
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("取消路径应不留下条目: %d", n)
	}

	// 取消的 ctx 对 LockCtx / RLockCtx / WithR 同样生效
	if _, err := l.LockCtx(cctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("LockCtx 错误 = %v", err)
	}
	if _, err := l.RLockCtx(cctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RLockCtx 错误 = %v", err)
	}
	if err := xlock.WithR(cctx, l, "k", func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WithR 错误 = %v", err)
	}
	if _, err := xlock.WithValue(cctx, l, "k", func() (int, error) { return 1, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("WithValue 错误 = %v", err)
	}
}

func TestWithRScope(t *testing.T) {
	l := xlock.NewKeyed()
	ctx := context.Background()

	// 读作用域内可并行（同一 goroutine 内重复 RLock 成功即证明是读锁）
	if err := xlock.WithR(ctx, l, "k", func() error {
		un2, err := l.RLock("k")
		if err != nil {
			t.Fatalf("读作用域内应可再加读锁: %v", err)
		}
		un2()
		return nil
	}); err != nil {
		t.Fatalf("WithR: %v", err)
	}

	if err := xlock.WithR(ctx, nil, "k", func() error { return nil }); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("错误 = %v", err)
	}
	if err := xlock.WithR(ctx, l, "k", nil); err == nil {
		t.Fatal("nil fn 应报错")
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("WithR 后泄漏条目: %d", n)
	}
}

func TestWithValue(t *testing.T) {
	l := xlock.NewKeyed()
	ctx := context.Background()

	v, err := xlock.WithValue(ctx, l, "k", func() (string, error) { return "ok", nil })
	if err != nil || v != "ok" {
		t.Fatalf("WithValue = %q, %v", v, err)
	}

	sentinel := errors.New("失败")
	if _, err := xlock.WithValue(ctx, l, "k", func() (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v", err)
	}
	if _, err := xlock.WithValue[int](ctx, nil, "k", func() (int, error) { return 1, nil }); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("错误 = %v", err)
	}
	if _, err := xlock.WithValue[int](ctx, l, "k", nil); err == nil {
		t.Fatal("nil fn 应报错")
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("泄漏条目: %d", n)
	}
}

func TestLockAll(t *testing.T) {
	l := xlock.NewKeyed()
	ctx := context.Background()

	// 乱序传入、含重复：必须全部拿到
	un, err := xlock.LockAll(ctx, l, "c", "a", "b", "a", "")
	if err != nil {
		t.Fatalf("LockAll: %v", err)
	}
	if n := l.ActiveKeys(); n != 4 {
		t.Fatalf("持锁数 = %d, want 4（含空串 key）", n)
	}
	for _, k := range []string{"a", "b", "c", ""} {
		if _, ok := l.TryLock(k); ok {
			t.Fatalf("key %q 应已被持有", k)
		}
	}
	if err := un(); err != nil {
		t.Fatalf("统一释放: %v", err)
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("释放后残留 %d 个条目", n)
	}
	// 幂等
	if err := un(); err != nil {
		t.Fatalf("重复释放: %v", err)
	}

	// 空 keys
	un2, err := xlock.LockAll(ctx, l)
	if err != nil {
		t.Fatalf("LockAll(空): %v", err)
	}
	un2()

	// nil locker
	if _, err := xlock.LockAll(ctx, nil, "a"); !errors.Is(err, xlock.ErrNilLocker) {
		t.Fatalf("错误 = %v", err)
	}

	// 带超时的部分失败回滚
	l3 := xlock.NewKeyed()
	hold := mustLock(t, l3, "zzz") // 排序后最后 → 前面的 a/b 会先被拿到再回滚
	defer hold()
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := xlock.LockAll(cctx, l3, "a", "b", "zzz"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, want DeadlineExceeded", err)
	}
	// a / b 必须已回滚（可立即获取）
	unA, ok := l3.TryLock("a")
	if !ok {
		t.Fatal("失败后 a 未被回滚")
	}
	unA()
	unB, ok := l3.TryLock("b")
	if !ok {
		t.Fatal("失败后 b 未被回滚")
	}
	unB()
	if n := l3.ActiveKeys(); n != 1 {
		t.Fatalf("残留条目 = %d, want 1（仅 zzz 被外部持有）", n)
	}
}

func TestLockAllNoDeadlock(t *testing.T) {
	// A 按 (x,y) 顺序、B 按 (y,x) 顺序申请 —— 若不加排序会死锁
	l := xlock.NewKeyed()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			un, err := xlock.LockAll(ctx, l, "x", "y")
			if err != nil {
				t.Errorf("LockAll: %v", err)
				return
			}
			un()
		}()
		go func() {
			defer wg.Done()
			un, err := xlock.LockAll(ctx, l, "y", "x")
			if err != nil {
				t.Errorf("LockAll: %v", err)
				return
			}
			un()
		}()
	}
	// 用带超时的等待，把「死锁」变成「测试失败」而不是「测试挂住」
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LockAll 发生死锁")
	}
	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("残留 %d 个条目", n)
	}
}

// ---------------------------------------------------------------------------
// 哨兵错误
// ---------------------------------------------------------------------------

func TestSentinelErrors(t *testing.T) {
	cases := []struct {
		err  error
		code xerr.Code
	}{
		{xlock.ErrLockTimeout, xerr.CodeTimeout},
		{xlock.ErrTooManyKeys, xerr.CodeResourceExhausted},
		{xlock.ErrNoGoroutineID, xerr.CodeNotImplemented},
		{xlock.ErrNilLocker, xerr.CodeMissingParam},
		{xlock.ErrPoolClosed, xerr.CodeStateInvalid},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Fatal("哨兵不得为 nil")
		}
		if xerr.CodeOf(c.err) != c.code {
			t.Fatalf("%v 错误码 = %v, want %v", c.err, xerr.CodeOf(c.err), c.code)
		}
		if !errors.Is(c.err, c.err) {
			t.Fatalf("%v errors.Is 自身失败", c.err)
		}
	}
}

func TestUnlockFuncSemantics(t *testing.T) {
	// 零值 UnlockFunc 走接口方法必须安全
	var nilFunc xlock.UnlockFunc
	if err := nilFunc.Unlock(); err != nil {
		t.Fatalf("nil UnlockFunc 应返回 nil, got %v", err)
	}
	var u0 xlock.Unlocker = nilFunc
	if err := u0.Unlock(); err != nil {
		t.Fatalf("nil Unlocker 应返回 nil, got %v", err)
	}
	// 注意：直接把零值 UnlockFunc 当函数调用会 panic —— 这是 Go 的固有语义，
	// 因此约定是「始终通过 Unlock() 方法调用」（With/LockAll 内部都如此）。

	// Unlocker 接口
	calls := 0
	var u xlock.Unlocker = xlock.UnlockFunc(func() error { calls++; return nil })
	if err := u.Unlock(); err != nil || calls != 1 {
		t.Fatalf("Unlocker 调用失败: %v calls=%d", err, calls)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkKeyedUncontended(b *testing.B) {
	l := xlock.NewKeyed()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		un, err := l.Lock("k")
		if err != nil {
			b.Fatal(err)
		}
		un()
	}
}

func BenchmarkKeyedParallelDifferentKeys(b *testing.B) {
	l := xlock.NewKeyed()
	keys := make([]string, 64)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	var n atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			k := keys[n.Add(1)%64]
			un, err := l.Lock(k)
			if err != nil {
				b.Error(err)
				return
			}
			un()
		}
	})
}

func BenchmarkKeyedParallelSameKey(b *testing.B) {
	l := xlock.NewKeyed()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			un, err := l.Lock("hot")
			if err != nil {
				b.Error(err)
				return
			}
			un()
		}
	})
}

func BenchmarkKeyedRLockParallel(b *testing.B) {
	l := xlock.NewKeyed()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			un, err := l.RLock("hot")
			if err != nil {
				b.Error(err)
				return
			}
			un()
		}
	})
}

func BenchmarkShardedUncontended(b *testing.B) {
	l := xlock.NewSharded(64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		un, err := l.Lock("k")
		if err != nil {
			b.Fatal(err)
		}
		un()
	}
}

func BenchmarkReentrantUncontended(b *testing.B) {
	l := xlock.NewReentrant()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		un, err := l.Lock("k")
		if err != nil {
			b.Fatal(err)
		}
		un()
	}
}

func BenchmarkResourcePool(b *testing.B) {
	p := xlock.NewResourcePool(8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			un, _ := p.Acquire()
			un()
		}
	})
}
