package xerr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/indigo-apple/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 构造与格式化
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"new", xerr.New("boom"), "xerr: boom"},
		{"newf", xerr.Newf("boom %d", 42), "xerr: boom 42"},
		{"newc", xerr.NewC(xerr.CodeNotFound, "user missing"), "xerr: user missing"},
		{"newfc", xerr.NewfC(xerr.CodeNotFound, "user %d missing", 7), "xerr: user 7 missing"},
		// 文案为空时回退到错误码默认文案；无码无文案时回退到 unknown error
		{"code only", xerr.NewC(xerr.CodeUnknown, ""), "xerr: unknown error"},
		{"empty", xerr.New(""), "xerr: unknown error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.Error(); got != c.want {
				t.Fatalf("Error() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestErrorStringIncludesOpAndCause(t *testing.T) {
	base := errors.New("sql: no rows in result set")
	err := xerr.Wrap(base, "user missing")
	err = xerr.WithOp(err, "db.query")

	got := err.Error()
	want := "xerr: db.query: user missing: sql: no rows in result set"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestWrapNilReturnsNil(t *testing.T) {
	if err := xerr.Wrap(nil, "x"); err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}
	if err := xerr.Wrapf(nil, "x%d", 1); err != nil {
		t.Fatalf("Wrapf(nil) = %v, want nil", err)
	}
	if err := xerr.WithCode(nil, xerr.CodeInternal); err != nil {
		t.Fatalf("WithCode(nil) = %v, want nil", err)
	}
	if err := xerr.WithOp(nil, "op"); err != nil {
		t.Fatalf("WithOp(nil) = %v, want nil", err)
	}
	if err := xerr.WithMeta(nil, "k", "v"); err != nil {
		t.Fatalf("WithMeta(nil) = %v, want nil", err)
	}
	if err := xerr.WithMeta(errors.New("e"), "k", "v"); err == nil {
		t.Fatal("WithMeta(non-nil) should be non-nil")
	}
}

// ---------------------------------------------------------------------------
// 错误码
// ---------------------------------------------------------------------------

func TestCodeOfWalksChain(t *testing.T) {
	inner := xerr.NewC(xerr.CodeNotFound, "missing")
	mid := xerr.Wrap(inner, "lookup failed")
	outer := xerr.Wrap(mid, "handler failed")

	if got := xerr.CodeOf(outer); got != xerr.CodeNotFound {
		t.Fatalf("CodeOf = %v, want %v", got, xerr.CodeNotFound)
	}
	if got := xerr.MessageOf(outer); got != "missing" {
		t.Fatalf("MessageOf = %q, want %q", got, "missing")
	}
	if got := xerr.CodeOf(errors.New("plain")); got != xerr.CodeUnknown {
		t.Fatalf("CodeOf(plain) = %v, want %v", got, xerr.CodeUnknown)
	}
	if got := xerr.CodeOf(nil); got != xerr.CodeOK {
		t.Fatalf("CodeOf(nil) = %v, want CodeOK", got)
	}
}

func TestCodeOfStandardErrors(t *testing.T) {
	cases := []struct {
		err  error
		want xerr.Code
	}{
		{context.Canceled, xerr.CodeCanceled},
		{context.DeadlineExceeded, xerr.CodeTimeout},
		{os.ErrNotExist, xerr.CodeNotFound},
		{os.ErrExist, xerr.CodeAlreadyExists},
		{os.ErrPermission, xerr.CodeForbidden},
		{fmt.Errorf("wrap: %w", context.Canceled), xerr.CodeCanceled},
	}
	for _, c := range cases {
		if got := xerr.CodeOf(c.err); got != c.want {
			t.Fatalf("CodeOf(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestWithCodeDoesNotMutateOriginal(t *testing.T) {
	orig := xerr.NewC(xerr.CodeNotFound, "missing")
	got := xerr.WithCode(orig, xerr.CodeInternal)

	if xerr.CodeOf(orig) != xerr.CodeNotFound {
		t.Fatalf("original mutated: %v", xerr.CodeOf(orig))
	}
	if xerr.CodeOf(got) != xerr.CodeInternal {
		t.Fatalf("copy code = %v, want %v", xerr.CodeOf(got), xerr.CodeInternal)
	}
	// 副本仍保留原原因链（errors.Is 可沿链找到原错误）
	if !errors.Is(got, orig) && xerr.MessageOf(got) != "missing" {
		t.Fatal("copy should keep cause/message chain")
	}
	// 副本按新码匹配哨兵
	if !errors.Is(got, xerr.ErrInternal) {
		t.Fatal("copy should match ErrInternal")
	}
	if errors.Is(got, xerr.ErrNotFound) {
		t.Fatal("copy should no longer match ErrNotFound")
	}
}

func TestWithMeta(t *testing.T) {
	err := xerr.WithMeta(xerr.NewC(xerr.CodeInvalidParam, "bad"), "field", "age", "value", -1)
	m := xerr.MetaOf(err)
	if m["field"] != "age" || m["value"] != -1 {
		t.Fatalf("meta = %#v", m)
	}

	// 合并：第二次调用保留第一次的键
	err2 := xerr.WithMeta(err, "value", 99)
	m2 := xerr.MetaOf(err2)
	if m2["field"] != "age" || m2["value"] != 99 {
		t.Fatalf("merged meta = %#v", m2)
	}

	// 返回的是副本，外部修改不应影响内部
	m2["field"] = "hacked"
	if xerr.MetaOf(err2)["field"] != "age" {
		t.Fatal("MetaOf should return a copy")
	}

	// 奇数个参数、非字符串键
	err3 := xerr.WithMeta(xerr.New("x"), "a", 1, "odd")
	if got := xerr.MetaOf(err3); got["a"] != 1 || len(got) != 1 {
		t.Fatalf("odd kv meta = %#v", got)
	}
	err4 := xerr.WithMeta(xerr.New("x"), 1, "one")
	if got := xerr.MetaOf(err4); got["1"] != "one" {
		t.Fatalf("non-string key meta = %#v", got)
	}
	if got := xerr.MetaOf(xerr.New("no meta")); got != nil {
		t.Fatalf("MetaOf should be nil, got %#v", got)
	}
	if got := xerr.MetaOf(nil); got != nil {
		t.Fatalf("MetaOf(nil) = %#v", got)
	}
}

func TestOpOf(t *testing.T) {
	if got := xerr.OpOf(xerr.WithOp(xerr.New("x"), "svc.Do")); got != "svc.Do" {
		t.Fatalf("OpOf = %q", got)
	}
	if got := xerr.OpOf(errors.New("x")); got != "" {
		t.Fatalf("OpOf(plain) = %q", got)
	}
	if got := xerr.OpOf(nil); got != "" {
		t.Fatalf("OpOf(nil) = %q", got)
	}
}

// ---------------------------------------------------------------------------
// errors.Is / errors.As 集成
// ---------------------------------------------------------------------------

func TestErrorsIsSentinel(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"code match", xerr.NewC(xerr.CodeNotFound, "user 1 missing"), xerr.ErrNotFound, true},
		{"code mismatch", xerr.NewC(xerr.CodeInternal, "boom"), xerr.ErrNotFound, false},
		{"wrapped code match", xerr.Wrap(xerr.NewC(xerr.CodeConflict, "dup"), "insert"), xerr.ErrConflict, true},
		{"msg match", xerr.New("exact"), &xerr.Error{}, false}, // code=0 msg="" 不匹配
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := errors.Is(c.err, c.target); got != c.want {
				t.Fatalf("errors.Is = %v, want %v", got, c.want)
			}
		})
	}
}

func TestErrorsIsByCodeAndMsg(t *testing.T) {
	same := xerr.NewC(xerr.CodeInvalidParam, "same msg")
	if !errors.Is(xerr.NewC(xerr.CodeInvalidParam, "same msg"), same) {
		t.Fatal("identical code+msg should match")
	}
	if errors.Is(xerr.NewC(xerr.CodeInvalidParam, "other msg"), same) {
		t.Fatal("different msg should not match when target has msg")
	}
	// 只用码的哨兵应能匹配任意文案
	if !errors.Is(xerr.NewC(xerr.CodeInvalidParam, "anything"), xerr.ErrInvalidParam) {
		t.Fatal("code-only sentinel should match any msg")
	}
}

func TestErrorsAs(t *testing.T) {
	base := xerr.NewC(xerr.CodeTimeout, "dial timeout")
	err := xerr.Wrap(base, "rpc failed")

	e, ok := xerr.As[*xerr.Error](err)
	if !ok {
		t.Fatal("As[*Error] should succeed")
	}
	// As 返回最外层的 *Error（包装层），业务码需用 CodeOf 沿链获取
	if e.Msg() != "rpc failed" {
		t.Fatalf("msg = %q, want %q", e.Msg(), "rpc failed")
	}
	if got := xerr.CodeOf(err); got != xerr.CodeTimeout {
		t.Fatalf("CodeOf = %v, want %v", got, xerr.CodeTimeout)
	}

	// 不匹配的类型
	if _, ok := xerr.As[*os.PathError](err); ok {
		t.Fatal("As[*os.PathError] should fail")
	}

	// errors.As 直接遍历（非泛型）
	var target *xerr.Error
	if !errors.As(err, &target) || target.Msg() != "rpc failed" {
		t.Fatal("errors.As direct should work")
	}
}

func TestUnwrapChainToCause(t *testing.T) {
	cause := os.ErrNotExist
	err := xerr.WrapC(cause, xerr.CodeNotFound, "config missing")

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("should unwrap to os.ErrNotExist")
	}
	if xerr.Unwrap(err) != cause {
		t.Fatal("Unwrap should return cause")
	}
}

func TestJoin(t *testing.T) {
	a, b := xerr.New("a"), xerr.New("b")
	joined := xerr.Join(a, b)
	if !errors.Is(joined, a) || !errors.Is(joined, b) {
		t.Fatal("errors.Is should find both joined errors")
	}
	if xerr.Join() != nil {
		t.Fatal("Join() with no args should be nil")
	}
	if err := xerr.Join(nil, nil); err != nil {
		t.Fatalf("Join(nil,nil) = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// 分类判定
// ---------------------------------------------------------------------------

func TestClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		fn   func(error) bool
		want bool
	}{
		{"IsNotFound code", xerr.NewC(xerr.CodeNotFound, "x"), xerr.IsNotFound, true},
		{"IsNotFound std", os.ErrNotExist, xerr.IsNotFound, true},
		{"IsNotFound false", xerr.New("x"), xerr.IsNotFound, false},
		{"IsConflict exists", xerr.NewC(xerr.CodeAlreadyExists, "x"), xerr.IsConflict, true},
		{"IsConflict state", xerr.NewC(xerr.CodeStateInvalid, "x"), xerr.IsConflict, true},
		{"IsConflict false", xerr.New("x"), xerr.IsConflict, false},
		{"IsUnauthorized", xerr.NewC(xerr.CodeUnauthorized, "x"), xerr.IsUnauthorized, true},
		{"IsTokenExpired", xerr.NewC(xerr.CodeTokenExpired, "x"), xerr.IsUnauthorized, true},
		{"IsForbidden", xerr.NewC(xerr.CodeForbidden, "x"), xerr.IsForbidden, true},
		{"IsTimeout ctx", context.DeadlineExceeded, xerr.IsTimeout, true},
		{"IsTimeout code", xerr.NewC(xerr.CodeTimeout, "x"), xerr.IsTimeout, true},
		{"IsTimeout os", os.ErrDeadlineExceeded, xerr.IsTimeout, true},
		{"IsCanceled ctx", context.Canceled, xerr.IsCanceled, true},
		{"IsCanceled code", xerr.NewC(xerr.CodeCanceled, "x"), xerr.IsCanceled, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.fn(c.err); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}

	// nil 输入统一 false
	for _, fn := range []func(error) bool{
		xerr.IsNotFound, xerr.IsConflict, xerr.IsUnauthorized, xerr.IsForbidden,
		xerr.IsTimeout, xerr.IsCanceled, xerr.IsRetryable, xerr.IsTransient,
	} {
		if fn(nil) {
			t.Fatal("nil input should be false")
		}
	}
}

func TestIsTimeoutNetError(t *testing.T) {
	e := &net.OpError{Op: "dial", Err: timeoutErr{}}
	if !xerr.IsTimeout(e) {
		t.Fatal("net.OpError with timeout should be IsTimeout")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("op: %w", context.Canceled), false},
		{"timeout code", xerr.NewC(xerr.CodeTimeout, "x"), true},
		{"rate limited", xerr.NewC(xerr.CodeRateLimited, "x"), true},
		{"unavailable", xerr.NewC(xerr.CodeUnavailable, "x"), true},
		{"dependency", xerr.NewC(xerr.CodeDependencyFailed, "x"), true},
		{"conn reset", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"invalid param", xerr.NewC(xerr.CodeInvalidParam, "x"), false},
		{"forbidden", xerr.NewC(xerr.CodeForbidden, "x"), false},
		{"not found", xerr.NewC(xerr.CodeNotFound, "x"), false},
		{"plain", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := xerr.IsRetryable(c.err); got != c.want {
				t.Fatalf("IsRetryable = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHTTPStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 200},
		{xerr.NewC(xerr.CodeInvalidParam, "x"), 400},
		{xerr.NewC(xerr.CodeMissingParam, "x"), 400},
		{xerr.NewC(xerr.CodeUnauthorized, "x"), 401},
		{xerr.NewC(xerr.CodeTokenExpired, "x"), 401},
		{xerr.NewC(xerr.CodeForbidden, "x"), 403},
		{xerr.NewC(xerr.CodeNotFound, "x"), 404},
		{xerr.NewC(xerr.CodeConflict, "x"), 409},
		{xerr.NewC(xerr.CodeAlreadyExists, "x"), 409},
		{xerr.NewC(xerr.CodeRateLimited, "x"), 429},
		{xerr.NewC(xerr.CodeTimeout, "x"), 504},
		{xerr.NewC(xerr.CodeUnavailable, "x"), 503},
		{xerr.NewC(xerr.CodeNotImplemented, "x"), 501},
		{xerr.NewC(xerr.CodeInternal, "x"), 500},
		{context.Canceled, 499},
		{errors.New("boom"), 500},
	}
	for _, c := range cases {
		if got := xerr.HTTPStatus(c.err); got != c.want {
			t.Fatalf("HTTPStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestCodeString(t *testing.T) {
	if got := xerr.CodeNotFound.String(); got != "not found" {
		t.Fatalf("Code.String = %q", got)
	}
	if got := xerr.Code(999999).String(); got != "code(999999)" {
		t.Fatalf("unknown Code.String = %q", got)
	}
	if got := xerr.CodeNotFound.Text(); got != "not found" {
		t.Fatalf("Code.Text = %q", got)
	}
	if got := xerr.Code(999999).Text(); got != "" {
		t.Fatalf("unknown Code.Text = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 聚合错误
// ---------------------------------------------------------------------------

func TestMultiError(t *testing.T) {
	a := xerr.NewC(xerr.CodeNotFound, "a")
	b := xerr.NewC(xerr.CodeTimeout, "b")

	var m xerr.MultiError
	if m.Error() != "no errors" {
		t.Fatalf("empty MultiError = %q", m.Error())
	}

	// Append 自动跳过 nil
	m = xerr.Append(m, a, nil, b)
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
	if got := m.Error(); !strings.Contains(got, "2 errors:") {
		t.Fatalf("Error() = %q", got)
	}
	if m.First() != a {
		t.Fatal("First should be a")
	}
	if !m.Has(xerr.ErrNotFound) || !m.Has(xerr.ErrTimeout) || m.Has(xerr.ErrConflict) {
		t.Fatal("Has mismatch")
	}
	if !errors.Is(m, a) || !errors.Is(m, b) {
		t.Fatal("errors.Is should traverse MultiError")
	}
	if got := len(m.Errors()); got != 2 {
		t.Fatalf("Errors len = %d", got)
	}

	// 直接构造时允许存在 nil：Len 按切片长度，Error 只统计非 nil
	raw := xerr.MultiError{a, nil, b}
	if raw.Len() != 3 {
		t.Fatalf("raw Len = %d, want 3", raw.Len())
	}
	if got := raw.Error(); !strings.Contains(got, "2 errors:") || strings.Contains(got, "; ;") {
		t.Fatalf("raw Error() = %q", got)
	}

	// Unwrap 支持 errors.As 遍历
	var target *xerr.Error
	if !errors.As(m, &target) {
		t.Fatal("errors.As should traverse MultiError")
	}

	// 单元素格式化
	one := xerr.NewMulti(a)
	if one.Error() != a.Error() {
		t.Fatalf("single MultiError = %q, want %q", one.Error(), a.Error())
	}
	// 全 nil 过滤
	if got := xerr.NewMulti(nil, nil); len(got) != 0 {
		t.Fatalf("NewMulti(nil...) len = %d", len(got))
	}

	// nil 接收者安全
	var nilMulti xerr.MultiError
	if nilMulti.First() != nil || nilMulti.Has(a) || nilMulti.Len() != 0 {
		t.Fatal("nil MultiError should be safe")
	}
}

func TestCollect(t *testing.T) {
	var ran []int
	err := xerr.Collect(
		func() error { ran = append(ran, 1); return nil },
		func() error { ran = append(ran, 2); return xerr.New("two") },
		nil, // nil 函数安全跳过
		func() error { ran = append(ran, 3); return xerr.New("three") },
	)
	if len(ran) != 3 {
		t.Fatalf("all functions should run, ran = %v", ran)
	}
	var m xerr.MultiError
	if !errors.As(err, &m) || m.Len() != 2 {
		t.Fatalf("expected MultiError with 2 items, got %v", err)
	}
	if err := xerr.Collect(func() error { return nil }); err != nil {
		t.Fatalf("all-success Collect = %v, want nil", err)
	}
	if err := xerr.Collect(); err != nil {
		t.Fatalf("empty Collect = %v, want nil", err)
	}
}

func TestValidate(t *testing.T) {
	err := xerr.Validate(
		func() error { return nil },
		nil,
		func() error { return xerr.NewC(xerr.CodeInvalidParam, "bad") },
	)
	if xerr.CodeOf(err) != xerr.CodeInvalidParam {
		t.Fatalf("Validate err = %v", err)
	}
	if err := xerr.Validate(func() error { return nil }); err != nil {
		t.Fatalf("returned %v", err)
	}
	if err := xerr.Validate(); err != nil {
		t.Fatalf("empty Validate = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 断言
// ---------------------------------------------------------------------------

func TestCheck(t *testing.T) {
	if err := xerr.Check(true, "unused %d", 1); err != nil {
		t.Fatalf("Check(true) = %v", err)
	}
	err := xerr.Check(false, "age must be positive, got %d", -1)
	if err == nil || !strings.Contains(err.Error(), "age must be positive, got -1") {
		t.Fatalf("Check(false) = %v", err)
	}
	// 无格式化参数时不做 Sprintf，%s 保持原样
	if got := xerr.Check(false, "100%% done").Error(); !strings.Contains(got, "100% done") {
		t.Fatalf("Check no-args = %q", got)
	}
	if err := xerr.CheckC(true, xerr.CodeInternal, "x"); err != nil {
		t.Fatalf("CheckC(true) = %v", err)
	}
	if got := xerr.CodeOf(xerr.CheckC(false, xerr.CodeConflict, "dup")); got != xerr.CodeConflict {
		t.Fatalf("CheckC code = %v", got)
	}
}

func TestMust(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Must should panic on error")
		}
	}()
	xerr.Must(errors.New("boom"))
}

func TestMustT(t *testing.T) {
	if got := xerr.MustT(42, nil); got != 42 {
		t.Fatalf("MustT = %d", got)
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("MustT should panic on error")
			}
		}()
		_ = xerr.MustT(42, errors.New("boom"))
	}()
}

// ---------------------------------------------------------------------------
// panic 转换
// ---------------------------------------------------------------------------

func TestRecover(t *testing.T) {
	if err := xerr.Recover(nil); err != nil {
		t.Fatalf("Recover(nil) = %v", err)
	}
	if err := xerr.Recover(func() error { return nil }); err != nil {
		t.Fatalf("Recover(nil-fn) = %v", err)
	}
	want := errors.New("inner")
	if err := xerr.Recover(func() error { return want }); err != want {
		t.Fatalf("Recover returned %v, want %v", err, want)
	}

	err := xerr.Recover(func() error { panic("oh no") })
	if err == nil || !strings.Contains(err.Error(), "panic recovered: oh no") {
		t.Fatalf("Recover(panic string) = %v", err)
	}

	err = xerr.Recover(func() error { panic(errors.New("as error")) })
	if err == nil || !strings.Contains(err.Error(), "as error") {
		t.Fatalf("Recover(panic error) = %v", err)
	}
	if !strings.Contains(err.Error(), "panic recovered") {
		t.Fatalf("unexpected %v", err)
	}

	// panic 值是 error 时应保留原因链
	sentinel := xerr.NewC(xerr.CodeInternal, "sentinel")
	err = xerr.Recover(func() error { panic(sentinel) })
	if !errors.Is(err, xerr.ErrInternal) {
		t.Fatalf("panic error should keep chain: %v", err)
	}
}

func TestCatch(t *testing.T) {
	var err error
	xerr.Catch(&err, func() error { return nil })
	if err != nil {
		t.Fatalf("Catch success = %v", err)
	}

	xerr.Catch(&err, func() error { panic("bang") })
	if err == nil || !strings.Contains(err.Error(), "bang") {
		t.Fatalf("Catch panic = %v", err)
	}

	var nilP *error
	xerr.Catch(nilP, func() error { return nil }) // 不应 panic
	xerr.Catch(&err, nil)                         // 不应 panic
}

// ---------------------------------------------------------------------------
// 堆栈捕获
// ---------------------------------------------------------------------------

func TestCaptureStack(t *testing.T) {
	if got := xerr.New("x"); xerr.StackOf(got) != nil {
		t.Fatal("stack should be nil by default")
	}

	xerr.SetCaptureStack(true)
	defer xerr.SetCaptureStack(false)

	err := xerr.New("with stack")
	st := xerr.StackOf(err)
	if len(st) == 0 {
		t.Fatal("stack should be captured")
	}
	if !strings.Contains(string(st), "xerr_test") {
		t.Fatalf("stack does not look like a goroutine dump: %s", string(st)[:min(80, len(st))])
	}
	if got := xerr.StackOf(errors.New("plain")); got != nil {
		t.Fatal("StackOf(plain) should be nil")
	}
	if got := xerr.StackOf(nil); got != nil {
		t.Fatal("StackOf(nil) should be nil")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// 其他
// ---------------------------------------------------------------------------

func TestIsCode(t *testing.T) {
	if !xerr.IsCode(xerr.NewC(xerr.CodeNotFound, "x"), xerr.CodeNotFound) {
		t.Fatal("IsCode should be true")
	}
	if xerr.IsCode(xerr.NewC(xerr.CodeNotFound, "x"), xerr.CodeInternal) {
		t.Fatal("IsCode should be false")
	}
	if !xerr.IsCode(nil, xerr.CodeOK) {
		t.Fatal("IsCode(nil, CodeOK) should be true")
	}
	if xerr.IsCode(nil, xerr.CodeInternal) {
		t.Fatal("IsCode(nil, CodeInternal) should be false")
	}
}

func TestIgnore(t *testing.T) {
	xerr.Ignore(nil)
	xerr.Ignore(errors.New("ignored"))
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkNewC(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xerr.NewC(xerr.CodeNotFound, "not found")
	}
}

func BenchmarkNewCWithStack(b *testing.B) {
	xerr.SetCaptureStack(true)
	defer xerr.SetCaptureStack(false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xerr.NewC(xerr.CodeNotFound, "not found")
	}
}

func BenchmarkError(b *testing.B) {
	err := xerr.Wrap(xerr.NewC(xerr.CodeNotFound, "x"), "wrapped")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = err.Error()
	}
}

func BenchmarkCodeOf(b *testing.B) {
	err := xerr.Wrap(xerr.Wrap(xerr.NewC(xerr.CodeNotFound, "x"), "m"), "o")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xerr.CodeOf(err)
	}
}
