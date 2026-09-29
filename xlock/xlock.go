// Package xlock 提供**进程内**的唯一资源锁（Named Lock / Keyed Mutex）。
//
// 核心语义：**同一个资源 key，同一时刻只有一个持有者**；不同 key 之间互不阻塞。
//
// 四种实现，按场景选择：
//
//	KeyedLocker      —— 按 key 精确互斥（map + 引用计数），最常用
//	ShardedLocker    —— 固定锁表分片，内存恒定，热点场景吞吐更高（代价：偶发假冲突）
//	ReentrantLocker  —— 可重入锁（同一 owner 重复加锁不阻塞）
//	ResourcePool     —— 同一类资源允许 N 并发（信号量语义）
//
// 边界（重要）：本包**只解决单进程内的互斥**。跨进程 / 跨机器请使用 xdistlock。
//
// 设计要点：
//
//  1. **永不泄漏内存**：KeyedLocker 用「引用计数归零即删除」维护 key 表；
//     ShardedLocker 用固定锁表，两者都不会随 key 数量无限增长。
//  2. **UnlockFunc 幂等**：重复调用不 panic、不破坏状态（重复次数记入 Stats.DoubleUnlock）。
//  3. **支持取消**：LockCtx 通过 per-key 的等待队列实现真正可取消，
//     不像 sync.Mutex 那样「取消后仍占着等待位」。
//  4. **Unlock 不返回业务错误**：`UnlockFunc` 的错误返回值仅为接口兼容保留，恒为 nil，
//     因此 `defer unlock()` 是安全的。
//
// 并发安全：本包所有类型都可并发使用。
package xlock

import (
	"context"
	"hash/fnv"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/indigo-apple/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 接口
// ---------------------------------------------------------------------------

// Unlocker 是「可释放的锁」的最小接口。
type Unlocker interface {
	Unlock() error
}

// UnlockFunc 把函数适配成 Unlocker，便于 `defer` 使用。
type UnlockFunc func() error

// Unlock 实现 Unlocker。
func (f UnlockFunc) Unlock() error {
	if f == nil {
		return nil
	}
	return f()
}

// Locker 是所有进程内锁实现的统一接口。
//
// 全部方法都**不会因为 key 为空串而 panic**；空 key 是一个合法的 key（对应「全局锁」）。
type Locker interface {
	// Lock 阻塞获取写锁（互斥）。
	Lock(key string) (UnlockFunc, error)
	// LockCtx 阻塞获取写锁，支持超时 / 取消。
	LockCtx(ctx context.Context, key string) (UnlockFunc, error)
	// TryLock 尝试获取写锁，立即返回。
	TryLock(key string) (UnlockFunc, bool)
	// RLock 阻塞获取读锁（同 key 的多个读者可并行）。
	RLock(key string) (UnlockFunc, error)
	// RLockCtx 阻塞获取读锁，支持超时 / 取消。
	//
	// 设计文档（DESIGN.md 5.10）只列了无 ctx 的 RLock；这里补上带取消的版本 ——
	// 「读锁等待无法取消」在实践中同样是 goroutine 堆积的来源。
	RLockCtx(ctx context.Context, key string) (UnlockFunc, error)
	// TryRLock 尝试获取读锁，立即返回。
	TryRLock(key string) (UnlockFunc, bool)
}

// 编译期断言：两个实现都必须满足 Locker。
var (
	_ Locker = (*KeyedLocker)(nil)
	_ Locker = (*ShardedLocker)(nil)
)

// ---------------------------------------------------------------------------
// 哨兵错误
// ---------------------------------------------------------------------------

var (
	// ErrLockTimeout 获取锁等待超过 WithWaitTimeout 设置的上限。
	ErrLockTimeout = xerr.NewC(xerr.CodeTimeout, "xlock: 获取锁超时")

	// ErrTooManyKeys 活跃 key 数超过 WithMaxKeys 设置的上限。
	ErrTooManyKeys = xerr.NewC(xerr.CodeResourceExhausted, "xlock: 活跃 key 数超过上限")

	// ErrNoGoroutineID 无法取得当前 goroutine 标识，可重入锁退化为 LockFor（显式 owner）。
	ErrNoGoroutineID = xerr.NewC(xerr.CodeNotImplemented, "xlock: 无法获取 goroutine id，请改用 LockFor")

	// ErrNilLocker 传入了 nil Locker。
	ErrNilLocker = xerr.NewC(xerr.CodeMissingParam, "xlock: locker 为 nil")

	// ErrPoolClosed 资源池已关闭。
	ErrPoolClosed = xerr.NewC(xerr.CodeStateInvalid, "xlock: 资源池已关闭")
)

// ---------------------------------------------------------------------------
// 统计
// ---------------------------------------------------------------------------

// Stats 是锁的运行统计快照。
//
// 所有时长字段都是**累计值**，适合打点上报；单位统一为 time.Duration。
type Stats struct {
	Keys          int           // 锁表条目数（ShardedLocker 为固定分片数）
	WaitCount     int64         // 发生等待（需排队）的获取次数
	WaitTotal     time.Duration // 累计等待时长
	HoldTotal     time.Duration // 累计持有时长
	HoldMax       time.Duration // 单次最长持有
	Contention    int64         // 竞争次数：获取失败（超时/取消）+ TryLock 失败 + 需排队
	InvalidUnlock int64         // 无效释放次数（重复释放 / 非持有者释放），已幂等忽略
}

// stats 是 Stats 的原子累加器（内部使用，避免 Stats 的 int64 字段被并发写）。
type stats struct {
	waitCount     atomic.Int64
	waitTotal     atomic.Int64
	holdTotal     atomic.Int64
	holdMax       atomic.Int64
	contention    atomic.Int64
	invalidUnlock atomic.Int64
}

func (s *stats) snapshot(keys int) Stats {
	return Stats{
		Keys:          keys,
		WaitCount:     s.waitCount.Load(),
		WaitTotal:     time.Duration(s.waitTotal.Load()),
		HoldTotal:     time.Duration(s.holdTotal.Load()),
		HoldMax:       time.Duration(s.holdMax.Load()),
		Contention:    s.contention.Load(),
		InvalidUnlock: s.invalidUnlock.Load(),
	}
}

// observeHold 记录一次持有时长，并维护最大值（CAS 循环保证无锁更新）。
func (s *stats) observeHold(d time.Duration) {
	ns := int64(d)
	s.holdTotal.Add(ns)
	for {
		cur := s.holdMax.Load()
		if ns <= cur || s.holdMax.CompareAndSwap(cur, ns) {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// 选项
// ---------------------------------------------------------------------------

// config 是锁的可选配置。
type config struct {
	maxKeys             int
	waitTimeout         time.Duration
	contentionThreshold time.Duration
	onContention        func(key string, waited time.Duration)
}

// Option 是函数式选项。
type Option func(*config)

// WithMaxKeys 限制活跃 key 数上限；超出时获取锁返回 ErrTooManyKeys（默认 0 = 不限制）。
//
// 用途：把「key 由外部输入构造」（如 `user:<id>`）的接口保护起来，
// 避免恶意/异常输入导致锁表膨胀。注意 KeyedLocker 本身已用引用计数防泄漏，
// 本选项防的是「短时间内大量不同 key 同时活跃」。
func WithMaxKeys(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxKeys = n
		}
	}
}

// WithWaitTimeout 设置 Lock（无 ctx 版本）的等待上限，超时返回 ErrLockTimeout。
//
// LockCtx 不受本选项影响（由 ctx 控制）。
func WithWaitTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.waitTimeout = d
		}
	}
}

// WithContentionThreshold 只在等待时长超过 d 时才触发竞争回调（默认 0 = 每次都触发）。
func WithContentionThreshold(d time.Duration) Option {
	return func(c *config) {
		c.contentionThreshold = d
	}
}

// WithOnContention 注册竞争回调，用于把「锁等待」变成可观测事件（打日志 / 上报指标）。
//
// 回调在**获取锁的调用方 goroutine 上同步执行**，因此必须快速返回，禁止阻塞或再次加锁。
func WithOnContention(fn func(key string, waited time.Duration)) Option {
	return func(c *config) {
		c.onContention = fn
	}
}

func newConfig(opts []Option) config {
	c := config{}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	return c
}

// notifyContention 触发竞争回调（带阈值过滤）。
func (c *config) notifyContention(key string, waited time.Duration) {
	if c.onContention == nil || waited < c.contentionThreshold {
		return
	}
	c.onContention(key, waited)
}

// ---------------------------------------------------------------------------
// 哈希与分片（选型辅助）
// ---------------------------------------------------------------------------

// HashKey 用 FNV-1a 计算 key 的 64 位哈希。
//
// 刻意不用 hash/maphash：maphash 每次进程启动的种子不同，会让分片结果不可复现，
// 不利于排查「为什么这两个 key 冲突了」。
func HashKey(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// ShardOf 返回 key 应落入的分片下标；shards<=0 时视为 1。
func ShardOf(key string, shards int) int {
	if shards <= 0 {
		shards = 1
	}
	return int(HashKey(key) % uint64(shards))
}

// ---------------------------------------------------------------------------
// 便捷作用域用法（推荐：不可能忘记解锁）
// ---------------------------------------------------------------------------

// With 在 key 的写锁保护下执行 fn。
//
// 用法：`xlock.With(ctx, locker, "order:123", func() error { ... })`
//
// 语义：
//   - 获取锁失败时直接返回该错误，**不执行** fn；
//   - fn 返回的错误优先于释放错误；
//   - fn panic 时锁仍会被释放（defer 保证）。
func With(ctx context.Context, l Locker, key string, fn func() error) (err error) {
	if l == nil {
		return ErrNilLocker
	}
	if fn == nil {
		return xerr.NewC(xerr.CodeMissingParam, "xlock.With: fn 为 nil")
	}
	un, err := l.LockCtx(ctx, key)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := un(); uerr != nil && err == nil {
			err = uerr
		}
	}()
	return fn()
}

// WithR 与 With 相同，但使用读锁。
func WithR(ctx context.Context, l Locker, key string, fn func() error) (err error) {
	if l == nil {
		return ErrNilLocker
	}
	if fn == nil {
		return xerr.NewC(xerr.CodeMissingParam, "xlock.WithR: fn 为 nil")
	}
	// 注意：必须用 RLockCtx —— 否则 ctx 会被静默忽略（这正是本函数早期版本的 bug）
	un, err := l.RLockCtx(ctx, key)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := un(); uerr != nil && err == nil {
			err = uerr
		}
	}()
	return fn()
}

// WithValue 与 With 相同，但 fn 可以返回一个值（泛型版）。
//
// 注意：Go 的方法不能带类型参数，所以这类「跨类型参数」的能力只能由包级泛型函数提供。
func WithValue[T any](ctx context.Context, l Locker, key string, fn func() (T, error)) (T, error) {
	var zero T
	if l == nil {
		return zero, ErrNilLocker
	}
	if fn == nil {
		return zero, xerr.NewC(xerr.CodeMissingParam, "xlock.WithValue: fn 为 nil")
	}
	un, err := l.LockCtx(ctx, key)
	if err != nil {
		return zero, err
	}
	defer un()
	return fn()
}

// ---------------------------------------------------------------------------
// 多 key 加锁（防死锁）
// ---------------------------------------------------------------------------

// LockAll 按固定顺序获取多个 key 的写锁，返回一个**幂等**的统一释放函数。
//
// 关键设计：内部对 keys 先去重、再**字典序排序**，因此无论调用方传入的顺序如何，
// 加锁顺序都是全局一致的，从根本上消除 A→B / B→A 的交叉死锁。
//
// 失败时已获取的锁会被逆序释放，不会留下半持有的状态。
func LockAll(ctx context.Context, l Locker, keys ...string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	sorted := dedupeSorted(keys)
	acquired := make([]UnlockFunc, 0, len(sorted))
	for _, k := range sorted {
		un, err := l.LockCtx(ctx, k)
		if err != nil {
			releaseAll(acquired)
			return nil, err
		}
		acquired = append(acquired, un)
	}

	var once sync.Once
	return func() error {
		var err error
		once.Do(func() {
			for i := len(acquired) - 1; i >= 0; i-- {
				if uerr := acquired[i](); uerr != nil && err == nil {
					err = uerr
				}
			}
		})
		return err
	}, nil
}

// xerrWithMeta 为 ErrTooManyKeys 附加诊断信息（key / 上限 / 当前条数）。
func xerrWithMeta(base error, key string, limit, current int) error {
	return xerr.WithMeta(base, "key", key, "limit", limit, "keys", current)
}

// dedupeSorted 去重 + 升序排序（空串是合法 key，保留）。
func dedupeSorted(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// releaseAll 逆序释放（与加锁顺序相反）。
func releaseAll(acquired []UnlockFunc) {
	for i := len(acquired) - 1; i >= 0; i-- {
		_ = acquired[i]()
	}
}
