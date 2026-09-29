package xstack_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/indigo-apple/gokit/xcontainer"
	"github.com/indigo-apple/gokit/xcontainer/xstack"
	"github.com/indigo-apple/gokit/xerr"
)

func eqInts(t *testing.T, name string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Stack
// ---------------------------------------------------------------------------

func TestStackLIFO(t *testing.T) {
	s := xstack.New[int]()
	s.Push(1)
	s.Push(2, 3) // 多值：先传的在下

	// 栈底 → 栈顶
	eqInts(t, "Values", s.Values(), []int{1, 2, 3})

	// 弹出顺序：3, 2, 1
	for _, want := range []int{3, 2, 1} {
		got, ok := s.Pop()
		if !ok || got != want {
			t.Fatalf("Pop = %d,%v, want %d", got, ok, want)
		}
	}
	if !s.IsEmpty() {
		t.Fatal("应已清空")
	}
	if s.Len() != 0 {
		t.Fatalf("Len = %d", s.Len())
	}
}

func TestStackEmptySafety(t *testing.T) {
	s := xstack.New[string]()

	if v, ok := s.Pop(); ok || v != "" {
		t.Fatalf("空栈 Pop = %q,%v", v, ok)
	}
	if v, ok := s.Peek(); ok || v != "" {
		t.Fatalf("空栈 Peek = %q,%v", v, ok)
	}
	if v, ok := s.PeekN(0); ok || v != "" {
		t.Fatalf("空栈 PeekN = %q,%v", v, ok)
	}
	s.Clear() // 不应 panic
	eqInts(t, "空栈 Values", xstack.New[int]().Values(), nil)

	// MustPop 在空栈时 panic
	defer func() {
		if recover() == nil {
			t.Fatal("MustPop 应在空栈时 panic")
		}
	}()
	s.MustPop()
}

func TestStackNilReceiver(t *testing.T) {
	var s *xstack.Stack[int]
	// 所有方法都必须 nil 安全
	s.Push(1)
	if v, ok := s.Pop(); ok || v != 0 {
		t.Fatalf("nil Pop = %d,%v", v, ok)
	}
	if v, ok := s.Peek(); ok || v != 0 {
		t.Fatalf("nil Peek = %d,%v", v, ok)
	}
	if v, ok := s.PeekN(0); ok || v != 0 {
		t.Fatalf("nil PeekN = %d,%v", v, ok)
	}
	if s.Len() != 0 || s.Cap() != 0 || !s.IsEmpty() {
		t.Fatal("nil 的 Len/Cap/IsEmpty 应返回零值")
	}
	s.Clear()
	if s.Values() != nil {
		t.Fatal("nil Values 应为 nil")
	}
	s.ForEach(func(int) { t.Fatal("nil 不应触发回调") })
	if s.PopUntil(func(int) bool { return true }) != nil {
		t.Fatal("nil PopUntil 应为 nil")
	}
	if s.Search(1) != -1 {
		t.Fatal("nil Search 应为 -1")
	}
	if s.String() != "Stack(nil)" {
		t.Fatalf("nil String = %q", s.String())
	}
	if s.Clone() == nil {
		t.Fatal("nil Clone 应返回空栈")
	}
}

func TestStackPeekN(t *testing.T) {
	s := xstack.NewFrom([]int{10, 20, 30}) // 栈顶 30
	cases := []struct {
		n    int
		want int
		ok   bool
	}{
		{0, 30, true},
		{1, 20, true},
		{2, 10, true},
		{3, 0, false},
		{-1, 0, false},
	}
	for _, c := range cases {
		got, ok := s.PeekN(c.n)
		if ok != c.ok || got != c.want {
			t.Fatalf("PeekN(%d) = %d,%v, want %d,%v", c.n, got, ok, c.want, c.ok)
		}
	}
	// PeekN 不应改变栈
	if s.Len() != 3 {
		t.Fatalf("PeekN 改变了栈长度: %d", s.Len())
	}
}

// NewFrom 必须复制入参，否则调用方后续修改切片会串改栈内容。
func TestStackNewFromCopies(t *testing.T) {
	src := []int{1, 2, 3}
	s := xstack.NewFrom(src)
	src[0] = 999
	src[2] = 999
	eqInts(t, "NewFrom 应复制", s.Values(), []int{1, 2, 3})
}

func TestStackForEachIsPopOrder(t *testing.T) {
	s := xstack.NewFrom([]int{1, 2, 3})
	var got []int
	s.ForEach(func(v int) { got = append(got, v) })
	// ForEach 与 Pop 顺序一致：顶 → 底
	eqInts(t, "ForEach", got, []int{3, 2, 1})

	// 遍历不应消耗栈
	if s.Len() != 3 {
		t.Fatalf("ForEach 消耗了栈: %d", s.Len())
	}
}

func TestStackPopUntil(t *testing.T) {
	s := xstack.NewFrom([]int{1, 2, 3, 4, 5})
	got := s.PopUntil(func(v int) bool { return v == 3 })
	eqInts(t, "PopUntil", got, []int{5, 4, 3})
	eqInts(t, "PopUntil 之后栈内容", s.Values(), []int{1, 2})

	// 找不到标记时会把栈清空
	s2 := xstack.NewFrom([]int{1, 2})
	if got := s2.PopUntil(func(v int) bool { return v == 99 }); len(got) != 2 {
		t.Fatalf("PopUntil(未命中) = %v", got)
	}
	if !s2.IsEmpty() {
		t.Fatal("PopUntil 未命中应清空栈")
	}
	// nil pred 安全
	if got := xstack.NewFrom([]int{1}).PopUntil(nil); got != nil {
		t.Fatalf("PopUntil(nil) = %v", got)
	}
}

// Search 曾因 any(x) == any(y) 对不可比较类型 panic，这里锁定回归。
func TestStackSearch(t *testing.T) {
	s := xstack.NewFrom([]int{1, 2, 3})
	if got := s.Search(3); got != 0 {
		t.Fatalf("Search(栈顶) = %d", got)
	}
	if got := s.Search(1); got != 2 {
		t.Fatalf("Search(栈底) = %d", got)
	}
	if got := s.Search(99); got != -1 {
		t.Fatalf("Search(未命中) = %d", got)
	}
	// 自定义比较器
	if got := s.Search(2, func(a, b int) bool { return a == b }); got != 1 {
		t.Fatalf("Search(eq) = %d", got)
	}

	// 不可比较元素（切片）必须不 panic
	sl := xstack.NewFrom([][]int{{1}, {2}, {3}})
	if got := sl.Search([]int{1}); got != 2 {
		t.Fatalf("Search(不可比较类型) = %d", got)
	}
	if got := sl.Search([]int{9}); got != -1 {
		t.Fatalf("Search(不可比较未命中) = %d", got)
	}
}

func TestStackCloneIndependent(t *testing.T) {
	s := xstack.NewFrom([]int{1, 2})
	c := s.Clone()
	c.Push(3)
	if s.Len() != 2 {
		t.Fatalf("Clone 应与原栈独立, 原栈 Len = %d", s.Len())
	}
	eqInts(t, "Clone", c.Values(), []int{1, 2, 3})
}

func TestStackClearReleasesRefs(t *testing.T) {
	s := xstack.New[int](16)
	for i := 0; i < 10; i++ {
		s.Push(i)
	}
	capBefore := s.Cap()
	s.Clear()
	if s.Len() != 0 || !s.IsEmpty() {
		t.Fatal("Clear 未清空")
	}
	if s.Cap() != capBefore {
		t.Fatalf("Clear 应保留容量, %d → %d", capBefore, s.Cap())
	}
	// 清空后仍可继续使用
	s.Push(7)
	eqInts(t, "Clear 后复用", s.Values(), []int{7})
}

func TestStackString(t *testing.T) {
	s := xstack.NewFrom([]int{1, 2, 3})
	if got := s.String(); got != "Stack([1 2 3])" {
		t.Fatalf("String = %q", got)
	}
	if got := xstack.New[int]().String(); got != "Stack([])" {
		t.Fatalf("空栈 String = %q", got)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStack
// ---------------------------------------------------------------------------

func TestConcurrentStackBasic(t *testing.T) {
	s := xstack.NewConcurrent[int]()
	s.Push(1, 2, 3)
	if s.Len() != 3 {
		t.Fatalf("Len = %d", s.Len())
	}
	if v, ok := s.Peek(); !ok || v != 3 {
		t.Fatalf("Peek = %d,%v", v, ok)
	}
	eqInts(t, "Values", s.Values(), []int{1, 2, 3})

	var seen []int
	s.ForEach(func(v int) { seen = append(seen, v) })
	eqInts(t, "ForEach", seen, []int{3, 2, 1})

	c := s.Clone()
	c.Push(4)
	if s.Len() != 3 {
		t.Fatal("Clone 应独立")
	}
	s.Clear()
	if !s.IsEmpty() || s.Values() != nil {
		t.Fatal("Clear 后应为空")
	}
	if v, ok := s.Pop(); ok || v != 0 {
		t.Fatalf("空栈 Pop = %d,%v", v, ok)
	}
}

// 并发压入 N 个元素再用一个消费者弹出 N 个，验证无丢失 / 无重复。
func TestConcurrentStackNoLoss(t *testing.T) {
	const goroutines, perG = 16, 200
	total := goroutines * perG

	s := xstack.NewConcurrent[int](total)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		base := g * perG
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				s.Push(base + i)
			}
		}()
	}
	wg.Wait()

	if s.Len() != total {
		t.Fatalf("Len = %d, want %d", s.Len(), total)
	}

	// 并发弹出：统计每个值恰好出现一次
	var mu sync.Mutex
	seen := make([]int, total)
	var wg2 sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			for {
				v, ok := s.Pop()
				if !ok {
					return
				}
				if v < 0 || v >= total {
					t.Errorf("越界值 %d", v)
					return
				}
				mu.Lock()
				seen[v]++
				mu.Unlock()
			}
		}()
	}
	wg2.Wait()

	for v, n := range seen {
		if n != 1 {
			t.Fatalf("值 %d 出现 %d 次（应为 1 次）", v, n)
		}
	}
	if !s.IsEmpty() {
		t.Fatalf("应为空, Len = %d", s.Len())
	}
}

// ---------------------------------------------------------------------------
// BoundedStack
// ---------------------------------------------------------------------------

func TestBoundedStackPolicies(t *testing.T) {
	t.Run("DropNewest", func(t *testing.T) {
		s := xstack.NewBounded[int](3, xcontainer.DropNewest)
		for i := 1; i <= 5; i++ {
			if err := s.Push(i); err != nil {
				t.Fatalf("Push: %v", err)
			}
		}
		// 保留最早的 3 个，栈底 → 栈顶
		eqInts(t, "Values", s.Values(), []int{1, 2, 3})
		if s.Dropped() != 2 {
			t.Fatalf("Dropped = %d, want 2", s.Dropped())
		}
		if !s.IsFull() {
			t.Fatal("应为满")
		}
	})

	t.Run("DropOldest", func(t *testing.T) {
		s := xstack.NewBounded[int](3, xcontainer.DropOldest)
		for i := 1; i <= 5; i++ {
			if err := s.Push(i); err != nil {
				t.Fatalf("Push: %v", err)
			}
		}
		// 丢弃栈底后保留最新的 3 个
		eqInts(t, "Values", s.Values(), []int{3, 4, 5})
		if s.Dropped() != 2 {
			t.Fatalf("Dropped = %d, want 2", s.Dropped())
		}
		if v, ok := s.Peek(); !ok || v != 5 {
			t.Fatalf("Peek = %d,%v", v, ok)
		}
	})

	t.Run("Fail", func(t *testing.T) {
		s := xstack.NewBounded[int](2, xcontainer.Fail)
		if err := s.Push(1); err != nil {
			t.Fatalf("Push: %v", err)
		}
		if err := s.Push(2); err != nil {
			t.Fatalf("Push: %v", err)
		}
		err := s.Push(3)
		if !errors.Is(err, xcontainer.ErrFull) {
			t.Fatalf("Push = %v, want ErrFull", err)
		}
		if got := xerr.CodeOf(err); got != xerr.CodeResourceExhausted {
			t.Fatalf("错误码 = %v", got)
		}
		eqInts(t, "失败后应保留原数据", s.Values(), []int{1, 2})
	})

	t.Run("Block", func(t *testing.T) {
		s := xstack.NewBounded[int](2, xcontainer.Block)
		s.Push(1)
		s.Push(2)
		err := s.Push(3)
		if !errors.Is(err, xcontainer.ErrWouldBlock) {
			t.Fatalf("Push = %v, want ErrWouldBlock", err)
		}
	})

	t.Run("非法策略归一化", func(t *testing.T) {
		s := xstack.NewBounded[int](2, xcontainer.OverflowPolicy(99))
		if s.Policy() != xcontainer.DropNewest {
			t.Fatalf("Policy = %v, want DropNewest", s.Policy())
		}
		s.Push(1)
		s.Push(2)
		if err := s.Push(3); err != nil {
			t.Fatalf("Push: %v", err)
		}
		eqInts(t, "Values", s.Values(), []int{1, 2})
	})

	t.Run("maxSize<=0 视为 1", func(t *testing.T) {
		s := xstack.NewBounded[int](0, xcontainer.Fail)
		if s.Cap() != 1 {
			t.Fatalf("Cap = %d, want 1", s.Cap())
		}
		if err := s.Push(1); err != nil {
			t.Fatalf("Push: %v", err)
		}
		if err := s.Push(2); !errors.Is(err, xcontainer.ErrFull) {
			t.Fatalf("Push = %v, want ErrFull", err)
		}
	})
}

func TestBoundedStackMisc(t *testing.T) {
	s := xstack.NewBounded[int](4, xcontainer.DropNewest)
	s.Push(1)
	s.Push(2)
	if v, ok := s.Peek(); !ok || v != 2 {
		t.Fatalf("Peek = %d,%v", v, ok)
	}
	if v, ok := s.Pop(); !ok || v != 2 {
		t.Fatalf("Pop = %d,%v", v, ok)
	}
	if s.Len() != 1 || s.IsFull() || s.IsEmpty() {
		t.Fatalf("状态错误: Len=%d Full=%v Empty=%v", s.Len(), s.IsFull(), s.IsEmpty())
	}
	var seen []int
	s.ForEach(func(v int) { seen = append(seen, v) })
	eqInts(t, "ForEach", seen, []int{1})

	s.Clear()
	if !s.IsEmpty() || s.Values() != nil {
		t.Fatal("Clear 后应为空")
	}
	if v, ok := s.Pop(); ok || v != 0 {
		t.Fatalf("空 Pop = %d,%v", v, ok)
	}
	if v, ok := s.Peek(); ok || v != 0 {
		t.Fatalf("空 Peek = %d,%v", v, ok)
	}

	// nil 接收者
	var nilS *xstack.BoundedStack[int]
	if err := nilS.Push(1); err != nil {
		t.Fatalf("nil Push = %v", err)
	}
	if _, ok := nilS.Pop(); ok {
		t.Fatal("nil Pop 应为 false")
	}
	if _, ok := nilS.Peek(); ok {
		t.Fatal("nil Peek 应为 false")
	}
	nilS.Clear()
	nilS.ForEach(func(int) {})
	if nilS.Len() != 0 || nilS.Cap() != 0 || nilS.Dropped() != 0 || !nilS.IsEmpty() {
		t.Fatal("nil 零值方法错误")
	}
	if nilS.Policy() != xcontainer.DropNewest {
		t.Fatal("nil Policy 应为 DropNewest")
	}
	if nilS.Values() != nil {
		t.Fatal("nil Values 应为 nil")
	}
}

// OverflowPolicy.String 与归一化
func TestOverflowPolicy(t *testing.T) {
	cases := map[xcontainer.OverflowPolicy]string{
		xcontainer.Block:              "Block",
		xcontainer.DropOldest:         "DropOldest",
		xcontainer.DropNewest:         "DropNewest",
		xcontainer.Fail:               "Fail",
		xcontainer.OverflowPolicy(99): "Unknown",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("Policy(%d).String() = %q, want %q", p, got, want)
		}
	}
	if !xcontainer.DropOldest.Valid() || xcontainer.OverflowPolicy(-1).Valid() {
		t.Fatal("Valid 判断错误")
	}
	if xcontainer.OverflowPolicy(-1).Norm() != xcontainer.DropNewest {
		t.Fatal("Norm 应归一化为 DropNewest")
	}
	if xcontainer.Fail.Norm() != xcontainer.Fail {
		t.Fatal("Norm 不应改变合法值")
	}
}

// ---------------------------------------------------------------------------
// 真实场景：括号匹配（栈的经典用法）
// ---------------------------------------------------------------------------

func TestRealisticBracketMatching(t *testing.T) {
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	check := func(s string) bool {
		st := xstack.New[rune]()
		for _, r := range s {
			switch r {
			case '(', '[', '{':
				st.Push(r)
			case ')', ']', '}':
				top, ok := st.Pop()
				if !ok || top != pairs[r] {
					return false
				}
			}
		}
		return st.IsEmpty()
	}

	cases := map[string]bool{
		"":            true,
		"()":          true,
		"()[]{}":      true,
		"([{}])":      true,
		"(]":          false,
		"(((":         false,
		"())":         false,
		"a(b[c]d)e":   true,
		"(((){}[]))":  true,
		"(((){}[]))(": false,
	}
	for in, want := range cases {
		if got := check(in); got != want {
			t.Fatalf("括号匹配 %q = %v, want %v", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkStackPushPop(b *testing.B) {
	s := xstack.New[int](1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(i)
		s.Pop()
	}
}

func BenchmarkConcurrentStackPushPop(b *testing.B) {
	s := xstack.NewConcurrent[int](1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(i)
		s.Pop()
	}
}

func BenchmarkBoundedStackDropOldest(b *testing.B) {
	s := xstack.NewBounded[int](64, xcontainer.DropOldest)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Push(i)
	}
}
