// Package memory 提供 xdistlock 的**进程内**参考实现。
//
// # ⚠️ 严重警告：绝不用于跨进程场景
//
// 本实现只在**单个进程**内生效。以下场景**完全无效**：
//
//	✗ 同一台机器上的多个进程（例如多副本 Docker、多个 go run）
//	✗ 多台机器
//	✗ 任何形式的「水平扩容后的同一份数据」
//
// 它存在的意义有三个：
//
//  1. **单测**：不需要起 Redis/etcd 就能测业务代码里的加锁逻辑；
//  2. **参考实现**：作为 xdistlock.Backend 语义的规范范例（安全释放、fencing、公平队列）；
//  3. **单进程内需要「带 TTL 与 fencing 的锁」**：这类需求用 xlock 也能做，
//     但如果代码已经统一走 xdistlock 接口，用它避免为一个场景引入 Redis。
//
// 需要真正的分布式互斥时，请使用 xdistlock/redis 或 xdistlock/etcd。
//
// 并发安全。
package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhangwenxing/gokit/xdistlock"
)

// 编译期断言：必须同时满足 Backend / FairBackend / Snapshotter。
var (
	_ xdistlock.Backend     = (*Backend)(nil)
	_ xdistlock.FairBackend = (*Backend)(nil)
	_ xdistlock.Snapshotter = (*Backend)(nil)
)

// waiter 是一个公平锁等待者。
//
// 携带 token/ttl 的原因：公平交接由**释放方**直接授予，而不是唤醒后让等待者自己去抢 ——
// 后者会在唤醒与竞争之间产生插队窗口，破坏公平性。
type waiter struct {
	token string
	ttl   time.Duration
	ch    chan struct{}
}

// entry 是单个 key 的锁状态。
type entry struct {
	token   string // 空串表示未被持有
	expiry  time.Time
	fencing uint64
	waiters []waiter // 公平模式下的 FIFO 等待队列
}

// Backend 是进程内的分布式锁后端。
type Backend struct {
	mu      sync.Mutex
	locks   map[string]*entry
	fencing uint64 // 全局单调递增的 fencing 计数器
	now     func() time.Time
	acquire uint64 // 调用计数，用于触发周期性 GC
	closed  bool
}

// Option 是 memory 后端的选项。
type Option func(*Backend)

// WithClock 注入自定义时钟，用于在测试中确定性地模拟租约过期。
//
// ⚠️ 仅用于测试：生产环境请勿注入非单调时钟。
func WithClock(fn func() time.Time) Option {
	return func(b *Backend) {
		if fn != nil {
			b.now = fn
		}
	}
}

// New 创建进程内后端。
func New(opts ...Option) *Backend {
	b := &Backend{
		locks: make(map[string]*entry),
		now:   time.Now,
	}
	for _, o := range opts {
		if o != nil {
			o(b)
		}
	}
	return b
}

// Name 实现 Backend。
func (b *Backend) Name() string { return "memory" }

// Close 实现 Backend。
func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	b.locks = make(map[string]*entry)
	b.mu.Unlock()
	return nil
}

// gcInterval 每多少次调用做一次全表 GC。
const gcInterval = 1024

// ensureLocked 取（或创建）key 的条目，并做周期性 GC。
//
// 必须在持有 b.mu 时调用。
func (b *Backend) ensureLocked(key string) *entry {
	b.acquire++
	if b.acquire%gcInterval == 0 {
		now := b.now()
		for k, e := range b.locks {
			if e.token == "" && len(e.waiters) == 0 && now.After(e.expiry) {
				delete(b.locks, k)
			}
		}
	}
	e, ok := b.locks[key]
	if !ok {
		e = &entry{}
		b.locks[key] = e
	}
	return e
}

// freeLocked 判断条目当前是否可被获取（未持有或已过期）。
func (b *Backend) freeLocked(e *entry) bool {
	return e.token == "" || !b.now().Before(e.expiry)
}

// heldLocked 判断条目当前是否**真的被持有**（token 匹配且尚未过期）。
//
// 关键区别：租约过期的锁在语义上已经「没人持有」，只是条目还没被 GC 清掉。
// Snapshot / ActiveKeys 必须按本函数过滤，否则会报出「负 TTL 的僵尸锁」，
// 也会与真实 Redis（过期即自动删除）的行为不一致。
func (b *Backend) heldLocked(e *entry) bool {
	return e.token != "" && b.now().Before(e.expiry)
}

// grantLocked 把锁授予 token，并分配新的 fencing token。
func (b *Backend) grantLocked(e *entry, token string, ttl time.Duration) uint64 {
	b.fencing++
	e.token = token
	e.fencing = b.fencing
	e.expiry = b.now().Add(ttl)
	return e.fencing
}

// handoffLocked 把锁交给队首等待者（仅在空闲时）。
func (b *Backend) handoffLocked(e *entry) {
	if len(e.waiters) == 0 || !b.freeLocked(e) {
		return
	}
	w := e.waiters[0]
	e.waiters = e.waiters[1:]
	b.grantLocked(e, w.token, w.ttl)
	close(w.ch)
}

// removeWaiter 从队列中摘除指定等待者；返回是否成功摘除。
func (e *entry) removeWaiter(ch chan struct{}) bool {
	for i, w := range e.waiters {
		if w.ch == ch {
			e.waiters = append(e.waiters[:i], e.waiters[i+1:]...)
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Backend 实现
// ---------------------------------------------------------------------------

// Acquire 实现 xdistlock.Backend（非公平路径）。
//
// 若当前有公平等待者排队，本调用会返回 false 让路 ——
// 否则「公平排队 + 普通插队」混用时公平性会被破坏。
func (b *Backend) Acquire(ctx context.Context, key, token string, ttl time.Duration) (uint64, bool, error) {
	if b == nil {
		return 0, false, xdistlock.ErrNilBackend
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if token == "" {
		return 0, false, fmt.Errorf("memory: token 不能为空")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, false, xdistlock.ErrClosed
	}

	e := b.ensureLocked(key)
	if !b.freeLocked(e) || len(e.waiters) > 0 {
		return 0, false, nil
	}
	return b.grantLocked(e, token, ttl), true, nil
}

// AcquireFair 实现 xdistlock.FairBackend：按到达顺序获得锁。
func (b *Backend) AcquireFair(ctx context.Context, key, token string, ttl time.Duration) (uint64, bool, error) {
	if b == nil {
		return 0, false, xdistlock.ErrNilBackend
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if token == "" {
		return 0, false, fmt.Errorf("memory: token 不能为空")
	}
	if ttl <= 0 {
		return 0, false, fmt.Errorf("memory: ttl 必须为正")
	}

	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return 0, false, xdistlock.ErrClosed
		}
		e := b.ensureLocked(key)

		// 空闲且无人排队 → 直接拿
		if b.freeLocked(e) && len(e.waiters) == 0 {
			f := b.grantLocked(e, token, ttl)
			b.mu.Unlock()
			return f, true, nil
		}

		// 排队等待交接
		ch := make(chan struct{})
		e.waiters = append(e.waiters, waiter{token: token, ttl: ttl, ch: ch})
		b.mu.Unlock()

		// 优先判定「是否已被交接」：授予与 ctx 到期可能同时就绪，
		// 此时应当认定获取成功（锁已经在我们手里了），而不是返回 ctx 错误后
		// 再把它归还 —— 后者虽然也正确，但白白浪费一次获取机会。
		select {
		case <-ch:
			b.mu.Lock()
			f := e.fencing
			b.mu.Unlock()
			return f, true, nil
		default:
		}

		select {
		case <-ch:
			// 释放方已把锁直接授予我们（token/fencing/expiry 均已写好）
			b.mu.Lock()
			f := e.fencing
			b.mu.Unlock()
			return f, true, nil

		case <-ctx.Done():
			b.mu.Lock()
			removed := e.removeWaiter(ch)
			if !removed {
				// 唤醒与取消同时到达：锁已被授予我们 → 必须立刻归还并交接给下一位
				b.grantLockedCleanupLocked(e, token)
				b.handoffLocked(e)
			}
			b.mu.Unlock()
			return 0, false, ctx.Err()
		}
	}
}

// grantLockedCleanupLocked 把「已授予但我们放弃」的锁置回空闲。
func (b *Backend) grantLockedCleanupLocked(e *entry, token string) {
	if e.token == token {
		e.token = ""
		e.expiry = time.Time{}
	}
}

// Refresh 实现 xdistlock.Backend。
func (b *Backend) Refresh(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	if b == nil {
		return false, xdistlock.ErrNilBackend
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false, xdistlock.ErrClosed
	}

	e, ok := b.locks[key]
	if !ok {
		return false, nil
	}
	// 仅在「仍是我们的 token」且「尚未过期」时延期。
	// 注意：已过期但 token 还没被别人覆盖时也**不能**延期 ——
	// 因为在我们过期的这段时间里，别人可能已经认为这把锁空闲了。
	if e.token != token || !b.now().Before(e.expiry) {
		return false, nil
	}
	e.expiry = b.now().Add(ttl)
	return true, nil
}

// Release 实现 xdistlock.Backend 的 compare-and-delete 语义。
func (b *Backend) Release(ctx context.Context, key, token string) (bool, error) {
	if b == nil {
		return false, xdistlock.ErrNilBackend
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false, xdistlock.ErrClosed
	}

	e, ok := b.locks[key]
	if !ok || e.token != token {
		// 锁已不属于我们（被别人抢走 / 已过期被别人获取）→ 绝不误删
		return false, nil
	}
	e.token = ""
	e.expiry = time.Time{}
	b.handoffLocked(e) // 公平队列下直接交接给下一位
	return true, nil
}

// Snapshot 实现 xdistlock.Snapshotter。
//
// prefix 为空时列出全部；否则只列出以 prefix 开头的 key。
func (b *Backend) Snapshot(ctx context.Context, prefix string) ([]xdistlock.LockInfo, error) {
	if b == nil {
		return nil, xdistlock.ErrNilBackend
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	out := make([]xdistlock.LockInfo, 0, len(b.locks))
	for k, e := range b.locks {
		if !b.heldLocked(e) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		out = append(out, xdistlock.LockInfo{
			Key:     k,
			Token:   e.token,
			TTL:     e.expiry.Sub(now),
			Fencing: e.fencing,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 测试辅助（导出，便于业务侧写单测）
// ---------------------------------------------------------------------------

// ActiveKeys 返回当前**真正被持有**的 key 数量（已过期的租约不计入，
// 这与 Redis 的自动过期语义一致），用于测试断言「锁全部释放了」。
func (b *Backend) ActiveKeys() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, e := range b.locks {
		if b.heldLocked(e) {
			n++
		}
	}
	return n
}

// Waiters 返回指定 key 上公平队列的等待者数量。
func (b *Backend) Waiters(key string) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.locks[key]; ok {
		return len(e.waiters)
	}
	return 0
}

// FencingCounter 返回累计发放的 fencing token 数。
func (b *Backend) FencingCounter() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fencing
}
