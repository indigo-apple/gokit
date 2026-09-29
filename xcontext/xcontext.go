// Package xcontext 提供类型安全的 context 值传递、合并与链路信息提取。
//
// 设计要点：
//   - 使用带类型的键（NewKey[V]），键本身携带值类型，
//     因此 With/Get 之间不存在「存 string 取 int」的隐式错误；
//   - 键应作为包级变量创建一次（如 KeyUserID），
//     避免在热路径上动态创建（会让 Values 的枚举注册表增长）；
//   - 内置 trace / span / user / tenant / request id 等常用键与便捷函数，
//     与 xlog、xnet 的链路透传约定一致；
//   - Merge 支持「长生命周期 ctx + 请求 ctx」的组合，取消信号与截止时间取两者最早。
//
// 用法：
//
//	var KeyUserID = xcontext.NewKey[int64]("user_id")
//
//	ctx = xcontext.With(ctx, KeyUserID, 42)
//	id, ok := xcontext.Get(ctx, KeyUserID)   // id 是 int64，不需要类型断言
//
// 并发安全：所有导出函数均可并发使用。
package xcontext

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 类型安全的键
// ---------------------------------------------------------------------------

// ctxKey 是类型安全的 context 键。
//
// 未导出，外部只能通过 NewKey 构造，杜绝不同包之间用同名字符串键互相覆盖。
type ctxKey[V any] struct {
	name string
}

// Name 返回键名。
func (k *ctxKey[V]) Name() string {
	if k == nil {
		return ""
	}
	return k.name
}

// String 实现 fmt.Stringer，便于日志输出。
func (k *ctxKey[V]) String() string { return k.Name() }

type registryEntry struct {
	name string
	get  func(context.Context) (any, bool)
}

var (
	registryMu sync.RWMutex
	registry   = make([]registryEntry, 0, 32)
)

// NewKey 创建一个带类型的 context 键。
//
// 用法（务必作为包级变量声明一次）：
//
//	var KeyOrderID = xcontext.NewKey[string]("order_id")
func NewKey[V any](name string) *ctxKey[V] {
	k := &ctxKey[V]{name: name}
	registryMu.Lock()
	registry = append(registry, registryEntry{
		name: name,
		get: func(ctx context.Context) (any, bool) {
			v, ok := ctx.Value(k).(V)
			return v, ok
		},
	})
	registryMu.Unlock()
	return k
}

// Keys 返回当前进程中已注册的全部键名（按注册顺序）。
//
// 主要用于调试、日志字段枚举与测试。
func Keys() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for _, e := range registry {
		out = append(out, e.name)
	}
	return out
}

// Registered 判断某键名是否已注册。
func Registered(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for _, e := range registry {
		if e.name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 存取
// ---------------------------------------------------------------------------

// With 把值写入 context。
//
// ctx 为 nil 时视为 context.Background()；k 为 nil 时原样返回 ctx。
func With[V any](ctx context.Context, k *ctxKey[V], v V) context.Context {
	if k == nil {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, k, v)
}

// Get 读取键值。ctx 或 k 为 nil 时返回零值与 false。
//
// 注意：显式存入零值（如空字符串）时 ok 仍为 true，可用于区分「未设置」与「设置为空」。
func Get[V any](ctx context.Context, k *ctxKey[V]) (V, bool) {
	var zero V
	if ctx == nil || k == nil {
		return zero, false
	}
	v, ok := ctx.Value(k).(V)
	return v, ok
}

// Or 读取键值，不存在时返回默认值。
func Or[V any](ctx context.Context, k *ctxKey[V], def V) V {
	if v, ok := Get(ctx, k); ok {
		return v
	}
	return def
}

// Must 读取键值，不存在时 panic。仅用于「键一定存在」的内部关键路径。
//
// 对外部输入驱动的场景请使用 Get/Or，避免 panic。
func Must[V any](ctx context.Context, k *ctxKey[V]) V {
	v, ok := Get(ctx, k)
	if !ok {
		panic("xcontext: key not found: " + k.Name())
	}
	return v
}

// Values 返回 context 上所有已注册键的取值（键名 → 值）。
//
// 典型用途是给日志注入链路字段：
//
//	xlog.InfoCtx(ctx, "handled", "fields", xcontext.Values(ctx))
func Values(ctx context.Context) map[string]any {
	if ctx == nil {
		return nil
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out map[string]any
	for _, e := range registry {
		if v, ok := e.get(ctx); ok {
			if out == nil {
				out = make(map[string]any, len(registry))
			}
			out[e.name] = v
		}
	}
	return out
}

// Len 返回 context 上命中的已注册键数量（不构造 map，适合热路径判断）。
func Len(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	n := 0
	for _, e := range registry {
		if _, ok := e.get(ctx); ok {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 内置键与便捷函数
// ---------------------------------------------------------------------------

// 内置键。跨服务透传时建议统一使用这些键名。
var (
	KeyTraceID   = NewKey[string]("trace_id")
	KeySpanID    = NewKey[string]("span_id")
	KeyUserID    = NewKey[string]("user_id")
	KeyTenantID  = NewKey[string]("tenant_id")
	KeyRequestID = NewKey[string]("request_id")
	KeyLocale    = NewKey[string]("locale")
)

// WithTraceID 写入链路追踪 ID。
func WithTraceID(ctx context.Context, id string) context.Context { return With(ctx, KeyTraceID, id) }

// TraceID 读取链路追踪 ID，不存在时返回空串。
func TraceID(ctx context.Context) string { return Or(ctx, KeyTraceID, "") }

// WithSpanID 写入当前 span ID。
func WithSpanID(ctx context.Context, id string) context.Context { return With(ctx, KeySpanID, id) }

// SpanID 读取 span ID。
func SpanID(ctx context.Context) string { return Or(ctx, KeySpanID, "") }

// WithUserID 写入用户 ID。
func WithUserID(ctx context.Context, id string) context.Context { return With(ctx, KeyUserID, id) }

// UserID 读取用户 ID。
func UserID(ctx context.Context) string { return Or(ctx, KeyUserID, "") }

// WithTenantID 写入租户 ID。
func WithTenantID(ctx context.Context, id string) context.Context {
	return With(ctx, KeyTenantID, id)
}

// TenantID 读取租户 ID。
func TenantID(ctx context.Context) string { return Or(ctx, KeyTenantID, "") }

// WithRequestID 写入请求 ID。
func WithRequestID(ctx context.Context, id string) context.Context {
	return With(ctx, KeyRequestID, id)
}

// RequestID 读取请求 ID。
func RequestID(ctx context.Context) string { return Or(ctx, KeyRequestID, "") }

// WithLocale 写入语言/地区标识。
func WithLocale(ctx context.Context, locale string) context.Context {
	return With(ctx, KeyLocale, locale)
}

// Locale 读取语言/地区标识。
func Locale(ctx context.Context) string { return Or(ctx, KeyLocale, "") }

// ---------------------------------------------------------------------------
// 派生的 context
// ---------------------------------------------------------------------------

// Detach 返回一个只保留值、不含取消信号与截止时间的 context。
//
// 用于「请求结束后仍需继续执行」的后台任务：
// 避免请求 ctx 被取消时把后台任务一起取消。
//
// 等价于 Go 1.21 的 context.WithoutCancel。
func Detach(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

// Copy 返回 context 的浅拷贝（保留值，去掉取消信号）。
//
// 与 Detach 行为一致，命名上更贴近「复制一份」的语义。
func Copy(ctx context.Context) context.Context { return Detach(ctx) }

// ---------------------------------------------------------------------------
// 合并
// ---------------------------------------------------------------------------

// mergedContext 是 Merge 返回的 context：
// 取值为 override 优先、base 兜底；取消信号与截止时间为两者最早。
type mergedContext struct {
	base     context.Context
	override context.Context
	done     chan struct{}

	mu       sync.Mutex
	err      error
	finished bool
	stopBase func() bool
	stopOver func() bool
}

// Merge 合并两个 context。
//
// 语义：
//   - 值：先查 override，未命中再查 base（override 优先）；
//   - 取消：base 或 override 任一被取消，结果 context 即被取消，
//     且 Err() 返回先触发者的错误；
//   - 截止时间：取两者中**更早**的那个。
//
// 典型场景：以服务级的 base ctx 承载全局配置/租户信息，
// 以请求 ctx 承载请求级取消与超时：
//
//	ctx := xcontext.Merge(svcCtx, reqCtx)
//
// 若长期持有结果 context 且 base 生命周期很长，建议改用 MergeCancel
// 以便主动释放内部注册。
func Merge(base, override context.Context) context.Context {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	m := &mergedContext{
		base:     base,
		override: override,
		done:     make(chan struct{}),
	}

	// 先注册，再同步补检，避免「注册前已取消」导致的可见性延迟
	sb := context.AfterFunc(base, func() { m.finish(base.Err()) })
	so := context.AfterFunc(override, func() { m.finish(override.Err()) })
	if !m.setStops(sb, so) {
		// 已在 finish 中被清理，立刻回滚注册，避免泄漏
		sb()
		so()
	}
	if err := base.Err(); err != nil {
		m.finish(err)
	} else if err := override.Err(); err != nil {
		m.finish(err)
	}
	return m
}

// MergeCancel 与 Merge 相同，但额外返回取消函数，用于主动释放内部资源。
//
// 调用方在使用完结果 context 后应调用 cancel()，避免长期持有造成的注册残留。
func MergeCancel(base, override context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(Merge(base, override))
}

func (m *mergedContext) setStops(a, b func() bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return false
	}
	m.stopBase, m.stopOver = a, b
	return true
}

func (m *mergedContext) finish(err error) {
	m.mu.Lock()
	if m.finished {
		m.mu.Unlock()
		return
	}
	m.finished = true
	m.err = err
	sb, so := m.stopBase, m.stopOver
	m.stopBase, m.stopOver = nil, nil
	m.mu.Unlock()

	if sb != nil {
		sb()
	}
	if so != nil {
		so()
	}
	close(m.done)
}

// Deadline 返回两者中更早的截止时间。
func (m *mergedContext) Deadline() (time.Time, bool) {
	d1, ok1 := m.base.Deadline()
	d2, ok2 := m.override.Deadline()
	switch {
	case ok1 && ok2:
		if d2.Before(d1) {
			return d2, true
		}
		return d1, true
	case ok1:
		return d1, true
	case ok2:
		return d2, true
	default:
		return time.Time{}, false
	}
}

// Done 返回在任一来源结束时关闭的 channel。
func (m *mergedContext) Done() <-chan struct{} { return m.done }

// Err 返回结束原因。
func (m *mergedContext) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Value 先查 override，未命中再查 base。
//
// 注意：若 override 中显式存入了 nil（如 (*T)(nil)），本次查询会继续落到 base。
func (m *mergedContext) Value(key any) any {
	if v := m.override.Value(key); v != nil {
		return v
	}
	return m.base.Value(key)
}

// ---------------------------------------------------------------------------
// HTTP 集成
// ---------------------------------------------------------------------------

// 常用 HTTP 头。
const (
	HeaderRequestID = "X-Request-Id"
	HeaderTraceID   = "X-Trace-Id"
	HeaderTenantID  = "X-Tenant-Id"
	HeaderUserID    = "X-User-Id"
	HeaderLocale    = "X-Locale"
)

// FromRequest 从 HTTP 请求中提取链路信息，返回携带 trace / request / tenant / user / locale 的 context。
//
// 返回值以 r.Context() 为基础（因此继承请求的取消信号）；
// r 为 nil 时返回 context.Background()。
func FromRequest(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	ctx := r.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if v := firstHeader(r, HeaderRequestID); v != "" {
		ctx = WithRequestID(ctx, v)
	}
	if v := firstHeader(r, HeaderTraceID); v != "" {
		ctx = WithTraceID(ctx, v)
	} else if v := traceIDFromTraceparent(r.Header.Get("Traceparent")); v != "" {
		ctx = WithTraceID(ctx, v)
	}
	if v := firstHeader(r, HeaderTenantID); v != "" {
		ctx = WithTenantID(ctx, v)
	}
	if v := firstHeader(r, HeaderUserID); v != "" {
		ctx = WithUserID(ctx, v)
	}
	if v := firstHeader(r, HeaderLocale); v != "" {
		ctx = WithLocale(ctx, v)
	} else if v := r.Header.Get("Accept-Language"); v != "" {
		ctx = WithLocale(ctx, v)
	}
	return ctx
}

// firstHeader 返回给定头的第一个非空值（大小写不敏感）。
func firstHeader(r *http.Request, name string) string {
	if r == nil {
		return ""
	}
	if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
		return v
	}
	// 兼容全小写形式（部分网关会改写大小写）
	if v := strings.TrimSpace(r.Header.Get(strings.ToLower(name))); v != "" {
		return v
	}
	return ""
}

// traceIDFromTraceparent 从 W3C traceparent 头中提取 trace-id。
//
// 格式：00-<32位hex trace-id>-<16位hex span-id>-<2位hex flags>
func traceIDFromTraceparent(tp string) string {
	tp = strings.TrimSpace(tp)
	if tp == "" {
		return ""
	}
	parts := strings.Split(tp, "-")
	if len(parts) < 3 {
		return ""
	}
	id := strings.TrimSpace(parts[1])
	if len(id) != 32 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return ""
		}
	}
	return id
}
