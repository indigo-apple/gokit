// Package xtype 提供泛型类型操作：取地址、解引用、算术、比较、条件选择与类型断言。
//
// 设计要点：
//   - 「取地址」用 Ptr / Addr；「相加」等算术用 Add / Sub / Mul / Div / Sum，
//     使用 xconstraints.Number 约束，编译期杜绝字符串参与运算；
//   - 算术运算**不做溢出检查**（与 Go 语言一致，溢出即回绕）；
//     除法、取模遇到除数为零时返回零值而**不 panic**；
//   - Abs 对最小负数会因溢出返回负数（Go 语言的固有限制，已文档化）；
//   - 泛型无法在类型参数上做重载，因此类型转换请使用 xconv（本包只做同类型运算与安全断言）。
//
// 并发安全：本包全部为纯函数，可并发调用。
package xtype

import (
	"fmt"
	"reflect"

	"github.com/indigo-apple/gokit/xconstraints"
)

// ---------------------------------------------------------------------------
// 取地址 / 解引用
// ---------------------------------------------------------------------------

// Ptr 返回 v 的地址。
//
// 等价于 &v，但可内联在表达式里，避免先声明临时变量：
//
//	cfg.Name = xtype.Ptr("gokit")
//
// 注意：绝不要在循环里对循环变量取地址（Go 1.21 及以前所有指针会指向同一变量），
// 批量场景请使用 Ptrs。
func Ptr[T any](v T) *T { return &v }

// Addr 是 Ptr 的别名，语义上更贴近「取地址符 &」。
func Addr[T any](v T) *T { return &v }

// Ptrs 对切片每个元素取地址，返回新切片。
//
// 实现中对每个元素做了显式拷贝，因此不存在「所有指针指向同一变量」的陷阱。
func Ptrs[T any](s []T) []*T {
	if s == nil {
		return nil
	}
	out := make([]*T, len(s))
	for i := range s {
		v := s[i]
		out[i] = &v
	}
	return out
}

// Deref 解引用；p 为 nil 时返回类型零值。
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// DerefOr 解引用；p 为 nil 时返回 def。
func DerefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// Derefs 批量解引用，跳过 nil 指针。
func Derefs[T any](s []*T) []T {
	if s == nil {
		return nil
	}
	out := make([]T, 0, len(s))
	for _, p := range s {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// Swap 交换两个指针指向的值；任一为 nil 时不做任何事。
func Swap[T any](a, b *T) {
	if a == nil || b == nil {
		return
	}
	*a, *b = *b, *a
}

// ClonePtr 复制指针指向的值，返回指向新值的指针；p 为 nil 时返回 nil。
//
// 与 Ptr(Deref(p)) 等价，意图更清晰。
func ClonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// IsNilPtr 判断指针是否为 nil。
func IsNilPtr[T any](p *T) bool { return p == nil }

// ---------------------------------------------------------------------------
// 算术运算
// ---------------------------------------------------------------------------

// Add 返回 a + b。溢出回绕（不做检查）。
func Add[T xconstraints.Number](a, b T) T { return a + b }

// Sub 返回 a - b。溢出回绕（不做检查）。
func Sub[T xconstraints.Number](a, b T) T { return a - b }

// Mul 返回 a * b。溢出回绕（不做检查）。
func Mul[T xconstraints.Number](a, b T) T { return a * b }

// Div 返回 a / b；b 为零时返回类型零值（不 panic）。
//
// 注意：浮点除零在 Go 中本会产生 Inf/NaN，本函数统一改写为返回零值，
// 以避免把非法值悄悄传播到下游。
func Div[T xconstraints.Number](a, b T) T {
	if b == 0 {
		var zero T
		return zero
	}
	return a / b
}

// Mod 返回 a % b；b 为零时返回零值（不 panic）。
func Mod[T xconstraints.Integer](a, b T) T {
	if b == 0 {
		var zero T
		return zero
	}
	return a % b
}

// Neg 返回 -a。
func Neg[T xconstraints.Signed | xconstraints.Float](a T) T { return -a }

// Abs 返回 a 的绝对值。
//
// 注意：对整数最小值（如 math.MinInt64）取绝对值会溢出，返回的仍是负数
// —— 这是 Go 语言的固有限制。
func Abs[T xconstraints.Signed | xconstraints.Float](a T) T {
	if a < 0 {
		return -a
	}
	return a
}

// Inc 把 *a 增加 delta（原地修改）。
func Inc[T xconstraints.Number](a *T, delta T) {
	if a == nil {
		return
	}
	*a += delta
}

// Sum 返回全部参数之和；无参数时返回零值。
func Sum[T xconstraints.Number](vs ...T) T {
	var r T
	for _, v := range vs {
		r += v
	}
	return r
}

// Average 返回平均值；无参数时返回 0。
func Average[T xconstraints.Number](vs ...T) float64 {
	if len(vs) == 0 {
		return 0
	}
	var r float64
	for _, v := range vs {
		r += float64(v)
	}
	return r / float64(len(vs))
}

// Min 返回较小者。
func Min[T xconstraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

// Max 返回较大者。
func Max[T xconstraints.Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

// MinN 返回最小值；空输入返回零值与 false。
func MinN[T xconstraints.Ordered](vs ...T) (T, bool) {
	if len(vs) == 0 {
		var zero T
		return zero, false
	}
	m := vs[0]
	for _, v := range vs[1:] {
		if v < m {
			m = v
		}
	}
	return m, true
}

// MaxN 返回最大值；空输入返回零值与 false。
func MaxN[T xconstraints.Ordered](vs ...T) (T, bool) {
	if len(vs) == 0 {
		var zero T
		return zero, false
	}
	m := vs[0]
	for _, v := range vs[1:] {
		if v > m {
			m = v
		}
	}
	return m, true
}

// Clamp 把 v 限制在 [lo, hi] 区间内。
//
// 若 lo > hi 会自动交换二者，保证行为可预期（而不是返回意外结果）。
func Clamp[T xconstraints.Ordered](v, lo, hi T) T {
	if lo > hi {
		lo, hi = hi, lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampMin 保证返回值不小于 lo。
func ClampMin[T xconstraints.Ordered](v, lo T) T {
	if v < lo {
		return lo
	}
	return v
}

// ClampMax 保证返回值不大于 hi。
func ClampMax[T xconstraints.Ordered](v, hi T) T {
	if v > hi {
		return hi
	}
	return v
}

// InRange 判断 lo <= v <= hi；lo > hi 时自动交换。
func InRange[T xconstraints.Ordered](v, lo, hi T) bool {
	if lo > hi {
		lo, hi = hi, lo
	}
	return v >= lo && v <= hi
}

// Between 是 InRange 的别名，语义更贴近业务表述。
func Between[T xconstraints.Ordered](v, lo, hi T) bool { return InRange(v, lo, hi) }

// MapRange 把 v 从 [inMin, inMax] 线性映射到 [outMin, outMax]。
//
// inMax == inMin 时返回 outMin（避免除零）。
func MapRange(v, inMin, inMax, outMin, outMax float64) float64 {
	if inMax == inMin {
		return outMin
	}
	return outMin + (v-inMin)*(outMax-outMin)/(inMax-inMin)
}

// ---------------------------------------------------------------------------
// 比较
// ---------------------------------------------------------------------------

// Equal 判断两值相等。
func Equal[T comparable](a, b T) bool { return a == b }

// NotEqual 判断两值不等。
func NotEqual[T comparable](a, b T) bool { return a != b }

// EqualFunc 用自定义比较函数判断两值是否相等（适用于不可比较类型）。
func EqualFunc[T any](a, b T, eq func(a, b T) bool) bool {
	if eq == nil {
		return false
	}
	return eq(a, b)
}

// Compare 返回 -1 / 0 / 1，可直接用于 slices.SortFunc。
func Compare[T xconstraints.Ordered](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// CompareFunc 用 less 函数返回 -1 / 0 / 1；less 为 nil 时返回 0。
func CompareFunc[T any](a, b T, less func(a, b T) bool) int {
	if less == nil {
		return 0
	}
	switch {
	case less(a, b):
		return -1
	case less(b, a):
		return 1
	default:
		return 0
	}
}

// Less 判断 a < b。
func Less[T xconstraints.Ordered](a, b T) bool { return a < b }

// More 判断 a > b。
func More[T xconstraints.Ordered](a, b T) bool { return a > b }

// IsZero 判断 v 是否等于类型零值（仅适用于可比较类型）。
func IsZero[T comparable](v T) bool {
	var zero T
	return v == zero
}

// IsDeepZero 判断 v 是否为深层零值，支持切片 / map / 结构体 / 指针等不可比较类型。
func IsDeepZero[T any](v T) bool {
	return reflect.ValueOf(v).IsZero()
}

// Zero 返回类型 T 的零值。
func Zero[T any]() T {
	var zero T
	return zero
}

// DefaultIfZero 在 v 为零值时返回 def。
func DefaultIfZero[T comparable](v, def T) T {
	if IsZero(v) {
		return def
	}
	return v
}

// ---------------------------------------------------------------------------
// 条件与选择
// ---------------------------------------------------------------------------

// Ternary 是三元表达式的泛型版本。
//
// 注意：两个分支都会被求值（Go 没有惰性求值），
// 需要惰性请使用 If / Unless。
func Ternary[T any](cond bool, ifTrue, ifFalse T) T {
	if cond {
		return ifTrue
	}
	return ifFalse
}

// If 惰性求值：cond 为真时调用 fn，否则调用 elseFn（未提供时返回零值）。
func If[T any](cond bool, fn func() T, elseFn ...func() T) T {
	if cond {
		if fn == nil {
			var zero T
			return zero
		}
		return fn()
	}
	if len(elseFn) > 0 && elseFn[0] != nil {
		return elseFn[0]()
	}
	var zero T
	return zero
}

// Unless 与 If 相反：cond 为假时调用 fn。
func Unless[T any](cond bool, fn func() T, elseFn ...func() T) T {
	return If(!cond, fn, elseFn...)
}

// Coalesce 返回第一个非零值；全部为零值时返回零值。
func Coalesce[T comparable](vals ...T) T {
	for _, v := range vals {
		if !IsZero(v) {
			return v
		}
	}
	var zero T
	return zero
}

// OrDefault 在 v 为零值时返回 def（Coalesce 的双参形式）。
func OrDefault[T comparable](v, def T) T { return DefaultIfZero(v, def) }

// Must 在 err != nil 时 panic，否则返回 v。仅用于初始化与测试场景。
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Try 执行 fn 并返回结果；若 fn panic 则把 panic 值作为 error 返回。
func Try[T any](fn func() T) (v T, err error) {
	if fn == nil {
		return v, nil
	}
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v = zero
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("xtype: recovered from panic: %v", r)
			}
		}
	}()
	return fn(), nil
}

// ---------------------------------------------------------------------------
// 类型断言
// ---------------------------------------------------------------------------

// As 是安全类型断言，等价于 v.(T) 的双返回值形式。
func As[T any](v any) (T, bool) {
	if t, ok := v.(T); ok {
		return t, true
	}
	var zero T
	return zero, false
}

// MustAs 是 As 的 panic 版本，断言失败时 panic。仅用于「类型必然匹配」的场景。
func MustAs[T any](v any) T {
	if t, ok := v.(T); ok {
		return t
	}
	panic(fmt.Sprintf("xtype: cannot assert %s to %s", TypeName(v), typeNameOf[T]()))
}

// Is 判断 v 能否断言为 T。
func Is[T any](v any) bool {
	_, ok := v.(T)
	return ok
}

// TypeName 返回 v 的运行时类型名；v 为 nil 时返回 "<nil>"。
func TypeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).String()
}

// typeNameOf 返回类型参数 T 的名称（依赖 reflect 的泛型支持）。
func typeNameOf[T any]() string {
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

// Switch 按运行时类型分派：命中的 case 优先，否则走 def。
//
// cases / def 均为 nil 时返回 R 的零值。
func Switch[R any](v any, cases map[reflect.Type]func(any) R, def func(any) R) R {
	if v != nil {
		if fn, ok := cases[reflect.TypeOf(v)]; ok && fn != nil {
			return fn(v)
		}
	}
	if def != nil {
		return def(v)
	}
	var zero R
	return zero
}

// Implements 判断 v 的运行时类型是否实现了 iface。
func Implements(v any, iface reflect.Type) bool {
	if v == nil || iface == nil {
		return false
	}
	return reflect.TypeOf(v).Implements(iface)
}
