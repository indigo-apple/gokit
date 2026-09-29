package xdistlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// token 生成
// ---------------------------------------------------------------------------

// instanceID 标识「当前进程」，用于让 token 一眼看出归属（排障友好）。
var instanceID = genInstanceID()

var tokenSeq atomic.Uint64

func genInstanceID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// newToken 生成全局唯一的持有者标识：`<实例ID>-<随机16字节hex>`。
//
// 用 crypto/rand 而非时间戳：时间戳在容器快照恢复 / 高并发下可能重复，
// 而 token 一旦重复，「安全释放」会误判锁仍属于自己 —— 后果是删掉别人的锁。
func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败在标准库里几乎不可能；退化到实例ID+单调序号，仍保证进程内唯一
		return fmt.Sprintf("%s-%d", instanceID, tokenSeq.Add(1))
	}
	return instanceID + "-" + hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Client 是基于某个 Backend 的分布式锁客户端。
//
// 一个进程通常只需一个 Client（内部无状态、可并发使用）。
type Client struct {
	be  Backend
	cfg clientConfig
	m   metrics

	closed    atomic.Bool
	closeOnce sync.Once
}

// New 创建客户端；opts 提供各类默认值（可被单次 Lock 的 LockOption 覆盖）。
func New(be Backend, opts ...Option) *Client {
	cfg := defaultClientConfig()
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return &Client{be: be, cfg: cfg}
}

// BackendName 返回后端名称。
func (c *Client) BackendName() string {
	if c == nil || c.be == nil {
		return ""
	}
	return c.be.Name()
}

// Metrics 返回累计统计快照。
func (c *Client) Metrics() Metrics {
	if c == nil {
		return Metrics{}
	}
	return c.m.snapshot()
}

// Snapshot 列出现存锁（需要后端实现 Snapshotter，否则返回 ErrNotSupported）。
func (c *Client) Snapshot(ctx context.Context, prefix string) ([]LockInfo, error) {
	if c == nil || c.be == nil {
		return nil, ErrNilBackend
	}
	s, ok := c.be.(Snapshotter)
	if !ok {
		return nil, xerr.WithMeta(ErrNotSupported, "backend", c.be.Name(), "capability", "snapshot")
	}
	return s.Snapshot(ctx, prefix)
}

// Close 关闭客户端并释放后端资源；重复调用安全。
func (c *Client) Close() error {
	if c == nil || c.be == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		err = c.be.Close()
	})
	return err
}

// resolve 把客户端默认值与单次选项合并。
func (c *Client) resolve(opts []LockOption) lockConfig {
	lc := lockConfig{
		ttl:            c.cfg.ttl,
		waitTimeout:    c.cfg.waitTimeout,
		retryInterval:  c.cfg.retryInterval,
		retryMax:       c.cfg.retryMax,
		attemptTimeout: c.cfg.attemptTimeout,
		autoRenew:      c.cfg.autoRenew,
		renewInterval:  c.cfg.renewInterval,
		blocking:       true,
		onEvent:        c.cfg.onEvent,
	}
	for _, o := range opts {
		if o != nil {
			o(&lc)
		}
	}
	if lc.token == "" {
		lc.token = newToken()
	}
	if lc.attemptTimeout <= 0 {
		lc.attemptTimeout = 3 * time.Second
	}
	if lc.retryInterval <= 0 {
		lc.retryInterval = 100 * time.Millisecond
	}
	return lc
}

func (c *Client) emit(lc lockConfig, ev Event) {
	if lc.onEvent != nil {
		lc.onEvent(ev)
	}
}

// ---------------------------------------------------------------------------
// 获取锁
// ---------------------------------------------------------------------------

// Lock 阻塞获取锁，直到成功、ctx 结束或等待超时。
func (c *Client) Lock(ctx context.Context, key string, opts ...LockOption) (*Lock, error) {
	return c.lock(ctx, key, true, opts)
}

// TryLock 尝试获取锁，只试一次（受 WithRetry 的 max 影响）。
//
// 未获取到时返回 (nil, false, nil)；只有基础设施错误 / ctx 结束才返回 error。
func (c *Client) TryLock(ctx context.Context, key string, opts ...LockOption) (*Lock, bool, error) {
	lk, err := c.lock(ctx, key, false, opts)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return lk, true, nil
}

func (c *Client) lock(ctx context.Context, key string, blocking bool, opts []LockOption) (*Lock, error) {
	if c == nil || c.be == nil {
		return nil, ErrNilBackend
	}
	if key == "" {
		return nil, xerr.NewC(xerr.CodeMissingParam, "xdistlock: key 不能为空")
	}
	if c.closed.Load() {
		return nil, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lc := c.resolve(opts)
	// 公平锁需要后端支持：在进入重试循环前就快速失败，而不是每个请求都失败一轮
	if lc.fair {
		if _, ok := c.be.(FairBackend); !ok {
			return nil, xerr.WithMeta(ErrNotSupported,
				"backend", c.be.Name(), "capability", "fair", "key", key)
		}
	}
	if !blocking {
		// TryLock 语义：一次尝试 + 不等待
		lc.blocking = false
		lc.retryMax = 1
		if lc.waitTimeout <= 0 {
			lc.waitTimeout = 0
		}
	}
	return c.acquireLoop(ctx, key, lc)
}

// acquireLoop 是获取锁的重试循环。
//
// 退出条件（按优先级）：
//  1. 成功获取；
//  2. ctx 结束 → 返回 ctx.Err()；
//  3. 等待超过 waitTimeout → ErrWaitTimeout；
//  4. blocking=false 或重试次数用尽 → ErrLocked。
func (c *Client) acquireLoop(ctx context.Context, key string, lc lockConfig) (*Lock, error) {
	start := time.Now()

	var deadline time.Time
	if lc.waitTimeout > 0 {
		deadline = start.Add(lc.waitTimeout)
	}

	attempts := 0
	var lastErr error

	for {
		if err := ctx.Err(); err != nil {
			c.failAcquire(key, lastErr)
			return nil, err
		}
		attempts++
		c.m.acquireAttempts.Add(1)

		actx, cancel := context.WithTimeout(ctx, lc.attemptTimeout)
		var fencing uint64
		var ok bool
		var err error
		if lc.fair {
			// 上面已预检过类型，这里必然是 FairBackend
			fencing, ok, err = c.be.(FairBackend).AcquireFair(actx, key, lc.token, lc.ttl)
		} else {
			fencing, ok, err = c.be.Acquire(actx, key, lc.token, lc.ttl)
		}
		cancel()

		if err == nil && ok {
			return c.onAcquired(key, lc, fencing, attempts, start)
		}
		if err != nil {
			lastErr = err
		}

		// 是否还有下一次机会
		if !lc.blocking {
			break
		}
		if lc.retryMax > 0 && attempts >= lc.retryMax {
			break
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			c.failAcquire(key, lastErr)
			return nil, xerr.WithMeta(ErrWaitTimeout, "key", key, "attempts", attempts,
				"wait", time.Since(start))
		}

		wait := jitter(lc.retryInterval)
		if !deadline.IsZero() && time.Now().Add(wait).After(deadline) {
			// 下一次重试必然超时：直接结束，避免多跑一轮无意义的等待
			c.failAcquire(key, lastErr)
			return nil, xerr.WithMeta(ErrWaitTimeout, "key", key, "attempts", attempts,
				"wait", time.Since(start))
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.failAcquire(key, lastErr)
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	c.failAcquire(key, lastErr)
	if lastErr != nil {
		// 基础设施错误：包装后返回，便于调用方区分「被别人占用」与「连不上存储」
		return nil, xerr.WrapC(lastErr, xerr.CodeDependencyFailed, "xdistlock: 获取锁失败（后端错误）")
	}
	return nil, ErrLocked
}

// onAcquired 在成功获取后做统一的收尾：启动看门狗、埋点、回调。
func (c *Client) onAcquired(key string, lc lockConfig, fencing uint64, attempts int, start time.Time) (*Lock, error) {
	total := time.Since(start)
	lk := newLock(c, key, lc, fencing)

	c.m.acquireOK.Add(1)
	c.m.waitTotalNS.Add(int64(total))
	c.m.addHold(1)

	// ★ 先启动看门狗再回调：WithOnAcquired 里可能会做耗时操作（写日志/上报），
	//   若此时还没开始续约，长回调会把租约耗尽
	if lc.autoRenew {
		lk.startWatchdog()
	}
	c.emit(lc, Event{
		Type: EventAcquireOK, Key: key, Token: lc.token,
		Attempts: attempts, Waited: total, Fencing: fencing,
	})
	if lc.onAcquired != nil {
		lc.onAcquired(lk)
	}
	return lk, nil
}

func (c *Client) failAcquire(key string, lastErr error) {
	c.m.acquireFail.Add(1)
}

// jitter 给重试间隔加入 50%~100% 的随机抖动，避免大量客户端同时重试造成惊群。
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Millisecond
	}
	half := d / 2
	n, err := rand.Int(rand.Reader, big.NewInt(int64(half)+1))
	if err != nil {
		return d
	}
	return half + time.Duration(n.Int64())
}

// ---------------------------------------------------------------------------
// Lock 句柄
// ---------------------------------------------------------------------------

// minimumRenewInterval 是看门狗心跳的下限，避免 ttl 很小时疯狂打后端。
const minimumRenewInterval = 50 * time.Millisecond

// Lock 是一把已获取的分布式锁。
//
// 生命周期：由 Client.Lock/TryLock 创建 → 持有期间可用 Refresh/Extend 延期
// → Unlock 释放（或续约失败导致丢锁）。
//
// 并发安全：所有方法都可并发调用。
type Lock struct {
	c       *Client
	key     string
	token   string
	fencing uint64
	cfg     lockConfig

	mu     sync.Mutex
	held   bool
	expiry time.Time

	leaseNS atomic.Int64 // 当前租约（纳秒），Extend 可改

	stopOnce sync.Once
	stopCh   chan struct{}
	wdStart  sync.Once
	wdDone   chan struct{}
}

func newLock(c *Client, key string, lc lockConfig, fencing uint64) *Lock {
	l := &Lock{
		c:       c,
		key:     key,
		token:   lc.token,
		fencing: fencing,
		cfg:     lc,
		held:    true,
		expiry:  time.Now().Add(lc.ttl),
		stopCh:  make(chan struct{}),
		wdDone:  make(chan struct{}),
	}
	l.leaseNS.Store(int64(lc.ttl))
	return l
}

// Key 返回锁的 key。
func (l *Lock) Key() string {
	if l == nil {
		return ""
	}
	return l.key
}

// Token 返回持有者标识（安全释放时使用）。
func (l *Lock) Token() string {
	if l == nil {
		return ""
	}
	return l.token
}

// FencingToken 返回单调递增的防脑裂令牌。
//
// ⚠️ 只有在**被保护资源主动校验**时它才起作用：
// 资源侧必须拒绝 token 小于等于「已见最大值」的写入。只发不校验等于没做。
func (l *Lock) FencingToken() uint64 {
	if l == nil {
		return 0
	}
	return l.fencing
}

// Expiry 返回当前租约的预计到期时间。
func (l *Lock) Expiry() time.Time {
	if l == nil {
		return time.Time{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expiry
}

// IsHeld 返回锁当前是否仍被认为持有。
//
// ⚠️ 这是「本地视角」：返回 true 只代表本地尚未观测到丢锁。
// 真正的判定依赖续约（看门狗或手动 Refresh）的返回值。
func (l *Lock) IsHeld() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// TTL 返回当前租约时长（Extend 会改变它）。
func (l *Lock) TTL() time.Duration {
	if l == nil {
		return 0
	}
	return time.Duration(l.leaseNS.Load())
}

// String 实现 fmt.Stringer（调试友好，不含完整 token）。
func (l *Lock) String() string {
	if l == nil {
		return "Lock(nil)"
	}
	return fmt.Sprintf("Lock{key=%s token=%s… fencing=%d held=%v}",
		l.key, shortToken(l.token), l.fencing, l.IsHeld())
}

func shortToken(t string) string {
	if len(t) <= 8 {
		return t
	}
	return t[:8]
}

// ---------------------------------------------------------------------------
// 看门狗
// ---------------------------------------------------------------------------

// startWatchdog 启动自动续约（幂等）。
func (l *Lock) startWatchdog() {
	l.wdStart.Do(func() { go l.watchdog() })
}

// watchdog 按租约的 1/3 周期续约。
//
// 关键设计：
//   - 每次循环都重新计算间隔，因此 Extend 改租约后**下一个心跳自动生效**；
//   - 续约失败分两类：基础设施错误（继续重试）与「锁已不属于我」（判定丢锁）；
//   - 退出时关闭 wdDone，让 Unlock 能确认「看门狗真的停了」。
func (l *Lock) watchdog() {
	defer close(l.wdDone)
	for {
		interval := l.renewInterval()
		timer := time.NewTimer(interval)
		select {
		case <-l.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}

		if !l.IsHeld() {
			return
		}

		// 用独立的 ctx：调用方的 ctx 可能在 Lock 返回后就结束了
		ctx, cancel := context.WithTimeout(context.Background(), l.cfg.attemptTimeout)
		err := l.refreshOnce(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, ErrLockLost) {
				return // 已丢锁，refreshOnce 内部已回调 OnLost
			}
			// 基础设施错误：本次心跳失败不代表丢锁，下一轮继续尝试。
			// 若错误持续到租约过期，下一次 Refresh 会返回 ok=false，从而被正确判定为丢锁。
			continue
		}
	}
}

// renewInterval 计算下一次心跳间隔。
func (l *Lock) renewInterval() time.Duration {
	if l.cfg.renewInterval > 0 {
		return l.cfg.renewInterval
	}
	d := time.Duration(l.leaseNS.Load()) / 3
	if d < minimumRenewInterval {
		d = minimumRenewInterval
	}
	return d
}

// stopWatchdog 停止看门狗并等待其退出（幂等）。
//
// ★ Unlock 必须先调用它：否则看门狗 goroutine 泄漏，且可能与释放操作竞态。
func (l *Lock) stopWatchdog() {
	if !l.cfg.autoRenew {
		return
	}
	l.stopOnce.Do(func() { close(l.stopCh) })
	<-l.wdDone
}

// ---------------------------------------------------------------------------
// 续约 / 释放
// ---------------------------------------------------------------------------

// Refresh 手动续约一次。
//
// 返回 ErrLockLost 表示锁已不属于我们（此时 IsHeld 变为 false 且 OnLost 已触发）。
func (l *Lock) Refresh(ctx context.Context) error {
	if l == nil {
		return ErrNotHeld
	}
	if ctx == nil {
		ctx = context.Background()
	}
	actx, cancel := context.WithTimeout(ctx, l.cfg.attemptTimeout)
	defer cancel()
	return l.refreshOnce(actx)
}

// refreshOnce 是续约的实际实现（看门狗与手动 Refresh 共用）。
func (l *Lock) refreshOnce(ctx context.Context) error {
	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		return ErrNotHeld
	}
	lease := time.Duration(l.leaseNS.Load())
	l.mu.Unlock()

	ok, err := l.c.be.Refresh(ctx, l.key, l.token, lease)
	if err != nil {
		l.c.m.renewFail.Add(1)
		l.c.emit(l.cfg, Event{Type: EventRenewFail, Key: l.key, Token: l.token, Fencing: l.fencing, Err: err})
		return err
	}
	if !ok {
		// 锁已不属于我们 —— 这才是真正的丢锁
		l.markLost()
		return ErrLockLost
	}

	l.mu.Lock()
	l.expiry = time.Now().Add(lease)
	l.mu.Unlock()
	l.c.m.renewOK.Add(1)
	l.c.emit(l.cfg, Event{Type: EventRenewOK, Key: l.key, Token: l.token, Fencing: l.fencing})
	return nil
}

// markLost 标记丢锁并触发回调（只会生效一次）。
func (l *Lock) markLost() {
	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		return
	}
	l.held = false
	l.mu.Unlock()

	l.c.m.locksLost.Add(1)
	l.c.m.addHold(-1)
	l.c.emit(l.cfg, Event{Type: EventLost, Key: l.key, Token: l.token, Fencing: l.fencing, Err: ErrLockLost})
	if l.cfg.onLost != nil {
		l.cfg.onLost(l)
	}
}

// Extend 调整租约时长并立即生效（会把新租约推到后端，而不是等下一次心跳）。
func (l *Lock) Extend(ttl time.Duration) error {
	if l == nil {
		return ErrNotHeld
	}
	if ttl <= 0 {
		return xerr.NewC(xerr.CodeInvalidParam, "xdistlock: Extend 的 ttl 必须为正")
	}
	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		return ErrNotHeld
	}
	l.leaseNS.Store(int64(ttl))
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), l.cfg.attemptTimeout)
	defer cancel()
	return l.refreshOnce(ctx)
}

// Unlock 安全释放锁。
//
// 语义：
//   - 先停看门狗（避免 goroutine 泄漏与释放竞态），再做 **compare-and-delete**；
//   - 若锁已丢失（租约过期 / 被别人抢走），返回 ErrLockLost 且**不触碰后端**，
//     绝不会删掉别人的锁；
//   - 重复调用返回 ErrNotHeld（用于尽早暴露「忘记释放 / 重复释放」的 bug）。
func (l *Lock) Unlock(ctx context.Context) error {
	if l == nil {
		return ErrNotHeld
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.stopWatchdog()

	l.mu.Lock()
	if !l.held {
		l.mu.Unlock()
		return ErrNotHeld
	}
	l.held = false
	l.mu.Unlock()
	l.c.m.addHold(-1)

	actx, cancel := context.WithTimeout(ctx, l.cfg.attemptTimeout)
	defer cancel()

	ok, err := l.c.be.Release(actx, l.key, l.token)
	if err != nil {
		l.c.m.releaseFail.Add(1)
		l.c.emit(l.cfg, Event{Type: EventReleaseFail, Key: l.key, Token: l.token, Fencing: l.fencing, Err: err})
		return err
	}
	if !ok {
		l.c.m.releaseFail.Add(1)
		l.c.emit(l.cfg, Event{Type: EventReleaseFail, Key: l.key, Token: l.token, Fencing: l.fencing, Err: ErrLockLost})
		return ErrLockLost
	}

	l.c.m.releaseOK.Add(1)
	l.c.emit(l.cfg, Event{Type: EventRelease, Key: l.key, Token: l.token, Fencing: l.fencing})
	return nil
}
