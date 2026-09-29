package xevent

import (
	"context"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 中间件
// ---------------------------------------------------------------------------
//
// 约定：`Middleware(next) Handler`。多个中间件按**注册顺序**包裹，
// 第一个注册的在最外层（最先执行、最后返回）：
//
//	Chain(A, B)(h) == A(B(h))
//
// 中间件运行在**投递 goroutine** 上，因此：
//   - 必须遵守 ctx（否则订阅取消时无法及时退出）；
//   - 阻塞它会直接拖慢该订阅（这正是背压的传导方式）；
//   - panic 会被总线的兜底 recover 捕获（但会丢失 key 上下文，建议显式用 Recover()）。

// Chain 把多个中间件合并成一个（按顺序包裹）。
//
// 空参数时返回恒等中间件（直接返回 next）。
func Chain(mw ...Middleware) Middleware {
	return func(next Handler) Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			if mw[i] != nil {
				next = mw[i](next)
			}
		}
		return next
	}
}

// Recover 捕获 handler panic 并转成 error（随后按重试/死信规则处理）。
//
// 为什么要显式加它：总线内部已有一层兜底 recover（保证 worker 不死），
// 但兜底层拿不到「业务语义上的错误类型」，交给你自己的 Recover
// 可以在转 error 时带上业务上下文（订单号、事件 ID 等）。
func Recover() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, ev Event) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = xerr.NewfC(xerr.CodeInternal, "xevent: handler panic: %v (event=%s)", r, ev.ID)
				}
			}()
			return next(ctx, ev)
		}
	}
}

// Timeout 给 handler 加上超时：超时后 ctx 结束（handler 应遵守 ctx 才能被真正打断）。
//
// ⚠️ Go 无法强制杀死 goroutine：若 handler 不理会 ctx，它仍会跑下去，
// 只是调用方（投递流程）会立刻拿到超时错误并按重试/死信处理。
func Timeout(d time.Duration) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, ev Event) error {
			if d <= 0 {
				return next(ctx, ev)
			}
			tctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next(tctx, ev)
		}
	}
}

// Validate 在 handler 之前做校验；校验失败直接返回错误（走重试/死信）。
//
// 适合「schema 版本 / 必填头字段 / 载荷类型」这类前置检查。
func Validate(fn func(Event) error) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, ev Event) error {
			if fn != nil {
				if err := fn(ev); err != nil {
					return err
				}
			}
			return next(ctx, ev)
		}
	}
}

// Skip 在 handler 之前短路：pred 返回 true 的事件直接视为处理成功（不调用 handler）。
//
// 与 WithFilter 的差别：WithFilter 在投递流程更外层（连重试都不进入，计入 Filtered），
// Skip 在 handler 链内部，适合「需要按中间件顺序决定是否处理」的场景。
func Skip(pred func(Event) bool) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, ev Event) error {
			if pred != nil && pred(ev) {
				return nil
			}
			return next(ctx, ev)
		}
	}
}

// Tap 是观测型中间件：记录每次 handler 调用的耗时与结果。
//
// before 可为 nil；after 会收到耗时（含错误）。回调运行在投递 goroutine 上，
// 必须快速返回（不要在其中做 IO）。
func Tap(before func(ev Event), after func(ev Event, err error, elapsed time.Duration)) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, ev Event) error {
			if before != nil {
				before(ev)
			}
			start := time.Now()
			err := next(ctx, ev)
			if after != nil {
				after(ev, err, time.Since(start))
			}
			return err
		}
	}
}
