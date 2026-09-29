package xpipe_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xpipe"
)

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// assertNoLeak 断言 fn 执行后 goroutine 数回到基线（本包最硬的不变量之一）。
func assertNoLeak(t *testing.T, fn func()) {
	t.Helper()
	runtime.GC()
	base := runtime.NumGoroutine()
	fn()

	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= base {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine 泄漏: base=%d now=%d", base, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustCollect[T any](t *testing.T, p *xpipe.Pipe[T]) []T {
	t.Helper()
	out, err := xpipe.Collect(p)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return out
}

func eq[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedInts(vs []int) []int {
	out := make([]int, len(vs))
	copy(out, vs)
	sort.Ints(out)
	return out
}

// ---------------------------------------------------------------------------
// 数据源
// ---------------------------------------------------------------------------

func TestSources(t *testing.T) {
	// FromSlice / Just / Empty
	if got := mustCollect(t, xpipe.FromSlice([]int{1, 2, 3})); !eq(got, []int{1, 2, 3}) {
		t.Fatalf("FromSlice = %v", got)
	}
	if got := mustCollect(t, xpipe.Just("a", "b")); !eq(got, []string{"a", "b"}) {
		t.Fatalf("Just = %v", got)
	}
	if got := mustCollect(t, xpipe.Empty[int]()); len(got) != 0 {
		t.Fatalf("Empty = %v", got)
	}
	if got := mustCollect(t, xpipe.FromSlice([]int(nil))); len(got) != 0 {
		t.Fatalf("nil 切片 = %v", got)
	}

	// Generate / Range（半开区间）
	if got := mustCollect(t, xpipe.Generate(3, func(i int) int { return i * i })); !eq(got, []int{0, 1, 4}) {
		t.Fatalf("Generate = %v", got)
	}
	if got := mustCollect(t, xpipe.Range(2, 6)); !eq(got, []int{2, 3, 4, 5}) {
		t.Fatalf("Range = %v", got)
	}
	if got := mustCollect(t, xpipe.Range(5, 5)); len(got) != 0 {
		t.Fatalf("空 Range = %v", got)
	}
	if got := mustCollect(t, xpipe.Range(5, 2)); len(got) != 0 {
		t.Fatalf("反向 Range = %v", got)
	}
	if got := mustCollect(t, xpipe.Generate(-1, func(i int) int { return i })); len(got) != 0 {
		t.Fatalf("负 n = %v", got)
	}
	// nil fn 不应 panic
	if got := mustCollect(t, xpipe.Generate[int](2, nil)); !eq(got, []int{0, 0}) {
		t.Fatalf("nil fn Generate = %v", got)
	}
}

func TestFromChannel(t *testing.T) {
	ch := make(chan int, 3)
	for i := 1; i <= 3; i++ {
		ch <- i
	}
	close(ch)
	if got := mustCollect(t, xpipe.FromChannel(ch)); !eq(got, []int{1, 2, 3}) {
		t.Fatalf("FromChannel = %v", got)
	}

	// 永不关闭的 channel + First：必须立即返回且不泄漏（FromChannelCtx 同时监听取消）
	t.Run("不关闭的流用First提前收工", func(t *testing.T) {
		assertNoLeak(t, func() {
			// ⚠️ 必须先放入元素再拉取：无缓冲 channel 上的「先发后收」在本测试里会死锁
			// （这是测试自身的时序问题，不是实现问题 —— 首版就是这么卡住的）。
			inf := make(chan int, 1)
			inf <- 42 // 之后不再写入、也不 close
			p := xpipe.FromChannelCtx(context.Background(), inf)

			v, ok, err := xpipe.First(p)
			if err != nil || !ok || v != 42 {
				t.Fatalf("First = %v,%v,%v", v, ok, err)
			}
		})
	})
}

func TestFromFunc(t *testing.T) {
	// 推送式源：emit 直到被 ctx 打断
	p := xpipe.FromFunc(func(ctx context.Context, emit func(int) error) error {
		for i := 0; i < 5; i++ {
			if err := emit(i); err != nil {
				return err
			}
		}
		return nil
	})
	if got := mustCollect(t, p); !eq(got, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("FromFunc = %v", got)
	}

	// 上游返回错误：必须上报
	p2 := xpipe.FromFunc(func(ctx context.Context, emit func(int) error) error {
		_ = emit(1)
		return errors.New("上游炸了")
	})
	if _, err := xpipe.Collect(p2); err == nil || !strings.Contains(err.Error(), "上游炸了") {
		t.Fatalf("FromFunc 错误未上报: %v", err)
	}

	// 下游只要 1 个元素：emit 必须在 ctx 取消后返回错误而退出（不泄漏）
	t.Run("下游提前收工不泄漏", func(t *testing.T) {
		var emitted atomic.Int64
		assertNoLeak(t, func() {
			// 用带 buffer 的版本，让生产者能跑在前面
			p := xpipe.FromFunc(func(ctx context.Context, emit func(int) error) error {
				for i := 0; ; i++ {
					if err := emit(i); err != nil {
						return err // 必须把 ctx 错误返回，goroutine 才会退出
					}
					emitted.Add(1)
				}
			}, xpipe.WithBufferSize(4))
			v, ok, err := xpipe.First(p)
			if err != nil || !ok || v != 0 {
				t.Fatalf("First = %v,%v,%v", v, ok, err)
			}
		})
	})

	// 生产者 panic：转成错误而不是崩进程
	p3 := xpipe.FromFunc(func(ctx context.Context, emit func(int) error) error { panic("源 panic") })
	if _, err := xpipe.Collect(p3); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("FromFunc panic 未转换: %v", err)
	}
}

func TestFromReaderAndLines(t *testing.T) {
	// FromLines：逐行、不含换行符、末尾无换行也要取到
	p := xpipe.FromLines(strings.NewReader("a\nbb\nccc"))
	if got := mustCollect(t, p); !eq(got, []string{"a", "bb", "ccc"}) {
		t.Fatalf("FromLines = %v", got)
	}
	// 空输入
	if got := mustCollect(t, xpipe.FromLines(strings.NewReader(""))); len(got) != 0 {
		t.Fatalf("空输入 = %v", got)
	}
	// 空行保留
	if got := mustCollect(t, xpipe.FromLines(strings.NewReader("a\n\nb"))); !eq(got, []string{"a", "", "b"}) {
		t.Fatalf("空行 = %v", got)
	}

	// FromReader：分块
	chunks := mustCollect(t, xpipe.FromReader(strings.NewReader("hello world"), 4))
	var sb strings.Builder
	for _, c := range chunks {
		sb.Write(c)
	}
	if sb.String() != "hello world" {
		t.Fatalf("FromReader = %q（块数 %d）", sb.String(), len(chunks))
	}
	if len(chunks) != 3 { // 4+4+3
		t.Fatalf("分块数 = %d, want 3", len(chunks))
	}
}

// ---------------------------------------------------------------------------
// 变换
// ---------------------------------------------------------------------------

func TestMapFilterChain(t *testing.T) {
	got := mustCollect(t, xpipe.Filter(
		xpipe.Map(xpipe.Range(1, 6), func(i int) int { return i * 2 }),
		func(i int) bool { return i > 4 },
	))
	if !eq(got, []int{6, 8, 10}) {
		t.Fatalf("Map+Filter = %v", got)
	}

	// 类型变换 + 链式
	ss := mustCollect(t, xpipe.Map(xpipe.Range(0, 3), func(i int) string { return fmt.Sprintf("#%d", i) }))
	if !eq(ss, []string{"#0", "#1", "#2"}) {
		t.Fatalf("Map[int]string = %v", ss)
	}

	// Reject 是 Filter 的补集
	got = mustCollect(t, xpipe.Reject(xpipe.Range(0, 5), func(i int) bool { return i%2 == 0 }))
	if !eq(got, []int{1, 3}) {
		t.Fatalf("Reject = %v", got)
	}

	// FilterCtx
	got = mustCollect(t, xpipe.FilterCtx(xpipe.Range(0, 5), func(_ context.Context, i int) (bool, error) {
		return i%2 == 1, nil
	}))
	if !eq(got, []int{1, 3}) {
		t.Fatalf("FilterCtx = %v", got)
	}
}

func TestFlatMapDistinctScanPeek(t *testing.T) {
	// FlatMap：展开 + 空切片跳过
	got := mustCollect(t, xpipe.FlatMap(xpipe.FromSlice([]int{1, 0, 2}), func(i int) []int {
		out := make([]int, 0, i)
		for j := 0; j < i; j++ {
			out = append(out, i)
		}
		return out
	}))
	if !eq(got, []int{1, 2, 2}) {
		t.Fatalf("FlatMap = %v", got)
	}

	// Distinct（保序）
	got = mustCollect(t, xpipe.Distinct(xpipe.FromSlice([]int{3, 1, 3, 2, 1})))
	if !eq(got, []int{3, 1, 2}) {
		t.Fatalf("Distinct = %v", got)
	}
	// DistinctBy
	words := mustCollect(t, xpipe.DistinctBy(
		xpipe.FromSlice([]string{"apple", "avocado", "banana", "blueberry"}),
		func(s string) byte { return s[0] },
	))
	if !eq(words, []string{"apple", "banana"}) {
		t.Fatalf("DistinctBy = %v", words)
	}

	// Scan：为每个元素产出累积值（不产出初始值）
	sums := mustCollect(t, xpipe.Scan(xpipe.Range(1, 5), 0, func(acc, v int) int { return acc + v }))
	if !eq(sums, []int{1, 3, 6, 10}) {
		t.Fatalf("Scan = %v", sums)
	}

	// Peek：旁路观察，不影响数据
	var seen []int
	out := mustCollect(t, xpipe.Peek(xpipe.Range(0, 3), func(i int) { seen = append(seen, i) }))
	if !eq(out, []int{0, 1, 2}) || !eq(seen, []int{0, 1, 2}) {
		t.Fatalf("Peek = %v / %v", out, seen)
	}
	// Peek 的 panic 不得吃掉数据
	out = mustCollect(t, xpipe.Peek(xpipe.Range(0, 3), func(int) { panic("peek boom") }))
	if !eq(out, []int{0, 1, 2}) {
		t.Fatalf("Peek panic 影响了数据: %v", out)
	}
	// Peek 的 panic 应被记录
	p := xpipe.Peek(xpipe.Range(0, 3), func(int) { panic("peek boom") })
	_, _ = xpipe.Collect(p)
	if err := p.Err(); err == nil {
		t.Fatal("Peek 的 panic 应记入 Err()")
	}
}

// ---------------------------------------------------------------------------
// 批处理 / 窗口 / 限速
// ---------------------------------------------------------------------------

func TestBatch(t *testing.T) {
	got := mustCollect(t, xpipe.Batch(xpipe.Range(0, 5), 2))
	if len(got) != 3 || !eq(got[0], []int{0, 1}) || !eq(got[1], []int{2, 3}) || !eq(got[2], []int{4}) {
		t.Fatalf("Batch = %v", got)
	}
	// 空流不产出空批
	if got := mustCollect(t, xpipe.Batch(xpipe.Empty[int](), 3)); len(got) != 0 {
		t.Fatalf("空流 Batch = %v", got)
	}
	// size<=0 视为 1
	got = mustCollect(t, xpipe.Batch(xpipe.Range(0, 3), 0))
	if len(got) != 3 {
		t.Fatalf("size=0 Batch = %v", got)
	}
	// size 整除
	got = mustCollect(t, xpipe.Batch(xpipe.Range(0, 4), 2))
	if len(got) != 2 {
		t.Fatalf("整除 Batch = %v", got)
	}
	// 超时切批（元素能立刻取到时生效）
	got = mustCollect(t, xpipe.Batch(xpipe.Range(0, 5), 10, xpipe.WithBatchTimeout(time.Nanosecond)))
	if len(got) < 2 {
		t.Fatalf("超时切批未生效: %v", got)
	}
	total := 0
	for _, b := range got {
		total += len(b)
	}
	if total != 5 {
		t.Fatalf("超时切批丢元素: %v", got)
	}
}

func TestWindow(t *testing.T) {
	got := mustCollect(t, xpipe.Window(xpipe.Range(1, 6), 3))
	want := [][]int{{1, 2, 3}, {2, 3, 4}, {3, 4, 5}}
	if len(got) != len(want) {
		t.Fatalf("Window = %v", got)
	}
	for i := range want {
		if !eq(got[i], want[i]) {
			t.Fatalf("Window[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// 步长 2（不重叠）
	got = mustCollect(t, xpipe.Window(xpipe.Range(1, 7), 2, 2))
	if len(got) != 3 || !eq(got[0], []int{1, 2}) || !eq(got[1], []int{3, 4}) || !eq(got[2], []int{5, 6}) {
		t.Fatalf("Window(2,2) = %v", got)
	}

	// 数据不足一个窗口 → 无输出
	if got := mustCollect(t, xpipe.Window(xpipe.Range(0, 2), 3)); len(got) != 0 {
		t.Fatalf("数据不足 = %v", got)
	}
	// 刚好一个窗口
	got = mustCollect(t, xpipe.Window(xpipe.Range(0, 3), 3))
	if len(got) != 1 || !eq(got[0], []int{0, 1, 2}) {
		t.Fatalf("刚好一窗 = %v", got)
	}
	// 步长大于窗口：必须真正跳过中间元素（而不是把它们拼进下一窗）
	got = mustCollect(t, xpipe.Window(xpipe.Range(1, 9), 2, 3))
	want23 := [][]int{{1, 2}, {4, 5}, {7, 8}}
	if len(got) != len(want23) {
		t.Fatalf("Window(2,3) = %v", got)
	}
	for i := range want23 {
		if !eq(got[i], want23[i]) {
			t.Fatalf("Window(2,3)[%d] = %v, want %v", i, got[i], want23[i])
		}
	}
	// 空流
	if got := mustCollect(t, xpipe.Window(xpipe.Empty[int](), 2)); len(got) != 0 {
		t.Fatalf("空流 = %v", got)
	}
}

func TestThrottle(t *testing.T) {
	start := time.Now()
	got := mustCollect(t, xpipe.Throttle(xpipe.Range(0, 3), 30*time.Millisecond))
	elapsed := time.Since(start)
	if !eq(got, []int{0, 1, 2}) {
		t.Fatalf("Throttle = %v", got)
	}
	// 3 个元素 → 至少 2 个间隔
	if elapsed < 55*time.Millisecond {
		t.Fatalf("限速未生效: %v", elapsed)
	}
	// interval <= 0 直接透传
	p := xpipe.Range(0, 3)
	if xpipe.Throttle(p, 0) != p {
		t.Fatal("Throttle(0) 应原样返回")
	}
}

// ---------------------------------------------------------------------------
// 并发阶段
// ---------------------------------------------------------------------------

func TestParallelUnordered(t *testing.T) {
	var inFlight, maxInFlight int64
	const n = 40

	got, err := xpipe.Collect(xpipe.Parallel(xpipe.Range(0, n), 8, func(i int) (int, error) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt64(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(time.Duration(n-i) * 200 * time.Microsecond) // 制造乱序完成
		atomic.AddInt64(&inFlight, -1)
		return i * 10, nil
	}))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != n {
		t.Fatalf("元素数 = %d, want %d", len(got), n)
	}
	if !eq(sortedInts(got), func() []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i * 10
		}
		return out
	}()) {
		t.Fatalf("元素集合错误: %v", got)
	}
	if atomic.LoadInt64(&maxInFlight) < 2 {
		t.Fatalf("未观察到并行: maxInFlight = %d", maxInFlight)
	}
	if atomic.LoadInt64(&inFlight) != 0 {
		t.Fatalf("并行未收敛: inFlight = %d", inFlight)
	}
}

func TestParallelOrdered(t *testing.T) {
	const n = 60
	got, err := xpipe.Collect(xpipe.ParallelOrdered(xpipe.Range(0, n), 8, func(i int) (int, error) {
		// 越靠后的元素越快完成 → 若不重排必然乱序
		time.Sleep(time.Duration(n-i) * 150 * time.Microsecond)
		return i, nil
	}))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != n {
		t.Fatalf("元素数 = %d", len(got))
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("保序失败: 第 %d 位是 %d", i, v)
		}
	}

	// 元素级错误：默认 FailFast 下应上报
	src := xpipe.ParallelOrdered(xpipe.Range(0, 6), 4, func(i int) (int, error) {
		if i == 2 {
			return 0, errors.New("坏元素")
		}
		return i, nil
	})
	// FailFast（默认）：应返回错误
	if _, err := xpipe.Collect(src); err == nil || !strings.Contains(err.Error(), "坏元素") {
		t.Fatalf("FailFast 未上报错误: %v", err)
	}
}

func TestParallelErrorsAndDefaultWorkers(t *testing.T) {
	// 默认并行度来自配置（WithWorkers）
	p := xpipe.Range(0, 10, xpipe.WithWorkers(3))
	got, err := xpipe.Collect(xpipe.Parallel(p, 0, func(i int) (int, error) { return i, nil }))
	if err != nil || len(got) != 10 {
		t.Fatalf("默认并行度: %v / %v", got, err)
	}
	// 负数并行度归一化为 1
	got, err = xpipe.Collect(xpipe.Parallel(xpipe.Range(0, 3), -5, func(i int) (int, error) { return i, nil }))
	if err != nil || len(got) != 3 {
		t.Fatalf("负并行度: %v / %v", got, err)
	}
	// 上游错误优先上报
	up := xpipe.MapErr(xpipe.Range(0, 10), func(i int) (int, error) {
		if i == 3 {
			return 0, errors.New("上游错")
		}
		return i, nil
	})
	if _, err := xpipe.Collect(xpipe.Parallel(up, 4, func(i int) (int, error) { return i, nil })); err == nil {
		t.Fatal("上游错误应上报")
	}
	// Skip 策略：并行阶段的错误被跳过，其余元素正常产出
	pSkip := xpipe.Range(0, 6, xpipe.WithErrorPolicy(xpipe.PolicySkip))
	got, err = xpipe.Collect(xpipe.Parallel(pSkip, 3, func(i int) (int, error) {
		if i%2 == 0 {
			return 0, errors.New("偶数失败")
		}
		return i, nil
	}))
	if err != nil {
		t.Fatalf("Skip 不应返回错误: %v", err)
	}
	if !eq(sortedInts(got), []int{1, 3, 5}) {
		t.Fatalf("Skip 结果 = %v", got)
	}
}

func TestBufferPrefetch(t *testing.T) {
	// 预取：顺序必须保持
	got := mustCollect(t, xpipe.Buffer(xpipe.Map(xpipe.Range(0, 20), func(i int) int { return i * 2 }), 4))
	if len(got) != 20 {
		t.Fatalf("长度 = %d", len(got))
	}
	for i, v := range got {
		if v != i*2 {
			t.Fatalf("预取后顺序错乱: [%d] = %d", i, v)
		}
	}
	// n<=0 原样返回
	p := xpipe.Range(0, 3)
	if xpipe.Buffer(p, 0) != p {
		t.Fatal("Buffer(0) 应原样返回")
	}

	// 预取必须真的让上下游重叠（上游先跑在前面）
	t.Run("上游可跑在前面", func(t *testing.T) {
		var produced atomic.Int64
		src := xpipe.Map(xpipe.Range(0, 50), func(i int) int {
			produced.Add(1)
			return i
		})
		buf := xpipe.Buffer(src, 32)
		v, ok, err := xpipe.First(buf)
		if err != nil || !ok || v != 0 {
			t.Fatalf("First = %v,%v,%v", v, ok, err)
		}
		if produced.Load() <= 1 {
			t.Fatalf("未观察到预取: produced = %d", produced.Load())
		}
	})
}

// ---------------------------------------------------------------------------
// 错误策略 / 重试 / panic 隔离
// ---------------------------------------------------------------------------

func TestErrorPolicies(t *testing.T) {
	bad := func(i int) (int, error) {
		if i == 2 {
			return 0, errors.New("元素 2 失败")
		}
		return i, nil
	}

	// FailFast（默认）：立刻中止
	got, err := xpipe.Collect(xpipe.MapErr(xpipe.Range(0, 5), bad))
	if err == nil || !strings.Contains(err.Error(), "元素 2 失败") {
		t.Fatalf("FailFast = %v / %v", got, err)
	}
	if len(got) != 2 { // 0、1 已产出
		t.Fatalf("FailFast 已产出 = %v", got)
	}

	// Skip：跳过失败元素
	got, err = xpipe.Collect(xpipe.MapErr(xpipe.Range(0, 5, xpipe.WithErrorPolicy(xpipe.PolicySkip)), bad))
	if err != nil {
		t.Fatalf("Skip 不应返回错误: %v", err)
	}
	if !eq(got, []int{0, 1, 3, 4}) {
		t.Fatalf("Skip = %v", got)
	}

	// Collect：与 Skip 相同但终结时返回聚合错误
	pc := xpipe.MapErr(xpipe.Range(0, 5, xpipe.WithErrorPolicy(xpipe.PolicyCollect)), bad)
	got, err = xpipe.Collect(pc)
	if err == nil || !strings.Contains(err.Error(), "元素 2 失败") {
		t.Fatalf("Collect = %v / %v", got, err)
	}
	if !eq(got, []int{0, 1, 3, 4}) {
		t.Fatalf("Collect 数据 = %v", got)
	}
	// ErrAll 也应能看到
	if err := pc.ErrAll(); err == nil {
		t.Fatal("ErrAll 应有错误")
	}

	// Filter 里的错误同样遵守策略
	got, err = xpipe.Collect(xpipe.FilterCtx(xpipe.Range(0, 4, xpipe.WithErrorPolicy(xpipe.PolicySkip)),
		func(_ context.Context, i int) (bool, error) {
			if i == 1 {
				return false, errors.New("谓词失败")
			}
			return true, nil
		}))
	if err != nil || !eq(got, []int{0, 2, 3}) {
		t.Fatalf("FilterCtx Skip = %v / %v", got, err)
	}
}

func TestWithRetry(t *testing.T) {
	var attempts atomic.Int64
	got, err := xpipe.Collect(xpipe.MapErr(xpipe.Range(0, 3), func(i int) (int, error) {
		if i == 1 {
			if attempts.Add(1) < 3 {
				return 0, errors.New("暂时性失败")
			}
		}
		return i, nil
	}, xpipe.WithRetry(5, time.Millisecond)))
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if !eq(got, []int{0, 1, 2}) {
		t.Fatalf("重试结果 = %v", got)
	}
	if attempts.Load() != 3 {
		t.Fatalf("尝试次数 = %d, want 3", attempts.Load())
	}

	// 重试次数用尽 → 按策略上报
	_, err = xpipe.Collect(xpipe.MapErr(xpipe.Range(0, 1), func(int) (int, error) {
		return 0, errors.New("永久失败")
	}, xpipe.WithRetry(2, time.Millisecond)))
	if err == nil || !strings.Contains(err.Error(), "永久失败") {
		t.Fatalf("重试耗尽 = %v", err)
	}

	// 取消能让重试立刻退出（不泄漏、不傻等）
	t.Run("取消打断重试", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p := xpipe.MapErr(xpipe.FromSlice([]int{1}, xpipe.WithErrorPolicy(xpipe.PolicySkip)),
			func(int) (int, error) {
				cancel() // 第一次失败后立即取消
				return 0, errors.New("x")
			}, xpipe.WithRetry(100, time.Hour))
		start := time.Now()
		_, _ = xpipe.Collect(xpipe.WithContext(p, ctx))
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("取消未打断重试: %v", elapsed)
		}
	})
}

func TestPanicIsolation(t *testing.T) {
	// Map 里 panic → 转成错误（FailFast）
	_, err := xpipe.Collect(xpipe.Map(xpipe.Range(0, 3), func(i int) int {
		if i == 1 {
			panic("map boom")
		}
		return i
	}))
	if err == nil || !strings.Contains(err.Error(), "map boom") {
		t.Fatalf("panic 未转换: %v", err)
	}

	// Skip 策略下 panic 元素被跳过
	got, err := xpipe.Collect(xpipe.Map(xpipe.Range(0, 4, xpipe.WithErrorPolicy(xpipe.PolicySkip)), func(i int) int {
		if i == 2 {
			panic("boom")
		}
		return i
	}))
	if err != nil || !eq(got, []int{0, 1, 3}) {
		t.Fatalf("panic Skip = %v / %v", got, err)
	}

	// panic(error) 应保留原因（errors.Is 可判定）
	sentinel := errors.New("业务错误")
	_, err = xpipe.Collect(xpipe.Map(xpipe.Range(0, 1), func(int) int { panic(sentinel) }))
	if !errors.Is(err, sentinel) {
		t.Fatalf("panic(error) 未保留原因: %v", err)
	}
	if code := xerr.CodeOf(err); code != xerr.CodeInternal {
		t.Fatalf("错误码 = %v", code)
	}

	// 终结操作里的 panic 也要被隔离
	_, err = xpipe.Collect(xpipe.Peek(xpipe.Range(0, 3), func(int) {}))
	if err != nil {
		t.Fatalf("正常 Peek 不应报错: %v", err)
	}
	err = xpipe.ForEach(xpipe.Range(0, 3), func(int) { panic("sink boom") })
	if err == nil || !strings.Contains(err.Error(), "sink boom") {
		t.Fatalf("sink panic 未隔离: %v", err)
	}
}

func TestOnError(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	// 上游错误被拦截并跳过
	got, err := xpipe.Collect(xpipe.OnError(
		xpipe.MapErr(xpipe.Range(0, 4), func(i int) (int, error) {
			if i == 1 {
				return 0, errors.New("坏元素")
			}
			return i, nil
		}),
		func(e error, _ int) error {
			mu.Lock()
			seen = append(seen, e.Error())
			mu.Unlock()
			return nil // 跳过继续
		},
	))
	if err != nil {
		t.Fatalf("OnError 应吞掉错误: %v", err)
	}
	mu.Lock()
	if len(seen) != 1 || seen[0] != "坏元素" {
		t.Fatalf("OnError 未收到错误: %v", seen)
	}
	mu.Unlock()

	// 注意：FailFast 策略下上游会直接中止，OnError 只能拦到「多次」错误；
	// 这里断言的是「不 panic 且不丢已有数据」。
	if len(got) == 0 {
		t.Fatalf("数据全丢: %v", got)
	}
}

// ---------------------------------------------------------------------------
// 终结操作
// ---------------------------------------------------------------------------

func TestTerminators(t *testing.T) {
	// Drain / ForEach
	var sum int
	if err := xpipe.Drain(xpipe.Range(1, 5), func(i int) error { sum += i; return nil }); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if sum != 10 {
		t.Fatalf("Drain 汇总 = %d", sum)
	}
	sum = 0
	if err := xpipe.ForEach(xpipe.Range(1, 5), func(i int) { sum += i }); err != nil || sum != 10 {
		t.Fatalf("ForEach = %d / %v", sum, err)
	}
	// Drain 的错误必须原样返回（可 errors.Is）
	sentinel := errors.New("消费失败")
	if err := xpipe.Drain(xpipe.Range(0, 5), func(int) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Drain 错误被改写: %v", err)
	}

	// CollectN
	got, err := xpipe.CollectN(xpipe.Range(0, 100), 3)
	if err != nil || !eq(got, []int{0, 1, 2}) {
		t.Fatalf("CollectN = %v / %v", got, err)
	}
	got, err = xpipe.CollectN(xpipe.Range(0, 2), 10)
	if err != nil || !eq(got, []int{0, 1}) {
		t.Fatalf("CollectN 超出长度 = %v / %v", got, err)
	}
	got, err = xpipe.CollectN(xpipe.Range(0, 5), 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("CollectN(0) = %v / %v", got, err)
	}

	// Reduce
	total, err := xpipe.Reduce(xpipe.Range(1, 101), 0, func(acc, v int) int { return acc + v })
	if err != nil || total != 5050 {
		t.Fatalf("Reduce = %d / %v", total, err)
	}
	// 空流 Reduce 返回初始值
	total, err = xpipe.Reduce(xpipe.Empty[int](), 7, func(acc, v int) int { return acc + v })
	if err != nil || total != 7 {
		t.Fatalf("空 Reduce = %d / %v", total, err)
	}

	// Count
	if n, err := xpipe.Count(xpipe.Range(0, 7)); err != nil || n != 7 {
		t.Fatalf("Count = %d / %v", n, err)
	}
	if n, err := xpipe.Count(xpipe.Empty[int]()); err != nil || n != 0 {
		t.Fatalf("空 Count = %d / %v", n, err)
	}

	// First / Last
	v, ok, err := xpipe.First(xpipe.Range(5, 9))
	if err != nil || !ok || v != 5 {
		t.Fatalf("First = %v,%v,%v", v, ok, err)
	}
	v, ok, err = xpipe.Last(xpipe.Range(5, 9))
	if err != nil || !ok || v != 8 {
		t.Fatalf("Last = %v,%v,%v", v, ok, err)
	}
	if _, ok, err := xpipe.First(xpipe.Empty[int]()); ok || err != nil {
		t.Fatalf("空 First: ok=%v err=%v", ok, err)
	}
	if _, ok, err := xpipe.Last(xpipe.Empty[int]()); ok || err != nil {
		t.Fatalf("空 Last: ok=%v err=%v", ok, err)
	}

	// Any / All
	if hit, err := xpipe.Any(xpipe.Range(0, 10), func(i int) bool { return i == 3 }); err != nil || !hit {
		t.Fatalf("Any = %v / %v", hit, err)
	}
	if hit, err := xpipe.Any(xpipe.Range(0, 10), func(i int) bool { return i > 100 }); err != nil || hit {
		t.Fatalf("Any(不命中) = %v / %v", hit, err)
	}
	if all, err := xpipe.All(xpipe.Range(0, 10), func(i int) bool { return i < 10 }); err != nil || !all {
		t.Fatalf("All = %v / %v", all, err)
	}
	if all, err := xpipe.All(xpipe.Range(0, 10), func(i int) bool { return i < 5 }); err != nil || all {
		t.Fatalf("All(不全) = %v / %v", all, err)
	}

	// GroupBy / GroupByCount
	groups, err := xpipe.GroupBy(xpipe.Range(0, 7), func(i int) string {
		if i%2 == 0 {
			return "even"
		}
		return "odd"
	})
	if err != nil || len(groups["even"]) != 4 || len(groups["odd"]) != 3 {
		t.Fatalf("GroupBy = %v / %v", groups, err)
	}
	counts, err := xpipe.GroupByCount(xpipe.Range(0, 7), func(i int) int { return i % 3 })
	if err != nil || counts[0] != 3 || counts[1] != 2 || counts[2] != 2 {
		t.Fatalf("GroupByCount = %v / %v", counts, err)
	}

	// ToMap（后者覆盖）+ merge
	m, err := xpipe.ToMap(xpipe.FromSlice([]int{1, 2, 3, 4}), func(i int) int { return i % 2 })
	if err != nil || m[0] != 4 || m[1] != 3 {
		t.Fatalf("ToMap = %v / %v", m, err)
	}
	m, err = xpipe.ToMap(xpipe.FromSlice([]int{1, 2, 3, 4}), func(i int) int { return i % 2 },
		func(old, new int) int { return old + new })
	if err != nil || m[0] != 6 || m[1] != 4 {
		t.Fatalf("ToMap merge = %v / %v", m, err)
	}

	// SortedKeys（map 遍历顺序随机的对策）
	keys := xpipe.SortedKeys(m, func(a, b int) bool { return a < b })
	if !eq(keys, []int{0, 1}) {
		t.Fatalf("SortedKeys = %v", keys)
	}

	// nil 函数保护
	if err := xpipe.Drain(xpipe.Range(0, 1), nil); !errors.Is(err, xpipe.ErrNilFunc) {
		t.Fatalf("Drain(nil) = %v", err)
	}
	if _, err := xpipe.Reduce[int, int](xpipe.Range(0, 1), 0, nil); !errors.Is(err, xpipe.ErrNilFunc) {
		t.Fatalf("Reduce(nil) = %v", err)
	}
	if _, err := xpipe.Count[int](nil); !errors.Is(err, xpipe.ErrNilPipe) {
		t.Fatalf("Count(nil) = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 扇出 / 汇聚
// ---------------------------------------------------------------------------

func TestBroadcastFanOutSplit(t *testing.T) {
	// Broadcast：每个分支都收到全部
	branches := make([][]int, 3)
	err := xpipe.Broadcast(xpipe.Range(0, 4), 3, func(b int, v int) error {
		branches[b] = append(branches[b], v)
		return nil
	})
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	for i, b := range branches {
		if !eq(b, []int{0, 1, 2, 3}) {
			t.Fatalf("分支 %d = %v", i, b)
		}
	}

	// FanOutTo：轮询分发，每个元素只去一个分支
	counts := make([]int, 3)
	seen := map[int]int{}
	err = xpipe.FanOutTo(xpipe.Range(0, 9), 3, func(b int, v int) error {
		counts[b]++
		seen[v]++
		return nil
	})
	if err != nil {
		t.Fatalf("FanOutTo: %v", err)
	}
	if counts[0] != 3 || counts[1] != 3 || counts[2] != 3 {
		t.Fatalf("轮询不均: %v", counts)
	}
	if len(seen) != 9 {
		t.Fatalf("元素重复或丢失: %v", seen)
	}

	// SplitTo：按谓词分流
	var yes, no []int
	cy, cn, err := xpipe.SplitTo(xpipe.Range(0, 6),
		func(i int) bool { return i%2 == 0 },
		func(i int) error { yes = append(yes, i); return nil },
		func(i int) error { no = append(no, i); return nil })
	if err != nil || cy != 3 || cn != 3 {
		t.Fatalf("SplitTo = %d,%d,%v", cy, cn, err)
	}
	if !eq(yes, []int{0, 2, 4}) || !eq(no, []int{1, 3, 5}) {
		t.Fatalf("分流结果 = %v / %v", yes, no)
	}

	// 错误传递
	sentinel := errors.New("分支失败")
	if err := xpipe.Broadcast(xpipe.Range(0, 3), 2, func(int, int) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Broadcast 错误 = %v", err)
	}
}

func TestMergeAndChain(t *testing.T) {
	// Merge：内容齐全（顺序不定）
	got, err := xpipe.Collect(xpipe.Merge(
		xpipe.Range(0, 3),
		xpipe.Range(10, 13),
		xpipe.Just(99),
	))
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eq(sortedInts(got), []int{0, 1, 2, 10, 11, 12, 99}) {
		t.Fatalf("Merge = %v", got)
	}

	// Chain：严格顺序拼接
	got, err = xpipe.Collect(xpipe.Chain(xpipe.Range(0, 3), xpipe.Range(10, 12)))
	if err != nil || !eq(got, []int{0, 1, 2, 10, 11}) {
		t.Fatalf("Chain = %v / %v", got, err)
	}

	// 边界：单条 / 空参 / nil
	if got, err := xpipe.Collect(xpipe.Merge(xpipe.Range(0, 2))); err != nil || !eq(got, []int{0, 1}) {
		t.Fatalf("Merge 单条 = %v / %v", got, err)
	}
	if got, err := xpipe.Collect(xpipe.Merge[int]()); err != nil || len(got) != 0 {
		t.Fatalf("Merge 空参 = %v / %v", got, err)
	}
	if got, err := xpipe.Collect(xpipe.Chain(xpipe.Range(0, 2), nil, xpipe.Range(5, 6))); err != nil || !eq(got, []int{0, 1, 5}) {
		t.Fatalf("Chain 含 nil = %v / %v", got, err)
	}

	// Merge 的错误上报
	bad := xpipe.MapErr(xpipe.Range(0, 5), func(i int) (int, error) {
		if i == 1 {
			return 0, errors.New("merge 上游错")
		}
		return i, nil
	})
	if _, err := xpipe.Collect(xpipe.Merge(bad, xpipe.Range(100, 102))); err == nil {
		t.Fatal("Merge 应上报上游错误")
	}
}

func TestZip(t *testing.T) {
	got, err := xpipe.Collect(xpipe.Zip(
		xpipe.FromSlice([]string{"a", "b", "c"}),
		xpipe.Range(1, 10),
	))
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Zip 长度 = %d", len(got))
	}
	for i, pr := range got {
		if pr.Key != []string{"a", "b", "c"}[i] || pr.Val != i+1 {
			t.Fatalf("Zip[%d] = %+v", i, pr)
		}
	}

	// 短的一侧先结束 → 以短为准
	got2, err := xpipe.Collect(xpipe.Zip(xpipe.FromSlice([]int{1, 2}), xpipe.Range(0, 100)))
	if err != nil || len(got2) != 2 {
		t.Fatalf("Zip 短侧 = %v / %v", got2, err)
	}
	// nil 上游
	if xpipe.Zip[int, int](nil, xpipe.Range(0, 1)) != nil {
		t.Fatal("Zip(nil) 应为 nil")
	}
}

// ---------------------------------------------------------------------------
// 生命周期
// ---------------------------------------------------------------------------

func TestLifecycle(t *testing.T) {
	// 重复终结
	p := xpipe.Range(0, 3)
	if _, err := xpipe.Collect(p); err != nil {
		t.Fatalf("首次 Collect: %v", err)
	}
	if _, err := xpipe.Collect(p); !errors.Is(err, xpipe.ErrConsumed) {
		t.Fatalf("重复终结 = %v, want ErrConsumed", err)
	}

	// Close 幂等
	p2 := xpipe.Range(0, 100)
	if err := p2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p2.Close(); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
	if !p2.IsClosed() {
		t.Fatal("IsClosed 应为 true")
	}
	// 关闭后终结应立刻返回（不拿到数据）
	if got, err := xpipe.Collect(p2); err == nil || len(got) != 0 {
		t.Fatalf("关闭后 Collect = %v / %v", got, err)
	}

	// nil 管道
	var nilPipe *xpipe.Pipe[int]
	if err := nilPipe.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if !nilPipe.IsClosed() || nilPipe.Name() != "" {
		t.Fatal("nil 查询方法异常")
	}
	if err := nilPipe.Err(); !errors.Is(err, xpipe.ErrNilPipe) {
		t.Fatalf("nil Err = %v", err)
	}
	if _, err := xpipe.Collect[int](nil); !errors.Is(err, xpipe.ErrNilPipe) {
		t.Fatalf("nil Collect = %v", err)
	}
	// nil 阶段原样返回 nil（链式安全）
	if xpipe.Map[int, int](nil, func(i int) int { return i }) != nil {
		t.Fatal("Map(nil) 应为 nil")
	}
	if xpipe.Filter[int](nil, func(int) bool { return true }) != nil {
		t.Fatal("Filter(nil) 应为 nil")
	}
	if xpipe.Parallel[int, int](nil, 2, func(i int) (int, error) { return i, nil }) != nil {
		t.Fatal("Parallel(nil) 应为 nil")
	}
	if xpipe.Map[int, int](xpipe.Range(0, 1), nil) != nil {
		t.Fatal("Map(nil fn) 应为 nil")
	}

	// Name / Named / Ctx
	p3 := xpipe.Range(0, 1, xpipe.WithName("源"))
	if p3.Name() != "源" {
		t.Fatalf("Name = %q", p3.Name())
	}
	p4 := xpipe.Named(xpipe.Map(p3, func(i int) int { return i }), "改名")
	if p4.Name() != "改名" {
		t.Fatalf("Named = %q", p4.Name())
	}
	if xpipe.Named(p4, "").Name() != "改名" {
		t.Fatal("Named(\"\") 应沿用原名")
	}
	if p4.Ctx() == nil {
		t.Fatal("Ctx 不应为 nil")
	}

	// WithContext：取消后终结立即返回 ctx 错误
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pc := xpipe.WithContext(xpipe.Range(0, 1000), ctx)
	if _, err := xpipe.Collect(pc); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后 Collect = %v", err)
	}
	// 取消一个阻塞在 channel 上的源
	ctx2, cancel2 := context.WithCancel(context.Background())
	blocking := xpipe.WithContext(xpipe.FromChannelCtx(ctx2, make(chan int)), ctx2)
	go func() { time.Sleep(30 * time.Millisecond); cancel2() }()
	start := time.Now()
	if _, err := xpipe.Collect(blocking); !errors.Is(err, context.Canceled) {
		t.Fatalf("阻塞源取消 = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("取消未及时生效: %v", elapsed)
	}
}

func TestErrorPolicyString(t *testing.T) {
	cases := map[xpipe.ErrorPolicy]string{
		xpipe.PolicyFailFast:  "PolicyFailFast",
		xpipe.PolicySkip:      "PolicySkip",
		xpipe.PolicyCollect:   "PolicyCollect",
		xpipe.ErrorPolicy(99): "ErrorPolicy(99)",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("String(%d) = %q, want %q", p, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// goroutine 泄漏（本包最硬的不变量）
// ---------------------------------------------------------------------------

func TestNoGoroutineLeak(t *testing.T) {
	t.Run("预取管道全消费", func(t *testing.T) {
		assertNoLeak(t, func() {
			if _, err := xpipe.Collect(xpipe.Buffer(xpipe.Range(0, 500), 16)); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("预取管道提前收工", func(t *testing.T) {
		assertNoLeak(t, func() {
			if _, _, err := xpipe.First(xpipe.Buffer(xpipe.Range(0, 100000), 16)); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("并行管道全消费", func(t *testing.T) {
		assertNoLeak(t, func() {
			_, err := xpipe.Collect(xpipe.Parallel(xpipe.Range(0, 300), 8, func(i int) (int, error) {
				return i * 2, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("并行管道中途出错", func(t *testing.T) {
		assertNoLeak(t, func() {
			_, _ = xpipe.Collect(xpipe.Parallel(xpipe.Range(0, 100000), 8, func(i int) (int, error) {
				if i == 50 {
					return 0, errors.New("中止")
				}
				return i, nil
			}))
		})
	})
	t.Run("并行保序提前收工", func(t *testing.T) {
		assertNoLeak(t, func() {
			if _, _, err := xpipe.First(xpipe.ParallelOrdered(xpipe.Range(0, 100000), 8,
				func(i int) (int, error) { return i, nil })); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("FromFunc提前收工", func(t *testing.T) {
		assertNoLeak(t, func() {
			p := xpipe.FromFunc(func(ctx context.Context, emit func(int) error) error {
				for i := 0; ; i++ {
					if err := emit(i); err != nil {
						return err
					}
				}
			})
			if _, _, err := xpipe.First(p); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("Merge提前收工", func(t *testing.T) {
		assertNoLeak(t, func() {
			if _, _, err := xpipe.First(xpipe.Merge(
				xpipe.Range(0, 100000), xpipe.Range(0, 100000), xpipe.Range(0, 100000))); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("Any短路", func(t *testing.T) {
		assertNoLeak(t, func() {
			if _, err := xpipe.Any(xpipe.Buffer(xpipe.Range(0, 100000), 8), func(i int) bool { return i == 3 }); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("取消阻塞源", func(t *testing.T) {
		assertNoLeak(t, func() {
			ctx, cancel := context.WithCancel(context.Background())
			p := xpipe.WithContext(xpipe.FromChannelCtx(ctx, make(chan int)), ctx)
			go func() { time.Sleep(20 * time.Millisecond); cancel() }()
			_, _ = xpipe.Collect(p)
		})
	})
	t.Run("Throttle取消", func(t *testing.T) {
		assertNoLeak(t, func() {
			ctx, cancel := context.WithCancel(context.Background())
			p := xpipe.WithContext(xpipe.Throttle(xpipe.Range(0, 100000), time.Second), ctx)
			go func() { time.Sleep(20 * time.Millisecond); cancel() }()
			_, _ = xpipe.Collect(p)
		})
	})
	t.Run("多次批量运行", func(t *testing.T) {
		assertNoLeak(t, func() {
			for i := 0; i < 20; i++ {
				_, err := xpipe.Collect(xpipe.Batch(xpipe.Parallel(xpipe.Buffer(xpipe.Range(0, 100), 4), 4,
					func(v int) (int, error) { return v, nil }), 8))
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	})
}

// ---------------------------------------------------------------------------
// 综合
// ---------------------------------------------------------------------------

func TestFullPipeline(t *testing.T) {
	// 一条接近真实使用的管道：
	// 生成 → 并行放大 → 过滤 → 去重 → 分批 → 汇总
	batchSizes, err := xpipe.Collect(xpipe.Batch(
		xpipe.Distinct(
			xpipe.Filter(
				xpipe.Parallel(xpipe.Range(1, 201, xpipe.WithErrorPolicy(xpipe.PolicySkip)), 8, func(i int) (int, error) {
					if i%97 == 0 {
						return 0, errors.New("坏元素") // 由 Skip 策略跳过（默认 FailFast 会如实中止）
					}
					return i % 10, nil
				}),
				func(i int) bool { return i >= 3 }),
		), 4))
	if err != nil {
		t.Fatalf("管道错误: %v", err)
	}
	total := 0
	for _, b := range batchSizes {
		total += len(b)
	}
	// i%10 ∈ {0..9}，过滤掉 <3 → {3,4,5,6,7,8,9}（0 也可能出现，因为 i%97==0 的被跳过，
	// 但不影响集合上界）；上界检查：最多 10 个不同值，分批后总元素数等于不同值个数
	if total < 7 || total > 10 {
		t.Fatalf("汇总异常: total=%d batches=%v", total, batchSizes)
	}
	for _, b := range batchSizes {
		if len(b) == 0 || len(b) > 4 {
			t.Fatalf("批大小异常: %v", b)
		}
	}

	// 计数与去重一致性
	n, err := xpipe.Count(xpipe.Distinct(xpipe.Map(xpipe.Range(0, 1000), func(i int) int { return i % 7 })))
	if err != nil || n != 7 {
		t.Fatalf("Count(Distinct) = %d / %v", n, err)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkMapFilter(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := xpipe.Collect(xpipe.Filter(
			xpipe.Map(xpipe.Range(0, 1000), func(v int) int { return v * 2 }),
			func(v int) bool { return v%3 == 0 }))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParallel(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := xpipe.Collect(xpipe.Parallel(xpipe.Range(0, 1000), 4, func(v int) (int, error) {
			return v * 2, nil
		}))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuffer(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := xpipe.Collect(xpipe.Buffer(xpipe.Range(0, 1000), 64))
		if err != nil {
			b.Fatal(err)
		}
	}
}
