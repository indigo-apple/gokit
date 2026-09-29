package xslice_test

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/zhangwenxing/gokit/xmap"
	"github.com/zhangwenxing/gokit/xslice"
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

func eqStrs(t *testing.T, name string, got, want []string) {
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

func eqB(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func eqI(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// 去重
// ---------------------------------------------------------------------------

func TestDedup(t *testing.T) {
	eqInts(t, "Dedup", xslice.Dedup([]int{1, 2, 1, 3, 2}), []int{1, 2, 3})
	eqInts(t, "Dedup(已唯一)", xslice.Dedup([]int{1, 2, 3}), []int{1, 2, 3})
	eqInts(t, "Dedup(全同)", xslice.Dedup([]int{5, 5, 5}), []int{5})
	eqStrs(t, "Dedup(字符串)", xslice.Dedup([]string{"b", "a", "b"}), []string{"b", "a"})
	if got := xslice.Dedup[int](nil); got != nil {
		t.Fatalf("Dedup(nil) = %v", got)
	}
	if got := xslice.Dedup([]int{}); got != nil {
		t.Fatalf("Dedup(空) = %v", got)
	}

	eqInts(t, "DedupLast", xslice.DedupLast([]int{1, 2, 1, 3}), []int{2, 1, 3})
	eqInts(t, "DedupLast(无重复)", xslice.DedupLast([]int{1, 2}), []int{1, 2})

	// 按奇偶去重
	eqInts(t, "DedupBy", xslice.DedupBy([]int{1, 2, 3, 4}, func(v int) int { return v % 2 }), []int{1, 2})
	eqInts(t, "DedupByLast", xslice.DedupByLast([]int{1, 2, 3, 4}, func(v int) int { return v % 2 }), []int{3, 4})
	if got := xslice.DedupBy[int, int](nil, func(v int) int { return v }); got != nil {
		t.Fatalf("DedupBy(nil) = %v", got)
	}

	// 原地去重
	orig := []int{1, 2, 1, 3}
	got := xslice.DedupInPlace(orig)
	eqInts(t, "DedupInPlace", got, []int{1, 2, 3})
	if len(orig) < 3 || orig[0] != 1 || orig[1] != 2 || orig[2] != 3 {
		t.Fatalf("DedupInPlace 应改写底层数组: %v", orig)
	}
	if got := xslice.DedupInPlace([]int{1}); len(got) != 1 {
		t.Fatalf("DedupInPlace(单元素) = %v", got)
	}
	if got := xslice.DedupInPlace[int](nil); got != nil {
		t.Fatalf("DedupInPlace(nil) = %v", got)
	}

	counts := xslice.CountBy([]int{1, 1, 2, 3, 3, 3}, func(v int) int { return v })
	if counts[1] != 2 || counts[2] != 1 || counts[3] != 3 {
		t.Fatalf("CountBy = %v", counts)
	}
	if got := xslice.CountBy[int, int](nil, func(v int) int { return v }); got != nil {
		t.Fatalf("CountBy(nil) = %v", got)
	}

	eqInts(t, "Duplicates", xslice.Duplicates([]int{1, 2, 1, 3, 2, 2}), []int{1, 2})
	if got := xslice.Duplicates([]int{1, 2, 3}); got != nil {
		t.Fatalf("Duplicates(无重复) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 变换
// ---------------------------------------------------------------------------

func TestTransform(t *testing.T) {
	eqInts(t, "Map", xslice.Map([]int{1, 2, 3}, func(v int) int { return v * 2 }), []int{2, 4, 6})
	eqStrs(t, "MapToOtherType", xslice.Map([]int{1, 2}, func(v int) string { return strings.Repeat("x", v) }), []string{"x", "xx"})
	if got := xslice.Map[int, int](nil, func(v int) int { return v }); got != nil {
		t.Fatalf("Map(nil) = %v", got)
	}
	eqInts(t, "MapIdx", xslice.MapIdx([]int{10, 10}, func(i, v int) int { return i + v }), []int{10, 11})

	got, err := xslice.MapErr([]int{1, 2, 3}, func(v int) (int, error) {
		if v == 3 {
			return 0, errors.New("boom")
		}
		return v * 2, nil
	})
	eqInts(t, "MapErr(部分结果)", got, []int{2, 4})
	if err == nil {
		t.Fatal("MapErr 应返回错误")
	}
	if got, err := xslice.MapErr([]int{1}, func(v int) (int, error) { return v, nil }); err != nil || got[0] != 1 {
		t.Fatalf("MapErr(成功) = %v,%v", got, err)
	}
	if got, err := xslice.MapErr[int, int](nil, func(v int) (int, error) { return v, nil }); got != nil || err != nil {
		t.Fatalf("MapErr(nil) = %v,%v", got, err)
	}

	eqInts(t, "MapConcat", xslice.MapConcat([]int{1, 2}, func(v int) []int { return []int{v, v} }), []int{1, 1, 2, 2})

	eqInts(t, "FilterMap", xslice.FilterMap([]int{1, 2, 3, 4}, func(v int) (int, bool) {
		return v * 10, v%2 == 0
	}), []int{20, 40})

	eqInts(t, "Filter", xslice.Filter([]int{1, 2, 3, 4}, func(v int) bool { return v%2 == 0 }), []int{2, 4})
	eqInts(t, "FilterIdx", xslice.FilterIdx([]int{9, 9, 9}, func(i, v int) bool { return i > 0 }), []int{9, 9})
	eqInts(t, "Reject", xslice.Reject([]int{1, 2, 3, 4}, func(v int) bool { return v%2 == 0 }), []int{1, 3})
	eqInts(t, "Compact", xslice.Compact([]int{0, 1, 0, 2}), []int{1, 2})

	a, b := 1, 2
	eqInts(t, "FilterNil", xslice.FilterNil([]*int{&a, nil, &b}), []int{1, 2})
	if got := xslice.FilterNil[int](nil); got != nil {
		t.Fatalf("FilterNil(nil) = %v", got)
	}

	eqI(t, "Reduce", xslice.Reduce([]int{1, 2, 3}, 0, func(acc, v int) int { return acc + v }), 6)
	eqI(t, "ReduceRight", xslice.ReduceRight([]int{1, 2, 3}, 0, func(acc, v int) int { return acc*10 + v }), 321)
	eqI(t, "Reduce(初值)", xslice.Reduce([]int{1, 2}, 10, func(acc, v int) int { return acc + v }), 13)

	var seen []int
	sliceForEach([]int{1, 2}, func(v int) { seen = append(seen, v) })
	eqInts(t, "ForEach", seen, []int{1, 2})

	seen = nil
	sliceForEachIdx([]int{5, 6}, func(i, v int) { seen = append(seen, i*10+v) })
	eqInts(t, "ForEachIdx", seen, []int{5, 16})

	// 空切片不触发回调
	sliceForEach(nil, func(v int) { t.Fatal("nil 切片不应触发回调") })
	sliceForEach[int]([]int{}, nil) // nil 函数安全

	err = sliceForEachErr([]int{1, 2, 3}, func(v int) error {
		if v == 2 {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil {
		t.Fatal("ForEachErr 应返回错误")
	}
	if err := xslice.ForEachErr([]int{1}, func(v int) error { return nil }); err != nil {
		t.Fatalf("ForEachErr(成功) = %v", err)
	}
	if err := xslice.ForEachErr([]int{1}, nil); err != nil {
		t.Fatalf("ForEachErr(nil fn) = %v", err)
	}
}

// 这些是让测试更易读的小包装（显式类型实参，避免与局部变量重名）
func sliceForEach[T any](s []T, fn func(T))                { xslice.ForEach(s, fn) }
func sliceForEachIdx[T any](s []T, fn func(int, T))        { xslice.ForEachIdx(s, fn) }
func sliceForEachErr[T any](s []T, fn func(T) error) error { return xslice.ForEachErr(s, fn) }

// ---------------------------------------------------------------------------
// 查找
// ---------------------------------------------------------------------------

func TestSearch(t *testing.T) {
	s := []int{1, 2, 3, 2}

	eqB(t, "Contains(有)", xslice.Contains(s, 3), true)
	eqB(t, "Contains(无)", xslice.Contains(s, 9), false)
	eqB(t, "Contains(空)", xslice.Contains([]int{}, 1), false)
	eqB(t, "ContainsBy", xslice.ContainsBy(s, func(v int) bool { return v > 2 }), true)

	eqI(t, "IndexOf", xslice.IndexOf(s, 2), 1)
	eqI(t, "IndexOf(无)", xslice.IndexOf(s, 9), -1)
	eqI(t, "IndexOfBy", xslice.IndexOfBy(s, func(v int) bool { return v == 3 }), 2)
	eqI(t, "IndexOfBy(无)", xslice.IndexOfBy(s, func(v int) bool { return v == 9 }), -1)
	eqI(t, "LastIndexOf", xslice.LastIndexOf(s, 2), 3)
	eqI(t, "LastIndexOf(无)", xslice.LastIndexOf(s, 9), -1)

	if v, ok := xslice.Find(s, func(v int) bool { return v > 2 }); !ok || v != 3 {
		t.Fatalf("Find = %v,%v", v, ok)
	}
	if _, ok := xslice.Find(s, func(v int) bool { return v > 9 }); ok {
		t.Fatal("Find 未命中应为 false")
	}
	if i, ok := xslice.FindIdx(s, func(v int) bool { return v == 3 }); !ok || i != 2 {
		t.Fatalf("FindIdx = %v,%v", i, ok)
	}
	if _, ok := xslice.FindIdx(s, func(v int) bool { return false }); ok {
		t.Fatal("FindIdx 未命中应为 false")
	}
	if v, ok := xslice.FindLast(s, func(v int) bool { return v == 2 }); !ok || v != 2 {
		t.Fatalf("FindLast = %v,%v", v, ok)
	}

	eqB(t, "Any", xslice.Any(s, func(v int) bool { return v == 3 }), true)
	eqB(t, "Any(空)", xslice.Any([]int{}, func(v int) bool { return true }), false)
	eqB(t, "All(真)", xslice.All(s, func(v int) bool { return v > 0 }), true)
	eqB(t, "All(假)", xslice.All(s, func(v int) bool { return v > 1 }), false)
	eqB(t, "All(空切片为 true)", xslice.All([]int{}, func(v int) bool { return false }), true)
	eqB(t, "None", xslice.None(s, func(v int) bool { return v > 9 }), true)
	eqB(t, "None(假)", xslice.None(s, func(v int) bool { return v > 1 }), false)
	eqI(t, "Count", xslice.Count(s, func(v int) bool { return v == 2 }), 2)
}

// ---------------------------------------------------------------------------
// 分组与切分
// ---------------------------------------------------------------------------

func TestGroupAndSplit(t *testing.T) {
	grp := xslice.GroupBy([]int{1, 2, 3, 4}, func(v int) int { return v % 2 })
	if len(grp[1]) != 2 || len(grp[0]) != 2 {
		t.Fatalf("GroupBy = %v", grp)
	}
	if got := xslice.GroupBy[int, int](nil, func(v int) int { return v }); got != nil {
		t.Fatalf("GroupBy(nil) = %v", got)
	}

	order, grp2 := xslice.GroupByOrdered([]string{"b", "a", "b"}, func(v string) string { return v })
	eqStrs(t, "GroupByOrdered(order)", order, []string{"b", "a"})
	if len(grp2["b"]) != 2 {
		t.Fatalf("GroupByOrdered = %v", grp2)
	}
	if o, g := xslice.GroupByOrdered[string, string](nil, func(v string) string { return v }); o != nil || g != nil {
		t.Fatalf("GroupByOrdered(nil) = %v,%v", o, g)
	}

	yes, no := xslice.Partition([]int{1, 2, 3, 4}, func(v int) bool { return v%2 == 0 })
	eqInts(t, "Partition(yes)", yes, []int{2, 4})
	eqInts(t, "Partition(no)", no, []int{1, 3})
	if y, n := xslice.Partition[int](nil, func(v int) bool { return true }); y != nil || n != nil {
		t.Fatalf("Partition(nil) = %v,%v", y, n)
	}

	chunks := xslice.Chunk([]int{1, 2, 3, 4, 5}, 2)
	if len(chunks) != 3 {
		t.Fatalf("Chunk = %v", chunks)
	}
	eqInts(t, "Chunk[0]", chunks[0], []int{1, 2})
	eqInts(t, "Chunk[2]", chunks[2], []int{5})
	if got := xslice.Chunk([]int{1}, 0); got != nil {
		t.Fatalf("Chunk(size=0) = %v", got)
	}
	if got := xslice.Chunk([]int{1}, 5); len(got) != 1 {
		t.Fatalf("Chunk(size>len) = %v", got)
	}

	w := xslice.Window([]int{1, 2, 3, 4}, 3)
	if len(w) != 2 {
		t.Fatalf("Window = %v", w)
	}
	eqInts(t, "Window[0]", w[0], []int{1, 2, 3})
	eqInts(t, "Window[1]", w[1], []int{2, 3, 4})
	w2 := xslice.Window([]int{1, 2, 3, 4}, 2, 2)
	if len(w2) != 2 {
		t.Fatalf("Window(step=2) = %v", w2)
	}
	eqInts(t, "Window(step=2)[1]", w2[1], []int{3, 4})
	if got := xslice.Window([]int{1}, 2); got != nil {
		t.Fatalf("Window(size>len) = %v", got)
	}
	if got := xslice.Window([]int{1, 2}, 0); got != nil {
		t.Fatalf("Window(size=0) = %v", got)
	}

	l, r := xslice.SplitAt([]int{1, 2, 3}, 1)
	eqInts(t, "SplitAt(left)", l, []int{1})
	eqInts(t, "SplitAt(right)", r, []int{2, 3})
	l, r = xslice.SplitAt([]int{1, 2}, -5)
	eqInts(t, "SplitAt(夹到 0)", l, nil)
	eqInts(t, "SplitAt(夹到 0).right", r, []int{1, 2})
	l, r = xslice.SplitAt([]int{1, 2}, 99)
	eqInts(t, "SplitAt(夹到 len)", l, []int{1, 2})
	eqInts(t, "SplitAt(夹到 len).right", r, nil)

	parts := xslice.SplitBy([]int{1, 0, 2, 0, 3}, func(v int) bool { return v == 0 })
	if len(parts) != 3 {
		t.Fatalf("SplitBy = %v", parts)
	}
	eqInts(t, "SplitBy[0]", parts[0], []int{1})
	eqInts(t, "SplitBy[2]", parts[2], []int{3})
	if got := xslice.SplitBy([]int{0, 0}, func(v int) bool { return v == 0 }); got != nil {
		t.Fatalf("SplitBy(全分隔) = %v", got)
	}

	eqInts(t, "Flatten", xslice.Flatten([][]int{{1, 2}, {3}, nil, {4}}), []int{1, 2, 3, 4})
	if got := xslice.Flatten([][]int{nil, {}}); got != nil {
		t.Fatalf("Flatten(空) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 集合运算
// ---------------------------------------------------------------------------

func TestSetOperations(t *testing.T) {
	eqInts(t, "Union", xslice.Union([]int{1, 2}, []int{2, 3}, []int{3, 4}), []int{1, 2, 3, 4})
	eqInts(t, "Union(单)", xslice.Union([]int{1, 1}), []int{1})
	if got := xslice.Union[int](); got != nil {
		t.Fatalf("Union() = %v", got)
	}

	eqInts(t, "Intersect", xslice.Intersect([]int{1, 2, 3}, []int{2, 3, 4}), []int{2, 3})
	eqInts(t, "Intersect(三组)", xslice.Intersect([]int{1, 2}, []int{2, 3}, []int{2, 4}), []int{2})
	eqInts(t, "Intersect(单组去重)", xslice.Intersect([]int{1, 1, 2}), []int{1, 2})
	eqInts(t, "Intersect(无交集)", xslice.Intersect([]int{1}, []int{2}), []int{})
	if got := xslice.Intersect[int](); got != nil {
		t.Fatalf("Intersect() = %v", got)
	}
	if got := xslice.Intersect([]int{}, []int{1}); got != nil {
		t.Fatalf("Intersect(空首组) = %v", got)
	}

	eqInts(t, "Difference", xslice.Difference([]int{1, 2, 3}, []int{2}), []int{1, 3})
	eqInts(t, "Difference(去重)", xslice.Difference([]int{1, 1, 2}, nil), []int{1, 2})
	if got := xslice.Difference[int](nil, []int{1}); got != nil {
		t.Fatalf("Difference(空) = %v", got)
	}
	eqInts(t, "SymmetricDifference", xslice.SymmetricDifference([]int{1, 2}, []int{2, 3}), []int{1, 3})

	eqB(t, "IsSubset", xslice.IsSubset([]int{1, 2}, []int{1, 2, 3}), true)
	eqB(t, "IsSubset(否)", xslice.IsSubset([]int{1, 9}, []int{1, 2}), false)
	eqB(t, "IsSubset(空集)", xslice.IsSubset([]int{}, []int{1}), true)

	eqB(t, "Overlaps", xslice.Overlaps([]int{1}, []int{1, 2}), true)
	eqB(t, "Overlaps(否)", xslice.Overlaps([]int{1}, []int{2}), false)
	eqB(t, "Overlaps(空)", xslice.Overlaps(nil, []int{1}), false)

	eqB(t, "Equal(顺序相同)", xslice.Equal([]int{1, 2}, []int{1, 2}), true)
	eqB(t, "Equal(顺序不同)", xslice.Equal([]int{1, 2}, []int{2, 1}), false)
	eqB(t, "EqualUnordered", xslice.EqualUnordered([]int{1, 2, 2}, []int{2, 1, 2}), true)
	eqB(t, "EqualUnordered(重复数不同)", xslice.EqualUnordered([]int{1, 2}, []int{1, 2, 2}), false)
	eqB(t, "EqualUnordered(元素不同)", xslice.EqualUnordered([]int{1, 2}, []int{1, 3}), false)
	eqB(t, "EqualUnordered(空)", xslice.EqualUnordered(nil, []int{}), true)
}

// ---------------------------------------------------------------------------
// 顺序操作
// ---------------------------------------------------------------------------

func TestOrderOps(t *testing.T) {
	src := []int{1, 2, 3}
	rev := xslice.Reverse(src)
	eqInts(t, "Reverse", rev, []int{3, 2, 1})
	eqInts(t, "Reverse 不应修改入参", src, []int{1, 2, 3})
	if got := xslice.Reverse[int](nil); got != nil {
		t.Fatalf("Reverse(nil) = %v", got)
	}

	inp := []int{1, 2, 3}
	xslicetSliceReverseInPlace(inp)
	eqInts(t, "ReverseInPlace", inp, []int{3, 2, 1})

	eqInts(t, "Rotate(左2)", xslice.Rotate([]int{1, 2, 3, 4, 5}, 2), []int{3, 4, 5, 1, 2})
	eqInts(t, "Rotate(右2)", xslice.Rotate([]int{1, 2, 3, 4, 5}, -2), []int{4, 5, 1, 2, 3})
	eqInts(t, "Rotate(0)", xslice.Rotate([]int{1, 2, 3}, 0), []int{1, 2, 3})
	eqInts(t, "Rotate(超长)", xslice.Rotate([]int{1, 2, 3}, 7), []int{2, 3, 1})
	eqInts(t, "Rotate(单元素)", xslice.Rotate([]int{1}, 5), []int{1})
	if got := xslice.Rotate[int](nil, 1); got != nil {
		t.Fatalf("Rotate(nil) = %v", got)
	}

	eqInts(t, "Take", xslice.Take([]int{1, 2, 3}, 2), []int{1, 2})
	eqInts(t, "Take(超长)", xslice.Take([]int{1, 2}, 5), []int{1, 2})
	if got := xslice.Take([]int{1}, 0); got != nil {
		t.Fatalf("Take(0) = %v", got)
	}
	eqInts(t, "TakeLast", xslice.TakeLast([]int{1, 2, 3}, 2), []int{2, 3})
	eqInts(t, "TakeLast(超长)", xslice.TakeLast([]int{1, 2}, 5), []int{1, 2})
	eqInts(t, "TakeWhile", xslice.TakeWhile([]int{1, 2, 3, 1}, func(v int) bool { return v < 3 }), []int{1, 2})
	eqInts(t, "TakeWhile(全满足)", xslice.TakeWhile([]int{1, 2}, func(v int) bool { return v < 3 }), []int{1, 2})
	eqInts(t, "DropWhile", xslice.DropWhile([]int{1, 2, 3, 1}, func(v int) bool { return v < 3 }), []int{3, 1})
	if got := xslice.DropWhile([]int{1, 2}, func(v int) bool { return v < 3 }); got != nil {
		t.Fatalf("DropWhile(全满足) = %v", got)
	}
	eqInts(t, "Skip", xslice.Skip([]int{1, 2, 3}, 1), []int{2, 3})
	eqInts(t, "Skip(0)", xslice.Skip([]int{1, 2}, 0), []int{1, 2})
	if got := xslice.Skip([]int{1, 2}, 5); got != nil {
		t.Fatalf("Skip(超长) = %v", got)
	}
	eqInts(t, "SkipWhile", xslice.SkipWhile([]int{1, 2, 3}, func(v int) bool { return v < 2 }), []int{2, 3})

	if v, ok := xslice.Head([]int{1, 2}); !ok || v != 1 {
		t.Fatalf("Head = %v,%v", v, ok)
	}
	if _, ok := xslice.Head[int](nil); ok {
		t.Fatal("Head(nil) 应为 false")
	}
	if v, ok := xslice.Last([]int{1, 2}); !ok || v != 2 {
		t.Fatalf("Last = %v,%v", v, ok)
	}
	if _, ok := xslice.Last[int](nil); ok {
		t.Fatal("Last(nil) 应为 false")
	}
	eqInts(t, "Tail", xslice.Tail([]int{1, 2, 3}), []int{2, 3})
	if got := xslice.Tail([]int{1}); got != nil {
		t.Fatalf("Tail(单元素) = %v", got)
	}

	// Shuffle 只打乱顺序，不改元素集合
	orig := []int{1, 2, 3, 4, 5}
	xslicetSliceShuffleAsCopy(orig)
	if !xslice.EqualUnordered(orig, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("Shuffle 改变了元素集合: %v", orig)
	}
	// 自定义随机源：确定性
	s1 := xslice.ShuffleCopyN([]int{1, 2, 3}, func(n int) int { return 0 })
	s2 := xslice.ShuffleCopyN([]int{1, 2, 3}, func(n int) int { return 0 })
	eqInts(t, "ShuffleCopyN 应确定性", s1, s2)
	srcUnchanged := []int{1, 2, 3}
	_ = xslice.ShuffleCopy(srcUnchanged)
	eqInts(t, "ShuffleCopy 不应修改入参", srcUnchanged, []int{1, 2, 3})
	if got := xslice.ShuffleCopyN([]int{1, 2}, nil); len(got) != 2 {
		t.Fatalf("ShuffleCopyN(nil src) = %v", got)
	}
	// 非法随机源不应越界 panic
	_ = xslice.ShuffleCopyN([]int{1, 2, 3}, func(n int) int { return n + 100 })
}

func xslicetSliceReverseInPlace(s []int) { xslice.ReverseInPlace(s) }
func xslicetSliceShuffleAsCopy(s []int)  { out := xslice.ShuffleCopy(s); copy(s, out) }

// ---------------------------------------------------------------------------
// 增删改
// ---------------------------------------------------------------------------

func TestMutate(t *testing.T) {
	eqInts(t, "Insert(中间)", xslice.Insert([]int{1, 3}, 1, 2), []int{1, 2, 3})
	eqInts(t, "Insert(头部)", xslice.Insert([]int{2}, 0, 1), []int{1, 2})
	eqInts(t, "Insert(尾部)", xslice.Insert([]int{1}, 5, 2), []int{1, 2})
	eqInts(t, "Insert(多个)", xslice.Insert([]int{1, 4}, 1, 2, 3), []int{1, 2, 3, 4})
	eqInts(t, "Insert(负数下标)", xslice.Insert([]int{1}, -3, 0), []int{0, 1})
	eqInts(t, "Insert(无元素)", xslice.Insert([]int{1}, 0), []int{1})

	eqInts(t, "RemoveAt", xslice.RemoveAt([]int{1, 2, 3}, 1), []int{1, 3})
	eqInts(t, "RemoveAt(越界)", xslice.RemoveAt([]int{1, 2}, 9), []int{1, 2})
	eqInts(t, "Remove", xslice.Remove([]int{1, 2, 3}, 2), []int{1, 3})
	eqInts(t, "Remove(不存在)", xslice.Remove([]int{1, 2}, 9), []int{1, 2})
	eqInts(t, "RemoveAll", xslice.RemoveAll([]int{1, 2, 3, 2}, 2), []int{1, 3})
	eqInts(t, "RemoveAll(无参)", xslice.RemoveAll([]int{1, 2}), []int{1, 2})
	eqInts(t, "RemoveBy", xslice.RemoveBy([]int{1, 2, 3}, func(v int) bool { return v%2 == 0 }), []int{1, 3})

	eqInts(t, "Replace(全部)", xslice.Replace([]int{1, 2, 1}, 1, 9), []int{9, 2, 9})
	eqInts(t, "Replace(限次)", xslice.Replace([]int{1, 1, 1}, 1, 9, 2), []int{9, 9, 1})
	eqInts(t, "Replace(限次=0 视为全部)", xslice.Replace([]int{1, 1}, 1, 9, 0), []int{9, 9})

	eqInts(t, "Concat", xslice.Concat([]int{1}, []int{2, 3}, nil), []int{1, 2, 3})
	if got := xslice.Concat[int](); got != nil {
		t.Fatalf("Concat() = %v", got)
	}
	eqInts(t, "Repeat", xslice.Repeat(7, 3), []int{7, 7, 7})
	if got := xslice.Repeat(7, 0); got != nil {
		t.Fatalf("Repeat(0) = %v", got)
	}
	eqInts(t, "Pad", xslice.Pad([]int{1}, 3, 0), []int{1, 0, 0})
	eqInts(t, "Pad(已够长)", xslice.Pad([]int{1, 2, 3}, 2, 0), []int{1, 2, 3})

	src := []int{1, 2, 3}
	cp := xslice.Clone(src)
	cp[0] = 99
	eqInts(t, "Clone 应解耦", src, []int{1, 2, 3})
	if xslice.Clone[int](nil) != nil {
		t.Fatal("Clone(nil) 应为 nil")
	}
	if got := xslice.Clone([]int{}); got == nil {
		t.Fatal("Clone(空) 应返回非 nil 空切片")
	}

	nested := [][]int{{1, 2}}
	deep := xslice.CloneDeep(nested)
	deep[0][0] = 99
	if nested[0][0] != 1 {
		t.Fatal("CloneDeep 未解耦嵌套切片")
	}
}

// ---------------------------------------------------------------------------
// 排序
// ---------------------------------------------------------------------------

func TestSorting(t *testing.T) {
	eqInts(t, "SortBy", xslice.SortBy([]int{3, 1, 2}, func(v int) int { return v }), []int{1, 2, 3})
	eqInts(t, "SortBy(降序)", xslice.SortBy([]int{3, 1, 2}, func(v int) int { return v }, true), []int{3, 2, 1})
	eqInts(t, "SortStableBy", xslice.SortStableBy([]int{3, 1, 2}, func(v int) int { return v }), []int{1, 2, 3})

	// 稳定性：按奇偶排序应保持原相对顺序
	eqInts(t, "SortBy(稳定性)", xslice.SortBy([]int{1, 2, 3, 4, 5}, func(v int) int { return v % 2 }), []int{2, 4, 1, 3, 5})
	src := []int{3, 1}
	_ = xslice.SortBy(src, func(v int) int { return v })
	eqInts(t, "SortBy 不应修改入参", src, []int{3, 1})
	if got := xslice.SortBy[int, int](nil, func(v int) int { return v }); got != nil {
		t.Fatalf("SortBy(nil) = %v", got)
	}

	type rec struct {
		A, B int
	}
	recs := []rec{{2, 1}, {1, 1}, {2, 2}, {1, 2}}
	got := xslice.SortMulti(recs,
		func(x, y rec) int { return x.A - y.A },
		func(x, y rec) int { return y.B - x.B },
	)
	want := []rec{{1, 2}, {1, 1}, {2, 2}, {2, 1}}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("SortMulti = %v, want %v", got, want)
		}
	}
	// 比较器返回 0 时继续下一个
	got2 := xslice.SortMulti([]int{2, 1, 3}, func(a, b int) int { return 0 })
	eqInts(t, "SortMulti(全 0 比较器)", got2, []int{2, 1, 3})
	if got := xslice.SortMulti([]int{2, 1}); len(got) != 2 {
		t.Fatalf("SortMulti(无比较器) = %v", got)
	}

	eqInts(t, "SortOrdered", xslice.SortOrdered([]int{3, 1, 2}), []int{1, 2, 3})
	inp := []int{3, 1, 2}
	xslicetSliceSortInPlace(inp)
	eqInts(t, "SortOrderedInPlace", inp, []int{1, 2, 3})

	if v, ok := xslice.MinBy([]string{"bbb", "a", "cc"}, func(s string) int { return len(s) }); !ok || v != "a" {
		t.Fatalf("MinBy = %q,%v", v, ok)
	}
	if v, ok := xslice.MaxBy([]string{"bbb", "a", "cc"}, func(s string) int { return len(s) }); !ok || v != "bbb" {
		t.Fatalf("MaxBy = %q,%v", v, ok)
	}
	if _, ok := xslice.MinBy([]int{}, func(v int) int { return v }); ok {
		t.Fatal("MinBy(空) 应为 false")
	}
	if _, ok := xslice.MaxBy([]int{}, func(v int) int { return v }); ok {
		t.Fatal("MaxBy(空) 应为 false")
	}
}

func xslicetSliceSortInPlace(s []int) { xslice.SortOrderedInPlace(s) }

// ---------------------------------------------------------------------------
// 聚合
// ---------------------------------------------------------------------------

func TestAggregate(t *testing.T) {
	eqI(t, "Sum", xslice.Sum([]int{1, 2, 3}), 6)
	eqI(t, "Sum(空)", xslice.Sum([]int{}), 0)
	if got := xslice.Average([]int{1, 2, 3, 4}); got != 2.5 {
		t.Fatalf("Average = %v", got)
	}
	if got := xslice.Average([]int{}); got != 0 {
		t.Fatalf("Average(空) = %v", got)
	}

	if v, ok := xslice.Min([]int{3, 1, 2}); !ok || v != 1 {
		t.Fatalf("Min = %v,%v", v, ok)
	}
	if v, ok := xslice.Max([]int{3, 1, 2}); !ok || v != 3 {
		t.Fatalf("Max = %v,%v", v, ok)
	}
	if _, ok := xslice.Min([]int{}); ok {
		t.Fatal("Min(空) 应为 false")
	}
	if _, ok := xslice.Max([]int{}); ok {
		t.Fatal("Max(空) 应为 false")
	}
	mn, mx, ok := xslice.MinMax([]int{3, 1, 2})
	if !ok || mn != 1 || mx != 3 {
		t.Fatalf("MinMax = %v,%v,%v", mn, mx, ok)
	}
	if _, _, ok := xslice.MinMax([]string{}); ok {
		t.Fatal("MinMax(空) 应为 false")
	}
	// 字符串也可比较
	if v, _ := xslice.Min([]string{"b", "a"}); v != "a" {
		t.Fatalf("Min(字符串) = %q", v)
	}
}

// ---------------------------------------------------------------------------
// 键值对与集合转换
// ---------------------------------------------------------------------------

func TestZipAndConvert(t *testing.T) {
	pairs := xslice.Zip([]int{1, 2, 3}, []string{"a", "b"})
	if len(pairs) != 2 {
		t.Fatalf("Zip = %v", pairs)
	}
	if pairs[0].Key != 1 || pairs[0].Val != "a" {
		t.Fatalf("Zip[0] = %v", pairs[0])
	}
	if got := xslice.Zip([]int{}, []string{"a"}); got != nil {
		t.Fatalf("Zip(空) = %v", got)
	}
	// 不可比较的元素也可 Zip
	p2 := xslice.Zip([][]int{{1}}, []string{"a"})
	if len(p2) != 1 || p2[0].Key[0] != 1 {
		t.Fatalf("Zip(不可比较元素) = %v", p2)
	}

	as, bs := xslice.Unzip(pairs)
	eqInts(t, "Unzip(keys)", as, []int{1, 2})
	eqStrs(t, "Unzip(vals)", bs, []string{"a", "b"})
	if k, v := xslice.Unzip[string, int](nil); k != nil || v != nil {
		t.Fatalf("Unzip(nil) = %v,%v", k, v)
	}

	m := xslice.ToMap([]int{1, 2}, func(v int) string { return strings.Repeat("x", v) }, func(v int) int { return v * 10 })
	if m["x"] != 10 || m["xx"] != 20 {
		t.Fatalf("ToMap = %v", m)
	}

	set := xslice.ToSet([]int{1, 1, 2})
	if len(set) != 2 {
		t.Fatalf("ToSet = %v", set)
	}
	if got := xslice.ToSet[int](nil); got != nil {
		t.Fatalf("ToSet(nil) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 随机
// ---------------------------------------------------------------------------

func TestRandAndSample(t *testing.T) {
	if _, ok := xslice.Rand([]int{1}); !ok {
		t.Fatal("Rand 应返回元素")
	}
	if _, ok := xslice.Rand[int](nil); ok {
		t.Fatal("Rand(nil) 应为 false")
	}
	for i := 0; i < 100; i++ {
		v, ok := xslice.Rand([]int{1, 2, 3})
		if !ok || v < 1 || v > 3 {
			t.Fatalf("Rand 越界: %v", v)
		}
	}

	s := xslice.Sample([]int{1, 2, 3, 4, 5}, 3)
	if len(s) != 3 || !xslice.IsSubset(s, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("Sample = %v", s)
	}
	if got := xslice.Sample([]int{1}, 0); got != nil {
		t.Fatalf("Sample(0) = %v", got)
	}
	all := xslice.Sample([]int{1, 2}, 5)
	if len(all) != 2 || !xslice.EqualUnordered(all, []int{1, 2}) {
		t.Fatalf("Sample(超量) = %v", all)
	}
	if got := xslice.SampleN([]int{1, 2, 3}, 2, func(n int) int { return 0 }); len(got) != 2 {
		t.Fatalf("SampleN = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 综合场景：Excel 导出去重 + 排序 + 分组
// ---------------------------------------------------------------------------

type row struct {
	Dept string
	Name string
	Age  int
}

func TestRealisticPipeline(t *testing.T) {
	rows := []row{
		{"研发", "张三", 30},
		{"研发", "李四", 25},
		{"销售", "王五", 28},
		{"研发", "张三", 30}, // 重复行
		{"销售", "赵六", 35},
	}

	// 1. 按「部门+姓名」去重，保持首次出现顺序
	uniq := xslice.DedupBy(rows, func(r row) string { return r.Dept + "/" + r.Name })
	eqI(t, "去重后行数", len(uniq), 4)

	// 2. 按部门分组，组内按年龄升序
	order, groups := xslice.GroupByOrdered(uniq, func(r row) string { return r.Dept })
	eqStrs(t, "部门出现顺序", order, []string{"研发", "销售"})
	dev := xslice.SortBy(groups["研发"], func(r row) int { return r.Age })
	eqStrs(t, "研发组按年龄排序", xslice.Map(dev, func(r row) string { return r.Name }), []string{"李四", "张三"})

	// 3. 统计各部门人数
	counts := xslice.CountBy(uniq, func(r row) string { return r.Dept })
	if counts["研发"] != 2 || counts["销售"] != 2 {
		t.Fatalf("CountBy = %v", counts)
	}

	// 4. 平均年龄
	if avg := xslice.Average(xslice.Map(uniq, func(r row) int { return r.Age })); avg != 29.5 {
		t.Fatalf("平均年龄 = %v", avg)
	}

	// 5. 分块导出
	if chunks := xslice.Chunk(uniq, 3); len(chunks) != 2 || len(chunks[1]) != 1 {
		t.Fatalf("Chunk = %v", chunks)
	}

	// 6. 与 xmap 协作
	byName := xmap.FromSliceSelf(uniq, func(r row) string { return r.Name })
	if byName["张三"].Dept != "研发" {
		t.Fatalf("xmap.FromSliceSelf = %v", byName)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func benchSlice(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i % 137
	}
	return s
}

func BenchmarkDedup(b *testing.B) {
	s := benchSlice(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xslice.Dedup(s)
	}
}

func BenchmarkFilter(b *testing.B) {
	s := benchSlice(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xslice.Filter(s, func(v int) bool { return v%2 == 0 })
	}
}

func BenchmarkSortBy(b *testing.B) {
	s := benchSlice(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xslice.SortBy(s, func(v int) int { return v })
	}
}

func BenchmarkUnion(b *testing.B) {
	a, c := benchSlice(500), benchSlice(500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xslice.Union(a, c)
	}
}

var _ = sort.Ints
