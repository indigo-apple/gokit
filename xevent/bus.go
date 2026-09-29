package xevent

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xkeyed"
)

// ---------------------------------------------------------------------------
// 接口
// ---------------------------------------------------------------------------

// Bus 是进程内事件总线。
type Bus interface {
	// Publish 发布一个事件（载荷为任意对象）。
	Publish(ctx context.Context, topic string, payload any, opts ...PublishOption) error
	// PublishEvent 发布一个已构造好的事件（用于跨进程转换、重放等场景）。
	PublishEvent(ctx context.Context, ev Event) error
	// Subscribe 订阅主题模式。
	//
	// ⚠️ 必须在拿到 Subscription 后调用 Cancel 或 Unsubscribe 释放；
	// 否则该订阅的队列与 worker 会一直存在（内存 + goroutine 泄漏）。
	Subscribe(topic string, h Handler, opts ...SubscribeOption) (Subscription, error)
	// Unsubscribe 取消订阅（等价于 s.Cancel() 并从总线摘除）。
	Unsubscribe(s Subscription) error
	// Close 关闭总线：停止接收新事件，取消所有订阅，等待在途投递结束。
	Close(ctx context.Context) error
	// Subscribers 返回匹配该 topic 的订阅数量。
	Subscribers(topic string) int

	// ── 以下为接口扩展（设计文档把它们写在具体类型上；提升到接口严格更有用，
	//    调用方无需类型断言即可拿到观测与重放能力）──

	// PublishSync 等价于 Publish(..., WithSync(true))。
	PublishSync(ctx context.Context, topic string, payload any, opts ...PublishOption) error
	// Stats 返回累计统计快照。
	Stats() Stats
	// Subscriptions 返回各订阅的统计快照。
	Subscriptions() []SubStats
	// Topics 返回当前订阅的主题模式（去重、升序）。
	Topics() []string
	// Replay 把最近 n 条匹配 topic 的历史事件按原顺序投递给 h（n<=0 表示全部）。
	Replay(ctx context.Context, topic string, h Handler, n int) error
	// ReplayCount 返回当前保留的可重放事件数。
	ReplayCount() int
	// IsClosed 返回总线是否已关闭。
	IsClosed() bool
}

// Subscription 是一次订阅。
type Subscription interface {
	// ID 返回订阅唯一标识。
	ID() string
	// Topic 返回订阅的主题模式。
	Topic() string
	// Cancel 取消订阅（幂等）。
	Cancel() error
	// Events 返回拉模式通道（仅在 WithPull 时有效；订阅取消后关闭）。
	Events() <-chan Event
	// Err 返回该订阅最近一次投递失败的错误（无则 nil）。
	Err() error
	// QueueDepth 返回当前待投递事件数（观测用）。
	QueueDepth() int
	// Stats 返回该订阅的统计快照。
	Stats() SubStats
}

// 编译期断言。
var (
	_ Bus          = (*bus)(nil)
	_ Subscription = (*subscription)(nil)
)

// errSubStopped 是内部信号：目标订阅在投递途中被取消（如 WithOnce 已消费完）。
//
// 刻意**不**对外暴露：对调用方来说「订阅自己消失了」不是发布失败，
// 因此 publish 会把它当作「跳过」处理。
var errSubStopped = errors.New("xevent: subscription stopped")

// traceKey 是 trace_id 在 Headers 中的键名。
const traceKey = "trace_id"

// ErrQueueFull 订阅缓冲已满且溢出策略为 Fail。
//
// 复用 xcontainer 的哨兵（同一个「容器已满」语义，避免调用方认两套错误）。
var ErrQueueFull = xcontainer.ErrFull

// ---------------------------------------------------------------------------
// 内部结构
// ---------------------------------------------------------------------------

// delivery 是一次投递：事件 + 可选的同步回执通道。
//
// 把 ack 放在 delivery 而不是 Event 上：一次发布要投给多个订阅，
// 每个订阅需要各自的回执；放进 Event 会让多个订阅争用同一个通道。
type delivery struct {
	ev  Event
	ack chan error
}

// subConfig 是订阅配置。
type subConfig struct {
	buffer       int
	overflow     xcontainer.OverflowPolicy
	concurrency  int
	retryTimes   int
	retryBackoff time.Duration
	deadLetter   func(ctx context.Context, ev Event, err error)
	once         bool
	filter       func(Event) bool
	middleware   []Middleware
	group        string
	name         string
	pull         bool
}

// subscription 是一次订阅的运行时状态。
type subscription struct {
	bus     *bus
	id      string
	pattern string
	handler Handler // 已套好中间件；拉模式为 nil
	cfg     subConfig

	ch     chan delivery // 入口队列（有界 → 背压）
	events chan Event    // 拉模式出口（仅拉模式创建，由 pump goroutine 关闭）
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	pipe *xkeyed.Pipe // 按 key 保序（懒创建）

	cancelOnce sync.Once
	stopOnce   sync.Once
	wg         sync.WaitGroup
	onceFired  atomic.Bool

	// lastErr 用互斥锁而不是 atomic.Value：
	// atomic.Value.Store 收到 nil 会 panic（"store of nil value"），
	// 而「最近一次投递错误」天然经常是 nil。写入频率极低（仅最终失败时），锁开销可忽略。
	errMu   sync.Mutex
	lastErr error

	delivered atomic.Int64
	failed    atomic.Int64
	dropped   atomic.Int64
	retried   atomic.Int64
	filtered  atomic.Int64
}

// bus 是 Bus 的实现。
type bus struct {
	cfg config

	mu      sync.RWMutex
	subs    []*subscription
	groupRR map[string]*atomic.Uint64

	closed    atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
	// drained 在「所有订阅 worker 都已退出」后关闭（由 Close 的后台 goroutine 负责）。
	// 单独拿出来是为了让 Close(ctx) 能真正**被打断**：等待逻辑绝不能写在
	// closeOnce.Do 内部，否则 ctx 超时检查永远执行不到 —— 这正是导致
	// Close 永久挂起的真实缺陷（写测试时被 catch 到）。
	drained chan struct{}

	// 延迟投递的定时器（Close 时必须全部停止）
	timerMu sync.Mutex
	timers  map[*time.Timer]struct{}
	timerWG sync.WaitGroup

	// 重放缓冲（环形，仅保留最近 n 条）
	replayMu sync.Mutex
	replay   []Event
	replayN  int

	published atomic.Int64
	delivered atomic.Int64
	failed    atomic.Int64
	dropped   atomic.Int64
	retried   atomic.Int64
	filtered  atomic.Int64
	panics    atomic.Int64
}

// New 创建事件总线。
//
// 创建后必须在使用完时调用 Close（释放订阅 worker 与延迟定时器）。
func New(opts ...Option) Bus {
	cfg := defaultConfig()
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.replayBuffer > 0 {
		cfg.replayBuffer = min(cfg.replayBuffer, 100000)
	}
	return &bus{
		cfg:     cfg,
		done:    make(chan struct{}),
		drained: make(chan struct{}),
		groupRR: make(map[string]*atomic.Uint64),
		timers:  make(map[*time.Timer]struct{}),
		replayN: cfg.replayBuffer,
	}
}

// ---------------------------------------------------------------------------
// 订阅
// ---------------------------------------------------------------------------

// Subscribe 实现 Bus。
func (b *bus) Subscribe(topic string, h Handler, opts ...SubscribeOption) (Subscription, error) {
	if b == nil {
		return nil, ErrNilBus
	}
	if b.closed.Load() {
		return nil, ErrClosed
	}
	if err := ValidatePattern(topic); err != nil {
		return nil, err
	}

	sc := subConfig{
		buffer:       b.cfg.buffer,
		overflow:     b.cfg.overflow,
		concurrency:  b.cfg.workers,
		retryBackoff: 50 * time.Millisecond,
	}
	for _, o := range opts {
		if o != nil {
			o(&sc)
		}
	}
	// 参数校验（显式报错优于静默行为）
	if sc.pull && h != nil {
		return nil, ErrPullWithHandler
	}
	if !sc.pull && h == nil {
		return nil, ErrNilHandler
	}
	if sc.buffer <= 0 {
		sc.buffer = 1
	}
	if sc.concurrency <= 0 {
		sc.concurrency = 1
	}
	sc.overflow = sc.overflow.Norm()

	ctx, cancel := context.WithCancel(context.Background())
	s := &subscription{
		bus:     b,
		id:      newEventID(),
		pattern: topic,
		cfg:     sc,
		ch:      make(chan delivery, sc.buffer),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	if sc.pull {
		s.events = make(chan Event, sc.buffer)
	} else {
		// 中间件按注册顺序包裹：第一个注册的在最外层
		s.handler = Chain(sc.middleware...)(h)
	}

	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	if sc.pull {
		s.wg.Add(1)
		go s.pump()
	} else {
		workers := sc.concurrency
		if sc.once {
			workers = 1 // WithOnce 语义下必须是单消费者，否则「消费一次」会投给多个 goroutine
		}
		for i := 0; i < workers; i++ {
			s.wg.Add(1)
			go s.worker()
		}
	}
	return s, nil
}

// Unsubscribe 实现 Bus。
func (b *bus) Unsubscribe(s Subscription) error {
	if s == nil {
		return ErrNoSubscription
	}
	sub, ok := s.(*subscription)
	if !ok || sub.bus != b {
		return ErrNoSubscription
	}
	return sub.Cancel()
}

// Subscribers 实现 Bus：返回匹配该 topic 的订阅数量。
func (b *bus) Subscribers(topic string) int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, s := range b.subs {
		if Match(s.pattern, topic) {
			n++
		}
	}
	return n
}

// Topics 返回当前所有订阅的主题模式（去重、升序）。
func (b *bus) Topics() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	seen := make(map[string]struct{}, len(b.subs))
	out := make([]string, 0, len(b.subs))
	for _, s := range b.subs {
		if _, ok := seen[s.pattern]; ok {
			continue
		}
		seen[s.pattern] = struct{}{}
		out = append(out, s.pattern)
	}
	b.mu.RUnlock()
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Subscription 实现
// ---------------------------------------------------------------------------

// ID 实现 Subscription。
func (s *subscription) ID() string { return s.id }

// Topic 实现 Subscription。
func (s *subscription) Topic() string { return s.pattern }

// Events 实现 Subscription（拉模式通道；非拉模式返回 nil）。
func (s *subscription) Events() <-chan Event { return s.events }

// Err 实现 Subscription：返回最近一次最终失败的错误（无则 nil）。
func (s *subscription) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

// stop 只做「停止消费」：取消 ctx 并关闭 done，让 worker 排空队列后退出。
//
// 与 Cancel 分开的原因：WithOnce / 内部触发时，调用点很可能就在 worker 自身，
// 此时不能执行 Cancel 里的 wg.Wait()（自己等自己 → 死锁）。
func (s *subscription) stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		close(s.done)
	})
}

// detach 把订阅从总线摘除并关闭其 keyed 管道（幂等，**不**等待 worker）。
//
// 前提：worker 已退出（或调用方另行保证不会再有人写入）。
func (s *subscription) detach() {
	s.cancelOnce.Do(func() {
		s.bus.mu.Lock()
		for i, x := range s.bus.subs {
			if x == s {
				s.bus.subs = append(s.bus.subs[:i], s.bus.subs[i+1:]...)
				break
			}
		}
		s.bus.mu.Unlock()

		if s.pipe != nil {
			_ = s.pipe.Close()
		}
	})
}

// Cancel 实现 Subscription：取消订阅并从总线摘除。
//
// 语义：**等 worker 把已入队的事件排空后**才返回（不丢已接收的事件），
// 因此理论上可能长时间阻塞（若某个 handler 不理会 ctx 且永不返回）。
// 需要「限时等待」请用 Close(ctx)，或自行放到 goroutine 里调用。
func (s *subscription) Cancel() error {
	if s == nil {
		return nil
	}
	s.stop()    // 1. 通知 worker 停止接收
	s.wg.Wait() // 2. 等 worker 排空队列并退出
	s.detach()  // 3. 摘除 + 关管道（幂等）
	return nil
}

// QueueDepth 返回当前待投递事件数。
func (s *subscription) QueueDepth() int {
	if s == nil {
		return 0
	}
	return len(s.ch)
}

// Stats 返回该订阅的统计。
func (s *subscription) Stats() SubStats {
	if s == nil {
		return SubStats{}
	}
	return SubStats{
		ID:        s.id,
		Pattern:   s.pattern,
		Name:      s.cfg.name,
		Group:     s.cfg.group,
		Pull:      s.cfg.pull,
		Queue:     len(s.ch),
		Delivered: s.delivered.Load(),
		Failed:    s.failed.Load(),
		Dropped:   s.dropped.Load(),
		Retried:   s.retried.Load(),
		Filtered:  s.filtered.Load(),
	}
}

// SubStats 是单个订阅的统计快照。
type SubStats struct {
	ID        string
	Pattern   string
	Name      string
	Group     string
	Pull      bool
	Queue     int
	Delivered int64
	Failed    int64
	Dropped   int64
	Retried   int64
	Filtered  int64
}

// ---------------------------------------------------------------------------
// 消费循环
// ---------------------------------------------------------------------------

// worker 是推模式消费循环。
//
// ctx 由订阅自身持有（Cancel 时结束），因此 handler 能感知到「总线正在关闭」。
func (s *subscription) worker() {
	defer s.wg.Done()
	for {
		select {
		case d := <-s.ch:
			s.handleDelivery(d, s.ctx)
		case <-s.done:
			// 排空队列（优雅取消：已入队的事件不丢）。
			//
			// ★ 这里的 ctx 必须去掉取消信号：s.ctx 此刻已被 stop() 取消，
			// 若继续用它投递，deliver 会认为「订阅已取消」而跳过 handler ——
			// 事件被静默丢弃（这正是「优雅关闭丢事件」的真实成因，测试抓到过）。
			dctx := context.WithoutCancel(s.ctx)
			for {
				select {
				case d := <-s.ch:
					s.handleDelivery(d, dctx)
				default:
					return
				}
			}
		}
	}
}

// pump 是拉模式循环：把队列搬到 events 通道。
//
// events 由本 goroutine**唯一持有写端**，因此可以安全地在退出时 close ——
// 消费者据此感知「订阅结束」。这也是不直接关闭 s.ch 的原因
// （发布方可能有并发写入，close 会造成 send on closed channel panic）。
func (s *subscription) pump() {
	defer s.wg.Done()
	defer close(s.events)
	for {
		select {
		case d := <-s.ch:
			if d.ack != nil {
				d.ack <- nil // 拉模式无 handler，投递即视为成功
			}
			select {
			case s.events <- d.ev:
			case <-s.done:
				return
			}
		case <-s.done:
			for {
				select {
				case d := <-s.ch:
					if d.ack != nil {
						d.ack <- nil
					}
					select {
					case s.events <- d.ev:
					case <-s.done:
						return
					}
				default:
					return
				}
			}
		}
	}
}

// handleDelivery 处理一次投递：按 key 保序分发 或 直接执行。
//
// hctx 是本次投递使用的 ctx：正常阶段是 s.ctx，排空阶段是「去掉取消信号」的派生 ctx。
func (s *subscription) handleDelivery(d delivery, hctx context.Context) {
	// 需要顺序（带 key 或全局有序）→ 交给 xkeyed 分片管道
	if d.ev.Key != "" {
		// 懒创建：只有真正出现带 key 的事件时才启动 keyed 管道
		if s.pipe == nil {
			s.pipe = xkeyed.New(
				xkeyed.WithShards(s.bus.cfg.keyedShards),
				xkeyed.WithQueueSize(s.cfg.buffer),
				xkeyed.WithOnError(func(_ string, err error) {
					if s.bus.cfg.onError != nil {
						s.bus.cfg.onError(s.pattern, d.ev, err)
					}
				}),
			)
		}
		err := s.pipe.Submit(hctx, d.ev.Key, func(ctx context.Context) error {
			herr := s.deliver(ctx, d.ev)
			if d.ack != nil {
				d.ack <- herr
			}
			return herr
		})
		if err != nil {
			// 订阅已取消 / 管道队列满：视为丢弃并回执
			s.dropped.Add(1)
			s.bus.dropped.Add(1)
			if d.ack != nil {
				select {
				case d.ack <- err:
				default:
				}
			}
		}
		return
	}

	err := s.deliver(hctx, d.ev)
	if d.ack != nil {
		d.ack <- err
	}
}

// deliver 执行「过滤 + 重试 + 死信」的完整投递流程。
func (s *subscription) deliver(ctx context.Context, ev Event) error {
	// WithOnce 已消费过 → 队列里残留的事件直接跳过（不回执错误，视为已完成）
	if s.cfg.once && s.onceFired.Load() {
		return nil
	}
	// 过滤器：不匹配视为「已处理」（不计失败，也不重试）
	if s.cfg.filter != nil && !s.cfg.filter(ev) {
		s.filtered.Add(1)
		s.bus.filtered.Add(1)
		return nil
	}

	var err error
	for attempt := 0; ; attempt++ {
		// ★ 只有「重试」才因为 ctx 取消而放弃。
		// 第 0 次（首次投递）绝不能跳过 —— 事件已经从队列取出，
		// 跳过就等于静默丢数据（优雅关闭 / WithOnce 都会命中这个分支）。
		if attempt > 0 && ctx.Err() != nil {
			break
		}
		ev.Attempt = attempt
		err = s.invoke(ctx, ev)
		if err == nil {
			s.delivered.Add(1)
			s.bus.delivered.Add(1)
			if s.cfg.once {
				// WithOnce：标记已消费并停止接收后续事件。
				// 注意不能在当前 goroutine 里执行 Cancel（会 wg.Wait() 等自己），
				// 因此先 stop，再交给后台完成摘除与清理。
				s.onceFired.Store(true)
				s.stop()
				go func() { _ = s.Cancel() }()
			}
			return nil
		}

		// 失败：是否还有重试机会
		if attempt >= s.cfg.retryTimes {
			break
		}
		s.retried.Add(1)
		s.bus.retried.Add(1)

		backoff := backoffFor(s.cfg.retryBackoff, attempt)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ErrClosed
		}
	}

	// 重试耗尽
	s.failed.Add(1)
	s.bus.failed.Add(1)
	s.errMu.Lock()
	s.lastErr = err
	s.errMu.Unlock()

	if s.cfg.deadLetter != nil {
		// 死信处理器：把事件交给业务自行落库/告警
		s.cfg.deadLetter(ctx, ev, err)
		return nil
	}
	if s.bus.cfg.onError != nil {
		s.bus.cfg.onError(s.pattern, ev, err)
	}
	return err
}

// invoke 调用 handler 并捕获 panic（worker 绝不能因业务 panic 而退出）。
func (s *subscription) invoke(ctx context.Context, ev Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.bus.panics.Add(1)
			err = xerr.NewfC(xerr.CodeInternal, "xevent: handler panic: %v", r)
		}
	}()
	return s.handler(ctx, ev)
}

// backoffFor 计算第 attempt 次重试的退避时长：指数增长 + 抖动，上限 5s。
func backoffFor(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = 50 * time.Millisecond
	}
	d := base
	for i := 0; i < attempt && d < 5*time.Second; i++ {
		d *= 2
	}
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	// 抖动 50%~100%，避免大量订阅同时重试造成尖峰
	half := d / 2
	return half + time.Duration(float64(half)*randFloat())
}
