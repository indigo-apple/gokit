// Package xevent 提供**进程内**消息订阅（事件总线）。
//
// 对应需求「消息订阅」。定位与边界：
//
//	xevent —— 进程内总线：模块解耦、领域事件、异步任务编排。零第三方依赖、无线程外开销
//	xmq    —— 跨进程消息中间件（Kafka/NATS/Pulsar…）：规划中，共享同一套 Handler/Middleware 抽象
//
// 因此本地开发用 xevent、上线切 xmq 时，业务 handler 与中间件可以原地复用。
//
// # 有序性（本包最需要想清楚的地方）
//
// 顺序与吞吐不可兼得，所以本包**不提供隐式行为**，必须显式选择：
//
//	默认（异步）          入队后立即返回；单订阅默认 1 个消费者 → 队列序（FIFO）
//	WithKey(k)            同 key 严格串行（内部走 xkeyed 分片管道），不同 key 并行
//	WithOrdered(true)     全局严格串行（代价：吞吐低）
//	WithConcurrency(n>1)  同一订阅 n 个消费者 → **不保证顺序**（换吞吐）
//	WithGroup(g)          同组只投一份（组内负载均衡），组间广播
//
// # 投递语义（必须知道的两个事实）
//
//  1. 默认是 **at-most-once**：进程崩溃会丢内存里的队列。需要不丢请用 xmq 或自己持久化。
//  2. handler 返回 error → 按 WithRetry 重试 → 超限进 WithDeadLetter 死信 → 都没有则记 Error。
//
// 并发安全：本包所有类型都可并发使用。
package xevent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/indigo-apple/gokit/xcontainer"
	"github.com/indigo-apple/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 事件模型
// ---------------------------------------------------------------------------

// Event 是一条事件。
//
// 设计要点：
//   - `Key` 是**顺序路由键**（不是主键）：带 Key 的事件在同 key 上串行投递；
//   - `Payload` 用于进程内传递任意对象；`Data` 用于跨进程场景的原始字节；
//   - `Headers` 是元数据（如 trace 信息、schema 版本）。**不要直接改它** ——
//     同一个事件会投给多个订阅，直接写会造成数据竞争；要加字段请用 WithHeader。
type Event struct {
	ID        string            // 事件唯一 ID（发布时自动生成）
	Topic     string            // 主题
	Key       string            // 顺序路由键（空 = 不参与 key 串行）
	Payload   any               // 载荷（进程内）
	Data      []byte            // 原始字节（跨进程场景）
	Headers   map[string]string // 元数据
	Timestamp time.Time         // 发生时间
	Attempt   int               // 第几次投递（0 = 首次，重试时递增）
	TraceID   string            // 链路 ID
	Source    string            // 发布者标识

	// done 非 nil 表示这是一次「同步发布」：投递完成后必须回传结果。
	// 用未导出字段承载，避免污染公开 API。
	done chan error
}

// WithHeader 返回带新增（或覆盖）头的事件副本。
//
// 刻意**复制** Headers 而不是原地写：同一事件可能同时投给多个订阅，
// 原地写会造成并发读写 map 的 panic。代价是每次调用多一次 map 拷贝。
func (e Event) WithHeader(k, v string) Event {
	out := e
	out.Headers = make(map[string]string, len(e.Headers)+1)
	for kk, vv := range e.Headers {
		out.Headers[kk] = vv
	}
	out.Headers[k] = v
	return out
}

// Header 读取头字段（不存在返回空串）。
func (e Event) Header(k string) string {
	if e.Headers == nil {
		return ""
	}
	return e.Headers[k]
}

// Clone 返回事件副本（Headers 深拷贝），用于「要在 handler 里改事件」的场景。
func (e Event) Clone() Event {
	return e.WithHeader("", "") // WithHeader 已做浅拷贝 + 一份 map 深拷贝
}

// String 实现 fmt.Stringer（调试用，不打印 Payload 以免刷屏）。
func (e Event) String() string {
	return fmt.Sprintf("Event{topic=%s id=%s key=%q attempt=%d headers=%d}",
		e.Topic, e.ID, e.Key, e.Attempt, len(e.Headers))
}

// Handler 处理一条事件。返回 error 表示处理失败（触发重试 / 死信）。
type Handler func(ctx context.Context, ev Event) error

// Middleware 是 handler 中间件：`Middleware(next) Handler`。
//
// 多个中间件按**注册顺序**包裹：第一个注册的在最外层（最先执行、最后返回）。
type Middleware func(next Handler) Handler

// ---------------------------------------------------------------------------
// 主题匹配
// ---------------------------------------------------------------------------
//
// 支持四种模式：
//
//	精确       "order.created"
//	单级通配   "order.*"        → order.created、order.paid（**不匹配** order.item.added）
//	多级通配   "order.**"       → order.created、order.item.added（含自身）
//	正则       "re:^order\..+$" → 正则匹配（前缀 re: 之后的全部内容）
//	全匹配     "*" 或 "**"      → 所有主题
//
// 匹配是**大小写敏感**的（主题名应当视为标识符，不做大小写折叠）。

// matchAll 表示「匹配所有主题」的通配模式。
const (
	patternAll      = "*"
	patternAllDeep  = "**"
	regexpPrefix    = "re:"
	sep             = "."
	orderedKey      = "\x00ordered" // WithOrdered(true) 使用的内部顺序键（不可与业务 key 冲突）
	maxPatternCache = 256           // 正则编译缓存上限（超过则不缓存，避免恶意模式耗尽内存）
)

// 哨兵错误。
var (
	// ErrClosed 总线已关闭。
	ErrClosed = xerr.NewC(xerr.CodeStateInvalid, "xevent: bus is closed")

	// ErrNilBus 传入了 nil 总线。
	ErrNilBus = xerr.NewC(xerr.CodeMissingParam, "xevent: bus 为 nil")

	// ErrNilHandler 未提供 handler（且未启用拉模式）。
	ErrNilHandler = xerr.NewC(xerr.CodeMissingParam, "xevent: handler 为 nil（拉模式请用 WithPull）")

	// ErrPullWithHandler 同时使用了拉模式与 handler，语义不明确。
	ErrPullWithHandler = xerr.NewC(xerr.CodeInvalidParam, "xevent: WithPull 与 handler 不能同时使用")

	// ErrEmptyTopic 主题为空。
	ErrEmptyTopic = xerr.NewC(xerr.CodeMissingParam, "xevent: topic 不能为空")

	// ErrNoSubscription 传入的订阅不属于本总线或已取消。
	ErrNoSubscription = xerr.NewC(xerr.CodeNotFound, "xevent: subscription not found")

	// ErrBadPattern 主题模式非法（如正则编译失败）。
	ErrBadPattern = xerr.NewC(xerr.CodeInvalidFormat, "xevent: 非法的主题模式")
)

// regexpCache 缓存编译好的正则（主题模式通常只有少量几个，命中率极高）。
var regexpCache = struct {
	mu sync.Mutex
	m  map[string]*regexp.Regexp
}{m: make(map[string]*regexp.Regexp)}

// compileRegexp 编译并缓存正则模式。
func compileRegexp(expr string) (*regexp.Regexp, error) {
	regexpCache.mu.Lock()
	if re, ok := regexpCache.m[expr]; ok {
		regexpCache.mu.Unlock()
		return re, nil
	}
	regexpCache.mu.Unlock()

	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, xerr.WrapCf(err, xerr.CodeInvalidFormat, "xevent: 正则模式 %q 编译失败", expr)
	}
	regexpCache.mu.Lock()
	if len(regexpCache.m) < maxPatternCache {
		regexpCache.m[expr] = re
	}
	regexpCache.mu.Unlock()
	return re, nil
}

// Match 判断 topic 是否匹配 pattern。
//
// 支持：精确、`*`（单级通配 / 全匹配）、`**`（多级通配 / 全匹配）、`re:` 前缀正则。
func Match(pattern, topic string) bool {
	switch pattern {
	case patternAll, patternAllDeep:
		return true
	}
	if strings.HasPrefix(pattern, regexpPrefix) {
		re, err := compileRegexp(strings.TrimPrefix(pattern, regexpPrefix))
		if err != nil {
			return false // 非法正则视为不匹配（而不是 panic）
		}
		return re.MatchString(topic)
	}
	if !strings.Contains(pattern, "*") {
		return pattern == topic
	}

	// 通配匹配：把 `**` 视为「剩余全部」，`*` 视为「单个层级」
	return wildcardMatch(pattern, topic)
}

// wildcardMatch 逐段匹配（`*` 只吃一段，`**` 吃剩余全部）。
//
// 参数使用 `**` 作为「任意剩余片段」的标记，等价于通配符里的 **。
func wildcardMatch(pattern, topic string) bool {
	pSegs := strings.Split(pattern, sep)
	tSegs := strings.Split(topic, sep)

	// `**` 必须是最后一段时才表示「剩余全部」（与常见实现一致，便于推理）
	for i, p := range pSegs {
		if p == patternAllDeep {
			if i == len(pSegs)-1 {
				return len(tSegs) >= i
			}
			return false // 中间出现 `**` 视为不匹配，避免歧义
		}
		if i >= len(tSegs) {
			return false
		}
		if p != patternAll && p != tSegs[i] {
			return false
		}
	}
	return len(pSegs) == len(tSegs)
}

// ValidatePattern 预校验主题模式（订阅时调用，快速失败）。
func ValidatePattern(pattern string) error {
	if pattern == "" {
		return ErrEmptyTopic
	}
	if strings.HasPrefix(pattern, regexpPrefix) {
		_, err := compileRegexp(strings.TrimPrefix(pattern, regexpPrefix))
		return err
	}
	if strings.Contains(pattern, patternAllDeep) {
		segs := strings.Split(pattern, sep)
		for i, s := range segs {
			if s == patternAllDeep && i != len(segs)-1 {
				return xerr.NewfC(xerr.CodeInvalidFormat,
					"xevent: 模式 %q 非法：** 只能出现在末尾（多级通配只能作为后缀）", pattern)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ID 生成
// ---------------------------------------------------------------------------

var eventSeq atomic.Uint64

// newEventID 生成事件 ID：`<时间戳36进制>-<进程内序号>-<随机4字节>`。
//
// 三重保证：时间戳保证大致有序、序号保证同进程内绝不重复、随机段保证跨进程/重启后不冲突。
func newEventID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		b = [4]byte{0, 0, 0, 0}
	}
	return fmt.Sprintf("%s-%d-%s",
		strings.ToLower(strconvFormatInt(time.Now().UnixMilli(), 36)),
		eventSeq.Add(1),
		hex.EncodeToString(b[:]))
}

// strconvFormatInt 是 strconv.FormatInt 的极简替代（避免为一个调用引入 strconv）。
func strconvFormatInt(v int64, base int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [64]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%int64(base)]
		v /= int64(base)
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// 选项
// ---------------------------------------------------------------------------

// config 是总线配置。
type config struct {
	workers      int // 订阅默认消费者数
	buffer       int // 订阅默认缓冲
	globalBuffer int // 拉模式通道缓冲
	overflow     xcontainer.OverflowPolicy
	errorPolicy  ErrorPolicy
	replayBuffer int
	onError      func(topic string, ev Event, err error)
	keyedShards  int
}

func defaultConfig() config {
	return config{
		workers:      1,
		buffer:       1024,
		globalBuffer: 64,
		overflow:     xcontainer.Block,
		errorPolicy:  ErrorCollect,
		keyedShards:  8,
	}
}

// Option 是总线级选项。
type Option func(*config)

// WithWorkers 设置订阅的**默认**消费者数（默认 1）。
//
// ⚠️ 大于 1 会打破「单订阅 FIFO」的默认保证；需要顺序时请用 WithKey/WithOrdered，
// 或在订阅上显式 WithConcurrency(1)。
func WithWorkers(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.workers = n
		}
	}
}

// WithGlobalBuffer 设置订阅默认缓冲长度（默认 1024）。
func WithGlobalBuffer(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.buffer = n
		}
	}
}

// WithDefaultOverflow 设置**订阅默认**的溢出策略（默认 Block：阻塞发布形成背压）。
//
//	Block      —— 发布方阻塞（推荐：不会静默丢事件）
//	DropOldest —— 丢最旧的（适合「只关心最新状态」的刷新事件）
//	DropNewest —— 丢新的并计入 Dropped
//	Fail       —— 返回 ErrQueueFull
//
// 命名说明：Go 不支持重载，而「总线默认值」与「单订阅覆盖」两个 `WithOverflow`
// 语义不同（前者是 Option，后者是 SubscribeOption），因此总线级沿用
// WithDefaultXxx 命名（与 xdistlock 的 WithDefaultTTL / WithDefaultRetry 一致），
// 单订阅级保留设计文档中的 WithOverflow。
func WithDefaultOverflow(p xcontainer.OverflowPolicy) Option {
	return func(c *config) { c.overflow = p.Norm() }
}

// ErrorPolicy 决定同步发布时如何汇总多个订阅的错误。
type ErrorPolicy int

const (
	// ErrorFailFast 遇到第一个错误立即返回（放弃后续订阅）。
	ErrorFailFast ErrorPolicy = iota
	// ErrorCollect 投递所有订阅，聚合全部错误（默认）。
	ErrorCollect
)

// WithErrorPolicy 设置错误汇总策略。
func WithErrorPolicy(p ErrorPolicy) Option {
	return func(c *config) { c.errorPolicy = p }
}

// WithReplayBuffer 保留最近 n 条事件用于 Replay（默认 0 = 不保留）。
//
// ⚠️ 这是**内存**保留，且只保留最近 n 条；不要把它当持久化用。
func WithReplayBuffer(n int) Option {
	return func(c *config) {
		if n >= 0 {
			c.replayBuffer = n
		}
	}
}

// WithOnError 注册「投递最终失败」的回调（重试耗尽且无死信处理器时触发）。
//
// 生产环境强烈建议设置：否则事件被丢弃时调用方一无所知。
func WithOnError(fn func(topic string, ev Event, err error)) Option {
	return func(c *config) { c.onError = fn }
}

// WithKeyedShards 设置按 key 保序管道的分片数（默认 8）。
//
// 与 xkeyed 一致：分片越少假冲突越多、分片越多调度开销越大。
func WithKeyedShards(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.keyedShards = n
		}
	}
}

// ---------------------------------------------------------------------------
// 发布选项
// ---------------------------------------------------------------------------

// publishConfig 是单次发布的配置。
type publishConfig struct {
	key       string
	ordered   bool
	delay     time.Duration
	headers   map[string]string
	sync      bool
	traceID   string
	source    string
	eventID   string
	timestamp time.Time
}

// PublishOption 是发布选项。
type PublishOption func(*publishConfig)

// WithKey 设置顺序路由键：**同 key 的事件严格按发布顺序串行投递**，不同 key 并行。
//
// 这是最常用的有序手段（如订单号、用户 ID）。（内部走 xkeyed 分片管道。）
func WithKey(key string) PublishOption {
	return func(c *publishConfig) { c.key = key }
}

// WithOrdered 开启全局严格顺序（所有事件单队列串行）。
//
// 代价明确：吞吐降为单消费者水平。只在「顺序比吞吐重要」的主题上使用。
func WithOrdered(ordered bool) PublishOption {
	return func(c *publishConfig) { c.ordered = ordered }
}

// WithDelay 延迟投递（延迟期间留在内存中）。
//
// ⚠️ 进程重启会丢失延迟中的事件；需要可靠性请用带持久化的 MQ。
func WithDelay(d time.Duration) PublishOption {
	return func(c *publishConfig) {
		if d > 0 {
			c.delay = d
		}
	}
}

// WithHeaders 附加头字段（会与事件已有头合并）。
func WithHeaders(kv map[string]string) PublishOption {
	return func(c *publishConfig) { c.headers = kv }
}

// WithSync 同步发布：**等待所有匹配订阅的 handler 返回**再返回，并聚合错误。
//
// 语义细节（重要）：
//   - 事件仍然走订阅队列，因此顺序保证不变；
//   - 调用方 ctx 结束即不再等待（事件可能仍在后台投递完）。
func WithSync(sync bool) PublishOption {
	return func(c *publishConfig) { c.sync = sync }
}

// WithTraceID 设置链路 ID（会写入 Headers 与字段）。
func WithTraceID(id string) PublishOption {
	return func(c *publishConfig) { c.traceID = id }
}

// WithSource 设置发布者标识（如服务名）。
func WithSource(src string) PublishOption {
	return func(c *publishConfig) { c.source = src }
}

// WithEventID 指定事件 ID（默认自动生成）。用于幂等重放场景。
func WithEventID(id string) PublishOption {
	return func(c *publishConfig) { c.eventID = id }
}

// WithTimestamp 指定事件时间（默认取当前时间）。
func WithTimestamp(ts time.Time) PublishOption {
	return func(c *publishConfig) { c.timestamp = ts }
}
