// Package xdistlock 提供**分布式锁**的统一抽象与通用机制。
//
// 本包只包含与具体存储无关的部分：接口约定、Lock 句柄、看门狗续约、重试/等待策略、
// fencing token、观测钩子。具体存储实现在子包中，且**核心包零第三方依赖**：
//
//	xdistlock/memory  —— 进程内实现（★ 仅单进程有效，用于单测与参考实现）
//	xdistlock/redis   —— Redis 实现（自带极简 RESP 客户端，同样零第三方依赖）
//
// # 为什么要有 Backend 抽象
//
// 分布式锁的「难」不在存储命令，而在**通用机制**：租约续约、丢锁检测、
// 安全释放（compare-and-delete）、重试退避、fencing token。把这些写进各实现里，
// 就会出现 N 套实现与 N 套 bug。因此本包把存储压缩成 3 个原语（Backend），
// 所有机制在客户端侧统一实现并被同一套契约测试覆盖。
//
// # 三个必须理解的语义
//
//  1. **租约（lease）而非永久锁**：拿锁即拿到一段有效时间；进程崩溃后锁会自动释放。
//     代价是「持有者以为自己还持有」的风险 —— 由看门狗与 IsHeld 暴露。
//
//  2. **安全释放**：释放必须带上自己的 token 做 compare-and-delete。
//     裸 DEL 会删掉别人已重新获取的锁 —— 这是分布式锁最常见的事故。
//
//  3. **fencing token**：租约只能保证「大概率」，做不到「绝对」。
//     持锁者 GC 停顿 → 租约过期 → 别人拿锁 → 原持有者继续写，就会数据损坏。
//     唯一可靠的防护是：锁服务发放**单调递增**的 token，
//     被保护资源（DB / 存储层）**主动校验只接受更大的 token**。
//
//     ⚠️ fencing token 只发不校验 == 没做。本包只负责发放，校验必须在资源侧实现。
//
// # 选型建议
//
//	优先 etcd（强一致、revision 天然单调、天然公平）
//	其次 Redis + fencing token（SET NX PX + Lua 安全释放）
//	Redlock 仅作最后选择（正确性依赖各节点时钟，业界争议大）
//
// 并发安全：本包所有类型都可并发使用。
package xdistlock

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// Backend —— 存储必须提供的原语
// ---------------------------------------------------------------------------

// Backend 是分布式锁存储必须实现的三个原语。
//
// 实现者必须满足以下**强语义**（否则整个分布式锁不成立）：
//
//  1. Acquire 只有在 key **当前无人持有** 时才成功；
//     成功时必须返回一个**严格单调递增**的 fencing token（同一后端内全局单调）。
//  2. Refresh 只有在 key **仍由该 token 持有** 时才延期并返回 true；
//     否则返回 (false, nil) —— 表示「锁已不属于我们」。
//  3. Release 必须做 **compare-and-delete**：仅当 key 的值仍是该 token 时才删除。
//     绝不裸删除。返回 (false, nil) 表示锁已不属于我们（已被别人获取）。
//
// 语义约定：
//   - token 不匹配属于**正常业务结果**，用 (false, nil) 表达，而不是 error；
//   - error 仅用于通信失败 / 存储故障等**基础设施问题**；
//   - 所有方法都必须尊重 ctx。
type Backend interface {
	Acquire(ctx context.Context, key, token string, ttl time.Duration) (fencing uint64, ok bool, err error)
	Refresh(ctx context.Context, key, token string, ttl time.Duration) (ok bool, err error)
	Release(ctx context.Context, key, token string) (ok bool, err error)
	// Name 返回后端名称（用于诊断输出）。
	Name() string
	// Close 释放底层资源（连接池等）。
	Close() error
}

// Snapshotter 是 Backend 的可选能力：列出现存锁，用于排障。
//
// 未实现该能力的后端，Client.Snapshot 返回 ErrNotSupported。
type Snapshotter interface {
	Snapshot(ctx context.Context, prefix string) ([]LockInfo, error)
}

// FairBackend 是 Backend 的可选能力：**公平（FIFO）获取**。
//
// 语义要求：多个等待者按**到达顺序**获得锁，不允许插队。
// 未实现该能力的后端，WithFair(true) 会返回 ErrNotSupported
// （设计文档亦指出 redis 需借助 sorted set / Redlock 变种才能做公平锁）。
type FairBackend interface {
	AcquireFair(ctx context.Context, key, token string, ttl time.Duration) (fencing uint64, ok bool, err error)
}

// LockInfo 描述一把现存锁（Snapshot 的结果）。
type LockInfo struct {
	Key      string
	Token    string        // 后端可返回时给出（memory 后端会返回）
	TTL      time.Duration // 剩余租约，<=0 表示已过期
	Fencing  uint64
	Metadata map[string]string
}

// ---------------------------------------------------------------------------
// Locker —— 对外接口
// ---------------------------------------------------------------------------

// Locker 是分布式锁的对外接口。
//
// TryLock 与 Lock 的差别：TryLock 只尝试一次（受 WithRetry 的 max 影响），
// 不会等待到超时；未获取到时返回 (nil, false, nil)。
type Locker interface {
	// Lock 阻塞获取锁，直到成功、ctx 结束或等待超时。
	Lock(ctx context.Context, key string, opts ...LockOption) (*Lock, error)
	// TryLock 尝试获取锁，不长时间等待。
	TryLock(ctx context.Context, key string, opts ...LockOption) (*Lock, bool, error)
	// Close 释放底层资源。
	Close() error
}

// 编译期断言。
var _ Locker = (*Client)(nil)

// ---------------------------------------------------------------------------
// 哨兵错误
// ---------------------------------------------------------------------------

var (
	// ErrLocked 非阻塞模式下锁已被他人持有。
	ErrLocked = xerr.NewC(xerr.CodeConflict, "xdistlock: already locked")

	// ErrNotHeld 调用方当前并未持有该锁（或已通过 Unlock 释放）。
	ErrNotHeld = xerr.NewC(xerr.CodeStateInvalid, "xdistlock: lock not held by caller")

	// ErrLockLost 锁已丢失：租约过期、被他人获取，或续约失败。
	//
	// ⚠️ 收到此错误后**必须立即停止对受保护资源的写入**，
	// 否则可能与新持有者产生数据竞争（这正是 fencing token 要解决的问题）。
	ErrLockLost = xerr.NewC(xerr.CodeStateInvalid, "xdistlock: lock lost (lease expired)")

	// ErrWaitTimeout 等待超过 WithWaitTimeout 设置的上限。
	ErrWaitTimeout = xerr.NewC(xerr.CodeTimeout, "xdistlock: wait timeout")

	// ErrNotSupported 后端不支持该能力（如 redis 后端的公平锁）。
	ErrNotSupported = xerr.NewC(xerr.CodeNotImplemented, "xdistlock: operation not supported")

	// ErrNilBackend 传入了 nil Backend。
	ErrNilBackend = xerr.NewC(xerr.CodeMissingParam, "xdistlock: backend 为 nil")

	// ErrClosed 客户端已关闭。
	ErrClosed = xerr.NewC(xerr.CodeStateInvalid, "xdistlock: client is closed")
)

// ---------------------------------------------------------------------------
// 配置
// ---------------------------------------------------------------------------

// clientConfig 是客户端级默认配置。
type clientConfig struct {
	ttl            time.Duration
	waitTimeout    time.Duration
	retryInterval  time.Duration
	retryMax       int
	attemptTimeout time.Duration
	autoRenew      bool
	renewInterval  time.Duration
	onEvent        func(Event)
}

func defaultClientConfig() clientConfig {
	return clientConfig{
		ttl:            10 * time.Second,
		retryInterval:  200 * time.Millisecond,
		retryMax:       0, // 0 = 无限（受 waitTimeout 约束）
		attemptTimeout: 3 * time.Second,
		renewInterval:  0, // 0 = ttl/3
	}
}

// Option 是客户端级选项（作为 Lock 选项的默认值）。
type Option func(*clientConfig)

// WithDefaultTTL 设置默认租约时长（默认 10s）。
func WithDefaultTTL(d time.Duration) Option {
	return func(c *clientConfig) {
		if d > 0 {
			c.ttl = d
		}
	}
}

// WithDefaultWaitTimeout 设置默认最长等待时间（默认 0 = 一直等到 ctx 结束）。
func WithDefaultWaitTimeout(d time.Duration) Option {
	return func(c *clientConfig) { c.waitTimeout = d }
}

// WithDefaultRetry 设置默认重试间隔与最大次数（max <= 0 表示不限次数）。
func WithDefaultRetry(interval time.Duration, max int) Option {
	return func(c *clientConfig) {
		if interval > 0 {
			c.retryInterval = interval
		}
		c.retryMax = max
	}
}

// WithDefaultAttemptTimeout 设置单次「尝试获取 / 续约 / 释放」的超时（默认 3s）。
//
// 这个超时必须设置：否则一次网络卡死会让看门狗永久停在那里，看似「还持有」实则早已丢锁。
func WithDefaultAttemptTimeout(d time.Duration) Option {
	return func(c *clientConfig) {
		if d > 0 {
			c.attemptTimeout = d
		}
	}
}

// WithDefaultAutoRenew 设置默认是否开启看门狗，以及续约间隔（默认 0 = ttl/3）。
func WithDefaultAutoRenew(enabled bool, interval ...time.Duration) Option {
	return func(c *clientConfig) {
		c.autoRenew = enabled
		if len(interval) > 0 && interval[0] > 0 {
			c.renewInterval = interval[0]
		}
	}
}

// WithOnEvent 注册事件回调，用于埋点 / 打日志。
//
// 回调在**调用方（或看门狗）的 goroutine 上同步执行**，必须快速返回，
// 禁止在其中再次操作同一把锁（会死锁）。
func WithOnEvent(fn func(Event)) Option {
	return func(c *clientConfig) { c.onEvent = fn }
}

// lockConfig 是单次加锁的配置（由客户端默认值 + Lock 选项合并而来）。
type lockConfig struct {
	ttl            time.Duration
	waitTimeout    time.Duration
	retryInterval  time.Duration
	retryMax       int
	attemptTimeout time.Duration
	autoRenew      bool
	renewInterval  time.Duration
	token          string
	blocking       bool
	fair           bool
	onAcquired     func(*Lock)
	onLost         func(*Lock)
	onEvent        func(Event)
}

// LockOption 是单次加锁的选项。
type LockOption func(*lockConfig)

// WithTTL 设置本次加锁的租约时长。
func WithTTL(d time.Duration) LockOption {
	return func(c *lockConfig) {
		if d > 0 {
			c.ttl = d
		}
	}
}

// WithWaitTimeout 设置本次加锁的最长等待时间；超时返回 ErrWaitTimeout。
func WithWaitTimeout(d time.Duration) LockOption {
	return func(c *lockConfig) { c.waitTimeout = d }
}

// WithRetry 设置重试间隔与最大次数（max <= 0 表示不限，仅受 waitTimeout / ctx 约束）。
func WithRetry(interval time.Duration, max int) LockOption {
	return func(c *lockConfig) {
		if interval > 0 {
			c.retryInterval = interval
		}
		c.retryMax = max
	}
}

// WithAttemptTimeout 设置本次加锁的单次尝试超时。
func WithAttemptTimeout(d time.Duration) LockOption {
	return func(c *lockConfig) {
		if d > 0 {
			c.attemptTimeout = d
		}
	}
}

// WithAutoRenew 开启 / 关闭看门狗自动续约。
//
// 长任务（超过 ttl）**必须**开启：否则任务还没跑完租约就过期了。
// 短任务建议关闭，少一个 goroutine 与几次网络往返。
func WithAutoRenew(enabled bool, interval ...time.Duration) LockOption {
	return func(c *lockConfig) {
		c.autoRenew = enabled
		if len(interval) > 0 && interval[0] > 0 {
			c.renewInterval = interval[0]
		}
	}
}

// WithBlocking 设置是否阻塞等待（默认 true）。
//
// 设为 false 时，未获取到锁立即返回 ErrLocked（等价于「只试一次」）。
func WithBlocking(block bool) LockOption {
	return func(c *lockConfig) { c.blocking = block }
}

// WithFair 请求公平锁（FIFO 等待顺序）。
//
// 后端不支持时返回 ErrNotSupported；当前仅 memory 后端支持
// （设计文档亦指出 redis 不做公平锁 —— 需要 sorted set / Redlock 变种）。
func WithFair(fair bool) LockOption {
	return func(c *lockConfig) { c.fair = fair }
}

// WithToken 指定本次加锁使用的持有者标识（默认随机生成）。
//
// 一般无需设置。显式指定的场景：需要让锁的持有者标识可读（如 `order-service#3`），
// 或需要在重试过程中保持同一 token 以便排障。
//
// ⚠️ 同一 token 被并发复用时，安全释放会误判「锁仍属于我」，
// 请确保 token 在「同一把锁的并发争抢者」之间唯一。
func WithToken(token string) LockOption {
	return func(c *lockConfig) { c.token = token }
}

// WithOnAcquired 在成功获取锁（含看门狗已启动）后同步回调。
//
// 典型用途：把 fencing token 写进日志 / 上报指标。
func WithOnAcquired(fn func(*Lock)) LockOption {
	return func(c *lockConfig) { c.onAcquired = fn }
}

// WithOnLost 在**续约失败导致丢锁**时回调。
//
// ⚠️ 这是本包最重要的钩子：收到回调意味着「你的租约已失效，别人可能已经拿到锁」。
// 长任务必须在回调里设置一个标志位（或 cancel 自己的 ctx），让业务逻辑尽快停下，
// 否则会与新持有者并发写同一份数据。
func WithOnLost(fn func(*Lock)) LockOption {
	return func(c *lockConfig) { c.onLost = fn }
}

// WithLockEvent 覆盖本次加锁的事件回调（默认继承客户端设置）。
func WithLockEvent(fn func(Event)) LockOption {
	return func(c *lockConfig) { c.onEvent = fn }
}

// ---------------------------------------------------------------------------
// 事件与统计
// ---------------------------------------------------------------------------

// EventType 是事件类型。
type EventType string

const (
	EventAcquireOK   EventType = "acquire_ok"
	EventAcquireFail EventType = "acquire_fail"
	EventRenewOK     EventType = "renew_ok"
	EventRenewFail   EventType = "renew_fail"
	EventLost        EventType = "lost"
	EventRelease     EventType = "release"
	EventReleaseFail EventType = "release_fail"
)

// Event 是一次锁操作的观测事件。
type Event struct {
	Type     EventType
	Key      string
	Token    string
	Attempts int           // 累计尝试次数（获取类事件有意义）
	Waited   time.Duration // 累计等待时长
	Fencing  uint64
	Err      error
}

// Metrics 是客户端累计统计快照。
type Metrics struct {
	AcquireAttempts int64         // 累计获取尝试次数（含失败重试）
	AcquireOK       int64         // 累计成功获取次数
	AcquireFail     int64         // 累计失败次数（超时 / 取消 / 非阻塞已被占用）
	WaitTotal       time.Duration // 累计等待时长
	RenewOK         int64         // 累计续约成功次数
	RenewFail       int64         // 累计续约失败次数
	LocksLost       int64         // 累计丢锁次数
	ReleaseOK       int64         // 累计成功释放次数
	ReleaseFail     int64         // 累计释放失败次数
	HeldMax         int64         // 历史并发持有数峰值
}

type metrics struct {
	acquireAttempts atomic.Int64
	acquireOK       atomic.Int64
	acquireFail     atomic.Int64
	waitTotalNS     atomic.Int64
	renewOK         atomic.Int64
	renewFail       atomic.Int64
	locksLost       atomic.Int64
	releaseOK       atomic.Int64
	releaseFail     atomic.Int64
	held            atomic.Int64
	heldMax         atomic.Int64
}

func (m *metrics) addHold(delta int64) {
	cur := m.held.Add(delta)
	for {
		old := m.heldMax.Load()
		if cur <= old || m.heldMax.CompareAndSwap(old, cur) {
			return
		}
	}
}

func (m *metrics) snapshot() Metrics {
	return Metrics{
		AcquireAttempts: m.acquireAttempts.Load(),
		AcquireOK:       m.acquireOK.Load(),
		AcquireFail:     m.acquireFail.Load(),
		WaitTotal:       time.Duration(m.waitTotalNS.Load()),
		RenewOK:         m.renewOK.Load(),
		RenewFail:       m.renewFail.Load(),
		LocksLost:       m.locksLost.Load(),
		ReleaseOK:       m.releaseOK.Load(),
		ReleaseFail:     m.releaseFail.Load(),
		HeldMax:         m.heldMax.Load(),
	}
}
