// Package xsafe 提供 panic 安全能力：安全启动 goroutine、把 panic 转换为错误、调用栈辅助。
//
// 设计要点：
//   - 所有 `go` 语句都应使用 xsafe.Go 而非裸 `go func()`，
//     否则单个 panic 会直接终止整个进程；
//   - PanicToError 保留 panic 值的原始错误链（若 panic 值是 error），
//     因此 errors.Is / errors.As 依然可用；
//   - panic 处理回调可替换（SetPanicHandler），默认输出到 stderr；
//   - 本包不依赖本仓库其它包，可单独引入。
//
// 并发安全：所有导出函数均可并发使用。
package xsafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// PanicError
// ---------------------------------------------------------------------------

// PanicError 表示由 panic 转换而来的错误。
//
// 当 panic 值本身是 error 时，Unwrap 会返回它，因此：
//
//	defer func() { err = xsafe.PanicToError(recover()) }()
//
// 之后 errors.Is(err, 原始错误) 依然成立。
type PanicError struct {
	// Value 是 panic 的原始值。
	Value any
	// Stack 是 panic 发生时的调用栈（未开启捕获时为 nil）。
	Stack []byte
}

var (
	_ error = (*PanicError)(nil)
	// 编译期断言：支持 errors.As 提取被包裹的 error
	_ interface{ Unwrap() error } = (*PanicError)(nil)
)

// Error 实现 error 接口。
func (e *PanicError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if err, ok := e.Value.(error); ok {
		return "panic: " + err.Error()
	}
	return fmt.Sprintf("panic: %v", e.Value)
}

// Unwrap 在 panic 值是 error 时返回它，使 errors.Is / errors.As 可继续工作。
func (e *PanicError) Unwrap() error {
	if e == nil {
		return nil
	}
	if err, ok := e.Value.(error); ok {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// 全局配置
// ---------------------------------------------------------------------------

// captureStack 控制 panic 转换时是否记录调用栈（默认开启）。
var captureStack = func() *atomic.Bool {
	b := &atomic.Bool{}
	b.Store(true)
	return b
}()

// SetCaptureStack 控制 panic 转换时是否记录调用栈（默认开启）。
//
// 在高频且已确认无 panic 的场景可关闭以降低开销。
func SetCaptureStack(on bool) { captureStack.Store(on) }

// panicHandler 保存全局 panic 处理回调。
var panicHandler atomic.Pointer[func(error)]

// panicWriterMu 保护默认输出目标。
var (
	panicWriterMu sync.RWMutex
	panicWriter   io.Writer = os.Stderr
)

// SetPanicWriter 设置默认 panic 输出目标（默认 os.Stderr）。传 nil 恢复默认。
//
// 仅在未设置 panic 处理回调时生效；典型用途是把兜底输出接入日志文件。
func SetPanicWriter(w io.Writer) {
	panicWriterMu.Lock()
	defer panicWriterMu.Unlock()
	if w == nil {
		panicWriter = os.Stderr
		return
	}
	panicWriter = w
}

// PanicWriter 返回当前的默认输出目标。
func PanicWriter() io.Writer {
	panicWriterMu.RLock()
	defer panicWriterMu.RUnlock()
	return panicWriter
}

// SetPanicHandler 设置全局 panic 处理回调（传 nil 恢复默认：输出到 stderr）。
//
// 典型用法是接入日志组件：
//
//	xsafe.SetPanicHandler(func(err error) { xlog.Error("panic", "err", err) })
func SetPanicHandler(fn func(error)) {
	if fn == nil {
		panicHandler.Store(nil)
		return
	}
	panicHandler.Store(&fn)
}

// PanicHandler 返回当前的 panic 处理回调（可能为 nil 表示使用默认实现）。
func PanicHandler() func(error) {
	if p := panicHandler.Load(); p != nil {
		return *p
	}
	return nil
}

// report 把 panic 错误交给回调处理；没有回调时输出到 stderr。
func report(err error) {
	if err == nil {
		return
	}
	if fn := PanicHandler(); fn != nil {
		fn(err)
		return
	}
	var buf bytes.Buffer
	buf.WriteString("[xsafe] ")
	buf.WriteString(err.Error())
	buf.WriteByte('\n')
	var pe *PanicError
	if errors.As(err, &pe) && len(pe.Stack) > 0 {
		buf.Write(pe.Stack)
		if pe.Stack[len(pe.Stack)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	_, _ = PanicWriter().Write(buf.Bytes())
}

// ---------------------------------------------------------------------------
// panic -> error
// ---------------------------------------------------------------------------

// PanicToError 把 recover() 的返回值转换为错误；v 为 nil 时返回 nil。
func PanicToError(v any) error {
	if v == nil {
		return nil
	}
	if pe, ok := v.(*PanicError); ok {
		return pe
	}
	pe := &PanicError{Value: v}
	if captureStack.Load() {
		pe.Stack = debug.Stack()
	}
	return pe
}

// IsPanicError 判断错误链上是否存在由 panic 转换而来的错误。
func IsPanicError(err error) bool {
	if err == nil {
		return false
	}
	var pe *PanicError
	return errors.As(err, &pe)
}

// ---------------------------------------------------------------------------
// 安全执行
// ---------------------------------------------------------------------------

// Recover 执行 fn 并把 panic 转换为错误返回；fn 为 nil 时返回 nil。
//
// 这是「同步调用需要防 panic」时的标准写法。
func Recover(fn func()) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = PanicToError(r)
		}
	}()
	fn()
	return nil
}

// Call 是 Recover 的别名，语义更贴近「安全调用」。
func Call(fn func()) error { return Recover(fn) }

// RecoverErr 执行返回 error 的 fn，并把 panic 转换为错误返回。
//
// 若 fn 正常返回错误，则该错误原样返回；若发生 panic，错误链为
// PanicError（可通过 errors.Is 找到原始 error）。
func RecoverErr(fn func() error) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = PanicToError(r)
		}
	}()
	return fn()
}

// Try 执行 fn 并返回其结果；发生 panic 时返回零值与错误。
func Try[T any](fn func() T) (v T, err error) {
	if fn == nil {
		return v, nil
	}
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v = zero
			err = PanicToError(r)
		}
	}()
	return fn(), nil
}

// TryE 执行返回 (T, error) 的 fn；发生 panic 时返回零值与错误。
func TryE[T any](fn func() (T, error)) (v T, err error) {
	if fn == nil {
		return v, nil
	}
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v = zero
			err = PanicToError(r)
		}
	}()
	return fn()
}

// ---------------------------------------------------------------------------
// 安全启动 goroutine
// ---------------------------------------------------------------------------

// Go 启动一个 goroutine，其中的 panic 会被捕获并交给 panic 处理回调，
// 不会导致进程崩溃。fn 为 nil 时不做任何事。
//
// 生产代码中请使用本函数替代裸 `go func()`。
func Go(fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				report(PanicToError(r))
			}
		}()
		fn()
	}()
}

// GoNamed 与 Go 相同，但在 panic 错误上附加 goroutine 名称，便于排查。
func GoNamed(name string, fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("goroutine %q: %w", name, PanicToError(r))
				report(err)
			}
		}()
		fn()
	}()
}

// GoCtx 启动 goroutine，若 ctx 已结束则不启动。
//
// 注意：这里只做「启动前检查」，fn 运行期间 ctx 被取消不会中断 fn，
// 需要感知取消的 fn 应自行 select ctx.Done()。
func GoCtx(ctx context.Context, fn func()) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return
		}
	}
	Go(fn)
}

// GoErr 启动 goroutine，panic 会被转换为错误交给 onErr；onErr 为 nil 时走默认处理。
func GoErr(onErr func(error), fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := PanicToError(r)
				if onErr != nil {
					onErr(err)
					return
				}
				report(err)
			}
		}()
		fn()
	}()
}

// ---------------------------------------------------------------------------
// 调用栈辅助
// ---------------------------------------------------------------------------

// Stack 返回当前调用栈；skip 表示跳过上方多少层调用（best-effort）。
//
// 实现基于 runtime/debug.Stack，skip 的处理是「按行丢弃」的近似做法：
// 它不会保留 goroutine 头部信息之外的精确帧号，仅用于日志排查。
func Stack(skip int) []byte {
	raw := debug.Stack()
	if skip <= 0 {
		return raw
	}
	var head []byte
	rest := raw
	if i := bytes.IndexByte(raw, '\n'); i >= 0 {
		head = raw[:i+1]
		rest = raw[i+1:]
	}
	lines := bytes.Split(rest, []byte("\n"))
	if skip >= len(lines) {
		return raw
	}
	out := make([]byte, 0, len(head)+len(rest))
	out = append(out, head...)
	out = append(out, bytes.Join(lines[skip:], []byte("\n"))...)
	return out
}

// Caller 返回调用方信息；skip=0 表示 Caller 自身的调用者。
//
// 返回的 fn 为完整函数名，如 "github.com/indigo-apple/gokit/xsafe.Caller"。
// 无法获取时返回空值。
func Caller(skip int) (file string, line int, fn string) {
	pc, f, l, ok := runtime.Caller(skip + 1)
	if !ok {
		return "", 0, ""
	}
	if fnc := runtime.FuncForPC(pc); fnc != nil {
		fn = fnc.Name()
	}
	return f, l, fn
}

// FuncName 返回调用方函数名，如 "github.com/indigo-apple/gokit/xsafe_test.TestX"。
func FuncName() string {
	_, _, fn := Caller(1)
	return fn
}

// ShortFuncName 返回去掉包路径前缀的调用方函数名，如 "xsafe_test.TestX"。
func ShortFuncName() string {
	_, _, fn := Caller(1)
	for i := len(fn) - 1; i >= 0; i-- {
		if fn[i] == '/' {
			return fn[i+1:]
		}
	}
	return fn
}

// ---------------------------------------------------------------------------
// 错误判定
// ---------------------------------------------------------------------------

// IsTimeout 判断错误是否为超时（context 截止时间或 net.Error 超时）。
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
