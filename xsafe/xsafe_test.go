package xsafe_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xsafe"
)

func capturePanic(t *testing.T) <-chan error {
	t.Helper()
	ch := make(chan error, 64)
	xsafe.SetPanicHandler(func(err error) { ch <- err })
	t.Cleanup(func() { xsafe.SetPanicHandler(nil) })
	return ch
}

func recv(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("panic handler 未被调用")
		return nil
	}
}

// ---------------------------------------------------------------------------
// PanicError
// ---------------------------------------------------------------------------

func TestPanicErrorFormat(t *testing.T) {
	if got := xsafe.PanicToError("boom").Error(); got != "panic: boom" {
		t.Fatalf("got %q", got)
	}
	if got := xsafe.PanicToError(errors.New("boom")).Error(); got != "panic: boom" {
		t.Fatalf("got %q", got)
	}
	if got := xsafe.PanicToError(42).Error(); got != "panic: 42" {
		t.Fatalf("got %q", got)
	}
	var nilPE *xsafe.PanicError
	if got := nilPE.Error(); got != "<nil>" {
		t.Fatalf("nil PanicError.Error() = %q", got)
	}
	if nilPE.Unwrap() != nil {
		t.Fatal("nil PanicError.Unwrap should be nil")
	}
}

func TestPanicToErrorNil(t *testing.T) {
	if err := xsafe.PanicToError(nil); err != nil {
		t.Fatalf("PanicToError(nil) = %v, want nil", err)
	}
}

func TestPanicToErrorKeepsChain(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := xsafe.PanicToError(sentinel)
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is should find the panicked error")
	}

	wrapped := fmt.Errorf("ctx: %w", err)
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("errors.Is should traverse the wrapper")
	}

	var pe *xsafe.PanicError
	if !errors.As(err, &pe) || pe.Value != sentinel {
		t.Fatalf("errors.As failed: %#v", pe)
	}
}

func TestPanicToErrorIdempotent(t *testing.T) {
	once := xsafe.PanicToError("boom")
	twice := xsafe.PanicToError(once)
	if once != twice {
		t.Fatal("PanicToError should not double-wrap a PanicError")
	}
}

func TestIsPanicError(t *testing.T) {
	if xsafe.IsPanicError(nil) {
		t.Fatal("nil should be false")
	}
	if xsafe.IsPanicError(errors.New("plain")) {
		t.Fatal("plain error should be false")
	}
	if !xsafe.IsPanicError(xsafe.PanicToError("boom")) {
		t.Fatal("PanicError should be true")
	}
	if !xsafe.IsPanicError(fmt.Errorf("wrap: %w", xsafe.PanicToError("boom"))) {
		t.Fatal("wrapped PanicError should be true")
	}
}

func TestCaptureStackToggle(t *testing.T) {
	defer xsafe.SetCaptureStack(true)

	xsafe.SetCaptureStack(true)
	err := xsafe.PanicToError("boom")
	var pe *xsafe.PanicError
	if !errors.As(err, &pe) || len(pe.Stack) == 0 {
		t.Fatal("stack should be captured when enabled")
	}
	if !strings.Contains(string(pe.Stack), "goroutine") {
		t.Fatalf("stack looks wrong: %.60s", string(pe.Stack))
	}

	xsafe.SetCaptureStack(false)
	err = xsafe.PanicToError("boom")
	if !errors.As(err, &pe) || len(pe.Stack) != 0 {
		t.Fatal("stack should be empty when disabled")
	}
}

// ---------------------------------------------------------------------------
// Go / GoCtx / GoErr
// ---------------------------------------------------------------------------

func TestGoRecoversPanic(t *testing.T) {
	ch := capturePanic(t)
	xsafe.Go(func() { panic("boom") })

	err := recv(t, ch)
	if !xsafe.IsPanicError(err) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestGoNormalPath(t *testing.T) {
	ch := capturePanic(t)
	var done sync.WaitGroup
	done.Add(1)
	xsafe.Go(func() { done.Done() })
	done.Wait()

	select {
	case err := <-ch:
		t.Fatalf("handler should not be called, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGoNilFuncIsNoop(t *testing.T) {
	xsafe.Go(nil)
	xsafe.GoNamed("n", nil)
	xsafe.GoErr(func(error) {}, nil)
	xsafe.GoCtx(context.Background(), nil)
}

func TestGoNamedIncludesName(t *testing.T) {
	ch := capturePanic(t)
	xsafe.GoNamed("worker-7", func() { panic("boom") })

	err := recv(t, ch)
	if !strings.Contains(err.Error(), "worker-7") {
		t.Fatalf("got %v", err)
	}
	// 包裹后仍能识别出 panic 来源
	if !xsafe.IsPanicError(err) {
		t.Fatalf("should still be a PanicError chain: %v", err)
	}
}

func TestGoCtxSkipsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var ran atomic.Bool
	xsafe.GoCtx(ctx, func() { ran.Store(true) })
	time.Sleep(50 * time.Millisecond)
	if ran.Load() {
		t.Fatal("fn should not run on canceled context")
	}

	// nil context 视为有效，正常启动
	var done sync.WaitGroup
	done.Add(1)
	xsafe.GoCtx(nil, func() { done.Done() })
	done.Wait()
}

func TestGoErrDeliversToCallback(t *testing.T) {
	ch := make(chan error, 1)
	xsafe.GoErr(func(err error) { ch <- err }, func() { panic("boom") })

	select {
	case err := <-ch:
		if !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("onErr 未被调用")
	}
}

func TestGoErrNilCallbackFallsBack(t *testing.T) {
	ch := capturePanic(t)
	xsafe.GoErr(nil, func() { panic("boom") })
	if err := recv(t, ch); !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestGoConcurrencyNoRace(t *testing.T) {
	// 注意：必须用 GoErr 的回调作为完成信号。
	// 若在 fn 内 defer wg.Done()，wg.Wait() 会在 panic 被**上报之前**就返回，
	// 后续测试便会与仍在运行的 reporter 竞争全局状态（会被 -race 抓到）。
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		i := i // Go 1.21 循环变量共享，必须显式拷贝，否则 -race 报数据竞争
		wg.Add(1)
		if i%3 == 0 {
			xsafe.GoErr(func(error) { wg.Done() }, func() { panic("boom") })
		} else {
			xsafe.Go(func() { wg.Done() })
		}
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Recover / Try
// ---------------------------------------------------------------------------

func TestRecover(t *testing.T) {
	if err := xsafe.Recover(nil); err != nil {
		t.Fatalf("Recover(nil) = %v", err)
	}
	if err := xsafe.Recover(func() {}); err != nil {
		t.Fatalf("Recover(ok) = %v", err)
	}
	if err := xsafe.Call(func() {}); err != nil {
		t.Fatalf("Call(ok) = %v", err)
	}

	err := xsafe.Recover(func() { panic("boom") })
	if !xsafe.IsPanicError(err) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Recover(panic) = %v", err)
	}

	// panic 值是 error 时保持链
	sentinel := errors.New("sentinel")
	err = xsafe.Recover(func() { panic(sentinel) })
	if !errors.Is(err, sentinel) {
		t.Fatalf("should wrap sentinel: %v", err)
	}
}

func TestRecoverErr(t *testing.T) {
	if err := xsafe.RecoverErr(nil); err != nil {
		t.Fatalf("RecoverErr(nil) = %v", err)
	}
	want := errors.New("want")
	if got := xsafe.RecoverErr(func() error { return want }); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	if err := xsafe.RecoverErr(func() error { return nil }); err != nil {
		t.Fatalf("got %v", err)
	}
	if err := xsafe.RecoverErr(func() error { panic("boom") }); !xsafe.IsPanicError(err) {
		t.Fatalf("got %v", err)
	}
}

func TestTry(t *testing.T) {
	if v, err := xsafe.Try(func() int { return 7 }); v != 7 || err != nil {
		t.Fatalf("Try(ok) = %v,%v", v, err)
	}
	v, err := xsafe.Try(func() int { panic("boom") })
	if v != 0 || !xsafe.IsPanicError(err) {
		t.Fatalf("Try(panic) = %v,%v", v, err)
	}
	if v, err := xsafe.Try[int](nil); v != 0 || err != nil {
		t.Fatalf("Try(nil) = %v,%v", v, err)
	}
	if s, err := xsafe.Try(func() string { return "s" }); s != "s" || err != nil {
		t.Fatalf("Try(string) = %q,%v", s, err)
	}
}

func TestTryE(t *testing.T) {
	if v, err := xsafe.TryE(func() (string, error) { return "a", nil }); v != "a" || err != nil {
		t.Fatalf("TryE(ok) = %q,%v", v, err)
	}
	want := errors.New("want")
	if _, err := xsafe.TryE(func() (string, error) { return "", want }); err != want {
		t.Fatalf("got %v", err)
	}
	v, err := xsafe.TryE(func() (string, error) { panic("boom") })
	if v != "" || !xsafe.IsPanicError(err) {
		t.Fatalf("TryE(panic) = %q,%v", v, err)
	}
	if v, err := xsafe.TryE[string](nil); v != "" || err != nil {
		t.Fatalf("TryE(nil) = %q,%v", v, err)
	}
}

// ---------------------------------------------------------------------------
// 回调管理
// ---------------------------------------------------------------------------

func TestPanicHandlerAccessors(t *testing.T) {
	if xsafe.PanicHandler() != nil {
		t.Fatal("default handler should be nil (stderr fallback)")
	}
	fn := func(error) {}
	xsafe.SetPanicHandler(fn)
	if xsafe.PanicHandler() == nil {
		t.Fatal("handler should be set")
	}
	xsafe.SetPanicHandler(nil)
	if xsafe.PanicHandler() != nil {
		t.Fatal("handler should be cleared")
	}
}

// syncBuffer 是并发安全的字节缓冲，用于并发写/读场景。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDefaultWriterReceivesPanic(t *testing.T) {
	xsafe.SetPanicHandler(nil)
	defer xsafe.SetPanicHandler(nil)

	if xsafe.PanicWriter() == nil {
		t.Fatal("default writer should never be nil")
	}

	ms := &syncBuffer{}
	xsafe.SetPanicWriter(ms)
	defer xsafe.SetPanicWriter(nil)
	if xsafe.PanicWriter() != ms {
		t.Fatal("SetPanicWriter should take effect")
	}

	xsafe.Go(func() { panic("default-handler-boom") })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out := ms.String()
		if strings.Contains(out, "default-handler-boom") {
			if !strings.Contains(out, "[xsafe]") {
				t.Fatalf("缺少 [xsafe] 前缀: %q", out)
			}
			if !strings.Contains(out, "goroutine") {
				t.Fatalf("缺少调用栈: %q", out)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("panic 未被写入默认输出目标: %q", ms.String())
}

func TestDefaultWriterReset(t *testing.T) {
	ms := &syncBuffer{}
	xsafe.SetPanicWriter(ms)
	xsafe.SetPanicWriter(nil)
	if got := xsafe.PanicWriter(); got != os.Stderr {
		t.Fatalf("nil should restore os.Stderr, got %#v", got)
	}
}

// ---------------------------------------------------------------------------
// 调用栈辅助
// ---------------------------------------------------------------------------

func TestCaller(t *testing.T) {
	file, line, fn := xsafe.Caller(0)
	if !strings.HasSuffix(file, "xsafe_test.go") {
		t.Fatalf("file = %q", file)
	}
	if line <= 0 {
		t.Fatalf("line = %d", line)
	}
	if !strings.Contains(fn, "TestCaller") {
		t.Fatalf("fn = %q", fn)
	}
}

func TestFuncName(t *testing.T) {
	if got := xsafe.FuncName(); !strings.Contains(got, "TestFuncName") {
		t.Fatalf("FuncName = %q", got)
	}
	if got := xsafe.ShortFuncName(); !strings.Contains(got, "TestFuncName") || strings.Contains(got, "/") {
		t.Fatalf("ShortFuncName = %q", got)
	}
}

func TestStack(t *testing.T) {
	full := xsafe.Stack(0)
	if !strings.Contains(string(full), "goroutine") {
		t.Fatalf("stack should contain goroutine header: %.60s", string(full))
	}
	if !strings.Contains(string(full), "TestStack") {
		t.Fatal("stack should contain TestStack")
	}

	skipped := xsafe.Stack(3)
	if !strings.Contains(string(skipped), "goroutine") {
		t.Fatal("skipped stack should keep goroutine header")
	}
	if len(skipped) >= len(full) {
		t.Fatal("skipped stack should be shorter")
	}

	// 极端 skip 不应 panic
	_ = xsafe.Stack(1 << 20)
	_ = xsafe.Stack(-5)
}

func TestCallerUnknown(t *testing.T) {
	file, line, fn := xsafe.Caller(1 << 20)
	if file != "" || line != 0 || fn != "" {
		t.Fatalf("huge skip should return empty, got %q %d %q", file, line, fn)
	}
}

// ---------------------------------------------------------------------------
// IsTimeout
// ---------------------------------------------------------------------------

func TestIsTimeout(t *testing.T) {
	if xsafe.IsTimeout(nil) {
		t.Fatal("nil")
	}
	if !xsafe.IsTimeout(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded")
	}
	if !xsafe.IsTimeout(fmt.Errorf("wrap: %w", context.DeadlineExceeded)) {
		t.Fatal("wrapped deadline")
	}
	if xsafe.IsTimeout(errors.New("boom")) {
		t.Fatal("plain error")
	}
	if !xsafe.IsTimeout(&net.OpError{Op: "dial", Err: netErrTimeout{}}) {
		t.Fatal("net timeout")
	}
}

type netErrTimeout struct{}

func (netErrTimeout) Error() string   { return "i/o timeout" }
func (netErrTimeout) Timeout() bool   { return true }
func (netErrTimeout) Temporary() bool { return true }
