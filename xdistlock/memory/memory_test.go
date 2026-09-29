package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/indigo-apple/gokit/xdistlock"
	"github.com/indigo-apple/gokit/xdistlock/memory"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// fakeClock 是可推进的时钟，用于**确定性**地模拟租约过期（不依赖 sleep）。
type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{cur: time.Now()}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.cur = c.cur.Add(d)
	c.mu.Unlock()
}

func newClockBackend() (*memory.Backend, *fakeClock) {
	clk := newFakeClock()
	return memory.New(memory.WithClock(clk.now)), clk
}

func waitCond(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Microsecond)
	}
	return cond()
}

// ---------------------------------------------------------------------------
// 基本语义
// ---------------------------------------------------------------------------

func TestAcquireReleaseRefresh(t *testing.T) {
	b := memory.New()
	ctx := context.Background()

	if b.Name() != "memory" {
		t.Fatalf("Name = %q", b.Name())
	}

	// 空 token
	if _, ok, err := b.Acquire(ctx, "k", "", time.Second); err == nil || ok {
		t.Fatalf("空 token 应报错: ok=%v err=%v", ok, err)
	}
	// 已取消的 ctx
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok, err := b.Acquire(cctx, "k", "t1", time.Second); !errors.Is(err, context.Canceled) || ok {
		t.Fatalf("ctx 已取消: ok=%v err=%v", ok, err)
	}

	f1, ok, err := b.Acquire(ctx, "k", "t1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("Acquire = %v,%v", ok, err)
	}
	if f1 == 0 {
		t.Fatal("fencing 不应为 0")
	}

	// 同 key 再获取必须失败
	if _, ok, err := b.Acquire(ctx, "k", "t2", time.Hour); ok || err != nil {
		t.Fatalf("重复获取应失败: ok=%v err=%v", ok, err)
	}
	// Refresh：token 必须匹配
	if ok, _ := b.Refresh(ctx, "k", "t2", time.Hour); ok {
		t.Fatal("非持有者 Refresh 应返回 false")
	}
	if ok, _ := b.Refresh(ctx, "k", "t1", time.Hour); !ok {
		t.Fatal("持有者 Refresh 应成功")
	}
	if ok, _ := b.Refresh(ctx, "不存在", "t1", time.Hour); ok {
		t.Fatal("不存在的 key Refresh 应返回 false")
	}
	// Release：token 必须匹配（compare-and-delete）
	if ok, _ := b.Release(ctx, "k", "t2"); ok {
		t.Fatal("非持有者 Release 应返回 false 且不删除")
	}
	if ok, _ := b.Release(ctx, "不存在", "t1"); ok {
		t.Fatal("不存在的 key Release 应返回 false")
	}
	if ok, _ := b.Release(ctx, "k", "t1"); !ok {
		t.Fatal("持有者 Release 应成功")
	}
	// 释放后可重新获取
	if _, ok, _ := b.Acquire(ctx, "k", "t3", time.Hour); !ok {
		t.Fatal("释放后应可获取")
	}

	// fencing 单调递增
	if _, ok, _ := b.Acquire(ctx, "other", "t4", time.Hour); !ok {
		t.Fatal("不同 key 应可并行持有")
	}
	if b.FencingCounter() < 3 {
		t.Fatalf("FencingCounter = %d", b.FencingCounter())
	}

	// ctx 已取消时的 Refresh / Release
	if _, err := b.Refresh(cctx, "k", "t3", time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh(ctx canceled) = %v", err)
	}
	if _, err := b.Release(cctx, "k", "t3"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Release(ctx canceled) = %v", err)
	}
}

func TestExpiryDeterministicWithClock(t *testing.T) {
	b, clk := newClockBackend()
	ctx := context.Background()

	if _, ok, err := b.Acquire(ctx, "k", "A", time.Second); !ok || err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 未过期：别人拿不到
	if _, ok, _ := b.Acquire(ctx, "k", "B", time.Second); ok {
		t.Fatal("未过期时不应被他人获取")
	}
	// 未过期：可以续约
	clk.advance(500 * time.Millisecond)
	if ok, _ := b.Refresh(ctx, "k", "A", 2*time.Second); !ok {
		t.Fatal("未过期应可续约")
	}

	// 越过新的到期时间
	clk.advance(2500 * time.Millisecond)

	// ★ 关键：过期后**不能**再续约 —— 因为这段时间里别人可能已认为锁空闲
	if ok, _ := b.Refresh(ctx, "k", "A", time.Second); ok {
		t.Fatal("过期后不应允许续约（否则会与新持有者并存）")
	}
	// 别人此时应能获取
	f2, ok, err := b.Acquire(ctx, "k", "B", time.Second)
	if err != nil || !ok {
		t.Fatalf("过期后应可被他人获取: ok=%v err=%v", ok, err)
	}
	// ★ 安全释放：老持有者不得删掉新持有者的锁
	if ok, _ := b.Release(ctx, "k", "A"); ok {
		t.Fatal("过期后的老持有者不应释放成功")
	}
	infos, _ := b.Snapshot(ctx, "")
	if len(infos) != 1 || infos[0].Token != "B" || infos[0].Fencing != f2 {
		t.Fatalf("锁被误删或状态错误: %+v", infos)
	}
	// 新持有者可以正常释放
	if ok, _ := b.Release(ctx, "k", "B"); !ok {
		t.Fatal("新持有者应可释放")
	}
}

func TestSnapshotAndObservation(t *testing.T) {
	b := memory.New()
	ctx := context.Background()

	if b.ActiveKeys() != 0 {
		t.Fatal("初始应为空")
	}
	if infos, err := b.Snapshot(ctx, ""); err != nil || len(infos) != 0 {
		t.Fatalf("空 Snapshot = %v, %v", infos, err)
	}

	_, _, _ = b.Acquire(ctx, "app:a", "t1", time.Minute)
	_, _, _ = b.Acquire(ctx, "app:b", "t2", time.Minute)
	_, _, _ = b.Acquire(ctx, "other:c", "t3", time.Minute)

	if b.ActiveKeys() != 3 {
		t.Fatalf("ActiveKeys = %d", b.ActiveKeys())
	}
	infos, err := b.Snapshot(ctx, "app:")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("前缀过滤失效: %d", len(infos))
	}
	for _, in := range infos {
		if in.TTL <= 0 || in.TTL > time.Minute {
			t.Fatalf("TTL 异常: %v", in.TTL)
		}
		if in.Token == "" || in.Fencing == 0 {
			t.Fatalf("条目信息不全: %+v", in)
		}
	}

	// 释放后不再出现在快照里
	_, _ = b.Release(ctx, "app:a", "t1")
	if infos, _ := b.Snapshot(ctx, "app:"); len(infos) != 1 {
		t.Fatalf("释放后仍出现在快照: %+v", infos)
	}
	if b.ActiveKeys() != 2 {
		t.Fatalf("ActiveKeys = %d", b.ActiveKeys())
	}

	// ctx 已取消
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Snapshot(cctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot(ctx canceled) = %v", err)
	}
}

func TestCloseAndNilSafety(t *testing.T) {
	b := memory.New()
	ctx := context.Background()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok, err := b.Acquire(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrClosed) || ok {
		t.Fatalf("关闭后 Acquire = %v, want ErrClosed", err)
	}
	if _, err := b.Refresh(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrClosed) {
		t.Fatalf("关闭后 Refresh = %v", err)
	}
	if _, err := b.Release(ctx, "k", "t"); !errors.Is(err, xdistlock.ErrClosed) {
		t.Fatalf("关闭后 Release = %v", err)
	}
	if _, ok, err := b.AcquireFair(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrClosed) || ok {
		t.Fatalf("关闭后 AcquireFair = %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}

	// nil 接收者
	var nb *memory.Backend
	if nb.Name() != "memory" {
		t.Fatal("nil Name")
	}
	if err := nb.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	if _, _, err := nb.Acquire(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Acquire = %v", err)
	}
	if _, _, err := nb.AcquireFair(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil AcquireFair = %v", err)
	}
	if _, err := nb.Refresh(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Refresh = %v", err)
	}
	if _, err := nb.Release(ctx, "k", "t"); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Release = %v", err)
	}
	if _, err := nb.Snapshot(ctx, ""); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Snapshot = %v", err)
	}
	if nb.ActiveKeys() != 0 || nb.Waiters("k") != 0 || nb.FencingCounter() != 0 {
		t.Fatal("nil 观测方法应返回零值")
	}

	// AcquireFair 参数校验
	b2 := memory.New()
	if _, ok, err := b2.AcquireFair(ctx, "k", "", time.Second); err == nil || ok {
		t.Fatal("空 token 应报错")
	}
	if _, ok, err := b2.AcquireFair(ctx, "k", "t", 0); err == nil || ok {
		t.Fatal("ttl<=0 应报错")
	}
}

// ---------------------------------------------------------------------------
// 公平（FIFO）模式
// ---------------------------------------------------------------------------

// 3 个公平等待者必须按到达顺序依次获得锁。
func TestFairFIFOOrder(t *testing.T) {
	b := memory.New()
	ctx := context.Background()

	if _, ok, err := b.Acquire(ctx, "k", "owner", time.Hour); !ok || err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	const n = 3
	order := make(chan string, n)
	var wg sync.WaitGroup

	for i := 1; i <= n; i++ {
		token := fmt.Sprintf("w%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, ok, err := b.AcquireFair(ctx, "k", token, time.Hour)
			if err != nil || !ok {
				t.Errorf("%s: ok=%v err=%v", token, ok, err)
				return
			}
			if f == 0 {
				t.Errorf("%s: fencing 为 0", token)
			}
			order <- token
			if ok, err := b.Release(ctx, "k", token); err != nil || !ok {
				t.Errorf("%s: Release ok=%v err=%v", token, ok, err)
			}
		}()

		// 等它真正入队，从而让「到达顺序」确定可预期
		if !waitCond(func() bool { return b.Waiters("k") == i }, 3*time.Second) {
			t.Fatalf("等待者 %d 未入队（Waiters=%d）", i, b.Waiters("k"))
		}
	}

	// 释放持有者 → 依次交接
	if ok, err := b.Release(ctx, "k", "owner"); err != nil || !ok {
		t.Fatalf("Release owner: %v", err)
	}

	wg.Wait()
	close(order)

	var got []string
	for s := range order {
		got = append(got, s)
	}
	if len(got) != n {
		t.Fatalf("获得锁的等待者数量 = %d, want %d (%v)", len(got), n, got)
	}
	for i, s := range got {
		want := fmt.Sprintf("w%d", i+1)
		if s != want {
			t.Fatalf("FIFO 顺序被破坏: %v（第 %d 个应为 %s）", got, i, want)
		}
	}
	if b.Waiters("k") != 0 {
		t.Fatalf("队列应已清空: %d", b.Waiters("k"))
	}
	if b.ActiveKeys() != 0 {
		t.Fatalf("ActiveKeys = %d", b.ActiveKeys())
	}
}

// 等待者取消必须把自己从队列摘除，并且不影响后续等待者。
func TestFairWaiterCancelDoesNotBlockOthers(t *testing.T) {
	b := memory.New()
	ctx := context.Background()

	if _, _, err := b.Acquire(ctx, "k", "owner", time.Hour); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// w1 入队后立刻取消
	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, ok, err := b.AcquireFair(cctx, "k", "w1", time.Hour)
		if ok {
			// 极端情况下（唤醒与取消竞态）拿到锁也必须归还
			_, _ = b.Release(ctx, "k", "w1")
		}
		errc <- err
	}()
	if !waitCond(func() bool { return b.Waiters("k") == 1 }, 3*time.Second) {
		t.Fatal("w1 未入队")
	}
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("w1 错误 = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("w1 未响应取消")
	}
	if !waitCond(func() bool { return b.Waiters("k") == 0 }, 3*time.Second) {
		t.Fatalf("取消后队列未清理: %d", b.Waiters("k"))
	}

	// w2 仍然可以正常拿到锁
	w2done := make(chan struct{})
	go func() {
		defer close(w2done)
		if _, ok, err := b.AcquireFair(ctx, "k", "w2", time.Hour); err != nil || !ok {
			t.Errorf("w2: ok=%v err=%v", ok, err)
			return
		}
		if ok, _ := b.Release(ctx, "k", "w2"); !ok {
			t.Error("w2 释放失败")
		}
	}()
	waitCond(func() bool { return b.Waiters("k") == 1 }, 3*time.Second)

	if ok, _ := b.Release(ctx, "k", "owner"); !ok {
		t.Fatal("释放 owner 失败")
	}
	select {
	case <-w2done:
	case <-time.After(3 * time.Second):
		t.Fatal("w2 未被交接")
	}
}

// 有公平等待者排队时，非公平 Acquire 必须让路（否则公平性形同虚设）。
func TestFairWaitersBlockUnfairAcquire(t *testing.T) {
	b := memory.New()
	ctx := context.Background()

	if _, _, err := b.Acquire(ctx, "k", "owner", time.Hour); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// 一个公平等待者排队
	go func() { _, _, _ = b.AcquireFair(ctx, "k", "fair-waiter", time.Hour) }()
	if !waitCond(func() bool { return b.Waiters("k") == 1 }, 3*time.Second) {
		t.Fatal("公平等待者未入队")
	}

	// 释放 owner 后，锁会直接交接给队首的公平等待者
	if ok, _ := b.Release(ctx, "k", "owner"); !ok {
		t.Fatal("释放失败")
	}
	if !waitCond(func() bool { return b.Waiters("k") == 0 }, 3*time.Second) {
		t.Fatal("未交接给公平等待者")
	}
	// 此刻锁被 fair-waiter 持有：非公平 Acquire 必须失败
	if _, ok, _ := b.Acquire(ctx, "k", "cut-in-line", time.Hour); ok {
		t.Fatal("插队成功 —— 公平性被破坏")
	}

	// 公平等待者归还后，非公平获取恢复
	if ok, _ := b.Release(ctx, "k", "fair-waiter"); !ok {
		t.Fatal("归还失败")
	}
	if _, ok, _ := b.Acquire(ctx, "k", "normal", time.Hour); !ok {
		t.Fatal("应可获取")
	}
}

// 公平锁在 ttl 过期后也必须能被别人拿走（租约语义优先于公平性）。
func TestFairExpiryAllowsNewHolder(t *testing.T) {
	b, clk := newClockBackend()
	ctx := context.Background()

	if _, ok, err := b.AcquireFair(ctx, "k", "A", time.Second); !ok || err != nil {
		t.Fatalf("AcquireFair: %v", err)
	}
	clk.advance(2 * time.Second)

	// 过期后别人可以直接拿到（无等待者时走快速路径）
	if _, ok, err := b.Acquire(ctx, "k", "B", time.Second); !ok || err != nil {
		t.Fatalf("过期后获取失败: ok=%v err=%v", ok, err)
	}
	// 老 token 释放无效
	if ok, _ := b.Release(ctx, "k", "A"); ok {
		t.Fatal("过期持有者不应释放成功")
	}
}

// ---------------------------------------------------------------------------
// 与 Client 集成（参考实现必须能跑通完整契约）
// ---------------------------------------------------------------------------

func TestIntegrationWithClient(t *testing.T) {
	b := memory.New()
	c := xdistlock.New(b,
		xdistlock.WithDefaultTTL(300*time.Millisecond),
		xdistlock.WithDefaultAutoRenew(true),
		xdistlock.WithDefaultRetry(5*time.Millisecond, 0),
	)
	defer c.Close()
	ctx := context.Background()

	// 看门狗让锁跨过多个租约仍然有效
	lk, err := c.Lock(ctx, "order:1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	time.Sleep(800 * time.Millisecond)
	if !lk.IsHeld() {
		t.Fatal("看门狗未能保住锁")
	}
	if f := lk.FencingToken(); f == 0 {
		t.Fatal("fencing 为 0")
	}
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if b.ActiveKeys() != 0 {
		t.Fatalf("释放后 ActiveKeys = %d", b.ActiveKeys())
	}

	// 公平模式端到端
	lk2, err := c.Lock(ctx, "fair", xdistlock.WithFair(true), xdistlock.WithTTL(time.Second))
	if err != nil {
		t.Fatalf("公平 Lock: %v", err)
	}
	if err := lk2.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	// 快照
	if infos, err := c.Snapshot(ctx, ""); err != nil || len(infos) != 0 {
		t.Fatalf("Snapshot = %v, %v", infos, err)
	}
}

// ---------------------------------------------------------------------------
// 模型式模糊测试：锁定「不抢锁 / 不误删 / fencing 单调」三条安全性质
// ---------------------------------------------------------------------------

// 用朴素模型逐步比对后端行为。任意操作序列下都必须满足：
//
//  1. 空闲的 key 必须能被获取；
//  2. 被持有的 key 绝不能被第二个 token 获取（不抢锁）；
//  3. 只有当前持有者能成功释放，且释放后状态必须回到空闲（不误删、不错乱）；
//  4. Refresh 成功当且仅当调用者就是当前持有者；
//  5. fencing token 严格单调递增。
//
// 这些性质一旦破坏就是「两个进程同时认为自己持有锁」，属于最严重的事故。
func FuzzMemoryTokenSafety(f *testing.F) {
	f.Add([]byte{0, 1, 2, 0})
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{1, 2, 1, 2})

	f.Fuzz(func(t *testing.T, ops []byte) {
		b := memory.New()
		ctx := context.Background()
		const key = "k"

		holder := "" // 模型：当前持有者（"" = 空闲）
		var maxFence uint64

		for i, op := range ops {
			token := fmt.Sprintf("t%d", op%4)

			switch op % 3 {
			case 0: // Acquire
				fence, ok, err := b.Acquire(ctx, key, token, time.Hour)
				if err != nil {
					t.Fatalf("第 %d 步: Acquire 出错: %v", i, err)
				}
				if ok {
					if holder != "" {
						t.Fatalf("第 %d 步: 抢到了别人持有的锁（holder=%s, 我=%s）", i, holder, token)
					}
					if fence <= maxFence {
						t.Fatalf("第 %d 步: fencing 未单调递增 %d → %d", i, maxFence, fence)
					}
					holder = token
					maxFence = fence
				} else if holder == "" {
					t.Fatalf("第 %d 步: key 空闲却获取失败", i)
				}

			case 1: // Release
				ok, err := b.Release(ctx, key, token)
				if err != nil {
					t.Fatalf("第 %d 步: Release 出错: %v", i, err)
				}
				if ok != (holder == token && holder != "") {
					t.Fatalf("第 %d 步: Release ok=%v 但模型认为 holder=%q 我=%q", i, ok, holder, token)
				}
				if ok {
					holder = ""
				}
				if holder != "" {
					// 未被释放时，锁必须仍属于 holder
					if _, ok2, _ := b.Acquire(ctx, key, "probe", time.Hour); ok2 {
						t.Fatalf("第 %d 步: 释放失败路径后锁被抢走", i)
					}
				}

			default: // Refresh
				ok, err := b.Refresh(ctx, key, token, time.Hour)
				if err != nil {
					t.Fatalf("第 %d 步: Refresh 出错: %v", i, err)
				}
				if ok != (holder == token && holder != "") {
					t.Fatalf("第 %d 步: Refresh ok=%v 但模型认为 holder=%q 我=%q", i, ok, holder, token)
				}
			}

			// 不变式：后端当前持有者必须与模型一致
			infos, err := b.Snapshot(ctx, "")
			if err != nil {
				t.Fatalf("第 %d 步: Snapshot 出错: %v", i, err)
			}
			if holder == "" {
				if len(infos) != 0 {
					t.Fatalf("第 %d 步: 模型认为空闲但后端有 %d 把锁", i, len(infos))
				}
			} else {
				if len(infos) != 1 || infos[0].Token != holder {
					t.Fatalf("第 %d 步: 模型 holder=%q 与后端 %+v 不一致", i, holder, infos)
				}
			}
		}
	})
}

// 公平模式的**有界确定性压力测试**。
//
// 为什么不写成 fuzz：每次 AcquireFair 在「锁被持有」时都要等到 ctx 超时才返回，
// 单次执行的代价与输入长度线性相关（一个几千字节的输入要跑上百秒），
// 而且「锁被交接」与「ctx 到期」同时就绪时 select 的随机选择会引入时序依赖，
// 让 fuzz 变成 flaky 的来源。这里改用有限的循环 + 极短超时，
// 保留全部安全性质（不抢锁 / 不误删 / fencing 单调 / 失败不残留），
// 代价是确定的（200 次迭代 ≈ 0.6s）。
func TestFairSafetyStress(t *testing.T) {
	b := memory.New()
	ctx := context.Background()

	var lastFence uint64
	for i := 0; i < 200; i++ {
		token := fmt.Sprintf("t%d", i)

		fence, ok, err := b.AcquireFair(ctx, "k", token, time.Hour)
		if err != nil || !ok {
			t.Fatalf("第 %d 轮: AcquireFair ok=%v err=%v", i, ok, err)
		}
		if fence <= lastFence {
			t.Fatalf("第 %d 轮: fencing 非单调 %d → %d", i, lastFence, fence)
		}
		lastFence = fence

		// 已持有期间：别人获取必须失败（排队后超时）
		cctx, cancel := context.WithTimeout(ctx, 3*time.Millisecond)
		other, ok2, err2 := b.AcquireFair(cctx, "k", "intruder", time.Hour)
		cancel()
		if ok2 {
			t.Fatalf("第 %d 轮: 抢锁成功（fencing=%d）", i, other)
		}
		if !errors.Is(err2, context.DeadlineExceeded) {
			t.Fatalf("第 %d 轮: 预期超时，实际 %v", i, err2)
		}
		if n := b.Waiters("k"); n != 0 {
			t.Fatalf("第 %d 轮: 超时后残留 %d 个等待者", i, n)
		}
		// 后端仍应只属于我们
		if got := currentToken(t, b, ctx); got != token {
			t.Fatalf("第 %d 轮: 后端持有 %q，期望 %q", i, got, token)
		}
		// 别人释放必须失败（不误删）
		if ok3, _ := b.Release(ctx, "k", "intruder"); ok3 {
			t.Fatalf("第 %d 轮: 非持有者释放成功（误删）", i)
		}
		// 持有者释放必须成功
		if ok4, err4 := b.Release(ctx, "k", token); err4 != nil || !ok4 {
			t.Fatalf("第 %d 轮: 持有者释放失败 ok=%v err=%v", i, ok4, err4)
		}
		if n := b.ActiveKeys(); n != 0 {
			t.Fatalf("第 %d 轮: 释放后残留 %d 把锁", i, n)
		}
	}
}

// currentToken 读取后端当前持有者的 token（空串表示空闲）。
func currentToken(t *testing.T, b *memory.Backend, ctx context.Context) string {
	t.Helper()
	infos, err := b.Snapshot(ctx, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(infos) == 0 {
		return ""
	}
	return infos[0].Token
}
