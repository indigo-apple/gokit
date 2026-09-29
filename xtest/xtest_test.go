package xtest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xtest"
)

// ---------------------------------------------------------------------------
// 伪造的 TestingT
//
// 关键设计：TestingT 是「最小接口」，因此可在单测里伪造。
// 真实的 *testing.T 在 Fatalf 后会 runtime.Goexit，无法观测后续代码；
// fakeT 只记录不退出，从而让「断言工具本身」可被测试。
// ---------------------------------------------------------------------------

type fakeT struct {
	name     string
	tmpDir   string
	mu       sync.Mutex
	fatals   []string
	errors   []string
	logs     []string
	skipped  bool
	failed   bool
	cleanups []func()
}

var _ xtest.TestingT = (*fakeT)(nil)

func newFakeT(tmpDir string) *fakeT {
	return &fakeT{name: "fake", tmpDir: tmpDir}
}

func (f *fakeT) Helper() {}

func (f *fakeT) Failed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failed
}

func (f *fakeT) Name() string { return f.name }

func (f *fakeT) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fakeT) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
	f.failed = true
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
	f.failed = true
}

func (f *fakeT) Skip(args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skipped = true
}

func (f *fakeT) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, fn)
}

func (f *fakeT) TempDir() string { return f.tmpDir }

// reset 清空累计的失败记录（供 mustPass/mustFail 消费状态）。
func (f *fakeT) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fatals = nil
	f.errors = nil
	f.failed = false
}

// lastFatal 返回最近一次失败消息（无失败则返回空串）。
func (f *fakeT) lastFatal() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fatals) == 0 {
		return ""
	}
	return f.fatals[len(f.fatals)-1]
}

func (f *fakeT) fatalCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fatals)
}

// 断言辅助：fakeT 必须没记录到失败。
//
// 注意：检查后立即 reset，即每次检查「消费」掉累计的失败记录。
// 否则同一个 fakeT 上「先验证失败、再验证成功」会因状态粘滞而误报。
func mustPass(t *testing.T, f *fakeT, what string) {
	t.Helper()
	defer f.reset()
	if f.failed {
		t.Fatalf("%s: 不该失败，但记录了失败：%s", what, f.lastFatal())
	}
}

// mustFailMsg 与 mustFail 相同，但返回被消费掉的失败消息，便于继续断言消息细节。
func mustFailMsg(t *testing.T, f *fakeT, what string) string {
	t.Helper()
	defer f.reset()
	if !f.failed {
		t.Fatalf("%s: 应该失败但没有记录失败", what)
	}
	return f.lastFatal()
}

// 断言辅助：fakeT 必须记录了失败，且消息包含关键字（检查后 reset）。
func mustFail(t *testing.T, f *fakeT, what, keyword string) {
	t.Helper()
	msg := mustFailMsg(t, f, what)
	if keyword != "" && !strings.Contains(msg, keyword) {
		t.Fatalf("%s: 失败消息 %q 不含关键字 %q", what, msg, keyword)
	}
}

// ---------------------------------------------------------------------------
// 相等性断言
// ---------------------------------------------------------------------------

func TestEqualPassAndFail(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Equal(f, 1, 1)
	xtest.Equal(f, "a", "a")
	xtest.Equal(f, []int{1, 2}, []int{1, 2})
	xtest.Equal(f, map[string]int{"a": 1}, map[string]int{"a": 1})
	mustPass(t, f, "Equal 正确输入")

	xtest.Equal(f, 1, 2)
	mustFail(t, f, "Equal 错误输入", "不相等")

	// 自定义消息应出现在输出里
	f2 := newFakeT(t.TempDir())
	xtest.Equal(f2, "a", "b", "用户ID 校验失败")
	mustFail(t, f2, "Equal 带消息", "用户ID 校验失败")
}

// DeepEqual 语义：nil 切片与空切片不相等（这是文档化的行为，必须锁定）
func TestEqualNilVsEmptySlice(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Equal(f, []int{}, []int(nil))
	mustFail(t, f, "Equal 应区分 nil 与空切片", "不相等")
}

func TestEquivToleratesNilVsEmpty(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Equiv(f, []int{}, []int(nil))
	xtest.Equiv(f, map[string]int{}, map[string]int(nil))
	xtest.Equiv(f, []string{}, []string(nil))
	mustPass(t, f, "Equiv nil vs 空容器")

	// 嵌套结构体里的 nil / 空也要被视为等价
	type inner struct {
		Tags  []string
		Extra map[string]string
	}
	type outer struct {
		Name  string
		Inner inner
		Items []inner
	}
	xtest.Equiv(f,
		outer{Name: "a", Inner: inner{Tags: []string{}, Extra: map[string]string{}}, Items: []inner{}},
		outer{Name: "a", Inner: inner{Tags: nil, Extra: nil}, Items: nil},
	)
	mustPass(t, f, "Equiv 嵌套 nil vs 空")

	// 但真实的差异仍要被抓到
	f2 := newFakeT(t.TempDir())
	xtest.Equiv(f2, []int{1}, []int{2})
	mustFail(t, f2, "Equiv 应抓到真实差异", "不等价")
	xtest.Equiv(f2, []int{1}, []int{1, 2})
	mustFail(t, f2, "Equiv 应抓到长度差异", "不等价")
	xtest.Equiv(f2, outer{Name: "a"}, outer{Name: "b"})
	mustFail(t, f2, "Equiv 应抓到字段差异", "不等价")
	xtest.Equiv(f2, &inner{Tags: []string{"x"}}, &inner{Tags: []string(nil)})
	mustFail(t, f2, "Equiv 指针指向内容不同", "不等价")

	// 指针 / 接口 nil 处理
	f3 := newFakeT(t.TempDir())
	var p1, p2 *inner
	xtest.Equiv(f3, p1, p2)
	var a1, a2 any
	xtest.Equiv(f3, a1, a2)
	mustPass(t, f3, "Equiv 双 nil 指针/接口")
	xtest.Equiv(f3, &inner{}, (*inner)(nil))
	mustFail(t, f3, "Equiv 一 nil 一非 nil", "不等价")
}

func TestNotEqual(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.NotEqual(f, 1, 2)
	mustPass(t, f, "NotEqual 正确输入")
	xtest.NotEqual(f, 1, 1)
	mustFail(t, f, "NotEqual 错误输入", "期望不相等")
}

func TestTrueFalse(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.True(f, 1 == 1)
	xtest.False(f, 1 == 2)
	mustPass(t, f, "True/False 正确输入")

	xtest.True(f, false)
	mustFail(t, f, "True 错误输入", "期望 true")
	xtest.False(f, true)
	mustFail(t, f, "False 错误输入", "期望 false")
}

// ---------------------------------------------------------------------------
// nil / 零值
// ---------------------------------------------------------------------------

func TestNilAndNotNil(t *testing.T) {
	f := newFakeT(t.TempDir())

	var err error
	var ptr *int
	var sl []int
	var mp map[string]int
	var ch chan int
	var fn func()
	var iface any

	// 各种「类型化 nil」都必须被识别
	xtest.Nil(f, nil)
	xtest.Nil(f, err)
	xtest.Nil(f, ptr)
	xtest.Nil(f, sl)
	xtest.Nil(f, mp)
	xtest.Nil(f, ch)
	xtest.Nil(f, fn)
	xtest.Nil(f, iface)
	mustPass(t, f, "Nil 各种类型化 nil")

	// 非 nil 值
	xtest.NotNil(f, 1)
	x := 1
	xtest.NotNil(f, &x)
	xtest.NotNil(f, []int{1})
	xtest.NotNil(f, map[string]int{})
	xtest.NotNil(f, errors.New("e"))
	mustPass(t, f, "NotNil 非 nil 值")

	// 关键回归：把「值为 nil 的指针」装进 any 后，== nil 判断会失效，reflect 判断仍正确
	var p *int
	var boxed any = p
	if boxed == nil {
		t.Fatal("前提错误：接口装 nil 指针后不应等于 nil")
	}
	xtest.Nil(f, boxed)
	mustPass(t, f, "Nil 应识别接口中的 nil 指针")

	// 失败路径
	xtest.Nil(f, 42)
	mustFail(t, f, "Nil 错误输入", "期望 nil")
	xtest.NotNil(f, nil)
	mustFail(t, f, "NotNil 错误输入", "期望非 nil")
}

func TestZero(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Zero(f, 0)
	xtest.Zero(f, "")
	xtest.Zero(f, (*int)(nil))
	xtest.NotZero(f, 1)
	xtest.NotZero(f, "x")
	mustPass(t, f, "Zero/NotZero 正确输入")

	xtest.Zero(f, 5)
	mustFail(t, f, "Zero 错误输入", "期望零值")
	xtest.NotZero(f, 0)
	mustFail(t, f, "NotZero 错误输入", "期望非零值")
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

var errSentinel = errors.New("哨兵错误")

func TestErrorAssertions(t *testing.T) {
	f := newFakeT(t.TempDir())

	xtest.NoError(f, nil)
	xtest.Error(f, errors.New("x"))
	xtest.ErrorIs(f, fmt.Errorf("包装: %w", errSentinel), errSentinel)
	xtest.ErrorContains(f, errors.New("连接超时了"), "超时")
	mustPass(t, f, "错误断言正确输入")

	xtest.NoError(f, errors.New("boom"))
	mustFail(t, f, "NoError 错误输入", "期望无错误")
	xtest.Error(f, nil)
	mustFail(t, f, "Error 错误输入", "期望有错误")
	xtest.ErrorIs(f, errSentinel, errors.New("别的"))
	mustFail(t, f, "ErrorIs 错误输入", "不匹配")
	xtest.ErrorContains(f, nil, "x")
	mustFail(t, f, "ErrorContains err 为 nil", "err 为 nil")
	xtest.ErrorContains(f, errors.New("abc"), "zzz")
	mustFail(t, f, "ErrorContains 未包含", "期望错误包含")
}

func TestPanicsAndNotPanics(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Panics(f, func() { panic("炸了") })
	xtest.NotPanics(f, func() {})
	mustPass(t, f, "Panics/NotPanics 正确输入")

	xtest.Panics(f, func() {})
	mustFail(t, f, "Panics 未 panic", "期望 panic")
	xtest.NotPanics(f, func() { panic("意外") })
	mustFail(t, f, "NotPanics 发生 panic", "意外 panic")
	xtest.Panics(f, nil)
	mustFail(t, f, "Panics(nil)", "fn 为 nil")
	xtest.NotPanics(f, nil)
	mustFail(t, f, "NotPanics(nil)", "fn 为 nil")
}

// ---------------------------------------------------------------------------
// 集合
// ---------------------------------------------------------------------------

func TestCollectionAssertions(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Contains(f, []int{1, 2, 3}, 2)
	xtest.NotContains(f, []int{1, 2, 3}, 9)
	xtest.ElementsMatch(f, []int{3, 1, 2}, []int{1, 2, 3})
	xtest.ElementsMatch(f, []string{"a"}, []string{"a"})
	xtest.Len(f, []int{1, 2}, 2)
	xtest.Len(f, map[string]int{"a": 1}, 1)
	xtest.Len(f, "abcd", 4)
	xtest.Len(f, [3]int{1, 2, 3}, 3)
	xtest.HasKey(f, map[string]int{"a": 1}, "a")
	mustPass(t, f, "集合断言正确输入")

	// 结构体元素
	type item struct{ ID int }
	xtest.Contains(f, []item{{1}, {2}}, item{2})
	xtest.ElementsMatch(f, []item{{2}, {1}}, []item{{1}, {2}})
	mustPass(t, f, "集合断言结构体元素")

	xtest.Contains(f, []int{1}, 9)
	mustFail(t, f, "Contains 未找到", "未找到")
	xtest.NotContains(f, []int{1, 2}, 2)
	mustFail(t, f, "NotContains 意外找到", "意外找到")
	xtest.ElementsMatch(f, []int{1}, []int{1, 2})
	mustFail(t, f, "ElementsMatch 长度不一致", "长度不一致")
	xtest.ElementsMatch(f, []int{1, 2}, []int{1, 3})
	mustFail(t, f, "ElementsMatch 元素缺失", "缺少元素")
	xtest.Len(f, []int{1}, 5)
	mustFail(t, f, "Len 不匹配", "长度 = 1, want 5")
	xtest.HasKey(f, map[string]int{}, "a")
	mustFail(t, f, "HasKey 不存在", "不存在 key")
	xtest.Len(f, 42, 1)
	mustFail(t, f, "Len 不支持的类型", "不支持的类型")
	xtest.Len(f, nil, 1)
	mustFail(t, f, "Len nil 入参", "传入 nil")

	// 重复元素必须按多重集比较
	f2 := newFakeT(t.TempDir())
	xtest.ElementsMatch(f2, []int{1, 1, 2}, []int{1, 2, 2})
	mustFail(t, f2, "ElementsMatch 多重集", "缺少元素")
}

func TestNumericAssertions(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Greater(f, 2, 1)
	xtest.Greater(f, 2.5, 2.4)
	xtest.Greater(f, "b", "a")
	xtest.Less(f, 1, 2)
	xtest.InDelta(f, 1.0, 1.05, 0.1)
	mustPass(t, f, "数值断言正确输入")

	xtest.Greater(f, 1, 2)
	mustFail(t, f, "Greater 错误输入", "期望 >")
	xtest.Less(f, 2, 1)
	mustFail(t, f, "Less 错误输入", "期望 <")
	xtest.InDelta(f, 1.0, 2.0, 0.01)
	mustFail(t, f, "InDelta 超差", "期望")
}

// ---------------------------------------------------------------------------
// 等待型断言
// ---------------------------------------------------------------------------

func TestEventually(t *testing.T) {
	f := newFakeT(t.TempDir())

	// 第 3 次调用时条件成立
	calls := 0
	start := time.Now()
	xtest.Eventually(f, func() bool {
		calls++
		return calls >= 3
	}, time.Second, time.Millisecond)
	mustPass(t, f, "Eventually 最终成立")
	if calls != 3 {
		t.Fatalf("调用次数 = %d, want 3", calls)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Eventually 应在 3ms 左右返回，实际 %v", elapsed)
	}

	// 条件立即成立：只调用一次
	once := 0
	xtest.Eventually(f, func() bool { once++; return true }, time.Second, time.Millisecond)
	if once != 1 {
		t.Fatalf("立即成立时调用次数 = %d, want 1", once)
	}
	mustPass(t, f, "Eventually 立即成立")

	// 超时失败
	f2 := newFakeT(t.TempDir())
	xtest.Eventually(f2, func() bool { return false }, 30*time.Millisecond, time.Millisecond)
	mustFail(t, f2, "Eventually 永假", "未满足")

	// 默认参数（timeout<=0 / interval<=0）
	f3 := newFakeT(t.TempDir())
	xtest.Eventually(f3, func() bool { return true }, 0, 0)
	mustPass(t, f3, "Eventually 默认参数")

	// nil 条件
	xtest.Eventually(f3, nil, time.Second, time.Millisecond)
	mustFail(t, f3, "Eventually(nil)", "cond 为 nil")
}

func TestEventuallyEqual(t *testing.T) {
	f := newFakeT(t.TempDir())
	var val atomic.Int64
	go func() {
		time.Sleep(20 * time.Millisecond)
		val.Store(7)
	}()
	xtest.EventuallyEqual(f, 7, func() int { return int(val.Load()) }, time.Second, time.Millisecond)
	mustPass(t, f, "EventuallyEqual 收敛")

	f2 := newFakeT(t.TempDir())
	xtest.EventuallyEqual(f2, 99, func() int { return 1 }, 30*time.Millisecond, time.Millisecond)
	mustFail(t, f2, "EventuallyEqual 不收敛", "未收敛")

	xtest.EventuallyEqual(f2, 1, nil, time.Second, time.Millisecond)
	mustFail(t, f2, "EventuallyEqual(nil)", "get 为 nil")
}

func TestNever(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.Never(f, func() bool { return false }, 30*time.Millisecond)
	mustPass(t, f, "Never 条件始终为假")

	// 条件立刻为真 → 立即失败
	xtest.Never(f, func() bool { return true }, 30*time.Millisecond)
	mustFail(t, f, "Never 条件为真", "意外为真")

	xtest.Never(f, nil, time.Millisecond)
	mustFail(t, f, "Never(nil)", "cond 为 nil")
}

// ---------------------------------------------------------------------------
// 差分输出
// ---------------------------------------------------------------------------

func TestDiffText(t *testing.T) {
	if got := xtest.DiffText("same", "same"); got != "" {
		t.Fatalf("相同文本应返回空串, got %q", got)
	}

	got := xtest.DiffText("a\nb\nc\n", "a\nX\nc\n")
	if !strings.Contains(got, "首个差异在第 2 行") {
		t.Fatalf("DiffText 缺少行号提示:\n%s", got)
	}
	if !strings.Contains(got, "- 2 | b") || !strings.Contains(got, "+ 2 | X") {
		t.Fatalf("DiffText 缺少 want/got 标注:\n%s", got)
	}

	// 行数不同
	got = xtest.DiffText("a", "a\nb\nc")
	if !strings.Contains(got, "差异区段") {
		t.Fatalf("行数不同的差异应给出说明:\n%s", got)
	}

	// 超长行应被截断，避免刷屏
	long := strings.Repeat("x", 500)
	got = xtest.DiffText(long, long+"y")
	if !strings.Contains(got, "…") {
		t.Fatalf("超长行应被截断:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// golden
// ---------------------------------------------------------------------------

func TestGoldenGenerateAndCompare(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testdata", "out.txt")

	// 1) 文件不存在且非更新模式 → 失败并给出提示
	f := newFakeT(dir)
	xtest.Golden(f, path, []byte("hello"))
	msg := mustFailMsg(t, f, "Golden 文件不存在")
	if !strings.Contains(msg, "golden 文件不存在") {
		t.Fatalf("应说明文件缺失: %s", msg)
	}
	if !strings.Contains(msg, "GOKIT_UPDATE=1") {
		t.Fatalf("应提示生成方式: %s", msg)
	}

	// 2) 更新模式 → 写入文件（含自动创建目录）
	defer xtest.SetUpdate(false)
	xtest.SetUpdate(true)
	f2 := newFakeT(dir)
	xtest.Golden(f2, path, []byte("hello\n"))
	mustPass(t, f2, "Golden 更新模式")
	if len(f2.logs) == 0 || !strings.Contains(f2.logs[0], "已更新") {
		t.Fatalf("更新模式应记录日志: %v", f2.logs)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden 文件失败: %v", err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("golden 内容 = %q", string(content))
	}

	// 3) 非更新模式 + 内容一致 → 通过
	xtest.SetUpdate(false)
	f3 := newFakeT(dir)
	xtest.Golden(f3, path, []byte("hello\n"))
	mustPass(t, f3, "Golden 一致")

	// 4) 非更新模式 + 内容不一致 → 失败并输出差分
	f4 := newFakeT(dir)
	xtest.Golden(f4, path, []byte("world\n"))
	msg4 := mustFailMsg(t, f4, "Golden 不一致")
	if !strings.Contains(msg4, "不一致") || !strings.Contains(msg4, "--- want") {
		t.Fatalf("应输出带标注的差分: %s", msg4)
	}

	// 5) 空 path → 失败
	f5 := newFakeT(dir)
	xtest.Golden(f5, "  ", nil)
	mustFail(t, f5, "Golden 空 path", "path 为空")
}

func TestGoldenBinaryAndJSON(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "bin.dat")
	defer xtest.SetUpdate(false)
	xtest.SetUpdate(true)
	xtest.Golden(newFakeT(dir), binPath, []byte{0, 1, 2, 3})
	xtest.SetUpdate(false)
	// 二进制内容不做行差分，但仍要报不一致
	f := newFakeT(dir)
	xtest.Golden(f, binPath, []byte{9, 9})
	mustFail(t, f, "Golden 二进制不一致", "二进制")

	// GoldenJSON
	jsonPath := filepath.Join(dir, "out.json")
	xtest.SetUpdate(true)
	xtest.GoldenJSON(newFakeT(dir), jsonPath, map[string]any{"b": 2, "a": 1})
	xtest.SetUpdate(false)
	content, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.HasPrefix(string(content), "{\n  \"a\": 1") {
		t.Fatalf("JSON 应缩进 2 空格且按键排序: %q", string(content))
	}
	if !strings.HasSuffix(string(content), "\n") {
		t.Fatalf("JSON 应以换行结尾: %q", string(content))
	}
	// 与自身比较应通过
	xtest.GoldenJSON(newFakeT(dir), jsonPath, map[string]any{"a": 1, "b": 2})

	// 序列化失败 → Fatalf
	f2 := newFakeT(dir)
	xtest.GoldenJSON(f2, filepath.Join(dir, "x.json"), make(chan int))
	mustFail(t, f2, "GoldenJSON 序列化失败", "序列化失败")
}

func TestUpdatingSources(t *testing.T) {
	// 环境变量方式
	t.Setenv("GOKIT_UPDATE", "")
	t.Setenv("XTEST_UPDATE", "")
	xtest.SetUpdate(false)
	defer xtest.SetUpdate(false)
	if xtest.Updating() {
		t.Fatal("默认应为非更新模式")
	}
	t.Setenv("GOKIT_UPDATE", "1")
	if !xtest.Updating() {
		t.Fatal("GOKIT_UPDATE=1 应开启更新模式")
	}
	t.Setenv("GOKIT_UPDATE", "false")
	if xtest.Updating() {
		t.Fatal("GOKIT_UPDATE=false 不应开启")
	}
	t.Setenv("GOKIT_UPDATE", "")
	t.Setenv("XTEST_UPDATE", "yes")
	if !xtest.Updating() {
		t.Fatal("XTEST_UPDATE=yes 应开启更新模式")
	}
	t.Setenv("XTEST_UPDATE", "on")
	if !xtest.Updating() {
		t.Fatal("XTEST_UPDATE=on 应开启更新模式")
	}
	t.Setenv("XTEST_UPDATE", " TRUE ")
	if !xtest.Updating() {
		t.Fatal("应忽略大小写与空白")
	}
	t.Setenv("XTEST_UPDATE", "0")

	// 编程方式
	xtest.SetUpdate(true)
	if !xtest.Updating() {
		t.Fatal("SetUpdate(true) 应开启更新模式")
	}
	xtest.SetUpdate(false)

	// DeclareUpdateFlag 幂等（重复调用不得 panic）
	xtest.DeclareUpdateFlag()
	xtest.DeclareUpdateFlag()
}

func TestEqualJSON(t *testing.T) {
	f := newFakeT(t.TempDir())

	// 键顺序、空白、转义差异都不应影响结果
	xtest.EqualJSON(f, `{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`)
	xtest.EqualJSON(f, []byte(`{"a":1}`), map[string]int{"a": 1})
	xtest.EqualJSON(f, map[string]any{"a": 1}, `{"a":1}`)
	mustPass(t, f, "EqualJSON 语义相等")

	xtest.EqualJSON(f, `{"a":1}`, `{"a":2}`)
	mustFail(t, f, "EqualJSON 值不同", "不相等")
	xtest.EqualJSON(f, `{bad json`, `{"a":1}`)
	mustFail(t, f, "EqualJSON want 非法", "want 不是合法 JSON")
	xtest.EqualJSON(f, `{"a":1}`, `{bad`)
	mustFail(t, f, "EqualJSON got 非法", "got 不是合法 JSON")

	// JSONEqual（纯布尔判断）
	if !xtest.JSONEqual(`{"a":1}`, `{"a":1}`) {
		t.Fatal("JSONEqual 应为 true")
	}
	if xtest.JSONEqual(`{"a":1}`, `{"a":2}`) {
		t.Fatal("JSONEqual 应为 false")
	}
	if xtest.JSONEqual(`bad`, `{}`) {
		t.Fatal("非法 JSON 应返回 false")
	}
	if !xtest.JSONEqual(nil, nil) {
		t.Fatal("双 nil 应等价")
	}
}

func TestJSONHelpers(t *testing.T) {
	f := newFakeT(t.TempDir())
	if got := xtest.MustJSON(f, map[string]int{"a": 1}); got != `{"a":1}` {
		t.Fatalf("MustJSON = %q", got)
	}
	mustPass(t, f, "MustJSON 正常")

	xtest.MustJSON(f, make(chan int))
	mustFail(t, f, "MustJSON 序列化失败", "序列化失败")

	if got := xtest.JSONString(map[string]int{"a": 1}); got != `{"a":1}` {
		t.Fatalf("JSONString = %q", got)
	}
	if got := xtest.JSONString(make(chan int)); !strings.Contains(got, "序列化失败") {
		t.Fatalf("JSONString 失败时应返回描述, got %q", got)
	}
	if got := xtest.JSONString(nil); got != "null" {
		t.Fatalf("JSONString(nil) = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 假数据
// ---------------------------------------------------------------------------

func TestRandStringFamilies(t *testing.T) {
	for n := 0; n <= 8; n++ {
		if got := xtest.RandString(n); len(got) != n {
			t.Fatalf("RandString(%d) 长度 = %d", n, len(got))
		}
		if got := xtest.RandDigits(n); len(got) != n {
			t.Fatalf("RandDigits(%d) 长度 = %d", n, len(got))
		}
		if got := xtest.RandLetters(n); len(got) != n {
			t.Fatalf("RandLetters(%d) 长度 = %d", n, len(got))
		}
		if got := xtest.RandHexStr(n); len(got) != n {
			t.Fatalf("RandHexStr(%d) 长度 = %d", n, len(got))
		}
	}
	if got := xtest.RandString(-5); got != "" {
		t.Fatalf("RandString(-5) = %q", got)
	}
	if got := xtest.RandDigits(6); strings.ContainsFunc(got, func(r rune) bool { return r < '0' || r > '9' }) {
		t.Fatalf("RandDigits 产生非数字: %q", got)
	}
}

// RandInt 曾是最容易溢出/死循环的地方：这里覆盖全量边界。
func TestRandIntEdges(t *testing.T) {
	// 常量区间
	if got := xtest.RandInt(5, 5); got != 5 {
		t.Fatalf("RandInt(5,5) = %d", got)
	}
	if got := xtest.RandInt(-3, -3); got != -3 {
		t.Fatalf("RandInt(-3,-3) = %d", got)
	}
	// 自动交换
	for i := 0; i < 50; i++ {
		if got := xtest.RandInt(10, 1); got < 1 || got > 10 {
			t.Fatalf("RandInt(10,1) 越界: %d", got)
		}
	}
	// 小范围必须能取到两个端点
	seenMin, seenMax := false, false
	for i := 0; i < 500; i++ {
		switch v := xtest.RandInt(1, 3); v {
		case 1:
			seenMin = true
		case 3:
			seenMax = true
		case 2:
		default:
			t.Fatalf("RandInt(1,3) 越界: %d", v)
		}
	}
	if !seenMin || !seenMax {
		t.Fatalf("闭区间端点未取到: min=%v max=%v", seenMin, seenMax)
	}

	// 极值：不得 panic、不得溢出
	const minI, maxI = -1 << 63, 1<<63 - 1
	for i := 0; i < 2000; i++ {
		v := xtest.RandInt(minI, maxI)
		if v < minI || v > maxI {
			t.Fatalf("极值区间越界: %d", v)
		}
	}
	// 跨度恰好为 1 的相邻极值
	if v := xtest.RandInt(maxI-1, maxI); v != maxI-1 && v != maxI {
		t.Fatalf("相邻极值: %d", v)
	}
	if v := xtest.RandInt(minI, minI+1); v != minI && v != minI+1 {
		t.Fatalf("相邻极值: %d", v)
	}
	// 跨零
	if v := xtest.RandInt(-1, 1); v < -1 || v > 1 {
		t.Fatalf("跨零越界: %d", v)
	}
}

func TestRandIntN(t *testing.T) {
	if got := xtest.RandIntN(0); got != 0 {
		t.Fatalf("RandIntN(0) = %d", got)
	}
	if got := xtest.RandIntN(-3); got != 0 {
		t.Fatalf("RandIntN(-3) = %d", got)
	}
	for i := 0; i < 200; i++ {
		if v := xtest.RandIntN(5); v < 0 || v >= 5 {
			t.Fatalf("RandIntN(5) 越界: %d", v)
		}
	}
}

func TestRandFloatAndBool(t *testing.T) {
	if got := xtest.RandFloat(1, 1); got != 1 {
		t.Fatalf("RandFloat(1,1) = %v", got)
	}
	for i := 0; i < 200; i++ {
		if v := xtest.RandFloat(0, 10); v < 0 || v >= 10 {
			t.Fatalf("RandFloat 越界: %v", v)
		}
		if v := xtest.RandFloat(10, 0); v < 0 || v >= 10 {
			t.Fatalf("RandFloat 交换后越界: %v", v)
		}
	}
	sawTrue, sawFalse := false, false
	for i := 0; i < 100; i++ {
		if xtest.RandBool() {
			sawTrue = true
		} else {
			sawFalse = true
		}
	}
	if !sawTrue || !sawFalse {
		t.Fatal("RandBool 未产生两种值")
	}
}

func TestRandBusinessShapes(t *testing.T) {
	// UUID v4 形态
	for i := 0; i < 50; i++ {
		u := xtest.RandUUID()
		if len(u) != 36 {
			t.Fatalf("UUID 长度 = %d: %q", len(u), u)
		}
		if u[14] != '4' {
			t.Fatalf("UUID 版本位应为 4: %q", u)
		}
		if c := u[19]; c != '8' && c != '9' && c != 'a' && c != 'b' {
			t.Fatalf("UUID 变体位非法: %q", u)
		}
		if strings.Count(u, "-") != 4 {
			t.Fatalf("UUID 分隔符数量错误: %q", u)
		}
	}

	// 手机号：11 位，1[3-9] 开头，全数字
	for i := 0; i < 50; i++ {
		p := xtest.RandPhone()
		if len(p) != 11 {
			t.Fatalf("手机号长度 = %d: %q", len(p), p)
		}
		if p[0] != '1' || p[1] < '3' || p[1] > '9' {
			t.Fatalf("手机号号段非法: %q", p)
		}
		if u := xtest.RandDigits(11); len(u) != 11 {
			t.Fatalf("数字串长度 = %d", len(u))
		}
	}

	// 邮箱
	for i := 0; i < 30; i++ {
		e := xtest.RandEmail()
		if strings.Count(e, "@") != 1 {
			t.Fatalf("邮箱格式错误: %q", e)
		}
		parts := strings.Split(e, "@")
		if parts[0] == "" || !strings.Contains(parts[1], ".") {
			t.Fatalf("邮箱格式错误: %q", e)
		}
	}

	// 中文姓名（UTF-8，2~3 个字符）
	for i := 0; i < 30; i++ {
		n := xtest.RandName()
		rc := []rune(n)
		if len(rc) < 2 || len(rc) > 3 {
			t.Fatalf("姓名长度异常: %q (%d 字)", n, len(rc))
		}
	}

	// IPv4：四段，范围合法
	for i := 0; i < 50; i++ {
		ip := xtest.RandIPv4()
		segs := strings.Split(ip, ".")
		if len(segs) != 4 {
			t.Fatalf("IPv4 段数错误: %q", ip)
		}
		for _, s := range segs {
			v := 0
			for _, c := range s {
				if c < '0' || c > '9' {
					t.Fatalf("IPv4 含非数字: %q", ip)
				}
				v = v*10 + int(c-'0')
			}
			if v > 255 {
				t.Fatalf("IPv4 段越界: %q", ip)
			}
		}
	}
}

func TestRandTimeAndBytes(t *testing.T) {
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 200; i++ {
		got := xtest.RandTime(from, to)
		if got.Before(from) || !got.Before(to) {
			t.Fatalf("RandTime 越界: %v", got)
		}
	}
	// to <= from → 返回 from
	if got := xtest.RandTime(from, from); !got.Equal(from) {
		t.Fatalf("RandTime(from,from) = %v", got)
	}
	if got := xtest.RandTime(to, from); !got.Equal(to) {
		t.Fatalf("RandTime 逆序应返回第一个参数, got %v", got)
	}
	// 极小区间（1ns）
	if got := xtest.RandTime(from, from.Add(time.Nanosecond)); !got.Equal(from) {
		t.Fatalf("1ns 区间 = %v", got)
	}

	if b := xtest.RandBytes(16); len(b) != 16 {
		t.Fatalf("RandBytes(16) 长度 = %d", len(b))
	}
	if b := xtest.RandBytes(0); b != nil {
		t.Fatalf("RandBytes(0) 应为 nil, got %v", b)
	}
}

func TestRandSlicePickShuffleMap(t *testing.T) {
	sl := xtest.RandSlice(5, func() int { return 7 })
	if len(sl) != 5 {
		t.Fatalf("RandSlice 长度 = %d", len(sl))
	}
	for _, v := range sl {
		if v != 7 {
			t.Fatalf("RandSlice 元素 = %d", v)
		}
	}
	if xtest.RandSlice(0, func() int { return 1 }) != nil {
		t.Fatal("RandSlice(0) 应为 nil")
	}
	if xtest.RandSlice[int](-1, nil) != nil {
		t.Fatal("RandSlice(-1, nil) 应为 nil")
	}
	if xtest.RandSlice[int](3, nil) != nil {
		t.Fatal("gen 为 nil 时应返回 nil")
	}

	f := newFakeT(t.TempDir())
	if got := xtest.Pick(f, 42); got != 42 {
		t.Fatalf("Pick 单元素 = %d", got)
	}
	mustPass(t, f, "Pick 正常")
	xtest.Pick[int](f)
	mustFail(t, f, "Pick 空列表", "候选列表为空")

	// Shuffle 不修改入参且保持元素集合
	src := []int{1, 2, 3, 4, 5}
	out := xtest.Shuffle(src)
	if len(out) != len(src) {
		t.Fatalf("Shuffle 长度 = %d", len(out))
	}
	for i, v := range src {
		if v != i+1 {
			t.Fatalf("Shuffle 修改了入参: %v", src)
		}
	}
	sorted := map[int]bool{}
	for _, v := range out {
		sorted[v] = true
	}
	if len(sorted) != 5 {
		t.Fatalf("Shuffle 元素丢失: %v", out)
	}
	if xtest.Shuffle([]int(nil)) != nil {
		t.Fatal("Shuffle(nil) 应为 nil")
	}

	m := xtest.RandMap(3, func() string { return xtest.RandUUID() }, func() int { return 1 })
	if len(m) != 3 {
		t.Fatalf("RandMap 长度 = %d", len(m))
	}
	if xtest.RandMap(0, func() int { return 1 }, func() int { return 1 }) != nil {
		t.Fatal("RandMap(0) 应为 nil")
	}
	if xtest.RandMap(3, func() int { return 1 }, func() int { return 1 }) == nil {
		t.Fatal("重复键时也应返回非 nil map")
	}
}

// ---------------------------------------------------------------------------
// Fill
// ---------------------------------------------------------------------------

type fillSub struct {
	Code string
	Flag bool
}

type fillTarget struct {
	Str     string
	Int     int
	Int8    int8
	Uint32  uint32
	Float64 float64
	Cplx    complex128
	Bool    bool
	Dur     time.Duration
	When    time.Time
	Ptr     *fillSub
	Slice   []int
	Array   [2]int
	Map     map[string]*fillSub
	Iface   any
	Chan    chan int
	Fn      func()
	Sub     fillSub
	hidden  string
}

func TestFill(t *testing.T) {
	f := newFakeT(t.TempDir())
	var target fillTarget
	xtest.Fill(f, &target)
	mustPass(t, f, "Fill 正常")

	if target.Str == "" || !strings.HasPrefix(target.Str, "s") {
		t.Fatalf("Str = %q", target.Str)
	}
	if target.Int == 0 || target.Int8 == 0 || target.Uint32 == 0 {
		t.Fatalf("整数未填充: %d %d %d", target.Int, target.Int8, target.Uint32)
	}
	if target.Float64 == 0 {
		t.Fatalf("Float64 = %v", target.Float64)
	}
	if target.Cplx == 0 {
		t.Fatalf("Cplx = %v", target.Cplx)
	}
	if !target.Bool {
		t.Fatal("Bool 未填充")
	}
	if target.Dur == 0 {
		t.Fatal("Duration 未填充")
	}
	if !target.When.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("When = %v", target.When)
	}
	if target.Ptr == nil || target.Ptr.Code == "" {
		t.Fatal("指针字段未分配/未填充")
	}
	if len(target.Slice) != 1 || target.Slice[0] == 0 {
		t.Fatalf("Slice = %v", target.Slice)
	}
	for _, v := range target.Array {
		if v == 0 {
			t.Fatalf("Array 未填充: %v", target.Array)
		}
	}
	if len(target.Map) != 1 {
		t.Fatalf("Map 长度 = %d", len(target.Map))
	}
	for _, v := range target.Map {
		if v == nil || v.Code == "" {
			t.Fatal("Map 值未填充")
		}
	}
	if target.Iface == nil {
		t.Fatal("Iface 未填充")
	}
	if _, ok := target.Iface.(string); !ok {
		t.Fatalf("Iface 应为 string, got %T", target.Iface)
	}
	if target.Chan != nil || target.Fn != nil {
		t.Fatal("chan/func 应保持零值")
	}
	if target.Sub.Code == "" || !target.Sub.Flag {
		t.Fatalf("嵌套结构体未填充: %+v", target.Sub)
	}
	if target.hidden != "" {
		t.Fatalf("未导出字段不应被填充: %q", target.hidden)
	}

	// 确定性：两次填充结果相同
	var a, b fillTarget
	xtest.Fill(f, &a)
	xtest.Fill(f, &b)
	xtest.Equal(f, a, b)
	mustPass(t, f, "Fill 确定性")
}

func TestFillEdgeCases(t *testing.T) {
	f := newFakeT(t.TempDir())

	// nil 指针
	xtest.Fill[int](f, nil)
	mustFail(t, f, "Fill(nil)", "nil 指针")

	// 非指针
	v := 0
	xtest.Fill(f, &v) // 正确用法
	mustPass(t, f, "Fill 标量指针")
	if v == 0 {
		t.Fatalf("标量未填充: %d", v)
	}
	xtest.Fill(f, new(int)) // *int 是合法指针
	mustPass(t, f, "Fill(new(int))")

	// 自引用结构体：不得无限递归
	type node struct {
		Next *node
		Val  int
	}
	var n node
	xtest.Fill(f, &n)
	mustPass(t, f, "Fill 自引用结构体")
	depth, cur := 0, &n
	for cur.Next != nil && depth < 20 {
		depth++
		cur = cur.Next
	}
	if depth == 0 {
		t.Fatal("自引用结构的 Next 应被分配")
	}
	if depth >= 20 {
		t.Fatalf("自引用结构深度失控: %d", depth)
	}

	// 空 map / 无键可构造的 map
	var noKeyMap map[fillSub]int
	xtest.Fill(f, &noKeyMap)
	mustPass(t, f, "Fill 结构体作键的 map")
	if noKeyMap == nil {
		t.Fatal("map 应被初始化（即使没有可构造的键）")
	}

	// 非空接口无法构造 → 保持 nil，且不 panic
	var errField struct{ Err error }
	xtest.Fill(f, &errField)
	mustPass(t, f, "Fill 非空接口字段")

	// FillValue 直接作用于 reflect.Value
	rv := xtest.FillValue(reflect.New(reflect.TypeOf("")).Elem())
	if rv.String() == "" {
		t.Fatal("FillValue 未填充")
	}
	// 不可设置的 Value 应被安全忽略
	xtest.FillValue(reflect.ValueOf("只读"))
	xtest.FillValue(reflect.Value{})
}

func TestFillNestedMapAndInterface(t *testing.T) {
	f := newFakeT(t.TempDir())
	var target struct {
		M1 map[int]string
		M2 map[bool]int
		M3 map[float64]int
		M4 map[any]int
		M5 map[uint8]string
		M6 map[*fillSub]int
	}
	xtest.Fill(f, &target)
	mustPass(t, f, "Fill 各种键类型")
	for name, n := range map[string]int{
		"M1": len(target.M1), "M2": len(target.M2), "M3": len(target.M3),
		"M4": len(target.M4), "M5": len(target.M5), "M6": len(target.M6),
	} {
		if n != 1 {
			t.Fatalf("%s 长度 = %d, want 1", name, n)
		}
	}
}

// ---------------------------------------------------------------------------
// goroutine 泄漏
// ---------------------------------------------------------------------------

func TestNoGoroutineLeakClean(t *testing.T) {
	f := newFakeT(t.TempDir())
	xtest.NoGoroutineLeak(f, func() {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() { defer wg.Done() }()
		}
		wg.Wait()
	})
	mustPass(t, f, "NoGoroutineLeak 正常退出")

	// 容差模式
	xtest.NoGoroutineLeakN(f, 5, func() {})
	mustPass(t, f, "NoGoroutineLeakN 容差")

	xtest.NoGoroutineLeak(f, nil)
	mustFail(t, f, "NoGoroutineLeak(nil)", "fn 为 nil")
}

func TestNoGoroutineLeakDetects(t *testing.T) {
	xtest.SkipShort(t)
	f := newFakeT(t.TempDir())
	release := make(chan struct{})
	xtest.NoGoroutineLeak(f, func() {
		go func() { <-release }() // 泄漏：直到测试结束才释放
	})
	close(release)
	mustFail(t, f, "NoGoroutineLeak 应检测到泄漏", "goroutine 泄漏")
}

// ---------------------------------------------------------------------------
// 其它
// ---------------------------------------------------------------------------

func TestTempDirAndSkipShort(t *testing.T) {
	dir := xtest.TempDir(t)
	if dir == "" {
		t.Fatal("TempDir 返回空")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("TempDir 目录不存在: %v", err)
	}
	// 非 -short 模式下不应跳过
	xtest.SkipShort(t)
}

func TestFormatVariants(t *testing.T) {
	// 通过失败消息间接验证 format（format 未导出）
	f := newFakeT(t.TempDir())
	xtest.Equal(f, 1, 2)
	if !strings.Contains(f.lastFatal(), "want: 1") {
		t.Fatalf("整数格式化错误: %s", f.lastFatal())
	}

	f2 := newFakeT(t.TempDir())
	xtest.Equal(f2, "a", "b")
	if !strings.Contains(f2.lastFatal(), `want: "a"`) {
		t.Fatalf("字符串应带引号: %s", f2.lastFatal())
	}

	f3 := newFakeT(t.TempDir())
	xtest.NoError(f3, errSentinel)
	if !strings.Contains(f3.lastFatal(), "哨兵错误") {
		t.Fatalf("错误应输出文案: %s", f3.lastFatal())
	}

	f4 := newFakeT(t.TempDir())
	var p *int
	xtest.NotNil(f4, p)
	if !strings.Contains(f4.lastFatal(), "nil") {
		t.Fatalf("nil 指针应被描述为 nil: %s", f4.lastFatal())
	}

	f5 := newFakeT(t.TempDir())
	xtest.Equal(f5, []byte{1, 2}, []byte{3})
	if !strings.Contains(f5.lastFatal(), `"\x01\x02"`) {
		t.Fatalf("字节切片应转义: %s", f5.lastFatal())
	}

	// 超长值应被截断
	f6 := newFakeT(t.TempDir())
	xtest.Equal(f6, strings.Repeat("x", 1000), "y")
	if !strings.Contains(f6.lastFatal(), "已截断") {
		t.Fatalf("超长值应被截断: %s", f6.lastFatal())
	}

	// 多种类型走默认分支
	f7 := newFakeT(t.TempDir())
	xtest.Equal(f7, struct{ A int }{1}, struct{ A int }{2})
	if !strings.Contains(f7.lastFatal(), "A:1") {
		t.Fatalf("结构体应带字段名: %s", f7.lastFatal())
	}
	xtest.Equal(f7, map[string]int{"a": 1}, map[string]int{"a": 2})

	// 前面故意制造了 2 次失败；下面验证「nil 与 nil 相等」不产生新失败
	f7.reset()
	xtest.Equal(f7, (*int)(nil), (*int)(nil))
	if f7.fatalCount() != 0 {
		t.Fatalf("nil 相等不应失败, 失败次数 = %d", f7.fatalCount())
	}
}

func TestContextInterop(t *testing.T) {
	// 断言工具应能直接用于 context 相关测试
	f := newFakeT(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	xtest.ErrorIs(f, ctx.Err(), context.Canceled)
	mustPass(t, f, "context 错误断言")
}

// ---------------------------------------------------------------------------
// 真实场景：用 xtest 测试被测代码
// ---------------------------------------------------------------------------

// 被测代码：一个简单的并发安全计数器
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) Inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestRealisticUsage(t *testing.T) {
	xtest.SkipShort(t)

	var c counter
	xtest.Equal(t, 0, c.Value())

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Inc() }()
	}
	wg.Wait()
	xtest.Equal(t, 100, c.Value())

	// Eventually：等异步收敛而不是裸 sleep
	go func() { time.Sleep(10 * time.Millisecond); c.Inc() }()
	xtest.EventuallyEqual(t, 101, c.Value, time.Second, time.Millisecond)

	// 集合断言
	xtest.Contains(t, []int{1, 2, 3}, 3)
	xtest.Len(t, []int{1, 2, 3}, 3)
	xtest.NoError(t, nil)
	xtest.NotNil(t, &c)
	xtest.InDelta(t, 1.0/3, 0.333, 0.001)

	// Equiv 处理 nil vs 空
	var got []string
	xtest.Equiv(t, []string{}, got, "反序列化空数组与 nil 应等价")
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkEqual(b *testing.B) {
	want := []int{1, 2, 3, 4, 5}
	got := []int{1, 2, 3, 4, 5}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !reflect.DeepEqual(want, got) {
			b.Fatal("不等")
		}
	}
}

func BenchmarkEquiv(b *testing.B) {
	type s struct {
		A []int
		B map[string]int
	}
	want := s{A: []int{1}, B: map[string]int{"k": 1}}
	got := s{A: []int{1}, B: map[string]int{"k": 1}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		xtest.Equiv(b, want, got)
	}
}

func BenchmarkDiffText(b *testing.B) {
	a := strings.Repeat("line\n", 100)
	c := strings.Repeat("line\n", 50) + "other\n" + strings.Repeat("line\n", 49)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xtest.DiffText(a, c)
	}
}

func BenchmarkFill(b *testing.B) {
	type target struct {
		A string
		B int
		C []int
		D map[string]int
		E time.Time
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var v target
		xtest.FillValue(reflect.ValueOf(&v).Elem())
	}
}
