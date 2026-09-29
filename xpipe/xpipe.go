// Package xpipe 提供**流式管道**：Source → Stage → Sink 的惰性数据处理链。
//
// 对应需求「pipe」。典型用法：
//
//	total, err := xpipe.Reduce(
//	    xpipe.Filter(
//	        xpipe.Map(xpipe.FromSlice(nums), func(i int) int { return i * 2 }),
//	        func(i int) bool { return i > 10 }),
//	    int64(0), func(acc int64, v int) int64 { return acc + int64(v) })
//
// # ⚠️ 为什么是「包级函数」而不是链式方法
//
// `p.Map(func(T) R) *Pipe[R]` 这种写法**在 Go 里无法编译** —— Go 的方法不能自带类型参数。
// 因此本包采用「包级泛型函数」风格（`xpipe.Map(p, fn)`），类型由 Go 自动推导。
//
// # ⚠️ 架构决策：拉取式（pull）而非推送式（push）
//
// 设计文档描述的是「每个阶段一条 channel + 一个 goroutine」的推送式管道。本实现刻意
// 改为**拉取式**：`Pipe` 只是一个「源 + 阶段链」的描述，终结操作（Drain/Collect/…）
// 顺链向下 `next()` 拉取元素。
//
// 理由（这是本包最重要的取舍）：
//
//	✓ **goroutine 泄漏在结构上不可能** —— 除 Parallel/Buffer/FromFunc 这类真正需要
//	  并发的阶段，全程单 goroutine，没有「上游还没关、下游已退出」的经典泄漏；
//	✓ **没有 close 竞态** —— 不需要在多处 close channel，也就不会出现
//	  send on closed channel / 重复 close / 关掉后仍有人读等一整类并发缺陷；
//	✓ **取消极其简单** —— 每次拉取前检查 ctx，天然不会裸读 channel
//	  （设计文档不变量 #2 说的「绝不裸读 channel」在拉取式下是默认成立的）；
//	✗ 阶段之间**不会自动流水线化**（上游算完一条才轮到下游处理它）。
//	  需要重叠时显式加 `Buffer(n)`（预取）或 `Parallel(n, fn)`（并行处理）。
//
// # 使用规则（重要）
//
//  1. **一个 Pipe 只能被终结一次**（拉取式管道是有状态的）。重复终结返回 ErrConsumed。
//  2. 终结操作返回后，管道内部创建的 goroutine **保证已退出**（终结时统一 cancel）。
//  3. 惰性：链上任何阶段在终结之前都不会执行任何工作；两个独立管道可以放心共享到终结为止。
//  4. panic 隔离：每个阶段的用户函数都有兜底 recover，panic 转成 error 走错误策略，
//     绝不会把整个进程带走。
package xpipe

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/indigo-apple/gokit/xerr"
)

// 哨兵错误。
var (
	// ErrConsumed 该管道已被终结过（拉取式管道只能消费一次）。
	ErrConsumed = xerr.NewC(xerr.CodeStateInvalid, "xpipe: pipe already consumed")

	// ErrNilPipe 传入了 nil 管道。
	ErrNilPipe = xerr.NewC(xerr.CodeMissingParam, "xpipe: pipe 为 nil")

	// ErrNilFunc 传给阶段的函数为 nil。
	ErrNilFunc = xerr.NewC(xerr.CodeMissingParam, "xpipe: 阶段函数不能为 nil")

	// ErrClosed 管道已关闭。
	ErrClosed = xerr.NewC(xerr.CodeStateInvalid, "xpipe: pipe is closed")
)

// ErrorPolicy 决定「阶段函数返回 error」时管道的处理方式。
//
// 命名说明：设计文档用的是 `FailFast / Skip / Collect`，但 Go 没有命名空间 ——
// 而终结操作 `xpipe.Collect` 已经占用了 `Collect` 这个名字，因此策略常量统一加
// `Policy` 前缀（`PolicyFailFast / PolicySkip / PolicyCollect`）。
type ErrorPolicy int

const (
	// FailFast 立刻中止整条管道并把错误返回给终结操作（默认）。
	//
	// 选它作为默认值的理由：**不静默丢数据**。数据处理链上悄悄跳过出错元素
	// 是排查噩梦，需要跳过请显式写 Skip 或 OnError。
	PolicyFailFast ErrorPolicy = iota
	// PolicySkip 跳过出错元素，继续处理后续元素（错误记入 Err()）。
	PolicySkip
	// PolicyCollect 与 PolicySkip 相同，但终结操作会返回聚合后的错误（MultiError）。
	PolicyCollect
)

// String 实现 fmt.Stringer。
func (p ErrorPolicy) String() string {
	switch p {
	case PolicyFailFast:
		return "PolicyFailFast"
	case PolicySkip:
		return "PolicySkip"
	case PolicyCollect:
		return "PolicyCollect"
	default:
		return "ErrorPolicy(" + itoa(int(p)) + ")"
	}
}

// itoa 是 strconv.Itoa 的极简替代（避免为一个调用引入 strconv）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// config 是管道配置（同一链条上的所有 Pipe 共享同一个指针）。
type config struct {
	buffer  int // 默认预取缓冲（Buffer 阶段未显式指定时使用）
	workers int // 默认并行度（Parallel 阶段未显式指定时使用）
	ordered bool
	policy  ErrorPolicy
	name    string
}

func defaultConfig() *config {
	return &config{buffer: 64, workers: 4, policy: PolicyFailFast}
}

// Option 是管道配置项（传给数据源构造函数）。
type Option func(*config)

// WithBufferSize 设置默认预取缓冲大小（默认 64；0 表示完全同步、无预取）。
func WithBufferSize(n int) Option {
	return func(c *config) {
		if n >= 0 {
			c.buffer = n
		}
	}
}

// WithWorkers 设置默认并行度（默认 4，供 Parallel 阶段使用）。
func WithWorkers(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.workers = n
		}
	}
}

// WithOrdered 设置 Parallel 阶段默认是否保序（默认 false = 谁先完成谁先出）。
func WithOrdered(ordered bool) Option {
	return func(c *config) { c.ordered = ordered }
}

// WithErrorPolicy 设置错误策略（默认 PolicyFailFast）。
func WithErrorPolicy(p ErrorPolicy) Option {
	return func(c *config) { c.policy = p }
}

// WithName 设置管道名（仅用于诊断输出）。
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// ---------------------------------------------------------------------------
// errBox：聚合阶段错误
// ---------------------------------------------------------------------------

// errBox 收集阶段产生的错误。
//
// 用互斥锁而不是 atomic.Value：需要保存「多个」错误（Collect 策略），
// 且 atomic.Value.Store(nil) 会 panic（xevent 里踩过）。
type errBox struct {
	mu    sync.Mutex
	errs  []error
	first error
}

func (b *errBox) add(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	if b.first == nil {
		b.first = err
	}
	// 上限保护：Collect 模式下海量错误不应无限吃内存
	if len(b.errs) < 64 {
		b.errs = append(b.errs, err)
	}
	b.mu.Unlock()
}

// err 返回聚合错误（Collect 策略下终结操作返回它）。
func (b *errBox) err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch len(b.errs) {
	case 0:
		return nil
	case 1:
		return b.errs[0]
	default:
		return xerr.NewMulti(b.errs...)
	}
}

// firstErr 返回第一个错误。
func (b *errBox) firstErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.first
}

// ---------------------------------------------------------------------------
// Pipe
// ---------------------------------------------------------------------------

// puller 是拉取函数：返回 (元素, 是否还有元素, 错误)。
//
// more=false 表示流结束；此时 value 无意义、err 必为 nil。
type puller[T any] func() (value T, more bool, err error)

// ctxHolder 是一个「派生 ctx + 它的 cancel」的不可变快照。
type ctxHolder struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// ctxBox 是同一条链**全链共享**的 ctx 槽位。
//
// 为什么要这么设计（这是本包第二个关键取舍）：
//   - 阶段是惰性闭包，若在构造时把 ctx 捕获进闭包，`WithContext` 之后再替换就**无法传播**
//     （曾经真实踩到：重试阶段的退避完全感知不到取消，一次 Collect 跑了 100+ 次 5s 退避）；
//   - 共享槽位让 `WithContext` 就地替换，**上下游所有阶段（含惰性阶段）立刻看到新 ctx**；
//   - 读路径用 atomic.Pointer（每次拉取都读一次 ctx，必须无锁）；
//   - 历史派生出的 ctx 全部登记，终结/关闭时统一 cancel，避免「旧 ctx 上的 goroutine」泄漏。
type ctxBox struct {
	cur atomic.Pointer[ctxHolder]
	mu  sync.Mutex
	all []*ctxHolder
}

func newCtxBox(parent context.Context) *ctxBox {
	b := &ctxBox{}
	b.derive(parent)
	return b
}

// derive 派生新 ctx 并设为当前（同时登记到「终结时统一取消」列表）。
func (b *ctxBox) derive(parent context.Context) *ctxHolder {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	h := &ctxHolder{ctx: ctx, cancel: cancel}
	b.mu.Lock()
	b.all = append(b.all, h)
	b.mu.Unlock()
	b.cur.Store(h)
	return h
}

// get 返回当前 ctx（无锁）。
func (b *ctxBox) get() context.Context {
	if b == nil {
		return context.Background()
	}
	if h := b.cur.Load(); h != nil {
		return h.ctx
	}
	return context.Background()
}

// cancelAll 取消全部派生 ctx（幂等）。终结/Close 时调用。
func (b *ctxBox) cancelAll() {
	if b == nil {
		return
	}
	b.mu.Lock()
	all := b.all
	b.all = nil
	b.mu.Unlock()
	for _, h := range all {
		h.cancel()
	}
}

// adopt 把另一个 ctxBox 的全部派生 ctx 纳入本箱的「统一取消」列表。
//
// 用途：Merge/Chain/Zip 这类「接管多条上游」的阶段，必须把上游已有的派生 ctx
// 一并接管 —— 否则上游在构造时就启动的 goroutine（Buffer/Parallel）永远等不到取消。
func (b *ctxBox) adopt(other *ctxBox) {
	if b == nil || other == nil || other == b {
		return
	}
	other.mu.Lock()
	all := other.all
	other.all = nil
	other.mu.Unlock()

	b.mu.Lock()
	b.all = append(b.all, all...)
	b.mu.Unlock()
}

// header 是同一条链上所有阶段**共享**的头部信息。
//
// 单独抽出来的原因：Go 的泛型不能把 `*Pipe[int]` 当作 `*Pipe[any]` 传递，
// 因此阶段构造需要一份「与元素类型无关」的共享载体（ctx 槽位/错误箱/配置）。
type header struct {
	name string
	cb   *ctxBox
	box  *errBox
	cfg  *config
}

// Pipe 是惰性数据管道。零值不可用，请用 FromXxx 系列构造。
//
// 并发安全性：Pipe 本身**不是**并发安全的 —— 一条管道只应由一个终结操作消费。
// （不同管道之间互不影响，可以并发终结。）
type Pipe[T any] struct {
	header
	pull puller[T]

	consumed atomic.Bool
	closed   atomic.Bool
}

// newStage 基于上游的共享头部创建新阶段管道。
func newStage[T any](h header, name string, pull puller[T]) *Pipe[T] {
	if name == "" {
		name = h.name
	}
	return &Pipe[T]{
		header: header{name: name, cb: h.cb, box: h.box, cfg: h.cfg},
		pull:   pull,
	}
}

// ctx 返回管道当前生效的 ctx（读共享槽位，因此 WithContext 能即时传播到全链）。
func (p *Pipe[T]) ctx() context.Context {
	if p == nil || p.cb == nil {
		return context.Background()
	}
	return p.cb.get()
}

// newSource 创建「源」管道（自己派生 ctx/cancel）。
func newSource[T any](ctx context.Context, cfg *config, name string, pull puller[T]) *Pipe[T] {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		cfg = defaultConfig()
	}
	// 派生而非直接用调用方的 ctx：终结操作需要 cancel 内部 goroutine，
	// 但**绝不能**去 cancel 调用方的 ctx。
	if cfg.name != "" {
		name = cfg.name // WithName 覆盖默认源名（"Range"/"FromSlice"…）
	}
	return &Pipe[T]{
		header: header{name: name, cb: newCtxBox(ctx), box: &errBox{}, cfg: cfg},
		pull:   pull,
	}
}

// Name 返回管道名。
func (p *Pipe[T]) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

// Ctx 返回管道当前使用的 ctx。
func (p *Pipe[T]) Ctx() context.Context { return p.ctx() }

// Err 返回管道记录的（第一个）错误。
//
// 注意：终结操作返回的 error 才是「权威结果」（FailFast 会直接返回它）；
// 本方法主要用于 Skip 策略下事后查证「跳过了什么」。
func (p *Pipe[T]) Err() error {
	if p == nil {
		return ErrNilPipe
	}
	return p.box.firstErr()
}

// ErrAll 返回管道记录的全部错误（Collect 策略下更有用）。
func (p *Pipe[T]) ErrAll() error {
	if p == nil {
		return ErrNilPipe
	}
	return p.box.err()
}

// IsClosed 返回管道是否已关闭或已消费。
func (p *Pipe[T]) IsClosed() bool {
	if p == nil {
		return true
	}
	return p.closed.Load() || p.consumed.Load()
}

// Close 关闭管道：取消内部 ctx（唤醒所有阻塞中的阶段 goroutine）并终止后续拉取。
//
// 幂等；多次调用安全。**不会**取消调用方传入的 ctx（WithContext 提供的那个）。
func (p *Pipe[T]) Close() error {
	if p == nil {
		return nil
	}
	if p.closed.CompareAndSwap(false, true) {
		p.cb.cancelAll()
	}
	return nil
}

// next 是「拉取下一个元素」的统一入口：先看关闭/取消，再问上游。
func (p *Pipe[T]) next() (T, bool, error) {
	var zero T
	if p == nil {
		return zero, false, ErrNilPipe
	}
	if p.closed.Load() {
		return zero, false, ErrClosed
	}
	if err := p.ctx().Err(); err != nil {
		return zero, false, err
	}
	return p.pull()
}

// begin 标记「开始消费」，防止一条管道被终结两次。
//
// 返回的 release 必须在终结结束时调用（保证内部 goroutine 全部退出）。
func (p *Pipe[T]) begin() (func(), error) {
	if p == nil {
		return func() {}, ErrNilPipe
	}
	if !p.consumed.CompareAndSwap(false, true) {
		return func() {}, ErrConsumed
	}
	return func() {
		p.closed.Store(true)
		// 终结即取消（全部派生 ctx）：保证 FromFunc/Buffer/Parallel 起的 goroutine 一定退出
		p.cb.cancelAll()
	}, nil
}

// filterNext 是「跳过空元素」类阶段的公共骨架：
// 反复向上游拉取，直到 pred 为真或上游结束。
func (p *Pipe[T]) filterNext(keep func(T) (bool, error)) (T, bool, error) {
	var zero T
	for {
		v, more, err := p.next()
		if err != nil || !more {
			return zero, false, err
		}
		ok, err := keep(v)
		if err != nil {
			// ★ 必须用 fail2（Skip/Collect 时 continue），不能用 fail ——
			// fail 返回 more=false 会被理解为「流结束」，Skip 策略就退化成「静默截断」
			// （真实缺陷：FilterCtx 出错后整个管道直接结束）。
			z, cont, ferr := p.fail2(zero, err)
			if ferr != nil {
				return z, false, ferr
			}
			if !cont {
				return zero, false, nil
			}
			continue
		}
		if ok {
			return v, true, nil
		}
	}
}

// fail2 是 fail 的「泛型结果版本」：按错误策略决定「继续还是中止」。
//
// 与 fail 的区别：fail 用于「跳过/中止」语义（filterNext），
// fail2 额外返回 needContinue，供需要「跳过但继续拉下一个」的阶段使用。
func (p *Pipe[T]) fail2(zero T, err error) (T, bool, error) {
	if err == nil {
		return zero, false, nil
	}
	p.box.add(err)
	if p.cfg.policy == PolicyFailFast {
		return zero, false, err
	}
	return zero, true, nil
}

// guard 执行用户函数并捕获 panic（panic 隔离：绝不让业务 panic 带崩进程）。
//
// ⚠️ 语义边界（踩过的坑）：guard **只**返回「panic 转换出来的错误」，
// 被包裹函数自身正常返回的 error 会被丢弃。因此绝不能写成
// `err = guard(func(){ out, err = fn(v) })` —— 那会把 fn 的真实错误覆盖成 nil。
// 需要连错误一起保留时请用 guardErr。
//
// 刻意手写而非套用 xsafe：这里需要把 panic 包成带包名的错误并交给错误策略，
// 而且 xpipe 的每个阶段都要用，手写只有几行、语义最直白。
func guard(fn func()) error {
	return guardErr(func() error {
		fn()
		return nil
	})
}

// guardErr 是 guard 的「返回 error」版本：既捕获 panic，也原样透出函数返回的错误。
func guardErr(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = xerr.WrapCf(e, xerr.CodeInternal, "xpipe: 阶段 panic")
				return
			}
			err = xerr.NewfC(xerr.CodeInternal, "xpipe: 阶段 panic: %v", r)
		}
	}()
	return fn()
}

// ---------------------------------------------------------------------------
// 生命周期阶段
// ---------------------------------------------------------------------------

// WithContext 让**整条链**改用新的 ctx（就地替换全链共享的 ctx 槽位）。
//
// 语义：
//   - 立刻对上下游所有阶段生效（包括已建好的惰性阶段）；
//   - 旧的派生 ctx 会在终结时统一取消，因此不会泄漏 goroutine；
//   - 必须在终结之前调用。
//
// 注意：**已经启动 goroutine 的并发阶段**（Buffer / Parallel / Merge / FromFunc）
// 在构造时就绑定了当时的 ctx，因此这些阶段应在 `WithContext` **之后**建立；
// 最稳妥的用法是直接把 ctx 传给数据源（如 `FromChannelCtx(ctx, ch)`）。
func WithContext[T any](p *Pipe[T], ctx context.Context) *Pipe[T] {
	if p == nil || ctx == nil || p.cb == nil {
		return p
	}
	p.cb.derive(ctx)
	return p
}

// Named 给管道起名（用于诊断）。传空串表示沿用上游名字。
//
// 实现说明：刻意**逐字段构造**新 Pipe 而不是 `out := *p` ——
// Pipe 含 atomic.Bool（带 noCopy），整体复制会被 go vet 判定为「复制锁」（实测确实报错）。
func Named[T any](p *Pipe[T], name string) *Pipe[T] {
	if p == nil {
		return p
	}
	if name == "" {
		name = p.name
	}
	out := newStage[T](p.header, name, p.pull)
	out.consumed.Store(p.consumed.Load())
	out.closed.Store(p.closed.Load())
	return out
}

// Buffer 插入一个**预取**阶段：用独立 goroutine 提前从上游取 n 个元素。
//
// 作用：让上下游重叠执行（拉取式管道默认串行：上游算完一条才轮到下游）。
// 代价：多一个 goroutine + n 个元素的内存 —— 这是本包唯一「为了流水线化」而起的 goroutine。
//
// n <= 0 时视为「无预取」（直接把上游透传），便于配置驱动时传 0 表示关闭。
func Buffer[T any](p *Pipe[T], n int) *Pipe[T] {
	if p == nil {
		return nil
	}
	if n <= 0 {
		return p
	}
	if n == 0 {
		n = p.cfg.buffer
	}

	type item struct {
		v    T
		more bool
		err  error
	}
	ch := make(chan item, n)
	ctx := p.ctx()

	go func() {
		defer close(ch)
		for {
			var it item
			v, more, err := p.next()
			it.v, it.more, it.err = v, more, err
			select {
			case ch <- it:
			case <-ctx.Done():
				return
			}
			if err != nil || !more {
				return
			}
		}
	}()

	out := newStage[T](p.header, "Buffer("+itoa(n)+")"+suffix(p.name), nil)
	out.pull = func() (T, bool, error) {
		var zero T
		select {
		case it, ok := <-ch:
			if !ok {
				return zero, false, nil
			}
			return it.v, it.more, it.err
		case <-ctx.Done():
			return zero, false, ctx.Err()
		}
	}
	return out
}

func suffix(name string) string {
	if name == "" {
		return ""
	}
	return " <- " + name
}

// ---------------------------------------------------------------------------
// 数据源
// ---------------------------------------------------------------------------

// FromSlice 用切片创建管道。
//
// ⚠️ 不复制切片：管道持有的是同一个底层数组（只读访问）。若切片会被并发改写，
// 请先自行复制（xslice.Clone）。
func FromSlice[T any](s []T, opts ...Option) *Pipe[T] {
	cfg := applyOpts(opts)
	i := 0
	return newSource(context.Background(), cfg, "FromSlice", func() (T, bool, error) {
		var zero T
		if i >= len(s) {
			return zero, false, nil
		}
		v := s[i]
		i++
		return v, true, nil
	})
}

// Just 用若干元素创建管道。
func Just[T any](vs ...T) *Pipe[T] { return FromSlice(vs) }

// Empty 创建空管道。
func Empty[T any]() *Pipe[T] { return FromSlice([]T(nil)) }

// Generate 生成 n 个元素（fn(i)），i 从 0 开始。
func Generate[T any](n int, fn func(i int) T, opts ...Option) *Pipe[T] {
	if fn == nil {
		fn = func(int) T { var zero T; return zero }
	}
	cfg := applyOpts(opts)
	i := 0
	return newSource(context.Background(), cfg, "Generate", func() (T, bool, error) {
		var zero T
		if i >= n {
			return zero, false, nil
		}
		v := fn(i)
		i++
		return v, true, nil
	})
}

// Range 生成 [start, end) 的整数序列（半开区间）。
func Range(start, end int, opts ...Option) *Pipe[int] {
	cfg := applyOpts(opts)
	i := start
	return newSource(context.Background(), cfg, "Range", func() (int, bool, error) {
		if i >= end {
			return 0, false, nil
		}
		v := i
		i++
		return v, true, nil
	})
}

// FromChannel 从 channel 读取，**尊重 channel 关闭**（关闭即流结束）。
//
// 取消安全：等待时同时监听 ctx.Done()，因此终结方提前退出不会泄漏 goroutine。
// 注意：channel 的关闭方必须是写入方（本包不会关闭调用方的 channel）。
func FromChannel[T any](ch <-chan T, opts ...Option) *Pipe[T] {
	return FromChannelCtx(context.Background(), ch, opts...)
}

// FromChannelCtx 是 FromChannel 的显式 ctx 版本。
//
// 等待 channel 时**同时**监听管道自身的取消信号（由终结操作/Close 触发），
// 因此即使上游 channel 永不关闭、下游提前收工，也不会泄漏 goroutine。
func FromChannelCtx[T any](ctx context.Context, ch <-chan T, opts ...Option) *Pipe[T] {
	cfg := applyOpts(opts)
	p := newSource[T](ctx, cfg, "FromChannel", nil)
	p.pull = func() (T, bool, error) {
		var zero T
		select {
		case v, ok := <-ch:
			if !ok {
				return zero, false, nil
			}
			return v, true, nil
		case <-p.ctx().Done():
			return zero, false, p.ctx().Err()
		}
	}
	return p
}

// FromFunc 用「推送式」函数创建管道（emit 把元素推给下游）。
//
// emit 返回 error 时它会停止：传入的 ctx 结束（下游提前收工/取消）或下游出错时，
// emit 返回 ctx.Err()，此时**必须**直接返回该错误（否则 goroutine 不会退出）。
//
// ⚠️ 本函数会起一个 goroutine；它由终结操作统一取消，泄漏测试覆盖。
func FromFunc[T any](fn func(ctx context.Context, emit func(T) error) error, opts ...Option) *Pipe[T] {
	cfg := applyOpts(opts)
	if cfg.buffer <= 0 {
		// 无缓冲时 emit 完全同步（生产者-消费者严格交替）
		cfg.buffer = 0
	}
	p := newSource[T](context.Background(), cfg, "FromFunc", nil)

	type item struct {
		v   T
		err error
	}
	ch := make(chan item, cfg.buffer)
	sent := make(chan error, 1) // 生产者结束信号

	go func() {
		// 必须用 guardErr 而不是 guard：guard 只带出 panic，会把 fn 正常返回的错误丢掉
		pctx := p.ctx()
		err := guardErr(func() error {
			return fn(pctx, func(v T) error {
				select {
				case ch <- item{v: v}:
					return nil
				case <-pctx.Done():
					return pctx.Err()
				}
			})
		})
		sent <- err
		close(ch)
	}()

	p.pull = func() (T, bool, error) {
		var zero T
		select {
		case it, ok := <-ch:
			if !ok {
				// 上游结束，取它的最终错误
				select {
				case err := <-sent:
					return zero, false, err
				default:
					return zero, false, nil
				}
			}
			return it.v, true, it.err
		case <-p.ctx().Done():
			return zero, false, p.ctx().Err()
		}
	}
	return p
}

// FromReader 分块读取 io.Reader（每块 bufSize 字节，默认 32KiB）。
//
// 与 io.Reader 的语义一致：返回的最后一块可能不足 bufSize；读到 EOF 结束。
func FromReader(r io.Reader, bufSize ...int) *Pipe[[]byte] {
	size := 32 << 10
	if len(bufSize) > 0 && bufSize[0] > 0 {
		size = bufSize[0]
	}
	return newSource(context.Background(), defaultConfig(), "FromReader", func() ([]byte, bool, error) {
		buf := make([]byte, size)
		n, err := r.Read(buf)
		if n > 0 {
			return buf[:n], true, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, xerr.Wrapf(err, "xpipe: 读取失败")
		}
		return nil, false, nil
	})
}

// FromLines 逐行读取 io.Reader（**不含换行符**）。
//
// 实现要点：用 bufio.Scanner 但把缓冲区上限调大（默认 64KiB 行），
// 且扫描错误（如超长行）会被上报而不是静默截断。
func FromLines(r io.Reader) *Pipe[string] {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20) // 单行上限 4MiB
	return newSource(context.Background(), defaultConfig(), "FromLines", func() (string, bool, error) {
		if sc.Scan() {
			return sc.Text(), true, nil
		}
		if err := sc.Err(); err != nil {
			return "", false, xerr.Wrapf(err, "xpipe: 按行读取失败")
		}
		return "", false, nil
	})
}

// Strings 用若干字符串创建管道（io.Strings 的管道版，便于测试与示例）。
func Strings(vs ...string) *Pipe[string] { return FromSlice(vs) }

// applyOpts 构造配置。
func applyOpts(opts []Option) *config {
	cfg := defaultConfig()
	for _, o := range opts {
		if o != nil {
			o(cfg)
		}
	}
	return cfg
}
