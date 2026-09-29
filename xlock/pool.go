package xlock

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
)

// ResourcePool 限制「同一类资源」的并发上限（信号量语义）。
//
// 与 KeyedLocker 的区别：
//
//	KeyedLocker    —— 按 key 互斥，同一 key 只允许 1 个持有者（互斥）
//	ResourcePool   —— 不区分 key，只限制总数不超过 size（限流 / 连接池 / 并发闸门）
//
// 典型场景：限制「同时访问某个外部接口的请求数」「同时打开的文件句柄数」
// 「同时执行的批处理任务数」。
//
// 关闭语义（与会话中 xqueue.Bounded 的经验一致）：
//   - 内部**不关闭**令牌 channel，而是单独关闭一个 done channel；
//     多生产者场景下 close(令牌 channel) 会与并发接收竞争，导致不可预期的行为；
//   - Close 之后 Acquire 返回 ErrPoolClosed，但**已借出的令牌仍可正常归还**；
//   - 重复 Close 安全。
//
// 并发安全。
type ResourcePool struct {
	name   string
	tokens chan struct{}
	done   chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	inUse     int
	maxInUse  int

	acquired atomic.Int64 // 累计成功借出次数
	waited   atomic.Int64 // 累计发生过等待的次数
	rejected atomic.Int64 // 累计因关闭/超时/放弃而失败次数
	invalid  atomic.Int64 // 无效归还次数
}

// PoolOption 是资源池的函数式选项。
type PoolOption func(*poolConfig)

type poolConfig struct {
	name string
}

// WithPoolName 给资源池起名（仅用于诊断输出）。
func WithPoolName(name string) PoolOption {
	return func(c *poolConfig) { c.name = name }
}

// NewResourcePool 创建容量为 size 的资源池；size <= 0 时按 1 处理。
func NewResourcePool(size int, opts ...PoolOption) *ResourcePool {
	if size <= 0 {
		size = 1
	}
	cfg := poolConfig{}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	p := &ResourcePool{
		name:   cfg.name,
		tokens: make(chan struct{}, size),
		done:   make(chan struct{}),
	}
	for i := 0; i < size; i++ {
		p.tokens <- struct{}{}
	}
	return p
}

// Name 返回资源池名称。
func (p *ResourcePool) Name() string { return p.name }

// Size 返回总容量。
func (p *ResourcePool) Size() int {
	if p == nil {
		return 0
	}
	return cap(p.tokens)
}

// Available 返回当前可借出的令牌数。
func (p *ResourcePool) Available() int {
	if p == nil {
		return 0
	}
	return len(p.tokens)
}

// InUse 返回当前已借出的令牌数。
func (p *ResourcePool) InUse() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inUse
}

// MaxInUse 返回历史峰值借用数（用于容量规划）。
func (p *ResourcePool) MaxInUse() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInUse
}

// IsClosed 判断是否已关闭。
func (p *ResourcePool) IsClosed() bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Closed 与 IsClosed 相同（语义更自然的别名）。
func (p *ResourcePool) Closed() bool { return p.IsClosed() }

// PoolStats 是资源池的运行统计。
type PoolStats struct {
	Size     int   // 总容量
	InUse    int   // 当前借用数
	MaxInUse int   // 峰值借用数
	Acquired int64 // 累计成功借出次数
	Waited   int64 // 累计发生过等待的次数
	Rejected int64 // 累计失败次数（超时 / 取消 / 已关闭）
	Invalid  int64 // 无效归还次数（重复归还，已幂等忽略）
}

// Stats 返回统计快照。
func (p *ResourcePool) Stats() PoolStats {
	if p == nil {
		return PoolStats{}
	}
	return PoolStats{
		Size:     p.Size(),
		InUse:    p.InUse(),
		MaxInUse: p.MaxInUse(),
		Acquired: p.acquired.Load(),
		Waited:   p.waited.Load(),
		Rejected: p.rejected.Load(),
		Invalid:  p.invalid.Load(),
	}
}

// markInUseLocked 更新借用计数与峰值。
func (p *ResourcePool) markInUseLocked(delta int) {
	p.inUse += delta
	if p.inUse > p.maxInUse {
		p.maxInUse = p.inUse
	}
}

// acquireCommon 是所有借用入口的公共实现。
//
// timeout <= 0 表示无限等待；ctx 非 nil 时同时受 ctx 约束。
func (p *ResourcePool) acquireCommon(ctx context.Context, timeout time.Duration) (UnlockFunc, error) {
	if p == nil {
		return nil, ErrPoolClosed
	}
	// 关闭判定必须先于 select：否则「有令牌」与「已关闭」同时就绪时
	// select 会随机选分支，导致 Close 后借用语义不确定。
	if p.IsClosed() {
		p.rejected.Add(1)
		return nil, ErrPoolClosed
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timeoutCh = t.C
	}
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}

	// 快速路径：立刻有令牌，无需等待
	select {
	case <-p.tokens:
		p.recordBorrow(false)
		return p.makeRelease(), nil
	default:
	}

	select {
	case <-p.tokens:
		p.recordBorrow(true)
		return p.makeRelease(), nil
	case <-p.done:
		p.rejected.Add(1)
		return nil, ErrPoolClosed
	case <-ctxDone:
		p.rejected.Add(1)
		return nil, ctx.Err()
	case <-timeoutCh:
		p.rejected.Add(1)
		return nil, xerr.WithMeta(ErrLockTimeout, "pool", p.name, "timeout", timeout)
	}
}

// recordBorrow 更新借用统计；waited 表示本次借用是否发生过排队等待。
func (p *ResourcePool) recordBorrow(waited bool) {
	p.acquired.Add(1)
	if waited {
		p.waited.Add(1)
	}
	p.mu.Lock()
	p.markInUseLocked(1)
	p.mu.Unlock()
}

// makeRelease 构造幂等的归还函数。
func (p *ResourcePool) makeRelease() UnlockFunc {
	done := new(atomic.Bool)
	return func() error {
		if !done.CompareAndSwap(false, true) {
			p.invalid.Add(1)
			return nil
		}
		p.mu.Lock()
		p.markInUseLocked(-1)
		p.mu.Unlock()
		// 归还令牌：容量固定，且我们是借出方，因此这里必然有空位
		select {
		case p.tokens <- struct{}{}:
		default:
			// 理论上不可达（借一还一）；出现则说明状态错乱，不计入统计避免误导
			p.invalid.Add(1)
		}
		return nil
	}
}

// Acquire 阻塞借出一个令牌，直到成功或资源池关闭。
func (p *ResourcePool) Acquire() (UnlockFunc, error) {
	return p.acquireCommon(nil, 0)
}

// AcquireCtx 阻塞借出一个令牌，支持 ctx 超时 / 取消。
//
// ctx 为 nil 时等价于 Acquire。
func (p *ResourcePool) AcquireCtx(ctx context.Context) (UnlockFunc, error) {
	return p.acquireCommon(ctx, 0)
}

// AcquireTimeout 借出令牌，最多等待 d；超时返回 ErrLockTimeout。
func (p *ResourcePool) AcquireTimeout(d time.Duration) (UnlockFunc, error) {
	return p.acquireCommon(nil, d)
}

// TryAcquire 尝试立即借出令牌；失败返回 (nil, false)。
func (p *ResourcePool) TryAcquire() (UnlockFunc, bool) {
	if p == nil || p.IsClosed() {
		if p != nil {
			p.rejected.Add(1)
		}
		return nil, false
	}
	select {
	case <-p.tokens:
		p.recordBorrow(false)
		return p.makeRelease(), true
	default:
		return nil, false
	}
}

// Close 关闭资源池：后续 Acquire 返回 ErrPoolClosed，已借出的令牌仍可归还。
//
// 重复调用安全。内部只关闭 done，绝不关闭令牌 channel。
func (p *ResourcePool) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() { close(p.done) })
}

// With 在某个令牌的保护下执行 fn（作用域用法，不可能忘记归还）。
func (p *ResourcePool) With(ctx context.Context, fn func() error) (err error) {
	if fn == nil {
		return xerr.NewC(xerr.CodeMissingParam, "xlock.ResourcePool.With: fn 为 nil")
	}
	un, err := p.AcquireCtx(ctx)
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

// WithValue 在某个令牌的保护下执行 fn 并返回其值。
func WithPoolValue[T any](p *ResourcePool, ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if p == nil {
		return zero, ErrPoolClosed
	}
	if fn == nil {
		return zero, xerr.NewC(xerr.CodeMissingParam, "xlock.WithPoolValue: fn 为 nil")
	}
	un, err := p.AcquireCtx(ctx)
	if err != nil {
		return zero, err
	}
	defer un()
	return fn()
}
