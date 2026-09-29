// Package xtest 提供零依赖的测试辅助工具：断言、golden 文件、假数据生成、结构体填充、
// goroutine 泄漏检测。
//
// 设计原则：
//
//  1. **零第三方依赖**：不引入 testify 等库，断言全部基于标准库；
//  2. **断言即致命（fatal）**：失败立刻调用 Fatalf 终止当前测试，避免错误级联。
//     需要「软断言」时请自行用 if + t.Error；
//  3. **接口最小化**：所有断言接受本包的 TestingT 接口，*testing.T 与 *testing.B 均满足，
//     因此断言工具自身也能被单测覆盖；
//  4. **nil / 空值安全**：所有工具对 nil、空输入都不会 panic。
//
// 与 xerr 的关系：本包**不**依赖任何 gokit 包之外的东西（仅依赖 xstr / xnum 的纯函数），
// 因此可以被任何包的测试引用而不产生循环依赖。
package xtest

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/indigo-apple/gokit/xconstraints"
	"github.com/indigo-apple/gokit/xnum"
)

// ---------------------------------------------------------------------------
// 测试接口
// ---------------------------------------------------------------------------

// TestingT 是断言所需的最小测试接口。
//
// *testing.T 与 *testing.B 都满足它；同时它也足够小，可以在单测中被伪造，
// 从而让断言工具本身可被测试（见 xtest_test.go 中的 fakeT）。
type TestingT interface {
	Helper()
	Failed() bool
	Name() string
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skip(args ...any)
	Cleanup(f func())
	TempDir() string
}

// 编译期断言：*testing.T 必须满足 TestingT。
var _ TestingT = (*testing.T)(nil)

// 编译期断言：*testing.B 必须满足 TestingT。
var _ TestingT = (*testing.B)(nil)

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// suffix 把可选的自定义消息拼成 "：xxx"。
func suffix(msg []any) string {
	if len(msg) == 0 {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprintln(msg...))
	if s == "" {
		return ""
	}
	return "：" + s
}

// format 把值格式化为易读、可诊断的字符串。
//
// 所有分支（含字符串 / 字节切片 / error）统一走截断，避免超长值刷屏。
func format(v any) string {
	if v == nil {
		return "<nil>"
	}
	switch x := v.(type) {
	case string:
		return clipLong(strconv.Quote(x))
	case []byte:
		return clipLong(strconv.Quote(string(x)))
	case error:
		return clipLong("err(" + x.Error() + ")")
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			return "<nil(" + rv.Type().String() + ")>"
		}
	}
	return clipLong(strings.TrimSpace(fmt.Sprintf("%+v", v)))
}

// clipLong 截断过长的单值输出。
func clipLong(s string) string {
	const maxLen = 400
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…(已截断)"
}

// clip 截断过长的单行文本（用于差分输出）。
func clip(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// DiffText 返回两段文本的差异摘要：首个差异区段 + 前后各 3 行上下文。
//
// 这是一个「够用即可」的差分（不做 LCS 最优匹配），目标是让 golden 测试失败时
// 一眼看出差异位置，而不引入第三方依赖。
func DiffText(want, got string) string {
	if want == got {
		return ""
	}
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")

	// 公共前缀 / 后缀
	pre := 0
	for pre < len(w) && pre < len(g) && w[pre] == g[pre] {
		pre++
	}
	suf := 0
	for suf < len(w)-pre && suf < len(g)-pre && w[len(w)-1-suf] == g[len(g)-1-suf] {
		suf++
	}

	const ctx = 3
	const maxLines = 12

	// 行号列宽按最大行号动态计算，短文件不会出现大片空白
	width := len(strconv.Itoa(len(w)))
	if n := len(strconv.Itoa(len(g))); n > width {
		width = n
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  --- want(%d 行) / +++ got(%d 行)，首个差异在第 %d 行\n", len(w), len(g), pre+1)

	line := func(marker string, i int, s string) {
		fmt.Fprintf(&b, "%s %*d | %s\n", marker, width, i+1, clip(s))
	}

	start := pre - ctx
	if start < 0 {
		start = 0
	}
	for i := start; i < pre; i++ {
		line("  ", i, w[i])
	}
	for i := pre; i < len(w)-suf && i < pre+maxLines; i++ {
		line(" -", i, w[i])
	}
	for i := pre; i < len(g)-suf && i < pre+maxLines; i++ {
		line(" +", i, g[i])
	}
	for i := len(w) - suf; i < len(w) && i < len(w)-suf+ctx; i++ {
		line("  ", i, w[i])
	}
	// want/got 行数不同时说明尾部被截断
	if nw, ng := len(w)-pre-suf, len(g)-pre-suf; nw != ng {
		fmt.Fprintf(&b, "  （差异区段：want %d 行 vs got %d 行，仅展示前 %d 行）\n", nw, ng, maxLines)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 相等性断言
// ---------------------------------------------------------------------------

// Equal 断言两值深度相等（reflect.DeepEqual）。
//
// ⚠️ 注意 DeepEqual 的语义：**nil 切片与空切片不相等**，nil map 与空 map 不相等。
// 若希望二者视为等价，请使用 Equiv。
func Equal[V any](t TestingT, want, got V, msg ...any) {
	t.Helper()
	if reflect.DeepEqual(want, got) {
		return
	}
	t.Fatalf("xtest.Equal 不相等\n  want: %s\n  got:  %s%s", format(want), format(got), suffix(msg))
}

// NotEqual 断言两值不深度相等。
func NotEqual[V any](t TestingT, want, got V, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		return
	}
	t.Fatalf("xtest.NotEqual 期望不相等，实际都等于 %s%s", format(got), suffix(msg))
}

// Equiv 断言两值等价，但比 Equal 宽容：**nil 与空容器视为等价**。
//
// 具体规则：
//   - nil 切片 / 长度为 0 的切片：等价；
//   - nil map / 长度为 0 的 map：等价；
//   - 指针 / 接口：递归比较指向的值，both nil 视为等价；
//   - 结构体：逐字段递归（含未导出字段时退化为 DeepEqual）；
//   - 其它类型：DeepEqual。
//
// 适用于「JSON 反序列化后 nil vs 空数组」这类常见噪声。
func Equiv[V any](t TestingT, want, got V, msg ...any) {
	t.Helper()
	if equivLenient(reflect.ValueOf(want), reflect.ValueOf(got), 0) {
		return
	}
	t.Fatalf("xtest.Equiv 不等价\n  want: %s\n  got:  %s%s", format(want), format(got), suffix(msg))
}

// equivLenient 递归比较，把 nil 与空容器视为等价。
func equivLenient(a, b reflect.Value, depth int) bool {
	if depth > 8 {
		return safeDeepEqual(a, b)
	}
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Slice:
		if a.Len() == 0 && b.Len() == 0 {
			return true // nil 与空切片等价
		}
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !equivLenient(a.Index(i), b.Index(i), depth+1) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.Len() == 0 && b.Len() == 0 {
			return true
		}
		if a.Len() != b.Len() {
			return false
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() || !equivLenient(a.MapIndex(k), bv, depth+1) {
				return false
			}
		}
		return true
	case reflect.Ptr, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equivLenient(a.Elem(), b.Elem(), depth+1)
	case reflect.String:
		return a.String() == b.String()
	case reflect.Struct:
		typ := a.Type()
		for i := 0; i < a.NumField(); i++ {
			if !typ.Field(i).IsExported() {
				// 含未导出字段：整体交给 DeepEqual，避免 panic 与语义漂移
				return safeDeepEqual(a, b)
			}
			if !equivLenient(a.Field(i), b.Field(i), depth+1) {
				return false
			}
		}
		return true
	default:
		return safeDeepEqual(a, b)
	}
}

// safeDeepEqual 在值不可反射导出时保守地返回 true（防 panic 的兜底）。
func safeDeepEqual(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if !a.CanInterface() || !b.CanInterface() {
		return true
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// True 断言条件为真。
func True(t TestingT, cond bool, msg ...any) {
	t.Helper()
	if cond {
		return
	}
	t.Fatalf("xtest.True 期望 true，实际 false%s", suffix(msg))
}

// False 断言条件为假。
func False(t TestingT, cond bool, msg ...any) {
	t.Helper()
	if !cond {
		return
	}
	t.Fatalf("xtest.False 期望 false，实际 true%s", suffix(msg))
}

// ---------------------------------------------------------------------------
// nil / 零值断言
// ---------------------------------------------------------------------------

// isNilLike 判断值是否为 nil（支持 interface、指针、slice、map、chan、func）。
func isNilLike(v any) (bool, bool) {
	if v == nil {
		return true, true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil(), true
	default:
		return false, false
	}
}

// Nil 断言值为 nil（兼容 nil 指针 / 切片 / map / channel / 函数 / error）。
//
// 判定用 reflect 而非 == nil，因此「值为 nil 的 *T 装进 any」也能被识别。
func Nil(t TestingT, v any, msg ...any) {
	t.Helper()
	got, ok := isNilLike(v)
	if ok && got {
		return
	}
	t.Fatalf("xtest.Nil 期望 nil，实际 %s%s", format(v), suffix(msg))
}

// NotNil 断言值不为 nil。
func NotNil(t TestingT, v any, msg ...any) {
	t.Helper()
	got, ok := isNilLike(v)
	if !ok || !got {
		return
	}
	t.Fatalf("xtest.NotNil 期望非 nil，实际 nil%s", suffix(msg))
}

// Zero 断言值为其类型的零值。
func Zero[V any](t TestingT, v V, msg ...any) {
	t.Helper()
	var zero V
	if reflect.DeepEqual(v, zero) {
		return
	}
	t.Fatalf("xtest.Zero 期望零值，实际 %s%s", format(v), suffix(msg))
}

// NotZero 断言值不是其类型的零值。
func NotZero[V any](t TestingT, v V, msg ...any) {
	t.Helper()
	var zero V
	if !reflect.DeepEqual(v, zero) {
		return
	}
	t.Fatalf("xtest.NotZero 期望非零值，实际是零值%s", suffix(msg))
}

// ---------------------------------------------------------------------------
// 错误断言
// ---------------------------------------------------------------------------

// NoError 断言 err 为 nil。
func NoError(t TestingT, err error, msg ...any) {
	t.Helper()
	if err == nil {
		return
	}
	t.Fatalf("xtest.NoError 期望无错误，实际 %v%s", err, suffix(msg))
}

// Error 断言 err 非 nil。
func Error(t TestingT, err error, msg ...any) {
	t.Helper()
	if err != nil {
		return
	}
	t.Fatalf("xtest.Error 期望有错误，实际为 nil%s", suffix(msg))
}

// ErrorIs 断言 errors.Is(err, target) 成立。
func ErrorIs(t TestingT, err, target error, msg ...any) {
	t.Helper()
	if errors.Is(err, target) {
		return
	}
	t.Fatalf("xtest.ErrorIs 不匹配\n  err:    %v\n  target: %v%s", err, target, suffix(msg))
}

// ErrorContains 断言 err 的文案包含 substr。
func ErrorContains(t TestingT, err error, substr string, msg ...any) {
	t.Helper()
	if err == nil {
		t.Fatalf("xtest.ErrorContains 期望错误包含 %q，实际 err 为 nil%s", substr, suffix(msg))
		return
	}
	if strings.Contains(err.Error(), substr) {
		return
	}
	t.Fatalf("xtest.ErrorContains 期望错误包含 %q，实际 %q%s", substr, err.Error(), suffix(msg))
}

// Panics 断言 fn 发生 panic。
func Panics(t TestingT, fn func(), msg ...any) {
	t.Helper()
	if fn == nil {
		t.Fatalf("xtest.Panics: fn 为 nil%s", suffix(msg))
		return
	}
	defer func() {
		if recover() == nil {
			t.Fatalf("xtest.Panics 期望 panic，实际正常返回%s", suffix(msg))
		}
	}()
	fn()
}

// NotPanics 断言 fn 不发生 panic。
func NotPanics(t TestingT, fn func(), msg ...any) {
	t.Helper()
	if fn == nil {
		t.Fatalf("xtest.NotPanics: fn 为 nil%s", suffix(msg))
		return
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("xtest.NotPanics 发生意外 panic: %v%s", r, suffix(msg))
		}
	}()
	fn()
}

// ---------------------------------------------------------------------------
// 集合断言
// ---------------------------------------------------------------------------

// Contains 断言 list 中包含 want（深度相等比较）。
func Contains[V any](t TestingT, list []V, want V, msg ...any) {
	t.Helper()
	for i := range list {
		if reflect.DeepEqual(list[i], want) {
			return
		}
	}
	t.Fatalf("xtest.Contains 未找到 %s\n  集合(%d 个): %s%s", format(want), len(list), format(list), suffix(msg))
}

// NotContains 断言 list 中不含 want。
func NotContains[V any](t TestingT, list []V, want V, msg ...any) {
	t.Helper()
	for i := range list {
		if reflect.DeepEqual(list[i], want) {
			t.Fatalf("xtest.NotContains 意外找到 %s（下标 %d）%s", format(want), i, suffix(msg))
			return
		}
	}
}

// ElementsMatch 断言两个集合元素相同（**与顺序无关**，按多重集比较）。
func ElementsMatch[V any](t TestingT, want, got []V, msg ...any) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("xtest.ElementsMatch 长度不一致: want=%d got=%d\n  want: %s\n  got:  %s%s",
			len(want), len(got), format(want), format(got), suffix(msg))
		return
	}
	used := make([]bool, len(got))
	for _, w := range want {
		found := false
		for j := range got {
			if !used[j] && reflect.DeepEqual(got[j], w) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("xtest.ElementsMatch 缺少元素 %s\n  want: %s\n  got:  %s%s",
				format(w), format(want), format(got), suffix(msg))
			return
		}
	}
}

// Len 断言长度（支持切片 / 数组 / map / 字符串 / channel）。
func Len(t TestingT, v any, want int, msg ...any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		t.Fatalf("xtest.Len: 传入 nil%s", suffix(msg))
		return
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Chan, reflect.String:
		if got := rv.Len(); got != want {
			t.Fatalf("xtest.Len 长度 = %d, want %d%s", got, want, suffix(msg))
		}
	default:
		t.Fatalf("xtest.Len: 不支持的类型 %T（仅支持切片/数组/map/字符串/channel）%s", v, suffix(msg))
	}
}

// HasKey 断言 map 含有 key。
func HasKey[K comparable, V any](t TestingT, m map[K]V, key K, msg ...any) {
	t.Helper()
	if _, ok := m[key]; ok {
		return
	}
	t.Fatalf("xtest.HasKey: map 中不存在 key %s（共 %d 项）%s", format(key), len(m), suffix(msg))
}

// ---------------------------------------------------------------------------
// 数值断言
// ---------------------------------------------------------------------------

// Greater 断言 got > threshold。
func Greater[V xconstraints.Ordered](t TestingT, got, threshold V, msg ...any) {
	t.Helper()
	if got > threshold {
		return
	}
	t.Fatalf("xtest.Greater 期望 > %s，实际 %s%s", format(threshold), format(got), suffix(msg))
}

// Less 断言 got < threshold。
func Less[V xconstraints.Ordered](t TestingT, got, threshold V, msg ...any) {
	t.Helper()
	if got < threshold {
		return
	}
	t.Fatalf("xtest.Less 期望 < %s，实际 %s%s", format(threshold), format(got), suffix(msg))
}

// InDelta 断言 |got - want| <= delta（浮点比较的推荐方式）。
func InDelta(t TestingT, want, got, delta float64, msg ...any) {
	t.Helper()
	if xnum.InDelta(want, got, delta) {
		return
	}
	t.Fatalf("xtest.InDelta 期望 %.10g ± %.10g，实际 %.10g（偏差 %.10g）%s",
		want, delta, got, absDiff(want, got), suffix(msg))
}

func absDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// ---------------------------------------------------------------------------
// 等待型断言
// ---------------------------------------------------------------------------

// Eventually 轮询 cond，直到为真或超时（默认超时 2s、间隔 10ms）。
//
// 用于测试异步行为：避免用裸 time.Sleep 造成偶发失败。
func Eventually(t TestingT, cond func() bool, timeout, interval time.Duration, msg ...any) {
	t.Helper()
	if cond == nil {
		t.Fatalf("xtest.Eventually: cond 为 nil%s", suffix(msg))
		return
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	start := time.Now()
	for {
		if cond() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("xtest.Eventually 条件在 %v 内未满足%s", time.Since(start).Round(time.Millisecond), suffix(msg))
			return
		}
		time.Sleep(interval)
	}
}

// EventuallyEqual 轮询 get，直到其返回值等于 want 或超时。
//
// 适合「等待异步写入最终一致」的场景。
func EventuallyEqual[V any](t TestingT, want V, get func() V, timeout, interval time.Duration, msg ...any) {
	t.Helper()
	if get == nil {
		t.Fatalf("xtest.EventuallyEqual: get 为 nil%s", suffix(msg))
		return
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	start := time.Now()
	var last V
	for {
		last = get()
		if reflect.DeepEqual(last, want) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("xtest.EventuallyEqual 未收敛（耗时 %v）\n  want: %s\n  got:  %s%s",
				time.Since(start).Round(time.Millisecond), format(want), format(last), suffix(msg))
			return
		}
		time.Sleep(interval)
	}
}

// Never 断言在 d 时间段内 cond 始终为假（默认 100ms）。
func Never(t TestingT, cond func() bool, d time.Duration, msg ...any) {
	t.Helper()
	if cond == nil {
		t.Fatalf("xtest.Never: cond 为 nil%s", suffix(msg))
		return
	}
	if d <= 0 {
		d = 100 * time.Millisecond
	}
	const interval = 5 * time.Millisecond
	deadline := time.Now().Add(d)
	for {
		if cond() {
			t.Fatalf("xtest.Never 条件意外为真%s", suffix(msg))
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(interval)
	}
}

// ---------------------------------------------------------------------------
// 其它工具
// ---------------------------------------------------------------------------

// TempDir 返回测试独占的临时目录（基于 t.TempDir()，测试结束自动清理）。
func TempDir(t TestingT) string {
	t.Helper()
	return t.TempDir()
}

// SkipShort 在 `go test -short` 模式下跳过当前测试。
//
// 用于耗时较长（>1s）或依赖外部环境的测试。
func SkipShort(t TestingT) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short 模式跳过耗时测试")
	}
}

// NoGoroutineLeak 断言 fn 执行完毕后没有残留 goroutine（容差 0）。
//
// 用法：把「启动 + 关闭」整段逻辑放进 fn，让它在内部完成优雅关闭。
//
// ⚠️ 注意事项：
//   - 不要与 t.Parallel() 同时使用：并行的其它测试会干扰 goroutine 计数；
//   - 若被测代码内部使用了 runtime 的后台 goroutine（如 net/http 的连接池），
//     请改用 NoGoroutineLeakN 给出合理容差；
//   - 检测会最多等待 2s 让 goroutine 自然退出。
func NoGoroutineLeak(t TestingT, fn func()) {
	NoGoroutineLeakN(t, 0, fn)
}

// NoGoroutineLeakN 与 NoGoroutineLeak 相同，但允许 tolerance 个额外 goroutine。
func NoGoroutineLeakN(t TestingT, tolerance int, fn func()) {
	t.Helper()
	if fn == nil {
		t.Fatalf("xtest.NoGoroutineLeakN: fn 为 nil")
		return
	}
	base := runtime.NumGoroutine()
	fn()

	deadline := time.Now().Add(2 * time.Second)
	var now int
	for {
		runtime.GC()
		now = runtime.NumGoroutine()
		if now <= base+tolerance {
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	stack := string(buf[:n])
	if len(stack) > 4000 {
		stack = stack[:4000] + "\n…(栈已截断)"
	}
	t.Fatalf("xtest: 检测到 goroutine 泄漏: 执行前=%d 执行后=%d（容差 %d）\n%s", base, now, tolerance, stack)
}
