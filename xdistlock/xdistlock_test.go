package xdistlock_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xdistlock"
	"github.com/zhangwenxing/gokit/xdistlock/memory"
	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 可编程的假后端
// ---------------------------------------------------------------------------

// fakeBackend 是可编程的 Backend，用于精确构造「基础设施故障」「锁被别人抢走」
// 等真实存储下难以稳定复现的场景。
type fakeBackend struct {
	mu      sync.Mutex
	keys    map[string]string // key → token（"" 表示空闲）
	fencing uint64

	acquireCalls int
	refreshCalls int
	releaseCalls int

	// 钩子：非 nil 时优先使用
	onAcquire func(key, token string) (uint64, bool, error)
	onRefresh func(key, token string) (bool, error)
	onRelease func(key, token string) (bool, error)

	closed bool
}

func newFake() *fakeBackend {
	return &fakeBackend{keys: make(map[string]string)}
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) Acquire(ctx context.Context, key, token string, ttl time.Duration) (uint64, bool, error) {
	f.mu.Lock()
	f.acquireCalls++
	hook := f.onAcquire
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if hook != nil {
		return hook(key, token)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.keys[key]; ok && cur != "" {
		return 0, false, nil
	}
	f.fencing++
	f.keys[key] = token
	return f.fencing, true, nil
}

func (f *fakeBackend) Refresh(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	f.refreshCalls++
	hook := f.onRefresh
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, err
	}
	if hook != nil {
		return hook(key, token)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[key] == token && token != "", nil
}

func (f *fakeBackend) Release(ctx context.Context, key, token string) (bool, error) {
	f.mu.Lock()
	f.releaseCalls++
	hook := f.onRelease
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, err
	}
	if hook != nil {
		return hook(key, token)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys[key] != token || token == "" {
		return false, nil
	}
	delete(f.keys, key)
	return true, nil
}

// steal 模拟「我们的租约过期后被别人拿到锁」。
func (f *fakeBackend) steal(key, newToken string) {
	f.mu.Lock()
	f.keys[key] = newToken
	f.fencing++
	f.mu.Unlock()
}

func (f *fakeBackend) holder(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[key]
}

func (f *fakeBackend) calls() (acquire, refresh, release int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquireCalls, f.refreshCalls, f.releaseCalls
}

func newTestClient(t *testing.T, opts ...xdistlock.Option) (*xdistlock.Client, *fakeBackend) {
	t.Helper()
	f := newFake()
	c := xdistlock.New(f, opts...)
	t.Cleanup(func() { _ = c.Close() })
	return c, f
}

func ctxT(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// ---------------------------------------------------------------------------
// 基本获取 / 释放
// ---------------------------------------------------------------------------

func TestLockUnlock(t *testing.T) {
	c, f := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "order:1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if lk.Key() != "order:1" {
		t.Fatalf("Key = %q", lk.Key())
	}
	if lk.Token() == "" {
		t.Fatal("Token 不应为空")
	}
	if lk.FencingToken() == 0 {
		t.Fatal("FencingToken 不应为 0")
	}
	if !lk.IsHeld() {
		t.Fatal("IsHeld 应为 true")
	}
	if exp := lk.Expiry(); exp.IsZero() || !exp.After(time.Now()) {
		t.Fatalf("Expiry = %v", exp)
	}
	if f.holder("order:1") != lk.Token() {
		t.Fatal("后端持有的 token 与句柄不一致")
	}
	if got := lk.String(); got == "" || got == "Lock(nil)" {
		t.Fatalf("String = %q", got)
	}

	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if lk.IsHeld() {
		t.Fatal("释放后 IsHeld 应为 false")
	}
	if f.holder("order:1") != "" {
		t.Fatal("后端未释放")
	}

	// 重复释放必须报错（尽早暴露 bug），且不触碰后端
	before, _, _ := f.calls()
	if err := lk.Unlock(ctx); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("重复 Unlock = %v, want ErrNotHeld", err)
	}
	after, _, _ := f.calls()
	if after != before {
		t.Fatal("重复 Unlock 不应调用后端")
	}
}

func TestFencingMonotonic(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	var last uint64
	seen := map[uint64]bool{}
	for i := 0; i < 20; i++ {
		lk, err := c.Lock(ctx, "k")
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		f := lk.FencingToken()
		if f <= last {
			t.Fatalf("fencing 未单调递增: %d → %d", last, f)
		}
		if seen[f] {
			t.Fatalf("fencing 重复: %d", f)
		}
		seen[f] = true
		last = f
		if err := lk.Unlock(ctx); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	}
}

func TestLockConflictAndTryLock(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = lk.Unlock(ctx) }()

	// TryLock：被占用时返回 (nil,false,nil)，不算错误
	got, ok, err := c.TryLock(ctx, "k")
	if err != nil {
		t.Fatalf("TryLock 不应返回错误: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("TryLock = (%v,%v), want (nil,false)", got, ok)
	}

	// WithBlocking(false)：返回 ErrLocked
	_, blockedErr := c.Lock(ctx, "k", xdistlock.WithBlocking(false))
	if !errors.Is(blockedErr, xdistlock.ErrLocked) {
		t.Fatalf("阻塞关闭时 = %v, want ErrLocked", blockedErr)
	}
	if xerr.CodeOf(blockedErr) != xerr.CodeConflict {
		t.Fatalf("错误码 = %v", xerr.CodeOf(blockedErr))
	}

	// WithRetry(_, 3)：只试 3 次
	f := newFake()
	c2 := xdistlock.New(f)
	holder, err := c2.Lock(ctx, "k2")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = holder.Unlock(ctx) }()
	before, _, _ := f.calls() // holder 的首次获取也计入，因此用增量断言
	_, err = c2.Lock(ctx, "k2", xdistlock.WithRetry(time.Millisecond, 3))
	if !errors.Is(err, xdistlock.ErrLocked) {
		t.Fatalf("重试耗尽 = %v, want ErrLocked", err)
	}
	if a, _, _ := f.calls(); a-before != 3 {
		t.Fatalf("重试尝试次数 = %d, want 3", a-before)
	}

	// 释放后 TryLock 成功
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	lk2, ok, err := c.TryLock(ctx, "k")
	if err != nil || !ok {
		t.Fatalf("TryLock = (%v,%v)", ok, err)
	}
	_ = lk2.Unlock(ctx)
}

func TestWaitTimeoutAndCtxCancel(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = lk.Unlock(ctx) }()

	// 等待超时
	start := time.Now()
	_, err = c.Lock(ctx, "k", xdistlock.WithWaitTimeout(80*time.Millisecond),
		xdistlock.WithRetry(10*time.Millisecond, 0))
	if !errors.Is(err, xdistlock.ErrWaitTimeout) {
		t.Fatalf("错误 = %v, want ErrWaitTimeout", err)
	}
	if xerr.CodeOf(err) != xerr.CodeTimeout {
		t.Fatalf("错误码 = %v", xerr.CodeOf(err))
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("过早超时: %v", elapsed)
	}
	meta := xerr.MetaOf(err)
	if meta["attempts"] == nil || meta["key"] != "k" {
		t.Fatalf("元数据缺失: %v", meta)
	}

	// ctx 取消
	cctx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	_, err = c.Lock(cctx, "k", xdistlock.WithRetry(10*time.Millisecond, 0))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v, want DeadlineExceeded", err)
	}

	// 已取消的 ctx 快速失败
	dead, dcancel := context.WithCancel(ctx)
	dcancel()
	if _, err := c.Lock(dead, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v, want Canceled", err)
	}

	// TryLock 已取消
	if _, _, err := c.TryLock(dead, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("TryLock 错误 = %v", err)
	}
}

// 等待期间锁被释放 → 应成功获取。
func TestWaitThenAcquire(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	got := make(chan *xdistlock.Lock, 1)
	errc := make(chan error, 1)
	go func() {
		l, err := c.Lock(ctx, "k", xdistlock.WithRetry(5*time.Millisecond, 0))
		if err != nil {
			errc <- err
			return
		}
		got <- l
	}()

	time.Sleep(50 * time.Millisecond)
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	select {
	case l := <-got:
		if err := l.Unlock(ctx); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	case err := <-errc:
		t.Fatalf("等待者失败: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("等待者未被唤醒")
	}
}

// ---------------------------------------------------------------------------
// 安全释放（本包最关键的语义）
// ---------------------------------------------------------------------------

// 锁被别人抢走后再 Unlock：绝不能删掉别人的锁。
func TestUnlockNeverDeletesOthersLock(t *testing.T) {
	c, f := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// 模拟租约过期后 B 拿到了锁
	f.steal("k", "token-of-B")

	err = lk.Unlock(ctx)
	if !errors.Is(err, xdistlock.ErrLockLost) {
		t.Fatalf("Unlock = %v, want ErrLockLost", err)
	}
	if h := f.holder("k"); h != "token-of-B" {
		t.Fatalf("别人的锁被删掉了！holder = %q", h)
	}
}

// 续约失败（锁已不属于我）→ 判定丢锁 + OnLost 回调 + IsHeld=false + Unlock 变 no-op。
func TestRefreshDetectsLossAndOnLost(t *testing.T) {
	var lostCalled int
	var lostLock *xdistlock.Lock

	f := newFake()
	c := xdistlock.New(f, xdistlock.WithOnEvent(nil))
	defer c.Close()
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k", xdistlock.WithOnLost(func(l *xdistlock.Lock) {
		lostCalled++
		lostLock = l
	}))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// 锁被别人抢走
	f.steal("k", "B")

	if err := lk.Refresh(ctx); !errors.Is(err, xdistlock.ErrLockLost) {
		t.Fatalf("Refresh = %v, want ErrLockLost", err)
	}
	if lk.IsHeld() {
		t.Fatal("丢锁后 IsHeld 应为 false")
	}
	if lostCalled != 1 {
		t.Fatalf("OnLost 调用次数 = %d, want 1", lostCalled)
	}
	if lostLock != lk {
		t.Fatal("OnLost 收到的句柄不对")
	}
	// 再次 Refresh 返回 ErrNotHeld（已经丢了）
	if err := lk.Refresh(ctx); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("再次 Refresh = %v, want ErrNotHeld", err)
	}
	// OnLost 只触发一次
	if err := lk.Unlock(ctx); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("丢锁后 Unlock = %v, want ErrNotHeld（应为 no-op）", err)
	}
	if lostCalled != 1 {
		t.Fatalf("OnLost 被重复触发: %d", lostCalled)
	}
	if h := f.holder("k"); h != "B" {
		t.Fatalf("丢锁后的 Unlock 不该触碰后端, holder = %q", h)
	}
}

// 基础设施错误（网络抖动）不应判定为丢锁 —— 否则轻微抖动就会让业务白白放弃。
func TestRefreshInfraErrorNotCountedAsLost(t *testing.T) {
	f := newFake()
	f.onRefresh = func(string, string) (bool, error) {
		return false, errors.New("连接超时")
	}
	c := xdistlock.New(f)
	defer c.Close()

	lk, err := c.Lock(context.Background(), "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := lk.Refresh(context.Background()); err == nil {
		t.Fatal("应返回基础设施错误")
	}
	if !lk.IsHeld() {
		t.Fatal("基础设施错误不应判定为丢锁")
	}
	if c.Metrics().LocksLost != 0 {
		t.Fatalf("LocksLost = %d, want 0", c.Metrics().LocksLost)
	}
	if c.Metrics().RenewFail != 1 {
		t.Fatalf("RenewFail = %d, want 1", c.Metrics().RenewFail)
	}
}

// ---------------------------------------------------------------------------
// 看门狗
// ---------------------------------------------------------------------------

// 看门狗必须在租约到期前续约，让长任务不丢锁。
func TestWatchdogKeepsLockAlive(t *testing.T) {
	c, f := newTestClient(t, xdistlock.WithDefaultAutoRenew(true))
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k", xdistlock.WithTTL(120*time.Millisecond))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	token := lk.Token()

	// 睡过 3 个租约，期间看门狗应持续续约
	time.Sleep(400 * time.Millisecond)

	if !lk.IsHeld() {
		t.Fatal("看门狗未能保住锁")
	}
	if h := f.holder("k"); h != token {
		t.Fatalf("锁已被他人持有: %q", h)
	}
	if _, refresh, _ := f.calls(); refresh < 2 {
		t.Fatalf("续约次数 = %d, 期望 >= 2", refresh)
	}
	if c.Metrics().RenewOK < 2 {
		t.Fatalf("RenewOK = %d", c.Metrics().RenewOK)
	}

	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

// 看门狗续约失败 → 自动判定丢锁并回调。
func TestWatchdogDetectsLostLock(t *testing.T) {
	f := newFake()
	var lostOnce sync.Once
	lost := make(chan struct{})

	c := xdistlock.New(f)
	defer c.Close()

	lk, err := c.Lock(context.Background(), "k",
		xdistlock.WithTTL(60*time.Millisecond),
		xdistlock.WithAutoRenew(true, 20*time.Millisecond),
		xdistlock.WithOnLost(func(*xdistlock.Lock) { lostOnce.Do(func() { close(lost) }) }),
	)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// 让后端认为锁已不属于我们
	f.mu.Lock()
	f.onRefresh = func(string, string) (bool, error) { return false, nil }
	f.mu.Unlock()

	select {
	case <-lost:
	case <-time.After(3 * time.Second):
		t.Fatal("看门狗未检测到丢锁")
	}
	if lk.IsHeld() {
		t.Fatal("IsHeld 应为 false")
	}
}

// ★ 看门狗必须随 Unlock 停止：否则 goroutine 泄漏。
func TestWatchdogStopsOnUnlockNoLeak(t *testing.T) {
	c, _ := newTestClient(t, xdistlock.WithDefaultAutoRenew(true))
	ctx := context.Background()

	// 预热，避免首次调用引入的运行时 goroutine 干扰计数
	for i := 0; i < 3; i++ {
		lk, err := c.Lock(ctx, "warm", xdistlock.WithTTL(150*time.Millisecond))
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if err := lk.Unlock(ctx); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	}

	runtime.GC()
	base := runtime.NumGoroutine()

	for i := 0; i < 40; i++ {
		lk, err := c.Lock(ctx, fmt.Sprintf("k%d", i), xdistlock.WithTTL(150*time.Millisecond))
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if err := lk.Unlock(ctx); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	}

	// 看门狗退出需要一点时间
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= base+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine 泄漏: base=%d now=%d（看门狗未停止）", base, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Unlock 必须等待看门狗真正退出，避免「释放后仍有一次续约」。
func TestUnlockWaitsWatchdogExit(t *testing.T) {
	f := newFake()
	c := xdistlock.New(f)
	defer c.Close()

	lk, err := c.Lock(context.Background(), "k",
		xdistlock.WithTTL(300*time.Millisecond),
		xdistlock.WithAutoRenew(true, 10*time.Millisecond))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // 让看门狗至少跑一轮

	if err := lk.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	// 释放后不应再有续约：记录此刻的续约次数，等一会必须不变
	_, refreshAfterUnlock, _ := f.calls()
	time.Sleep(120 * time.Millisecond)
	if _, refreshLater, _ := f.calls(); refreshLater != refreshAfterUnlock {
		t.Fatalf("Unlock 后仍有续约: %d → %d（看门狗未停）", refreshAfterUnlock, refreshLater)
	}
}

// ---------------------------------------------------------------------------
// Extend / 事件 / 统计 / 观测
// ---------------------------------------------------------------------------

func TestExtend(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k", xdistlock.WithTTL(time.Second))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = lk.Unlock(ctx) }()

	if lk.TTL() != time.Second {
		t.Fatalf("TTL = %v", lk.TTL())
	}
	before := lk.Expiry()

	if err := lk.Extend(5 * time.Second); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if lk.TTL() != 5*time.Second {
		t.Fatalf("Extend 后 TTL = %v", lk.TTL())
	}
	if !lk.Expiry().After(before) {
		t.Fatalf("Expiry 未延长: %v → %v", before, lk.Expiry())
	}

	// 非法参数
	if err := lk.Extend(0); err == nil {
		t.Fatal("Extend(0) 应报错")
	}
	if err := lk.Extend(-time.Second); err == nil {
		t.Fatal("Extend(-1s) 应报错")
	}

	// 释放后 Extend 报 ErrNotHeld
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := lk.Extend(time.Second); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("释放后 Extend = %v", err)
	}

	// nil 接收者
	var nilLock *xdistlock.Lock
	if nilLock.Key() != "" || nilLock.Token() != "" || nilLock.FencingToken() != 0 {
		t.Fatal("nil Lock 零值方法错误")
	}
	if nilLock.IsHeld() || nilLock.TTL() != 0 || !nilLock.Expiry().IsZero() {
		t.Fatal("nil Lock 状态方法错误")
	}
	if nilLock.String() != "Lock(nil)" {
		t.Fatalf("nil String = %q", nilLock.String())
	}
	if err := nilLock.Unlock(ctx); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("nil Unlock = %v", err)
	}
	if err := nilLock.Refresh(ctx); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("nil Refresh = %v", err)
	}
	if err := nilLock.Extend(time.Second); !errors.Is(err, xdistlock.ErrNotHeld) {
		t.Fatalf("nil Extend = %v", err)
	}
}

func TestEventsSequence(t *testing.T) {
	var mu sync.Mutex
	var types []xdistlock.EventType

	f := newFake()
	c := xdistlock.New(f, xdistlock.WithOnEvent(func(ev xdistlock.Event) {
		mu.Lock()
		types = append(types, ev.Type)
		mu.Unlock()
	}))
	defer c.Close()
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := lk.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []xdistlock.EventType{
		xdistlock.EventAcquireOK, xdistlock.EventRenewOK, xdistlock.EventRelease,
	}
	if len(types) != len(want) {
		t.Fatalf("事件序列 = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("事件序列 = %v, want %v", types, want)
		}
	}

	// 单次加锁可覆盖客户端级回调
	c2 := xdistlock.New(newFake(), xdistlock.WithOnEvent(func(xdistlock.Event) {
		t.Error("客户端级回调不应被调用")
	}))
	defer c2.Close()
	lk2, err := c2.Lock(ctx, "k", xdistlock.WithLockEvent(func(xdistlock.Event) {}))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	_ = lk2.Unlock(ctx)
}

func TestMetrics(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	if m := c.Metrics(); m != (xdistlock.Metrics{}) {
		t.Fatalf("初始指标应为零: %+v", m)
	}

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// 一次失败获取（非阻塞）
	if _, err := c.Lock(ctx, "k", xdistlock.WithBlocking(false)); err == nil {
		t.Fatal("预期失败")
	}
	if err := lk.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	m := c.Metrics()
	if m.AcquireAttempts != 2 || m.AcquireOK != 1 || m.AcquireFail != 1 {
		t.Fatalf("获取指标错误: %+v", m)
	}
	if m.RenewOK != 1 {
		t.Fatalf("RenewOK = %d", m.RenewOK)
	}
	if m.ReleaseOK != 1 {
		t.Fatalf("ReleaseOK = %d", m.ReleaseOK)
	}
	if m.HeldMax != 1 {
		t.Fatalf("HeldMax = %d", m.HeldMax)
	}
	if m.WaitTotal <= 0 {
		t.Fatalf("WaitTotal = %v", m.WaitTotal)
	}

	// 并发持有峰值
	c2, _ := newTestClient(t)
	l1, _ := c2.Lock(ctx, "a")
	l2, _ := c2.Lock(ctx, "b")
	if got := c2.Metrics().HeldMax; got != 2 {
		t.Fatalf("HeldMax = %d, want 2", got)
	}
	_ = l1.Unlock(ctx)
	_ = l2.Unlock(ctx)
}

func TestSnapshot(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	if _, err := c.Snapshot(ctx, ""); !errors.Is(err, xdistlock.ErrNotSupported) {
		t.Fatalf("fake 后端不支持 Snapshot, got %v", err)
	}

	// memory 后端支持
	mc := xdistlock.New(memory.New())
	defer mc.Close()
	lk, err := mc.Lock(ctx, "app:order:1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = lk.Unlock(ctx) }()

	infos, err := mc.Snapshot(ctx, "app:")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("Snapshot 条数 = %d", len(infos))
	}
	if infos[0].Key != "app:order:1" || infos[0].Token != lk.Token() {
		t.Fatalf("Snapshot 内容错误: %+v", infos[0])
	}
	if infos[0].TTL <= 0 {
		t.Fatalf("TTL = %v", infos[0].TTL)
	}

	// 前缀过滤
	if got, _ := mc.Snapshot(ctx, "other:"); len(got) != 0 {
		t.Fatalf("前缀过滤失效: %+v", got)
	}
	if got, _ := mc.Snapshot(ctx, ""); len(got) != 1 {
		t.Fatalf("空前缀应返回全部: %d", len(got))
	}
}

func TestClientLifecycle(t *testing.T) {
	// nil / 空 key / 关闭后
	var nilClient *xdistlock.Client
	if _, err := nilClient.Lock(context.Background(), "k"); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Client = %v", err)
	}
	if nilClient.BackendName() != "" {
		t.Fatal("nil BackendName 应为空")
	}
	if m := nilClient.Metrics(); m != (xdistlock.Metrics{}) {
		t.Fatalf("nil Metrics = %+v", m)
	}
	if err := nilClient.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	if _, _, err := nilClient.TryLock(context.Background(), "k"); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil TryLock = %v", err)
	}
	if _, err := nilClient.Snapshot(context.Background(), ""); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Snapshot = %v", err)
	}

	c := xdistlock.New(nil)
	if _, err := c.Lock(context.Background(), "k"); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil backend = %v", err)
	}

	c2, _ := newTestClient(t)
	if c2.BackendName() != "fake" {
		t.Fatalf("BackendName = %q", c2.BackendName())
	}
	if _, err := c2.Lock(context.Background(), ""); err == nil {
		t.Fatal("空 key 应报错")
	} else if xerr.CodeOf(err) != xerr.CodeMissingParam {
		t.Fatalf("错误码 = %v", xerr.CodeOf(err))
	}
	if _, err := c2.Lock(nil, "k"); err != nil { // nil ctx 视为 Background
		t.Fatalf("nil ctx: %v", err)
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
	if _, err := c2.Lock(context.Background(), "k"); !errors.Is(err, xdistlock.ErrClosed) {
		t.Fatalf("关闭后 = %v, want ErrClosed", err)
	}
	if _, _, err := c2.TryLock(context.Background(), "k"); !errors.Is(err, xdistlock.ErrClosed) {
		t.Fatalf("关闭后 TryLock = %v", err)
	}
}

// 公平锁需要后端支持：不支持时快速失败（而不是每次都失败一轮）。
func TestFairRequiresBackendSupport(t *testing.T) {
	c, f := newTestClient(t)
	_, err := c.Lock(context.Background(), "k", xdistlock.WithFair(true))
	if !errors.Is(err, xdistlock.ErrNotSupported) {
		t.Fatalf("错误 = %v, want ErrNotSupported", err)
	}
	if meta := xerr.MetaOf(err); meta["capability"] != "fair" || meta["backend"] != "fake" {
		t.Fatalf("元数据缺失: %v", meta)
	}
	// 必须快速失败：不能先试一次
	if a, _, _ := f.calls(); a != 0 {
		t.Fatalf("不公平的后端不应被调用: %d 次", a)
	}
}

func TestOptionsDefaults(t *testing.T) {
	f := newFake()
	// 客户端默认值可被单次选项覆盖
	c := xdistlock.New(f,
		xdistlock.WithDefaultTTL(90*time.Millisecond),
		xdistlock.WithDefaultAutoRenew(false),
	)
	defer c.Close()
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if lk.TTL() != 90*time.Millisecond {
		t.Fatalf("默认 TTL 未生效: %v", lk.TTL())
	}
	if err := lk.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	// 单次覆盖
	lk2, err := c.Lock(ctx, "k", xdistlock.WithTTL(500*time.Millisecond))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if lk2.TTL() != 500*time.Millisecond {
		t.Fatalf("单次 TTL = %v", lk2.TTL())
	}
	_ = lk2.Unlock(ctx)

	// 非法选项值应被忽略（不 panic、不产生 0 租约）
	c3 := xdistlock.New(newFake())
	defer c3.Close()
	lk3, err := c3.Lock(ctx, "k", xdistlock.WithTTL(0), xdistlock.WithRetry(0, 0),
		xdistlock.WithAttemptTimeout(-time.Second), xdistlock.WithWaitTimeout(-5))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if lk3.TTL() <= 0 {
		t.Fatalf("TTL = %v", lk3.TTL())
	}
	_ = lk3.Unlock(ctx)
}

func TestWithToken(t *testing.T) {
	c, f := newTestClient(t)
	ctx := context.Background()

	lk, err := c.Lock(ctx, "k", xdistlock.WithToken("order-service#3"))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if lk.Token() != "order-service#3" {
		t.Fatalf("Token = %q", lk.Token())
	}
	if f.holder("k") != "order-service#3" {
		t.Fatalf("后端 token = %q", f.holder("k"))
	}
	_ = lk.Unlock(ctx)
}

// 并发争抢：任何时刻最多一个持有者。
func TestConcurrentMutualExclusion(t *testing.T) {
	c, _ := newTestClient(t, xdistlock.WithDefaultRetry(2*time.Millisecond, 0))
	ctx := context.Background()

	var inCritical int32
	var violations int32
	var wg sync.WaitGroup
	for g := 0; g < 24; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				lk, err := c.Lock(ctx, "hot", xdistlock.WithWaitTimeout(5*time.Second))
				if err != nil {
					// 超时是可以接受的；其它错误才是问题
					if !errors.Is(err, xdistlock.ErrWaitTimeout) {
						t.Errorf("Lock: %v", err)
					}
					return
				}
				if n := atomic.AddInt32(&inCritical, 1); n != 1 {
					atomic.AddInt32(&violations, 1)
				}
				time.Sleep(50 * time.Microsecond)
				atomic.AddInt32(&inCritical, -1)
				if err := lk.Unlock(ctx); err != nil {
					t.Errorf("Unlock: %v", err)
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("并发争抢超时（疑似死锁）")
	}

	if violations != 0 {
		t.Fatalf("临界区被同时进入 %d 次 —— 互斥被破坏", violations)
	}
	m := c.Metrics()
	if m.AcquireOK == 0 {
		t.Fatal("没有任何成功获取")
	}
	if m.AcquireOK != m.ReleaseOK {
		t.Fatalf("获取 %d 次但只释放 %d 次（存在泄漏）", m.AcquireOK, m.ReleaseOK)
	}
}

func TestSentinelErrors(t *testing.T) {
	cases := []struct {
		err  error
		code xerr.Code
	}{
		{xdistlock.ErrLocked, xerr.CodeConflict},
		{xdistlock.ErrNotHeld, xerr.CodeStateInvalid},
		{xdistlock.ErrLockLost, xerr.CodeStateInvalid},
		{xdistlock.ErrWaitTimeout, xerr.CodeTimeout},
		{xdistlock.ErrNotSupported, xerr.CodeNotImplemented},
		{xdistlock.ErrNilBackend, xerr.CodeMissingParam},
		{xdistlock.ErrClosed, xerr.CodeStateInvalid},
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
		if !xerr.Is(c.err, c.err) {
			t.Fatalf("%v xerr.Is 失败", c.err)
		}
	}
	// 丢锁与未持有必须是**不同**的错误（调用方要区分「我早该停了」与「我调用错了」）
	if errors.Is(xdistlock.ErrLockLost, xdistlock.ErrNotHeld) {
		t.Fatal("ErrLockLost 与 ErrNotHeld 不应等价")
	}
}

func TestJitteredRetryBounded(t *testing.T) {
	// 重试间隔抖动应落在 [interval/2, interval] 内，且不为 0（避免忙等打爆后端）
	c, f := newTestClient(t)
	ctx := context.Background()
	holder, err := c.Lock(ctx, "k")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = holder.Unlock(ctx) }()

	before, _, _ := f.calls() // 排除 holder 的首次获取
	start := time.Now()
	_, err = c.Lock(ctx, "k", xdistlock.WithRetry(40*time.Millisecond, 3))
	if !errors.Is(err, xdistlock.ErrLocked) {
		t.Fatalf("错误 = %v", err)
	}
	elapsed := time.Since(start)
	// 3 次尝试 → 2 次等待，每次 >= 20ms
	if elapsed < 40*time.Millisecond {
		t.Fatalf("重试过快（可能未退避）: %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("重试过慢: %v", elapsed)
	}
	if a, _, _ := f.calls(); a-before != 3 {
		t.Fatalf("尝试次数 = %d, want 3", a-before)
	}
}
