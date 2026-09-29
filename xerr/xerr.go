// Package xerr 提供统一的错误定义、错误码、包装、分类与聚合能力。
//
// 设计要点：
//   - 错误码分段（见 Code 常量），便于跨服务映射与前端的统一处理；
//   - 所有错误都可通过标准库 errors.Is / errors.As 判定，错误链完整保留 cause；
//   - CodeOf / MessageOf / MetaOf / OpOf 沿着错误链查找，无需自行 Unwrap；
//   - 预定义哨兵错误（ErrNotFound 等）支持按错误码匹配：
//     errors.Is(err, xerr.ErrNotFound) 在 err 的错误码为 CodeNotFound 时为 true；
//   - 永不 panic：所有构造函数对 nil 输入均返回安全值。
//
// 并发安全：所有导出函数与类型均可并发使用。
package xerr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"sync/atomic"
)

// Code 业务/系统错误码。
//
// 分段规范：
//
//	0            成功
//	10000–19999  客户端 / 参数类
//	20000–29999  认证授权类
//	30000–39999  资源 / 业务冲突类
//	40000–49999  依赖 / 流量类
//	50000–59999  服务内部类
//	100000+      业务模块（按模块 ×1000 分段，如 100000 订单、101000 支付）
type Code int

// 系统级错误码。
const (
	CodeOK Code = 0

	// 客户端 / 参数类
	CodeUnknown       Code = 10000
	CodeInvalidParam  Code = 10001
	CodeMissingParam  Code = 10002
	CodeInvalidFormat Code = 10003
	CodeOutOfRange    Code = 10004

	// 认证授权类
	CodeUnauthorized Code = 20001
	CodeForbidden    Code = 20002
	CodeTokenExpired Code = 20003

	// 资源 / 冲突类
	CodeNotFound      Code = 30001
	CodeAlreadyExists Code = 30002
	CodeConflict      Code = 30003
	CodeStateInvalid  Code = 30004

	// 依赖 / 流量类
	CodeTimeout          Code = 40001
	CodeRateLimited      Code = 40002
	CodeUnavailable      Code = 40003
	CodeTooManyRequests  Code = 40004
	CodeDependencyFailed Code = 40005

	// 服务内部类
	CodeInternal          Code = 50000
	CodeNotImplemented    Code = 50001
	CodeResourceExhausted Code = 50002
	CodeCanceled          Code = 50003
)

// codeText 错误码的默认文案。
var codeText = map[Code]string{
	CodeOK:                "ok",
	CodeUnknown:           "unknown error",
	CodeInvalidParam:      "invalid parameter",
	CodeMissingParam:      "missing parameter",
	CodeInvalidFormat:     "invalid format",
	CodeOutOfRange:        "value out of range",
	CodeUnauthorized:      "unauthorized",
	CodeForbidden:         "forbidden",
	CodeTokenExpired:      "token expired",
	CodeNotFound:          "not found",
	CodeAlreadyExists:     "already exists",
	CodeConflict:          "conflict",
	CodeStateInvalid:      "invalid state",
	CodeTimeout:           "timeout",
	CodeRateLimited:       "rate limited",
	CodeUnavailable:       "service unavailable",
	CodeTooManyRequests:   "too many requests",
	CodeDependencyFailed:  "dependency failed",
	CodeInternal:          "internal error",
	CodeNotImplemented:    "not implemented",
	CodeResourceExhausted: "resource exhausted",
	CodeCanceled:          "canceled",
}

// codeUnset 表示「错误码未设置」（值为 0，与 CodeOK 同值）。
//
// New / Newf / Wrap / Wrapf 创建的错误不携带错误码，仅表示上下文；
// CodeOf 会沿错误链找到第一个**显式设置**了错误码的错误并返回它。
// 这样 xerr.Wrap(xerr.NewC(xerr.CodeNotFound, "missing"), "lookup failed")
// 的 CodeOf 仍然是 CodeNotFound，包装不会丢失业务码。
const codeUnset Code = CodeOK

// String 返回错误码的可读文案（未登记的码返回 "code(<n>)"）。
func (c Code) String() string {
	if s, ok := codeText[c]; ok {
		return s
	}
	return fmt.Sprintf("code(%d)", int(c))
}

// Text 返回错误码的默认文案，未知码返回空串。
func (c Code) Text() string { return codeText[c] }

// CodeOfText 按错误码取默认文案（别名，便于链式阅读）。
func CodeOfText(c Code) string { return codeText[c] }

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

// captureStack 控制是否在创建错误时抓取调用栈（默认关闭，性能敏感）。
var captureStack atomic.Bool

// SetCaptureStack 开启/关闭错误创建时的堆栈捕获。
// 建议仅在开发/调试环境开启，生产环境关闭以避免性能损耗。
func SetCaptureStack(on bool) { captureStack.Store(on) }

// Error 是 xerr 的标准错误类型。
//
// 字段不导出，强制通过构造函数创建，保证内部不变量（如 cause 链的完整性）。
type Error struct {
	code  Code
	msg   string
	op    string
	cause error
	meta  map[string]any
	stack []byte
}

// 编译期断言：*Error 实现 error 接口。
var _ error = (*Error)(nil)

// Error 实现 error 接口。
//
// 输出格式（尽量紧凑）：
//
//	xerr: invalid parameter: age must be positive
//	xerr: db.query: not found: user 1: sql: no rows in result set
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("xerr: ")
	if e.op != "" {
		b.WriteString(e.op)
		b.WriteString(": ")
	}
	b.WriteString(e.text())
	if e.cause != nil {
		b.WriteString(": ")
		b.WriteString(e.cause.Error())
	}
	return b.String()
}

// text 返回该错误自身的展示文案（不含 op 与 cause）。
//
// 优先级：自定义 msg > 错误码默认文案 > "unknown error"。
func (e *Error) text() string {
	if e == nil {
		return CodeUnknown.String()
	}
	if e.msg != "" {
		return e.msg
	}
	if e.code != codeUnset {
		return e.code.String()
	}
	return CodeUnknown.String()
}

// Unwrap 返回底层原因，使 errors.Is / errors.As 可沿链查找。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is 支持按错误码或文案匹配哨兵错误。
//
// 规则（target 必须是 *Error）：
//   - target.code != 0 时必须与 e.code 相等；
//   - target.msg 非空时必须与 e.msg 相等；
//   - 二者都为空的 target 不匹配任何错误。
//
// 因此 errors.Is(err, xerr.ErrNotFound) 可匹配任意「码为 CodeNotFound」的错误。
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	t, ok := target.(*Error)
	if !ok || t == nil {
		return false
	}
	if t.code == 0 && t.msg == "" {
		return false
	}
	if t.code != 0 && t.code != e.code {
		return false
	}
	if t.msg != "" && t.msg != e.msg {
		return false
	}
	return true
}

// Code 返回错误码。
func (e *Error) Code() Code {
	if e == nil {
		return CodeOK
	}
	return e.code
}

// Msg 返回错误文案（不含底层 cause）。
//
// 未设置 msg 时回退到错误码的默认文案；两者都没有时返回 "unknown error"。
func (e *Error) Msg() string {
	if e == nil {
		return ""
	}
	return e.text()
}

// Op 返回操作名（形如 "db.query" / "userService.Get"）。
func (e *Error) Op() string {
	if e == nil {
		return ""
	}
	return e.op
}

// Cause 返回底层原因。
func (e *Error) Cause() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Meta 返回附加上下文（只读，返回副本以免外部篡改）。
func (e *Error) Meta() map[string]any {
	if e == nil || len(e.meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(e.meta))
	for k, v := range e.meta {
		out[k] = v
	}
	return out
}

// Stack 返回创建错误时抓取的堆栈（未开启捕获时返回 nil）。
func (e *Error) Stack() []byte {
	if e == nil {
		return nil
	}
	return e.stack
}

// ---------------------------------------------------------------------------
// 构造函数
// ---------------------------------------------------------------------------

func newError(code Code, msg, op string, cause error) *Error {
	e := &Error{code: code, msg: msg, op: op, cause: cause}
	if captureStack.Load() {
		e.stack = captureStackBytes()
	}
	return e
}

func captureStackBytes() []byte { return debug.Stack() }

// New 创建一个不带错误码的错误（CodeOf 会返回 CodeUnknown）。
func New(msg string) error { return newError(codeUnset, msg, "", nil) }

// Newf 创建不带错误码的格式化错误。
func Newf(format string, args ...any) error {
	return newError(codeUnset, fmt.Sprintf(format, args...), "", nil)
}

// NewC 创建带错误码的错误。
func NewC(code Code, msg string) *Error { return newError(code, msg, "", nil) }

// NewfC 创建带错误码的格式化错误。
func NewfC(code Code, format string, args ...any) *Error {
	return newError(code, fmt.Sprintf(format, args...), "", nil)
}

// Wrap 包装错误并附加文案。新错误不携带错误码，因此内层业务码不会丢失。
//
// 若 err 为 nil，返回 nil（与 pkg/errors 一致）。
func Wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return newError(codeUnset, msg, "", err)
}

// Wrapf 包装错误并附加格式化文案。
func Wrapf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return newError(codeUnset, fmt.Sprintf(format, args...), "", err)
}

// WrapC 包装错误并附加错误码与文案。
func WrapC(err error, code Code, msg string) *Error {
	return newError(code, msg, "", err)
}

// WrapCf 包装错误并附加错误码与格式化文案。
func WrapCf(err error, code Code, format string, args ...any) *Error {
	return newError(code, fmt.Sprintf(format, args...), "", err)
}

// WithCode 为已有错误附加错误码。
//
//   - err 为 nil 时返回 nil；
//   - 若 err 已是 *Error，则返回其浅拷贝并覆盖 code（不修改原错误）；
//   - 否则包装为新错误，保留原因链。
func WithCode(err error, code Code) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		cp := *e
		cp.code = code
		return &cp
	}
	return newError(code, "", "", err)
}

// WithOp 为已有错误附加操作名。
func WithOp(err error, op string) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		cp := *e
		cp.op = op
		return &cp
	}
	return newError(codeUnset, "", op, err)
}

// WithMeta 为已有错误附加键值上下文（kv 必须成对，奇数个时忽略最后一个）。
//
// 多次调用会合并 meta，后者覆盖前者。
func WithMeta(err error, kv ...any) error {
	if err == nil {
		return nil
	}
	pairs := normalizeKV(kv)

	var base *Error
	if e, ok := err.(*Error); ok {
		cp := *e
		base = &cp
	} else {
		base = newError(codeUnset, "", "", err)
	}
	if len(pairs) == 0 {
		return base
	}
	meta := make(map[string]any, len(base.meta)+len(pairs)/2)
	for k, v := range base.meta {
		meta[k] = v
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		meta[fmt.Sprint(pairs[i])] = pairs[i+1]
	}
	base.meta = meta
	return base
}

// normalizeKV 过滤掉无法作为键的值，返回规整的 kv 切片。
func normalizeKV(kv []any) []any {
	if len(kv) < 2 {
		return nil
	}
	out := make([]any, 0, len(kv))
	for i := 0; i < len(kv); i++ {
		out = append(out, kv[i])
	}
	return out
}

// ---------------------------------------------------------------------------
// 沿链提取
// ---------------------------------------------------------------------------

// walkErrors 深度优先遍历错误链上的所有 *Error。
//
// 遍历顺序：外层 → 内层；遇到 Unwrap() []error 时按切片顺序展开各分支。
// fn 返回 true 表示提前终止遍历。walkErrors 返回 true 表示被 fn 终止。
func walkErrors(err error, fn func(*Error) bool) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			if fn(e) {
				return true
			}
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, sub := range u.Unwrap() {
				if walkErrors(sub, fn) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// CodeOf 沿错误链查找**第一个显式设置了错误码**的错误。
//
//   - err 为 nil 时返回 CodeOK；
//   - 错误链上没有任何 xerr 错误码时，尝试把常见标准错误语义化
//     （context.Canceled → CodeCanceled，os.ErrNotExist → CodeNotFound …）；
//   - 仍然无法判定时返回 CodeUnknown。
func CodeOf(err error) Code {
	if err == nil {
		return CodeOK
	}
	var code Code
	found := false
	walkErrors(err, func(e *Error) bool {
		if e.code != codeUnset {
			code = e.code
			found = true
			return true
		}
		return false
	})
	if found {
		return code
	}
	// 常见标准错误的语义化映射
	switch {
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return CodeTimeout
	case errors.Is(err, os.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, os.ErrExist):
		return CodeAlreadyExists
	case errors.Is(err, os.ErrPermission):
		return CodeForbidden
	}
	return CodeUnknown
}

// MessageOf 返回最有描述性的错误文案。
//
// 取错误链上**最内层的非空文案**（即根因的业务描述），
// 包装层添加的上下文不会覆盖它；链上没有任何 xerr 错误时返回 err.Error()。
func MessageOf(err error) string {
	if err == nil {
		return ""
	}
	var msg string
	walkErrors(err, func(e *Error) bool {
		if e.msg != "" {
			msg = e.msg
		}
		return false
	})
	if msg != "" {
		return msg
	}
	return err.Error()
}

// OpOf 沿错误链查找操作名（取最外层非空值），找不到返回空串。
func OpOf(err error) string {
	var op string
	walkErrors(err, func(e *Error) bool {
		if e.op != "" {
			op = e.op
			return true
		}
		return false
	})
	return op
}

// MetaOf 沿错误链查找第一个非空的附加上下文，找不到返回 nil。
//
// 返回的是副本，外部修改不会影响原错误。
func MetaOf(err error) map[string]any {
	var meta map[string]any
	walkErrors(err, func(e *Error) bool {
		if len(e.meta) > 0 {
			meta = e.meta
			return true
		}
		return false
	})
	if meta == nil {
		return nil
	}
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k] = v
	}
	return out
}

// StackOf 沿错误链查找堆栈信息，找不到返回 nil。
func StackOf(err error) []byte {
	var stack []byte
	walkErrors(err, func(e *Error) bool {
		if len(e.stack) > 0 {
			stack = e.stack
			return true
		}
		return false
	})
	return stack
}

// Is 转发到标准库 errors.Is。
func Is(err, target error) bool { return errors.Is(err, target) }

// IsCode 判断错误链上的错误码是否等于 code。
func IsCode(err error, code Code) bool {
	if err == nil {
		return code == CodeOK
	}
	return CodeOf(err) == code
}

// As 是标准库 errors.As 的类型安全版本。
//
// 用法：
//
//	if e, ok := xerr.As[*xerr.Error](err); ok { ... }
func As[T error](err error) (T, bool) {
	var target T
	if errors.As(err, &target) {
		return target, true
	}
	var zero T
	return zero, false
}

// AsCode 是 errors.As 的转发，保留泛型风格的调用一致性。
func AsCode(err error, target any) bool { return errors.As(err, target) }

// Unwrap 转发到标准库 errors.Unwrap。
func Unwrap(err error) error { return errors.Unwrap(err) }

// Join 转发到标准库 errors.Join（Go 1.20+）。
func Join(errs ...error) error { return errors.Join(errs...) }

// Ignore 显式忽略一个错误（用于让静态检查知晓「此处有意忽略」）。
//
// 编译期无副作用；建议写成 defer xerr.Ignore(f.Close()) 的形式。
func Ignore(err error) { _ = err }

// ---------------------------------------------------------------------------
// 哨兵错误
// ---------------------------------------------------------------------------

// 预定义哨兵错误：仅带错误码，可用 errors.Is 匹配任意同码错误。
var (
	ErrUnknown           = &Error{code: CodeUnknown}
	ErrInvalidParam      = &Error{code: CodeInvalidParam}
	ErrMissingParam      = &Error{code: CodeMissingParam}
	ErrInvalidFormat     = &Error{code: CodeInvalidFormat}
	ErrOutOfRange        = &Error{code: CodeOutOfRange}
	ErrUnauthorized      = &Error{code: CodeUnauthorized}
	ErrForbidden         = &Error{code: CodeForbidden}
	ErrTokenExpired      = &Error{code: CodeTokenExpired}
	ErrNotFound          = &Error{code: CodeNotFound}
	ErrAlreadyExists     = &Error{code: CodeAlreadyExists}
	ErrConflict          = &Error{code: CodeConflict}
	ErrStateInvalid      = &Error{code: CodeStateInvalid}
	ErrTimeout           = &Error{code: CodeTimeout}
	ErrRateLimited       = &Error{code: CodeRateLimited}
	ErrUnavailable       = &Error{code: CodeUnavailable}
	ErrTooManyRequests   = &Error{code: CodeTooManyRequests}
	ErrDependencyFailed  = &Error{code: CodeDependencyFailed}
	ErrInternal          = &Error{code: CodeInternal}
	ErrNotImplemented    = &Error{code: CodeNotImplemented}
	ErrResourceExhausted = &Error{code: CodeResourceExhausted}
	ErrCanceled          = &Error{code: CodeCanceled}
)

// ---------------------------------------------------------------------------
// 分类判定
// ---------------------------------------------------------------------------

// IsNotFound 判断是否为「资源不存在」。
func IsNotFound(err error) bool {
	return err != nil && (CodeOf(err) == CodeNotFound || errors.Is(err, ErrNotFound))
}

// IsConflict 判断是否为冲突（已存在 / 状态不允许 / 并发冲突）。
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	switch CodeOf(err) {
	case CodeConflict, CodeAlreadyExists, CodeStateInvalid:
		return true
	}
	return false
}

// IsUnauthorized 判断是否未认证（含 token 过期）。
func IsUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	switch CodeOf(err) {
	case CodeUnauthorized, CodeTokenExpired:
		return true
	}
	return false
}

// IsForbidden 判断是否无权限。
func IsForbidden(err error) bool {
	return err != nil && CodeOf(err) == CodeForbidden
}

// IsTimeout 判断是否为超时错误（含 context 截止时间、net.Error 超时）。
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if CodeOf(err) == CodeTimeout {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// IsCanceled 判断是否为主动取消。
func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || CodeOf(err) == CodeCanceled
}

// IsRetryable 判断错误是否值得重试。
//
// 可重试：超时、限流、下游不可用、连接类错误（net.OpError）、EOF。
// 不可重试：参数错误、认证/权限、资源不存在、主动取消。
//
// 典型用法：xconcurrency.Retry(ctx, fn, WithRetryIf(xerr.IsRetryable))
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// 主动取消永不重试
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	switch CodeOf(err) {
	case CodeTimeout, CodeRateLimited, CodeTooManyRequests,
		CodeUnavailable, CodeDependencyFailed, CodeResourceExhausted:
		return true
	}
	return false
}

// IsTransient 是 IsRetryable 的别名，语义化表达「瞬时故障」。
func IsTransient(err error) bool { return IsRetryable(err) }

// HTTPStatus 把错误映射为合适的 HTTP 状态码。
func HTTPStatus(err error) int {
	if err == nil {
		return 200
	}
	if errors.Is(err, context.Canceled) {
		return 499 // 客户端主动断开（nginx 约定）
	}
	switch CodeOf(err) {
	case CodeOK:
		return 200
	case CodeInvalidParam, CodeMissingParam, CodeInvalidFormat, CodeOutOfRange:
		return 400
	case CodeUnauthorized, CodeTokenExpired:
		return 401
	case CodeForbidden:
		return 403
	case CodeNotFound:
		return 404
	case CodeConflict, CodeAlreadyExists, CodeStateInvalid:
		return 409
	case CodeTimeout:
		return 504
	case CodeRateLimited, CodeTooManyRequests:
		return 429
	case CodeUnavailable, CodeDependencyFailed:
		return 503
	case CodeNotImplemented:
		return 501
	case CodeCanceled:
		return 499
	default:
		return 500
	}
}

// ---------------------------------------------------------------------------
// 聚合错误
// ---------------------------------------------------------------------------

// MultiError 把多个错误聚合成一个错误。
//
// 实现了 Unwrap() []error，因此 errors.Is / errors.As 会遍历全部子错误。
type MultiError []error

var _ error = MultiError(nil)

// Error 实现 error 接口，只统计非 nil 子错误，输出形如 "2 errors: a; b"。
func (m MultiError) Error() string {
	n := 0
	for _, err := range m {
		if err != nil {
			n++
		}
	}
	switch n {
	case 0:
		return "no errors"
	case 1:
		for _, err := range m {
			if err != nil {
				return err.Error()
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d errors: ", n)
	first := true
	for _, err := range m {
		if err == nil {
			continue
		}
		if !first {
			b.WriteString("; ")
		}
		first = false
		b.WriteString(err.Error())
	}
	return b.String()
}

// Unwrap 返回全部子错误，供 errors.Is / errors.As 遍历。
func (m MultiError) Unwrap() []error { return m }

// Errors 返回全部子错误（副本）。
func (m MultiError) Errors() []error {
	out := make([]error, len(m))
	copy(out, m)
	return out
}

// First 返回第一个非 nil 错误，没有则返回 nil。
func (m MultiError) First() error {
	for _, err := range m {
		if err != nil {
			return err
		}
	}
	return nil
}

// Has 判断是否存在（errors.Is 语义）某个错误。
func (m MultiError) Has(err error) bool { return errors.Is(m, err) }

// Len 返回子错误个数（含 nil）。
func (m MultiError) Len() int { return len(m) }

// Is 实现错误匹配，使 errors.Is(multi, target) 遍历子错误。
func (m MultiError) Is(target error) bool {
	for _, err := range m {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// NewMulti 创建聚合错误，自动过滤 nil。
func NewMulti(errs ...error) MultiError {
	out := make(MultiError, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// Append 追加错误到聚合错误（自动跳过 nil），返回新值。
func Append(m MultiError, errs ...error) MultiError {
	for _, err := range errs {
		if err != nil {
			m = append(m, err)
		}
	}
	return m
}

// Collect 顺序执行全部函数并聚合错误（与 errgroup 的 fail-fast 相反）。
//
// 全部函数都会被执行，忽略单个函数的错误不会中断后续执行。
func Collect(fns ...func() error) error {
	var m MultiError
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		if err := fn(); err != nil {
			m = append(m, err)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// CollectErr 是 Collect 的别名，语义更贴近「收集错误」。
func CollectErr(fns ...func() error) error { return Collect(fns...) }

// ---------------------------------------------------------------------------
// 断言与校验
// ---------------------------------------------------------------------------

// Check 在 cond 为 false 时生成一个错误，否则返回 nil。
//
//	if err := xerr.Check(age > 0, "age must be positive, got %d", age); err != nil { ... }
func Check(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return newError(codeUnset, fmt.Sprintf(format, args...), "", nil)
}

// CheckC 在 cond 为 false 时生成带错误码的错误。
func CheckC(cond bool, code Code, format string, args ...any) error {
	if cond {
		return nil
	}
	return newError(code, fmt.Sprintf(format, args...), "", nil)
}

// Must 在 err != nil 时 panic。仅用于初始化与测试场景。
//
// 生产代码请显式处理错误。
func Must(err error) {
	if err != nil {
		panic(err)
	}
}

// MustT 在 err != nil 时 panic，否则返回 v。仅用于初始化与测试场景。
func MustT[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Validator 是校验函数类型。
type Validator func() error

// Validate 顺序执行全部校验，返回聚合错误（全部都会执行）。
func Validate(vs ...Validator) error {
	var m MultiError
	for _, v := range vs {
		if v == nil {
			continue
		}
		if err := v(); err != nil {
			m = append(m, err)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// ---------------------------------------------------------------------------
// panic 转错误
// ---------------------------------------------------------------------------

// Catch 在 fn 内部发生 panic 时把 panic 值写入 *errp。
//
// 用法（必须 defer）：
//
//	func doSomething() (err error) {
//	    defer xerr.Catch(&err, func() error {
//	        // 可能 panic 的逻辑
//	        return nil
//	    })
//	    ...
//	}
func Catch(errp *error, fn func() error) {
	if errp == nil || fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				*errp = Wrap(e, "panic recovered")
				return
			}
			*errp = Newf("panic recovered: %v", r)
		}
	}()
	if err := fn(); err != nil {
		*errp = err
	}
}

// Recover 执行 fn 并把 panic 转为错误返回，正常返回其 error。
func Recover(fn func() error) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = Wrap(e, "panic recovered")
				return
			}
			err = Newf("panic recovered: %v", r)
		}
	}()
	return fn()
}
