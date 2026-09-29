// Package xkeyed 提供「按 key 串行化」的管道（KeyedPipe / 同 key 串行管道）。
//
// 核心语义：**同一个 key 上的任务严格按提交顺序执行；不同 key 之间尽可能并行**。
//
// 这是「既要并行、又要局部有序」场景的通用解法 —— 典型用途：
//
//	订单事件处理（同订单号串行、不同订单并行）
//	按用户聚合并发更新（避免同一用户的价格/库存竞争）
//	写同一份文档的并发请求（同文档串行）
//	消息总线的按 key 保序投递（xevent 的 WithKey 即基于本包）
//
// # 实现方式与取舍
//
// key 哈希到**固定分片**（默认 2*CPU，最少 8），每个分片一条有界队列 + 一个 worker：
//
//	✓ 同 key 必然落同一分片 → 严格串行（正确的强保证）
//	✓ 固定分片表 → **不会随 key 数量增长而泄漏内存**（这是与 xkeyed.MapPipe 逐 key 建队列的关键差别）
//	✗ 不同 key 可能落同一分片 → 偶发「假串行」（只影响吞吐，不影响正确性）
//
// 这个取舍与 xlock.ShardedLocker 完全一致：用「偶发假冲突」换「内存有界 + 实现简单」。
//
// # 与「加锁」的区别
//
//	xlock  解决互斥：拿到锁之前阻塞，可能长时间等待
//	xkeyed 解决顺序：不阻塞提交方（受队列上限约束），天然无死锁风险
//
// 需要互斥语义用 xlock；只需要「同 key 不并发」用本包（更轻、无死锁）。
package xkeyed

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xerr"
)

// 哨兵错误。
var (
	// ErrClosed 管道已关闭，不再接收新任务。
	ErrClosed = xerr.NewC(xerr.CodeStateInvalid, "xkeyed: pipe is closed")

	// ErrQueueFull 分片队列已满且溢出策略为 Fail / DropNewest。
	ErrQueueFull = xerr.NewC(xerr.CodeResourceExhausted, "xkeyed: shard queue is full")

	// ErrNilFunc 提交了 nil 任务。
	ErrNilFunc = xerr.NewC(xerr.CodeMissingParam, "xkeyed: fn 不能为 nil")
)

// HashKey 用 FNV-1a 计算 key 的 64 位哈希。
//
// 与 xlock.HashKey 是同一个算法（刻意不用 maphash：maphash 每次进程启动的种子不同，
// 会让分片结果不可复现，不利于排查「为什么这两个 key 撞到一起了」）。
func HashKey(key string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= prime64
	}
	return h
}

// ShardOf 返回 key 应落入的分片下标；shards <= 0 时视为 1。
func ShardOf(key string, shards int) int {
	if shards <= 0 {
		shards = 1
	}
	return int(HashKey(key) % uint64(shards))
}

// defaultShards 返回默认分片数：2*CPU，最少 8，最多 256。
//
// 上限 256 是为了「分片过多导致每分片队列小、假冲突少但调度开销上升」；
// 对绝大多数业务，8~64 片已经能把假冲突压到可忽略。
func defaultShards() int {
	n := runtime.NumCPU() * 2
	if n < 8 {
		n = 8
	}
	if n > 256 {
		n = 256
	}
	return n
}

// config 是管道配置。
type config struct {
	shards      int
	queueSize   int
	overflow    xcontainer.OverflowPolicy
	onError     func(key string, err error)
	onPanic     func(key string, r any)
	drainOnExit bool
}

// Option 是管道选项。
type Option func(*config)

// WithShards 设置分片数（<=0 时用默认值）。
//
// ⚠️ 注意：**同一分片的不同 key 会互相串行**。分片数越少，假冲突越多、
// 「同 key 严格串行」的保证不变但整体吞吐下降。设为 1 即「全局严格串行」。
func WithShards(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.shards = n
		}
	}
}

// WithQueueSize 设置每个分片的队列长度（默认 1024）。
//
// 队列长度决定「突发流量能缓冲多少任务」；总量约为 queueSize * shards。
func WithQueueSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.queueSize = n
		}
	}
}

// WithOverflow 设置队列满时的策略（默认 Block：阻塞提交直到有空位或 ctx 结束）。
//
//	Block      —— 阻塞提交（推荐：天然形成背压，不会静默丢数据）
//	DropOldest —— 丢弃队列里最旧的任务（适合「只关心最新状态」的刷新类任务）
//	DropNewest —— 丢弃新任务并返回 ErrQueueFull
//	Fail       —— 返回 ErrQueueFull
func WithOverflow(p xcontainer.OverflowPolicy) Option {
	return func(c *config) { c.overflow = p.Norm() }
}

// WithOnError 注册任务返回 error 时的回调。
//
// 「提交方不等待」的任务（Submit 且未 Do）出错时只能靠这个回调暴露问题，
// 否则错误会被静默吞掉 —— 强烈建议生产环境必须设置。
func WithOnError(fn func(key string, err error)) Option {
	return func(c *config) { c.onError = fn }
}

// WithOnPanic 注册任务 panic 时的回调（panic 已被安全捕获，worker 不会退出）。
//
// 不设置时 panic 仅被计入 Stats.Panics。
func WithOnPanic(fn func(key string, r any)) Option {
	return func(c *config) { c.onPanic = fn }
}

// Stats 是管道统计快照。
type Stats struct {
	Shards    int   // 分片数
	Submitted int64 // 累计提交（含被拒绝的）
	Completed int64 // 累计成功完成
	Failed    int64 // 累计返回 error
	Panics    int64 // 累计 panic（已被捕获）
	Dropped   int64 // 累计丢弃（DropOldest / DropNewest）
	Pending   int   // 当前在途任务总数（队列中 + 执行中）
	Running   int   // 当前正在执行任务的分片数
}

// Pipe 是按 key 串行化的管道。并发安全。
type Pipe struct {
	cfg    config
	shards []*shard

	closed    atomic.Bool
	closeOnce sync.Once
	closeCh   chan struct{}
	wg        sync.WaitGroup

	// prodMu/prodWG 用于「关闭时安全排空生产者」：
	// 绝不能在还有生产者可能往分片队列发送时 close(队列) —— 那会 panic
	// （send on closed channel）。这与 xqueue.Bounded 踩过的是同一个坑。
	prodMu sync.RWMutex
	prodWG sync.WaitGroup

	submitted atomic.Int64
	completed atomic.Int64
	failed    atomic.Int64
	panics    atomic.Int64
	dropped   atomic.Int64
	pending   atomic.Int64
	running   atomic.Int64
}

// task 是一次提交。
type task struct {
	ctx  context.Context
	key  string
	fn   func(ctx context.Context) error
	done chan error // 非 nil 表示调用方会同步等待结果
}

// shard 是一个分片：私有队列 + 单 worker（保证该分片内串行）。
type shard struct {
	idx int
	ch  chan *task
}

// New 创建按 key 串行管道。
//
// 创建即启动 worker goroutine，因此**必须**在使用完后调用 Close（否则 goroutine 泄漏）。
func New(opts ...Option) *Pipe {
	cfg := config{
		shards:    defaultShards(),
		queueSize: 1024,
		overflow:  xcontainer.Block,
	}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.queueSize <= 0 {
		cfg.queueSize = 1
	}

	p := &Pipe{
		cfg:     cfg,
		closeCh: make(chan struct{}),
		shards:  make([]*shard, cfg.shards),
	}
	for i := range p.shards {
		p.shards[i] = &shard{idx: i, ch: make(chan *task, cfg.queueSize)}
	}
	for _, s := range p.shards {
		p.wg.Add(1)
		go p.worker(s)
	}
	return p
}

// worker 是分片的消费循环：**单 goroutine**，因此该分片内的任务天然串行。
func (p *Pipe) worker(s *shard) {
	defer p.wg.Done()
	for t := range s.ch {
		p.pending.Add(-1)
		p.running.Add(1)
		err := p.run(t)
		p.running.Add(-1)

		if err != nil {
			p.failed.Add(1)
			if p.cfg.onError != nil {
				p.cfg.onError(t.key, err)
			}
		} else {
			p.completed.Add(1)
		}
		if t.done != nil {
			t.done <- err
		}
	}
}

// run 执行任务并捕获 panic（worker 绝不能因为业务 panic 而退出，否则该分片永久卡死）。
func (p *Pipe) run(t *task) error {
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				p.panics.Add(1)
				if p.cfg.onPanic != nil {
					p.cfg.onPanic(t.key, r)
				}
				err = xerr.Newf("xkeyed: 任务 panic: %v", r)
			}
		}()
		err = t.fn(ctx)
	}()
	return err
}

// shardFor 返回 key 对应的分片。
func (p *Pipe) shardFor(key string) *shard {
	return p.shards[ShardOf(key, len(p.shards))]
}

// Shards 返回分片数。
func (p *Pipe) Shards() int {
	if p == nil {
		return 0
	}
	return len(p.shards)
}

// Submit 提交一个**不等待结果**的任务：同一 key 的任务按提交顺序串行执行。
//
// 返回值：
//   - nil：已入队
//   - ErrClosed：管道已关闭
//   - ctx.Err()：Block 策略下等待空位时 ctx 结束
//   - ErrQueueFull：DropNewest / Fail 策略下队列已满
//
// ⚠️ 因为不等待结果，任务的业务错误**不会**通过返回值暴露，
// 必须通过 WithOnError 回调或 Do 来获取。
func (p *Pipe) Submit(ctx context.Context, key string, fn func(ctx context.Context) error) error {
	if p == nil {
		return ErrClosed
	}
	if fn == nil {
		return ErrNilFunc
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// 登记为「在途生产者」：Close 必须等我们退出后才能关闭队列
	p.prodMu.RLock()
	if p.closed.Load() {
		p.prodMu.RUnlock()
		return ErrClosed
	}
	p.prodWG.Add(1)
	p.prodMu.RUnlock()
	defer p.prodWG.Done()

	t := &task{ctx: ctx, key: key, fn: fn}
	p.submitted.Add(1)

	s := p.shardFor(key)
	switch p.cfg.overflow {
	case xcontainer.DropOldest:
		select {
		case s.ch <- t:
			p.pending.Add(1)
			return nil
		default:
			// 丢弃队首（最旧），再放新任务
			select {
			case <-s.ch:
				p.pending.Add(-1)
				p.dropped.Add(1)
			default:
			}
			select {
			case s.ch <- t:
				p.pending.Add(1)
				return nil
			default:
				p.dropped.Add(1)
				return ErrQueueFull
			}
		}

	case xcontainer.DropNewest:
		select {
		case s.ch <- t:
			p.pending.Add(1)
			return nil
		default:
			p.dropped.Add(1)
			return ErrQueueFull
		}

	case xcontainer.Fail:
		select {
		case s.ch <- t:
			p.pending.Add(1)
			return nil
		default:
			p.dropped.Add(1)
			return ErrQueueFull
		}

	default: // Block：阻塞提交形成背压
		select {
		case s.ch <- t:
			p.pending.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closeCh:
			return ErrClosed
		}
	}
}

// Do 提交任务并**同步等待结果**（返回任务自身的 error）。
//
// 语义要点：
//   - 同 key 的并发 Do 会按到达顺序串行执行（这是本包的核心保证）；
//   - 调用方的 ctx 同时用于排队（背压）与任务执行（任务内部应遵守 ctx）。
func (p *Pipe) Do(ctx context.Context, key string, fn func(ctx context.Context) error) error {
	if p == nil {
		return ErrClosed
	}
	if fn == nil {
		return ErrNilFunc
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p.prodMu.RLock()
	if p.closed.Load() {
		p.prodMu.RUnlock()
		return ErrClosed
	}
	p.prodWG.Add(1)
	p.prodMu.RUnlock()
	defer p.prodWG.Done()

	t := &task{ctx: ctx, key: key, fn: fn, done: make(chan error, 1)}
	p.submitted.Add(1)

	s := p.shardFor(key)
	select {
	case s.ch <- t:
		p.pending.Add(1)
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closeCh:
		return ErrClosed
	}

	select {
	case err := <-t.done:
		return err
	case <-ctx.Done():
		// 任务已经在执行了，无法取消它；只能不再等待。
		// 注意：任务仍会被执行完（并计入 Completed/Failed）。
		return ctx.Err()
	}
}

// DoValue 提交「带返回值」的任务并等待结果（包级泛型函数：Go 的方法不能带类型参数）。
//
// 出错时返回零值与错误；ctx 结束返回 ctx.Err()（任务可能仍在后台执行完）。
func DoValue[T any](ctx context.Context, p *Pipe, key string, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	if p == nil {
		return zero, ErrClosed
	}
	if fn == nil {
		return zero, ErrNilFunc
	}

	result := make(chan T, 1)
	err := p.Do(ctx, key, func(cctx context.Context) error {
		v, ferr := fn(cctx)
		if ferr == nil {
			result <- v
		}
		return ferr
	})
	if err != nil {
		return zero, err
	}
	select {
	case v := <-result:
		return v, nil
	default:
		// fn 返回了错误（Do 已把错误返回给我们），或根本没写入结果
		return zero, nil
	}
}

// Pending 返回当前在途任务数。
func (p *Pipe) Pending() int {
	if p == nil {
		return 0
	}
	return int(p.pending.Load())
}

// Stats 返回统计快照。
func (p *Pipe) Stats() Stats {
	if p == nil {
		return Stats{}
	}
	return Stats{
		Shards:    len(p.shards),
		Submitted: p.submitted.Load(),
		Completed: p.completed.Load(),
		Failed:    p.failed.Load(),
		Panics:    p.panics.Load(),
		Dropped:   p.dropped.Load(),
		Pending:   int(p.pending.Load()),
		Running:   int(p.running.Load()),
	}
}

// IsClosed 返回管道是否已关闭。
func (p *Pipe) IsClosed() bool {
	if p == nil {
		return true
	}
	return p.closed.Load()
}

// Close 优雅关闭：停止接收新任务，**等待在途任务全部执行完**，然后返回。
//
// 重复调用安全。关闭后 Submit/Do 返回 ErrClosed。
//
// 注意：Close 会一直等所有分片队列排空。若某个任务卡住（例如内部死循环且不理会 ctx），
// Close 就会一直阻塞 —— 这是刻意的（宁可暴露问题，也不要静默丢弃用户数据）。
// 需要「限时关闭」请用 CloseCtx。
func (p *Pipe) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		// 顺序很重要：
		//  1) 置关闭标志（阻止新生产者登记）
		//  2) 关 closeCh（唤醒阻塞在 Block 策略上的生产者，让它们退出）
		//  3) 等所有在途生产者退出（此后不可能再有人往队列发送）
		//  4) 才能安全地关闭分片队列，让 worker 排空后退出
		p.prodMu.Lock()
		p.closed.Store(true)
		p.prodMu.Unlock()
		close(p.closeCh)
		p.prodWG.Wait()
		for _, s := range p.shards {
			close(s.ch)
		}
	})
	p.wg.Wait()
	return nil
}

// CloseCtx 与 Close 相同，但最多等待 ctx 结束；超时返回 ctx.Err()。
//
// 超时后后台 worker 仍会继续把队列排空（不丢任务），只是调用方不再等待。
func (p *Pipe) CloseCtx(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.prodMu.Lock()
		p.closed.Store(true)
		p.prodMu.Unlock()
		close(p.closeCh)
		p.prodWG.Wait()
		for _, s := range p.shards {
			close(s.ch)
		}
	})

	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait 阻塞直到所有在途任务执行完（不关闭管道，之后仍可继续提交）。
//
// 用途：测试里等待「异步提交的任务都跑完了」，避免用固定 sleep。
func (p *Pipe) Wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	for {
		if p.pending.Load() == 0 && p.running.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return xerr.Wrapf(ctx.Err(), "xkeyed: 仍有 %d 个任务在途", p.pending.Load())
		case <-time.After(200 * time.Microsecond):
		}
	}
}

// 设计说明：worker 的 panic 保护刻意**手写**而不是用 xsafe.Go/Try ——
// 因为这里需要在 recover 时同时拿到「panic 值」与「任务 key」以便精准回调，
// 而 xsafe 的封装签名只暴露 panic 值。手写只有 6 行，且不损失任何能力。
