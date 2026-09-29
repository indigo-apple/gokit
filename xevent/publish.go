package xevent

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 订阅选项
// ---------------------------------------------------------------------------

// SubscribeOption 是订阅选项。
type SubscribeOption func(*subConfig)

// WithBuffer 设置该订阅的入口缓冲长度（背压水位）。
//
// 缓冲越大抗突发能力越强，但也意味着「崩溃时丢得更多」、内存占用更高。
func WithBuffer(n int) SubscribeOption {
	return func(c *subConfig) {
		if n > 0 {
			c.buffer = n
		}
	}
}

// WithOverflow 设置该订阅的溢出策略（默认继承总线设置）。
func WithOverflow(p xcontainer.OverflowPolicy) SubscribeOption {
	return func(c *subConfig) { c.overflow = p.Norm() }
}

// WithConcurrency 设置该订阅的消费者数量（默认 1）。
//
// ⚠️ 大于 1 会**打破顺序保证**：同一订阅的事件会被并发处理。
// 需要「并行但局部有序」请改用发布方的 WithKey（同 key 仍严格串行）。
func WithConcurrency(n int) SubscribeOption {
	return func(c *subConfig) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// WithRetry 设置失败重试次数与基础退避（默认 0 次 = 不重试）。
//
// 退避按指数增长 + 50%~100% 抖动，上限 5s（抖动用于避免大量订阅同时重试形成尖峰）。
func WithRetry(times int, backoff ...time.Duration) SubscribeOption {
	return func(c *subConfig) {
		if times >= 0 {
			c.retryTimes = times
		}
		if len(backoff) > 0 && backoff[0] > 0 {
			c.retryBackoff = backoff[0]
		}
	}
}

// WithDeadLetter 设置死信处理器：重试耗尽后把事件交给它（落库 / 告警 / 人工补偿）。
//
// ★ 这是「不丢事件」的最后一道闸。不设置时失败事件只会记入 Stats.Failed
// 并触发总线级 WithOnError（可能被忽略）—— 生产环境务必设置其中之一。
func WithDeadLetter(dl func(ctx context.Context, ev Event, err error)) SubscribeOption {
	return func(c *subConfig) { c.deadLetter = dl }
}

// WithOnce 只消费一条事件后自动取消订阅。
//
// 典型用途：一次性初始化钩子、等待某个信号事件。
func WithOnce() SubscribeOption {
	return func(c *subConfig) { c.once = true }
}

// WithFilter 设置前置过滤器；返回 false 的事件被跳过（不计失败、不重试）。
//
// 过滤器在**投递时**执行（不是订阅时），因此可以依赖事件内容。
func WithFilter(pred func(Event) bool) SubscribeOption {
	return func(c *subConfig) { c.filter = pred }
}

// WithMiddleware 为订阅的 handler 套上中间件（按传入顺序包裹，第一个在最外层）。
func WithMiddleware(mw ...Middleware) SubscribeOption {
	return func(c *subConfig) {
		for _, m := range mw {
			if m != nil {
				c.middleware = append(c.middleware, m)
			}
		}
	}
}

// WithGroup 设置消费组：**同组内每条事件只投给一个订阅**（组内负载均衡），
// 不同组之间照常广播。
//
// 场景：同一主题要「水平扩展多个 worker 但不能重复处理」。
// ⚠️ 组内选择是进程内轮询，不做持久化偏移量 —— 即「至少一次」也保证不了，
// 需要精确一次请用支持消费组的 MQ（xmq）。
func WithGroup(group string) SubscribeOption {
	return func(c *subConfig) { c.group = group }
}

// WithName 给订阅起名（仅用于诊断与统计输出）。
func WithName(name string) SubscribeOption {
	return func(c *subConfig) { c.name = name }
}

// WithPull 把订阅切换为**拉模式**：事件流入 Events() 通道，由调用方自行消费。
//
// 语义与限制：
//   - 拉模式下**不能**再传 handler（同时使用会返回 ErrPullWithHandler）；
//   - 消费者读取即视为处理完成（无 ack / 无重试 / 无死信）；
//   - 顺序与入口队列一致（单 pump goroutine），此时 WithConcurrency 无效。
//
// 适用场景：需要按自己的节奏批量消费（如攒批写库、限速处理）。
func WithPull() SubscribeOption {
	return func(c *subConfig) { c.pull = true }
}

// ---------------------------------------------------------------------------
// 发布 / 关闭 / 观测
// ---------------------------------------------------------------------------

// Publish 实现 Bus。
func (b *bus) Publish(ctx context.Context, topic string, payload any, opts ...PublishOption) error {
	if topic == "" {
		return ErrEmptyTopic
	}
	return b.publish(ctx, Event{Topic: topic, Payload: payload}, opts, false)
}

// PublishEvent 实现 Bus：发布已构造好的事件。
//
// 已设置的事件字段（ID/Timestamp/Key/Headers）优先于选项，不会被覆盖。
func (b *bus) PublishEvent(ctx context.Context, ev Event) error {
	if ev.Topic == "" {
		return ErrEmptyTopic
	}
	return b.publish(ctx, ev, nil, true)
}

// PublishSync 是 Publish(..., WithSync(true)) 的便捷形式。
func (b *bus) PublishSync(ctx context.Context, topic string, payload any, opts ...PublishOption) error {
	return b.publish(ctx, Event{Topic: topic, Payload: payload}, opts, false, WithSync(true))
}

// publish 是发布实现。
//
// keepEvent=true 表示事件来自 PublishEvent，其既有字段优先。
func (b *bus) publish(ctx context.Context, ev Event, opts []PublishOption, keepEvent bool, extra ...PublishOption) error {
	if b == nil {
		return ErrNilBus
	}
	if b.closed.Load() {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pc := publishConfig{}
	for _, o := range append(opts, extra...) {
		if o != nil {
			o(&pc)
		}
	}

	// ── 补全事件字段 ──
	if !keepEvent || ev.ID == "" {
		if pc.eventID != "" {
			ev.ID = pc.eventID
		} else if ev.ID == "" {
			ev.ID = newEventID()
		}
	}
	if ev.Timestamp.IsZero() {
		if pc.timestamp.IsZero() {
			ev.Timestamp = time.Now()
		} else {
			ev.Timestamp = pc.timestamp
		}
	}
	if pc.traceID != "" {
		ev.TraceID = pc.traceID
	}
	if pc.source != "" {
		ev.Source = pc.source
	}
	if pc.ordered {
		ev.Key = orderedKey
	} else if pc.key != "" && ev.Key == "" {
		ev.Key = pc.key
	}
	if len(pc.headers) > 0 || ev.TraceID != "" {
		// 统一在这里做「拷贝一次 + 合并」，原因：
		//  1. 不能直接改 ev.Headers —— 它可能是调用方传入的 map，且事件会投给多个订阅
		//     （并发写同一个 map 会 panic）；
		//  2. trace_id 必须**恒定注入** —— 早期版本只在 Headers 为 nil 时注入，
		//     导致「既带自定义头又带 TraceID」时 trace_id 静默丢失。
		h := make(map[string]string, len(ev.Headers)+len(pc.headers)+1)
		for k, v := range ev.Headers {
			h[k] = v
		}
		for k, v := range pc.headers {
			h[k] = v
		}
		if ev.TraceID != "" {
			if _, ok := h[traceKey]; !ok {
				h[traceKey] = ev.TraceID
			}
		}
		ev.Headers = h
	}

	// ── 延迟与同步互斥（语义冲突：延迟投递时调用方不可能等到「投递完成」）──
	if pc.delay > 0 {
		if pc.sync {
			return xerr.NewC(xerr.CodeInvalidParam, "xevent: WithDelay 与 WithSync 不能同时使用")
		}
		b.scheduleDelayed(ctx, ev, pc.delay)
		return nil
	}

	b.recordReplay(ev)
	b.published.Add(1)

	// ── 找出投递目标（含消费组选择）──
	targets := b.matching(ev.Topic)
	if len(targets) == 0 {
		return nil // 无人订阅：静默成功（发布方不需要知道有没有人听）
	}

	// ── 异步：入队即返回 ──
	if !pc.sync {
		for _, s := range targets {
			if s.ctx.Err() != nil {
				continue // 订阅已取消（如 WithOnce 已消费完）：跳过，不算发布失败
			}
			err := b.enqueue(ctx, s, delivery{ev: ev})
			if err == nil || errors.Is(err, errSubStopped) {
				continue
			}
			if errors.Is(err, ErrQueueFull) {
				return xerr.WithMeta(err, "topic", ev.Topic, "pattern", s.pattern)
			}
			return err
		}
		return nil
	}

	// ── 同步：等所有目标投递完成并聚合错误 ──
	acks := make([]chan error, 0, len(targets))
	for _, s := range targets {
		ack := make(chan error, 1)
		if err := b.enqueue(ctx, s, delivery{ev: ev, ack: ack}); err != nil {
			acks = append(acks, closedAck(err))
			continue
		}
		acks = append(acks, ack)
	}

	var errs []error
	for _, ack := range acks {
		select {
		case err := <-ack:
			if err != nil {
				errs = append(errs, err)
				if b.cfg.errorPolicy == ErrorFailFast {
					return errs[0]
				}
			}
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
			return xerr.NewMulti(append(errs, ctx.Err())...)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return xerr.NewMulti(errs...)
}

// closedAck 返回一个「已带回错误」的回执通道，用于入队失败时保持聚合逻辑统一。
func closedAck(err error) chan error {
	ch := make(chan error, 1)
	ch <- err
	return ch
}

// enqueue 把投递放入订阅队列，按溢出策略处理满队列。
func (b *bus) enqueue(ctx context.Context, s *subscription, d delivery) error {
	switch s.cfg.overflow {
	case xcontainer.DropOldest:
		select {
		case s.ch <- d:
			return nil
		default:
			// 丢最旧的
			select {
			case <-s.ch:
				s.dropped.Add(1)
				b.dropped.Add(1)
			default:
			}
			select {
			case s.ch <- d:
				return nil
			default:
				s.dropped.Add(1)
				b.dropped.Add(1)
				return nil
			}
		}

	case xcontainer.DropNewest:
		// 丢新事件但**不报错**：DropNewest 的语义是「宁可丢也不阻塞调用方」，
		// 调用方不需要（也不应该）为此写错误分支。需要感知容量不足请用 Fail。
		select {
		case s.ch <- d:
			return nil
		default:
			s.dropped.Add(1)
			b.dropped.Add(1)
			if d.ack != nil {
				// 同步发布必须回执，否则调用方会一直等
				d.ack <- nil
			}
			return nil
		}

	case xcontainer.Fail:
		// 容量不足即报错（调用方可据此降级/重试/告警）
		select {
		case s.ch <- d:
			return nil
		default:
			s.dropped.Add(1)
			b.dropped.Add(1)
			if d.ack != nil {
				d.ack <- ErrQueueFull
			}
			return ErrQueueFull
		}

	default: // Block：阻塞发布形成背压
		// 总线关闭优先判定（确定性）：否则「总线已关闭」与「订阅已取消」同时就绪时
		// select 会随机选择，错误类型变得不可预测。
		if b.closed.Load() {
			return ErrClosed
		}
		select {
		case s.ch <- d:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return errSubStopped
		case <-b.done:
			return ErrClosed
		}
	}
}

// matching 返回本次发布应投递的订阅列表（已处理消费组）。
//
// 消费组语义：同一 (group) 的多个订阅里**只挑一个**（轮询），组之间互不影响。
func (b *bus) matching(topic string) []*subscription {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]*subscription, 0, len(b.subs))
	groups := make(map[string][]*subscription)
	for _, s := range b.subs {
		if !Match(s.pattern, topic) {
			continue
		}
		if s.cfg.group != "" {
			groups[s.cfg.group] = append(groups[s.cfg.group], s)
			continue
		}
		out = append(out, s)
	}
	for g, members := range groups {
		rr, ok := b.groupRR[g]
		if !ok {
			rr = &atomic.Uint64{}
			b.groupRR[g] = rr
		}
		out = append(out, members[rr.Add(1)%uint64(len(members))])
	}
	return out
}

// scheduleDelayed 安排一次延迟投递。
func (b *bus) scheduleDelayed(ctx context.Context, ev Event, d time.Duration) {
	b.recordReplay(ev)
	b.published.Add(1)

	b.timerMu.Lock()
	if b.closed.Load() {
		b.timerMu.Unlock()
		return
	}
	b.timerWG.Add(1)
	var timer *time.Timer
	timer = time.AfterFunc(d, func() {
		defer b.timerWG.Done()
		b.timerMu.Lock()
		delete(b.timers, timer)
		b.timerMu.Unlock()

		if b.closed.Load() {
			return
		}
		// 延迟到点后按异步投递（用后台 ctx：调用方的 ctx 可能早已结束）
		bg := context.Background()
		for _, s := range b.matching(ev.Topic) {
			_ = b.enqueue(bg, s, delivery{ev: ev})
		}
	})
	b.timers[timer] = struct{}{}
	b.timerMu.Unlock()

	_ = ctx
}

// recordReplay 记录事件用于 Replay（仅在配置了缓冲时）。
func (b *bus) recordReplay(ev Event) {
	if b.replayN <= 0 {
		return
	}
	cp := ev
	cp.done = nil // 不保留回执通道，避免重放时误用
	b.replayMu.Lock()
	if len(b.replay) >= b.replayN {
		// 环形淘汰：一次移动一半，避免每次都 O(n) 拷贝
		half := b.replayN / 2
		if half < 1 {
			half = 1
		}
		copy(b.replay, b.replay[half:])
		b.replay = b.replay[:len(b.replay)-half]
	}
	b.replay = append(b.replay, cp)
	b.replayMu.Unlock()
}

// Replay 把最近 n 条匹配 topic 的历史事件按原顺序重新投递给 h（同步执行）。
//
// 用途：本地排查 / 数据修复脚本 / 新订阅者「补课」。
//
// 注意：
//   - 只重放内存里保留的部分（受 WithReplayBuffer 限制）；
//   - 重放不会再次写入重放缓冲（避免自增长），也不会触发订阅；
//   - n <= 0 表示重放全部已保留的匹配事件。
func (b *bus) Replay(ctx context.Context, topic string, h Handler, n int) error {
	if b == nil {
		return ErrNilBus
	}
	if h == nil {
		return ErrNilHandler
	}
	if ctx == nil {
		ctx = context.Background()
	}

	b.replayMu.Lock()
	copied := make([]Event, len(b.replay))
	copy(copied, b.replay)
	b.replayMu.Unlock()

	// 从后往前挑最近的 n 条，再反转回时间顺序
	selected := make([]Event, 0, n)
	if n <= 0 {
		n = len(copied)
	}
	for i := len(copied) - 1; i >= 0 && len(selected) < n; i-- {
		if Match(topic, copied[i].Topic) {
			selected = append(selected, copied[i])
		}
	}
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}

	var errs []error
	for _, ev := range selected {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := h(ctx, ev); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return xerr.NewMulti(errs...)
}

// ReplayCount 返回当前保留的可重放事件数。
func (b *bus) ReplayCount() int {
	if b == nil {
		return 0
	}
	b.replayMu.Lock()
	defer b.replayMu.Unlock()
	return len(b.replay)
}

// Stats 实现观测：返回总线累计统计。
type Stats struct {
	Published   int64 // 累计发布（含延迟中）
	Delivered   int64 // 累计成功投递
	Failed      int64 // 累计最终失败（重试耗尽）
	Retried     int64 // 累计重试次数
	Dropped     int64 // 累计丢弃（溢出策略）
	Filtered    int64 // 累计被过滤
	Panics      int64 // 累计 handler panic
	Subscribers int   // 当前订阅数
	QueueDepth  int   // 当前所有订阅的排队总数
	ReplayKept  int   // 当前保留的可重放事件数
}

// Stats 实现 Bus 的观测能力。
func (b *bus) Stats() Stats {
	if b == nil {
		return Stats{}
	}
	b.mu.RLock()
	n := len(b.subs)
	depth := 0
	for _, s := range b.subs {
		depth += len(s.ch)
	}
	b.mu.RUnlock()

	return Stats{
		Published:   b.published.Load(),
		Delivered:   b.delivered.Load(),
		Failed:      b.failed.Load(),
		Retried:     b.retried.Load(),
		Dropped:     b.dropped.Load(),
		Filtered:    b.filtered.Load(),
		Panics:      b.panics.Load(),
		Subscribers: n,
		QueueDepth:  depth,
		ReplayKept:  b.ReplayCount(),
	}
}

// Subscriptions 返回所有订阅的统计快照（按 pattern 排序，便于断言）。
func (b *bus) Subscriptions() []SubStats {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	out := make([]SubStats, 0, len(b.subs))
	for _, s := range b.subs {
		out = append(out, s.Stats())
	}
	b.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Close 实现 Bus：优雅关闭。
//
// 顺序（每一步都有理由）：
//  1. 置关闭标志 → 新的 Publish 直接返回 ErrClosed；
//  2. 关 done → 唤醒阻塞在背压上的发布方；
//  3. 停掉所有延迟定时器（否则它们会在关闭后再投一次）；
//  4. 取消所有订阅（worker 会把已入队的事件排空后再退出 —— 不丢已接收的事件）；
//  5. 等待全部 goroutine 退出，或 ctx 结束。
func (b *bus) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	b.closeOnce.Do(func() {
		b.closed.Store(true)
		close(b.done)

		b.timerMu.Lock()
		timers := b.timers
		b.timers = make(map[*time.Timer]struct{})
		b.timerMu.Unlock()
		for t := range timers {
			// ★ 关键：timer.Stop() 返回 true 表示「成功阻止了回调」，
			// 此时回调里的 timerWG.Done() 永远不会执行 —— 必须由这里补上，
			// 否则 Close 会永久阻塞在 timerWG.Wait() 上（曾经真实发生过的死锁）。
			if t.Stop() {
				b.timerWG.Done()
			}
		}

		b.mu.RLock()
		subs := make([]*subscription, len(b.subs))
		copy(subs, b.subs)
		b.mu.RUnlock()

		// 这里只做「非阻塞通知」（stop 不等待 worker）。
		// 原因：真正可能长时间阻塞的等待必须放在 Once **之外**，
		// 否则下面的 ctx 超时检查永远执行不到 —— Close 就变成不可打断的挂起。
		for _, s := range subs {
			s.stop()
		}

		go func() {
			for _, s := range subs {
				s.wg.Wait() // worker 排空队列后退出
				s.detach()  // 摘除 + 关 keyed 管道
			}
			b.mu.Lock()
			b.subs = nil // 兜底清空引用（detach 已各自摘过）
			b.mu.Unlock()

			b.timerWG.Wait() // 等延迟定时器回调结束
			close(b.drained)
		}()
	})

	select {
	case <-b.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsClosed 返回总线是否已关闭。
func (b *bus) IsClosed() bool {
	if b == nil {
		return true
	}
	return b.closed.Load()
}

// randFloat 返回 [0,1) 的随机数（用于退避抖动）。
func randFloat() float64 { return rand.Float64() }
