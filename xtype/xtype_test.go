package xtype_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zhangwenxing/gokit/xtype"
)

// ---------------------------------------------------------------------------
// 取地址 / 解引用
// ---------------------------------------------------------------------------

func TestPtrAndAddr(t *testing.T) {
	p := xtype.Ptr(42)
	if p == nil || *p != 42 {
		t.Fatalf("Ptr = %v", p)
	}
	if q := xtype.Addr("x"); q == nil || *q != "x" {
		t.Fatalf("Addr = %v", q)
	}
	// 返回的是新变量，与原变量解耦
	src := 7
	ps := xtype.Ptr(src)
	*ps = 8
	if src != 7 {
		t.Fatal("Ptr 应返回独立副本的地址")
	}
}

// Ptrs 是最容易写错的地方：必须验证每个指针互不相同且不共享底层变量。
func TestPtrsDistinctPointers(t *testing.T) {
	s := []int{1, 2, 3}
	ps := xtype.Ptrs(s)
	if len(ps) != 3 {
		t.Fatalf("len = %d", len(ps))
	}
	if ps[0] == ps[1] || ps[1] == ps[2] || ps[0] == ps[2] {
		t.Fatal("Ptrs 返回了重复指针（经典循环变量陷阱）")
	}
	*ps[0] = 100
	if s[0] != 1 {
		t.Fatal("Ptrs 不应改写原切片")
	}
	if *ps[1] != 2 || *ps[2] != 3 {
		t.Fatalf("元素值被串改: %v %v", *ps[1], *ps[2])
	}
	if got := xtype.Ptrs[int](nil); got != nil {
		t.Fatalf("Ptrs(nil) = %v", got)
	}
	if got := xtype.Ptrs([]string{}); len(got) != 0 {
		t.Fatalf("Ptrs(empty) = %v", got)
	}
}

func TestDeref(t *testing.T) {
	v := 5
	if got := xtype.Deref(&v); got != 5 {
		t.Fatalf("Deref = %d", got)
	}
	if got := xtype.Deref[int](nil); got != 0 {
		t.Fatalf("Deref(nil) = %d, 应为零值", got)
	}
	if got := xtype.DerefOr[int](nil, 9); got != 9 {
		t.Fatalf("DerefOr(nil) = %d", got)
	}
	if got := xtype.DerefOr(&v, 9); got != 5 {
		t.Fatalf("DerefOr = %d", got)
	}

	// nil 元素被跳过
	a, c := 1, 3
	ds := xtype.Derefs([]*int{&a, nil, &c})
	if len(ds) != 2 || ds[0] != 1 || ds[1] != 3 {
		t.Fatalf("Derefs = %v", ds)
	}
	if got := xtype.Derefs[int](nil); got != nil {
		t.Fatalf("Derefs(nil) = %v", got)
	}
}

func TestSwap(t *testing.T) {
	a, b := 1, 2
	xtype.Swap(&a, &b)
	if a != 2 || b != 1 {
		t.Fatalf("Swap = %d,%d", a, b)
	}
	// nil 安全
	xtype.Swap[int](nil, &a)
	xtype.Swap(&a, nil)
	xtype.Swap[int](nil, nil)
	if a != 2 {
		t.Fatalf("nil swap 不应改变值: %d", a)
	}
}

func TestClonePtr(t *testing.T) {
	v := 3
	cp := xtype.ClonePtr(&v)
	if cp == nil || *cp != 3 {
		t.Fatalf("ClonePtr = %v", cp)
	}
	if cp == &v {
		t.Fatal("ClonePtr 应返回新地址")
	}
	*cp = 9
	if v != 3 {
		t.Fatal("ClonePtr 应解耦原值")
	}
	if xtype.ClonePtr[int](nil) != nil {
		t.Fatal("ClonePtr(nil) 应为 nil")
	}
	if !xtype.IsNilPtr[int](nil) || xtype.IsNilPtr(&v) {
		t.Fatal("IsNilPtr 判断错误")
	}
}

// ---------------------------------------------------------------------------
// 算术
// ---------------------------------------------------------------------------

func TestArithmetic(t *testing.T) {
	if got := xtype.Add(1, 2); got != 3 {
		t.Fatalf("Add = %d", got)
	}
	if got := xtype.Sub(5, 2); got != 3 {
		t.Fatalf("Sub = %d", got)
	}
	if got := xtype.Mul(3, 4); got != 12 {
		t.Fatalf("Mul = %d", got)
	}
	if got := xtype.Div(7, 2); got != 3 {
		t.Fatalf("Div = %d", got)
	}
	if got := xtype.Mod(7, 3); got != 1 {
		t.Fatalf("Mod = %d", got)
	}
	if got := xtype.Neg(5); got != -5 {
		t.Fatalf("Neg = %d", got)
	}
	if got := xtype.Abs(-5); got != 5 {
		t.Fatalf("Abs = %d", got)
	}
	if got := xtype.Abs(5); got != 5 {
		t.Fatalf("Abs = %d", got)
	}
	if got := xtype.Abs(-1.5); got != 1.5 {
		t.Fatalf("Abs float = %v", got)
	}
	if got := xtype.Add(1.5, 2.25); got != 3.75 {
		t.Fatalf("Add float = %v", got)
	}
	if got := xtype.Mul(0.1, 3); got != 0.30000000000000004 {
		t.Fatalf("Mul float = %v", got)
	}

	a := 10
	xtype.Inc(&a, 5)
	if a != 15 {
		t.Fatalf("Inc = %d", a)
	}
	xtype.Inc[int](nil, 5) // nil 安全

	if got := xtype.Sum(1, 2, 3, 4); got != 10 {
		t.Fatalf("Sum = %d", got)
	}
	if got := xtype.Sum[int](); got != 0 {
		t.Fatalf("Sum() = %d", got)
	}
	if got := xtype.Average(1, 2, 3, 4); got != 2.5 {
		t.Fatalf("Average = %v", got)
	}
	if got := xtype.Average[int](); got != 0 {
		t.Fatalf("Average() = %v", got)
	}
}

// 除零绝不能 panic —— 这是最容易在线上炸掉的地方。
func TestDivisionByZeroIsSafe(t *testing.T) {
	if got := xtype.Div(1, 0); got != 0 {
		t.Fatalf("Div(1,0) = %d, want 0", got)
	}
	if got := xtype.Div(0, 0); got != 0 {
		t.Fatalf("Div(0,0) = %d", got)
	}
	if got := xtype.Div(1.5, 0.0); got != 0 {
		t.Fatalf("Div float(1.5,0) = %v, want 0（而非 Inf）", got)
	}
	if got := xtype.Div(-1.5, 0.0); got != 0 {
		t.Fatalf("Div float(-1.5,0) = %v", got)
	}
	if got := xtype.Mod(5, 0); got != 0 {
		t.Fatalf("Mod(5,0) = %d", got)
	}
	if got := xtype.Mod(0, 0); got != 0 {
		t.Fatalf("Mod(0,0) = %d", got)
	}
	// 负数取模保持 Go 语义
	if got := xtype.Mod(-7, 3); got != -1 {
		t.Fatalf("Mod(-7,3) = %d", got)
	}
}

// Abs 对最小整数会溢出（Go 固有限制），保证行为可预期而非静默错误值。
func TestAbsMinIntOverflowDocumented(t *testing.T) {
	// int8 最小值 -128 取绝对值后溢出回绕，仍是 -128
	if got := xtype.Abs[int8](-128); got != -128 {
		t.Fatalf("Abs(int8 min) = %d，期望 -128（Go 溢出语义，若变化请同步更新文档）", got)
	}
	if got := xtype.Abs(int8(-127)); got != 127 {
		t.Fatalf("Abs(int8 -127) = %d", got)
	}
	// 未指定类型参数时，无类型常量默认推导为 int，不会溢出
	if got := xtype.Abs(-128); got != 128 {
		t.Fatalf("Abs(-128) = %d, want 128", got)
	}
	// 浮点最小值同样溢出为 +Inf 取负，这里只验证不 panic
	_ = xtype.Abs(-1e308)
}

// ---------------------------------------------------------------------------
// Min / Max / Clamp
// ---------------------------------------------------------------------------

func TestMinMax(t *testing.T) {
	if got := xtype.Min(3, 9); got != 3 {
		t.Fatalf("Min = %d", got)
	}
	if got := xtype.Max(3, 9); got != 9 {
		t.Fatalf("Max = %d", got)
	}
	if got := xtype.Min("a", "b"); got != "a" {
		t.Fatalf("Min string = %q", got)
	}
	if got := xtype.Max(1.5, 1.25); got != 1.5 {
		t.Fatalf("Max float = %v", got)
	}

	if v, ok := xtype.MinN(5, 2, 9, 1); !ok || v != 1 {
		t.Fatalf("MinN = %d,%v", v, ok)
	}
	if v, ok := xtype.MaxN(5, 2, 9, 1); !ok || v != 9 {
		t.Fatalf("MaxN = %d,%v", v, ok)
	}
	if v, ok := xtype.MinN[int](); ok || v != 0 {
		t.Fatalf("MinN() = %d,%v", v, ok)
	}
	if v, ok := xtype.MaxN[int](); ok || v != 0 {
		t.Fatalf("MaxN() = %d,%v", v, ok)
	}
}

func TestClamp(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 1, 10, 5},
		{0, 1, 10, 1},
		{20, 1, 10, 10},
		{1, 1, 10, 1},
		{10, 1, 10, 10},
		{5, 10, 1, 5},   // lo > hi 自动交换
		{0, 10, 1, 1},   // lo > hi 自动交换后仍生效
		{99, 10, 1, 10}, // lo > hi 自动交换后仍生效
	}
	for _, c := range cases {
		if got := xtype.Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Fatalf("Clamp(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
	if got := xtype.ClampMin(0, 1); got != 1 {
		t.Fatalf("ClampMin = %d", got)
	}
	if got := xtype.ClampMin(5, 1); got != 5 {
		t.Fatalf("ClampMin = %d", got)
	}
	if got := xtype.ClampMax(20, 10); got != 10 {
		t.Fatalf("ClampMax = %d", got)
	}
	if got := xtype.ClampMax(5, 10); got != 5 {
		t.Fatalf("ClampMax = %d", got)
	}
}

func TestInRange(t *testing.T) {
	cases := []struct {
		v, lo, hi int
		want      bool
	}{
		{5, 1, 10, true},
		{1, 1, 10, true},
		{10, 1, 10, true},
		{0, 1, 10, false},
		{11, 1, 10, false},
		{5, 10, 1, true}, // 自动交换
	}
	for _, c := range cases {
		if got := xtype.InRange(c.v, c.lo, c.hi); got != c.want {
			t.Fatalf("InRange(%d,%d,%d) = %v", c.v, c.lo, c.hi, got)
		}
		if got := xtype.Between(c.v, c.lo, c.hi); got != c.want {
			t.Fatalf("Between(%d,%d,%d) = %v", c.v, c.lo, c.hi, got)
		}
	}
}

func TestMapRange(t *testing.T) {
	if got := xtype.MapRange(5, 0, 10, 0, 100); got != 50 {
		t.Fatalf("MapRange = %v", got)
	}
	if got := xtype.MapRange(0, 0, 10, 100, 200); got != 100 {
		t.Fatalf("MapRange = %v", got)
	}
	if got := xtype.MapRange(10, 0, 10, 100, 200); got != 200 {
		t.Fatalf("MapRange = %v", got)
	}
	// 区间退化为点时返回 outMin，避免除零
	if got := xtype.MapRange(3, 7, 7, 42, 99); got != 42 {
		t.Fatalf("MapRange degenerate = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 比较
// ---------------------------------------------------------------------------

func TestComparison(t *testing.T) {
	if !xtype.Equal(1, 1) || xtype.Equal(1, 2) {
		t.Fatal("Equal 判断错误")
	}
	if !xtype.NotEqual(1, 2) || xtype.NotEqual(1, 1) {
		t.Fatal("NotEqual 判断错误")
	}
	if !xtype.Less(1, 2) || xtype.Less(2, 1) {
		t.Fatal("Less 判断错误")
	}
	if !xtype.More(2, 1) || xtype.More(1, 2) {
		t.Fatal("More 判断错误")
	}

	if got := xtype.Compare(1, 2); got != -1 {
		t.Fatalf("Compare = %d", got)
	}
	if got := xtype.Compare(2, 1); got != 1 {
		t.Fatalf("Compare = %d", got)
	}
	if got := xtype.Compare(1, 1); got != 0 {
		t.Fatalf("Compare = %d", got)
	}
	if got := xtype.Compare("a", "b"); got != -1 {
		t.Fatalf("Compare string = %d", got)
	}

	less := func(a, b int) bool { return a < b }
	if got := xtype.CompareFunc(1, 2, less); got != -1 {
		t.Fatalf("CompareFunc = %d", got)
	}
	if got := xtype.CompareFunc(2, 1, less); got != 1 {
		t.Fatalf("CompareFunc = %d", got)
	}
	if got := xtype.CompareFunc(1, 1, less); got != 0 {
		t.Fatalf("CompareFunc = %d", got)
	}
	if got := xtype.CompareFunc(1, 2, nil); got != 0 {
		t.Fatalf("CompareFunc(nil) = %d", got)
	}

	eq := func(a, b []int) bool { return len(a) == len(b) }
	if !xtype.EqualFunc([]int{1}, []int{2}, eq) {
		t.Fatal("EqualFunc 判断错误")
	}
	if xtype.EqualFunc([]int{1}, []int{2, 3}, eq) {
		t.Fatal("EqualFunc 判断错误")
	}
	if xtype.EqualFunc([]int{1}, []int{1}, nil) {
		t.Fatal("EqualFunc(nil) 应为 false")
	}
}

func TestZeroHelpers(t *testing.T) {
	if !xtype.IsZero(0) || !xtype.IsZero("") || xtype.IsZero(1) {
		t.Fatal("IsZero 判断错误")
	}
	if !xtype.IsDeepZero([]int(nil)) || xtype.IsDeepZero([]int{1}) {
		t.Fatal("IsDeepZero slice 判断错误")
	}
	if !xtype.IsDeepZero(map[string]int(nil)) {
		t.Fatal("IsDeepZero map 判断错误")
	}
	type S struct{ A int }
	if !xtype.IsDeepZero(S{}) || xtype.IsDeepZero(S{A: 1}) {
		t.Fatal("IsDeepZero struct 判断错误")
	}
	if xtype.IsDeepZero(&S{}) {
		t.Fatal("非 nil 指针不应为零值")
	}

	if got := xtype.Zero[int](); got != 0 {
		t.Fatalf("Zero = %d", got)
	}
	if got := xtype.Zero[string](); got != "" {
		t.Fatalf("Zero = %q", got)
	}
	if got := xtype.DefaultIfZero(0, 9); got != 9 {
		t.Fatalf("DefaultIfZero = %d", got)
	}
	if got := xtype.DefaultIfZero(1, 9); got != 1 {
		t.Fatalf("DefaultIfZero = %d", got)
	}
}

// ---------------------------------------------------------------------------
// 条件
// ---------------------------------------------------------------------------

func TestTernary(t *testing.T) {
	if got := xtype.Ternary(true, "a", "b"); got != "a" {
		t.Fatalf("Ternary = %q", got)
	}
	if got := xtype.Ternary(false, "a", "b"); got != "b" {
		t.Fatalf("Ternary = %q", got)
	}
}

func TestIfLazyEvaluation(t *testing.T) {
	called := ""
	got := xtype.If(true,
		func() string { called = "then"; return "T" },
		func() string { called = "else"; return "F" },
	)
	if got != "T" || called != "then" {
		t.Fatalf("If = %q, called = %q", got, called)
	}

	called = ""
	got = xtype.If(false,
		func() string { called = "then"; return "T" },
		func() string { called = "else"; return "F" },
	)
	if got != "F" || called != "else" {
		t.Fatalf("If = %q, called = %q", got, called)
	}

	// 未提供 else 分支时返回零值
	if got := xtype.If(true, func() int { return 1 }); got != 1 {
		t.Fatalf("If = %d", got)
	}
	if got := xtype.If(false, func() int { return 1 }); got != 0 {
		t.Fatalf("If = %d", got)
	}
	// nil 分支安全
	if got := xtype.If[string](true, nil); got != "" {
		t.Fatalf("If(true,nil) = %q", got)
	}
	if got := xtype.If[string](false, nil, nil); got != "" {
		t.Fatalf("If(false,nil,nil) = %q", got)
	}
	if got := xtype.Unless(false, func() int { return 7 }); got != 7 {
		t.Fatalf("Unless = %d", got)
	}
	if got := xtype.Unless(true, func() int { return 7 }); got != 0 {
		t.Fatalf("Unless = %d", got)
	}
}

func TestCoalesce(t *testing.T) {
	if got := xtype.Coalesce("", "", "c", "d"); got != "c" {
		t.Fatalf("Coalesce = %q", got)
	}
	if got := xtype.Coalesce(0, 0, 3); got != 3 {
		t.Fatalf("Coalesce = %d", got)
	}
	if got := xtype.Coalesce(""); got != "" {
		t.Fatalf("Coalesce = %q", got)
	}
	if got := xtype.Coalesce[string](); got != "" {
		t.Fatalf("Coalesce() = %q", got)
	}
	if got := xtype.OrDefault("", "def"); got != "def" {
		t.Fatalf("OrDefault = %q", got)
	}
	if got := xtype.OrDefault("v", "def"); got != "v" {
		t.Fatalf("OrDefault = %q", got)
	}
}

func TestMustAndTry(t *testing.T) {
	if got := xtype.Must(7, nil); got != 7 {
		t.Fatalf("Must = %d", got)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Must 应在有错误时 panic")
			}
		}()
		_ = xtype.Must(7, errors.New("boom"))
	}()

	if v, err := xtype.Try(func() int { return 3 }); v != 3 || err != nil {
		t.Fatalf("Try = %d,%v", v, err)
	}
	if v, err := xtype.Try[int](nil); v != 0 || err != nil {
		t.Fatalf("Try(nil) = %d,%v", v, err)
	}
	v, err := xtype.Try(func() int { panic("boom") })
	if v != 0 || err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Try(panic string) = %d,%v", v, err)
	}
	sentinel := errors.New("sentinel")
	if _, err := xtype.Try(func() int { panic(sentinel) }); err != sentinel {
		t.Fatalf("Try(panic error) 应原样返回: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 类型断言
// ---------------------------------------------------------------------------

func TestAsAndIs(t *testing.T) {
	var v any = "hello"
	if got, ok := xtype.As[string](v); !ok || got != "hello" {
		t.Fatalf("As = %q,%v", got, ok)
	}
	if got, ok := xtype.As[int](v); ok || got != 0 {
		t.Fatalf("As 错误断言 = %d,%v", got, ok)
	}
	if !xtype.Is[string](v) || xtype.Is[int](v) {
		t.Fatal("Is 判断错误")
	}
	// nil 接口断言
	if _, ok := xtype.As[string](nil); ok {
		t.Fatal("nil 断言应为 false")
	}

	if got := xtype.MustAs[string](v); got != "hello" {
		t.Fatalf("MustAs = %q", got)
	}
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("MustAs 应在类型不符时 panic")
			}
			if !strings.Contains(mustAsString(r), "int") {
				t.Fatalf("panic 信息应包含目标类型: %v", r)
			}
		}()
		_ = xtype.MustAs[int](v)
	}()
}

// mustAsString 是测试辅助：把 panic 值转成字符串。
func mustAsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmtSprint(v)
}

func fmtSprint(v any) string {
	if v == nil {
		return "<nil>"
	}
	if s, ok := v.(interface{ Error() string }); ok {
		return s.Error()
	}
	return reflect.TypeOf(v).String()
}

func TestTypeName(t *testing.T) {
	if got := xtype.TypeName(1); got != "int" {
		t.Fatalf("TypeName = %q", got)
	}
	if got := xtype.TypeName("s"); got != "string" {
		t.Fatalf("TypeName = %q", got)
	}
	if got := xtype.TypeName(nil); got != "<nil>" {
		t.Fatalf("TypeName(nil) = %q", got)
	}
	type S struct{}
	if got := xtype.TypeName(S{}); got != "xtype_test.S" {
		t.Fatalf("TypeName = %q", got)
	}
}

func TestSwitch(t *testing.T) {
	cases := map[reflect.Type]func(any) string{
		reflect.TypeOf(int(0)):     func(v any) string { return "int" },
		reflect.TypeOf(string("")): func(v any) string { return "string" },
	}
	def := func(v any) string { return "other" }

	if got := xtype.Switch(1, cases, def); got != "int" {
		t.Fatalf("Switch = %q", got)
	}
	if got := xtype.Switch("s", cases, def); got != "string" {
		t.Fatalf("Switch = %q", got)
	}
	if got := xtype.Switch(1.5, cases, def); got != "other" {
		t.Fatalf("Switch = %q", got)
	}
	// nil 值走 default
	if got := xtype.Switch(nil, cases, def); got != "other" {
		t.Fatalf("Switch(nil) = %q", got)
	}
	// 无 default 时返回零值
	if got := xtype.Switch(1.5, cases, nil); got != "" {
		t.Fatalf("Switch(no def) = %q", got)
	}
	// 全 nil 安全
	if got := xtype.Switch[string](1, nil, nil); got != "" {
		t.Fatalf("Switch(nil,nil) = %q", got)
	}
}

func TestImplements(t *testing.T) {
	errType := reflect.TypeOf((*error)(nil)).Elem()
	if !xtype.Implements(errors.New("x"), errType) {
		t.Fatal("errors.New 应实现 error")
	}
	if xtype.Implements("s", errType) {
		t.Fatal("string 不应实现 error")
	}
	if xtype.Implements(nil, errType) {
		t.Fatal("nil 应为 false")
	}
	if xtype.Implements(errors.New("x"), nil) {
		t.Fatal("nil iface 应为 false")
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkPtr(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xtype.Ptr(i)
	}
}

func BenchmarkPtrs(b *testing.B) {
	s := make([]int, 100)
	for i := range s {
		s[i] = i
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xtype.Ptrs(s)
	}
}

func BenchmarkDivZeroCheck(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xtype.Div(i, 3)
	}
}

func BenchmarkClamp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xtype.Clamp(i, 0, 100)
	}
}
