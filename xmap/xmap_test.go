package xmap_test

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xmap"
)

func eqI(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

func eqB(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func eqKV(t *testing.T, name string, got, want map[string]int) {
	t.Helper()
	if !xmap.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// 基础查询
// ---------------------------------------------------------------------------

func TestBasicQueries(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2, "c": 3}

	eqI(t, "Len", xmap.Len(m), 3)
	eqI(t, "Len(nil)", xmap.Len[string, int](nil), 0)
	eqB(t, "IsEmpty(空)", xmap.IsEmpty(map[string]int{}), true)
	eqB(t, "IsEmpty(nil)", xmap.IsEmpty[string, int](nil), true)
	eqB(t, "IsEmpty(非空)", xmap.IsEmpty(m), false)

	keys := xmap.KeysSorted(m)
	if len(keys) != 3 || keys[0] != "a" || keys[2] != "c" {
		t.Fatalf("KeysSorted = %v", keys)
	}
	if got := xmap.KeysSortedBy(m, true); got[0] != "c" || got[2] != "a" {
		t.Fatalf("KeysSortedBy(desc) = %v", got)
	}
	if got := xmap.Keys[string, int](nil); got != nil {
		t.Fatalf("Keys(nil) = %v", got)
	}

	vals := xmap.ValuesSortedByKey(m)
	if len(vals) != 3 || vals[0] != 1 || vals[2] != 3 {
		t.Fatalf("ValuesSortedByKey = %v", vals)
	}
	if got := xmap.Values[string, int](nil); got != nil {
		t.Fatalf("Values(nil) = %v", got)
	}

	entries := xmap.EntriesSorted(m)
	if len(entries) != 3 || entries[0].Key != "a" || entries[0].Val != 1 {
		t.Fatalf("EntriesSorted = %v", entries)
	}
	if got := xmap.Entries[string, int](nil); got != nil {
		t.Fatalf("Entries(nil) = %v", got)
	}

	eqB(t, "Has(有)", xmap.Has(m, "a"), true)
	eqB(t, "Has(无)", xmap.Has(m, "z"), false)

	if v, ok := xmap.Get(m, "b"); !ok || v != 2 {
		t.Fatalf("Get = %v,%v", v, ok)
	}
	if _, ok := xmap.Get(m, "z"); ok {
		t.Fatal("Get(不存在) 应为 false")
	}
	eqI(t, "GetOr(有)", xmap.GetOr(m, "a", 9), 1)
	eqI(t, "GetOr(无)", xmap.GetOr(m, "z", 9), 9)

	eqI(t, "GetOrSet(有)", xmap.GetOrSet(m, "a", 99), 1)
	eqI(t, "GetOrSet(无)", xmap.GetOrSet(m, "d", 4), 4)
	eqI(t, "GetOrSet 已写入", m["d"], 4)
	delete(m, "d")

	if v, ok := xmap.Delete(m, "a"); !ok || v != 1 {
		t.Fatalf("Delete = %v,%v", v, ok)
	}
	if _, ok := m["a"]; ok {
		t.Fatal("Delete 未生效")
	}
	if _, ok := xmap.Delete(m, "a"); ok {
		t.Fatal("重复 Delete 应为 false")
	}

	xmap.SetDefault(m, "b", 99) // 已存在，不覆盖
	eqI(t, "SetDefault(已存在)", m["b"], 2)
	xmap.SetDefault(m, "e", 5)
	eqI(t, "SetDefault(不存在)", m["e"], 5)
}

// SortOrdered 是自研排序，必须与标准库结果一致
func TestSortOrderedMatchesStdlib(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 200; trial++ {
		n := rng.Intn(60)
		s := make([]int, n)
		for i := range s {
			s[i] = rng.Intn(10) // 大量重复值，覆盖相等元素路径
		}
		want := append([]int(nil), s...)
		sort.SliceStable(want, func(i, j int) bool { return want[i] < want[j] })

		xmap.SortOrdered(s)
		if len(s) != len(want) {
			t.Fatalf("长度不一致: %v vs %v", s, want)
		}
		for i := range s {
			if s[i] != want[i] {
				t.Fatalf("trial=%d 排序结果不一致:\n got=%v\nwant=%v", trial, s, want)
			}
		}
	}
	// 边界：空 / 单元素 / 已排序 / 逆序
	for _, c := range [][]int{nil, {}, {1}, {1, 2, 3}, {3, 2, 1}, {2, 2, 2}} {
		got := append([]int(nil), c...)
		xmap.SortOrdered(got)
		want := append([]int(nil), c...)
		sort.Ints(want)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("边界排序失败: %v → %v (want %v)", c, got, want)
			}
		}
	}
	// 字符串与浮点
	ss := []string{"b", "a", "c", "a"}
	xmap.SortOrdered(ss)
	if ss[0] != "a" || ss[1] != "a" || ss[2] != "b" || ss[3] != "c" {
		t.Fatalf("字符串排序 = %v", ss)
	}
}

// ---------------------------------------------------------------------------
// 变换
// ---------------------------------------------------------------------------

func TestTransform(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2}

	got := xmap.MapValues(m, func(v int) string { return fmt.Sprint(v * 10) })
	if got["a"] != "10" || got["b"] != "20" {
		t.Fatalf("MapValues = %v", got)
	}
	got2 := xmap.MapValuesKV(m, func(k string, v int) string { return k + fmt.Sprint(v) })
	if got2["a"] != "a1" {
		t.Fatalf("MapValuesKV = %v", got2)
	}
	got3 := xmap.MapKeys(m, func(k string, v int) int { return v })
	if got3[1] != 1 || got3[2] != 2 {
		t.Fatalf("MapKeys = %v", got3)
	}

	eqKV(t, "Filter", xmap.Filter(m, func(k string, v int) bool { return v > 1 }), map[string]int{"b": 2})
	eqKV(t, "FilterKeys", xmap.FilterKeys(m, func(k string) bool { return k == "a" }), map[string]int{"a": 1})
	eqKV(t, "FilterValues", xmap.FilterValues(m, func(v int) bool { return v%2 == 0 }), map[string]int{"b": 2})

	inv := xmap.Invert(m)
	if inv[1] != "a" || inv[2] != "b" {
		t.Fatalf("Invert = %v", inv)
	}
	eqKV(t, "Pick", xmap.Pick(m, "a", "zzz"), map[string]int{"a": 1})
	eqKV(t, "Omit", xmap.Omit(m, "a"), map[string]int{"b": 2})
	if got := xmap.Omit(m); len(got) != 2 {
		t.Fatalf("Omit(无参) = %v", got)
	}

	grp := xmap.GroupByValue(m, func(k string, v int) string {
		if v%2 == 0 {
			return "even"
		}
		return "odd"
	})
	if len(grp["odd"]) != 1 || len(grp["even"]) != 1 {
		t.Fatalf("GroupByValue = %v", grp)
	}

	// nil 输入
	if got := xmap.MapValues(map[string]int(nil), func(v int) int { return v }); got != nil {
		t.Fatal("MapValues(nil) 应为 nil")
	}
	if got := xmap.Filter(map[string]int(nil), func(string, int) bool { return true }); got != nil {
		t.Fatal("Filter(nil) 应为 nil")
	}
}

// ---------------------------------------------------------------------------
// 合并与集合运算
// ---------------------------------------------------------------------------

func TestMerge(t *testing.T) {
	a := map[string]int{"x": 1, "y": 2}
	b := map[string]int{"y": 20, "z": 3}

	eqKV(t, "Merge", xmap.Merge(a, b), map[string]int{"x": 1, "y": 20, "z": 3})
	eqKV(t, "MergeKeepFirst", xmap.MergeKeepFirst(a, b), map[string]int{"x": 1, "y": 2, "z": 3})
	eqKV(t, "MergeWith", xmap.MergeWith(func(old, new int) int { return old + new }, a, b),
		map[string]int{"x": 1, "y": 22, "z": 3})
	eqKV(t, "MergeWith(nil resolve)", xmap.MergeWith(nil, a, b),
		map[string]int{"x": 1, "y": 20, "z": 3})
	if got := xmap.Merge[string, int](); len(got) != 0 {
		t.Fatalf("Merge() = %v", got)
	}

	// 原 map 不应被修改
	eqKV(t, "Merge 不应修改入参 a", a, map[string]int{"x": 1, "y": 2})
	eqKV(t, "Merge 不应修改入参 b", b, map[string]int{"y": 20, "z": 3})
}

func TestMergeDeep(t *testing.T) {
	base := map[string]any{
		"db":   map[string]any{"host": "localhost", "port": 5432},
		"name": "svc",
		"list": []any{1, 2},
	}
	override := map[string]any{
		"db":   map[string]any{"port": 3306, "user": "root"},
		"name": "svc2",
	}
	got := xmap.MergeDeep(base, override)

	db := got["db"].(map[string]any)
	if db["host"] != "localhost" || db["port"] != 3306 || db["user"] != "root" {
		t.Fatalf("MergeDeep db = %v", db)
	}
	if got["name"] != "svc2" {
		t.Fatalf("MergeDeep name = %v", got["name"])
	}
	if got["list"].([]any)[0] != 1 {
		t.Fatalf("MergeDeep 非 map 值应被保留: %v", got["list"])
	}
	// 入参不应被修改
	if base["db"].(map[string]any)["port"] != 5432 {
		t.Fatal("MergeDeep 修改了 base")
	}
	// 单层 override 覆盖 map
	got2 := xmap.MergeDeep(map[string]any{"a": 1}, map[string]any{"a": map[string]any{"b": 2}})
	if got2["a"].(map[string]any)["b"] != 2 {
		t.Fatalf("MergeDeep 类型变化应覆盖: %v", got2)
	}
}

func TestSetOperations(t *testing.T) {
	a := map[string]int{"x": 1, "y": 2, "z": 3}
	b := map[string]int{"y": 2, "z": 99, "w": 4}

	eqKV(t, "Diff", xmap.Diff(a, b), map[string]int{"x": 1, "z": 3})
	eqKV(t, "IntersectKeys", xmap.IntersectKeys(a, b), map[string]int{"y": 2, "z": 3})

	onlyA, onlyB, both := xmap.DiffKeys(a, b)
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	sort.Strings(both)
	if strings.Join(onlyA, ",") != "x" ||
		strings.Join(onlyB, ",") != "w" ||
		strings.Join(both, ",") != "y,z" {
		t.Fatalf("DiffKeys = %v %v %v", onlyA, onlyB, both)
	}

	eqB(t, "Equal(相同)", xmap.Equal(a, map[string]int{"z": 3, "y": 2, "x": 1}), true)
	eqB(t, "Equal(不同值)", xmap.Equal(a, b), false)
	eqB(t, "Equal(长度不同)", xmap.Equal(a, map[string]int{"x": 1}), false)
	eqB(t, "Equal(都为空)", xmap.Equal(map[string]int{}, map[string]int(nil)), true)
}

// ---------------------------------------------------------------------------
// 遍历
// ---------------------------------------------------------------------------

func TestForEachSorted(t *testing.T) {
	m := map[int]string{3: "c", 1: "a", 2: "b"}

	var got []string
	xmap.ForEachSorted(m, func(k int, v string) { got = append(got, fmt.Sprintf("%d%s", k, v)) })
	if strings.Join(got, ",") != "1a,2b,3c" {
		t.Fatalf("ForEachSorted = %v", got)
	}

	var got2 []int
	xmap.ForEachSortedBy(m, func(a, b int) bool { return a > b }, func(k int, _ string) {
		got2 = append(got2, k)
	})
	if len(got2) != 3 || got2[0] != 3 || got2[2] != 1 {
		t.Fatalf("ForEachSortedBy = %v", got2)
	}

	// nil 函数安全
	xmap.ForEach(m, nil)
	xmap.ForEachSorted(m, nil)
	xmap.ForEachSortedBy(m, nil, nil)
	xmap.ForEachSortedBy(m, func(a, b int) bool { return a < b }, nil)

	// 空 map 不 panic
	xmap.ForEachSorted(map[int]string(nil), func(int, string) {})
}

// ---------------------------------------------------------------------------
// 嵌套路径
// ---------------------------------------------------------------------------

func TestPath(t *testing.T) {
	m := map[string]any{
		"user": map[string]any{
			"name": "bob",
			"tags": []any{"a", "b", "c"},
			"addr": map[string]any{"city": "sh"},
		},
		"n": 1,
	}

	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"user.name", "bob", true},
		{"user.addr.city", "sh", true},
		{"user.tags[1]", "b", true},
		{"user.tags.0", "a", true},
		{"n", 1, true},
		{"user.tags[9]", nil, false},
		{"user.tags[-1]", nil, false},
		{"user.tags[x]", nil, false},
		{"user.missing", nil, false},
		{"user.name.deep", nil, false},
		{"", nil, false},
		{"missing", nil, false},
	}
	for _, c := range cases {
		got, ok := xmap.GetPath(m, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("GetPath(%q) = %v,%v, want %v,%v", c.path, got, ok, c.want, c.ok)
		}
	}

	if got := xmap.GetPathOr(m, "user.name", "def"); got != "bob" {
		t.Fatalf("GetPathOr(命中) = %v", got)
	}
	if got := xmap.GetPathOr(m, "missing", "def"); got != "def" {
		t.Fatalf("GetPathOr(未命中) = %v", got)
	}
	// 弱类型转换：JSON 数字为 float64，目标为 int
	m2 := map[string]any{"age": float64(30)}
	if got := xmap.GetPathOr(m2, "age", 0); got != 30 {
		t.Fatalf("GetPathOr 弱类型 = %v (%T)", got, got)
	}
}

func TestSetPath(t *testing.T) {
	m := map[string]any{}
	if err := xmap.SetPath(m, "a.b.c", 1); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if got, _ := xmap.GetPath(m, "a.b.c"); got != 1 {
		t.Fatalf("SetPath 后 GetPath = %v", got)
	}
	// 覆盖已有值
	if err := xmap.SetPath(m, "a.b.c", 2); err != nil {
		t.Fatalf("SetPath(覆盖): %v", err)
	}
	if got, _ := xmap.GetPath(m, "a.b.c"); got != 2 {
		t.Fatalf("SetPath 覆盖失败 = %v", got)
	}
	// 顶层
	if err := xmap.SetPath(m, "x", 9); err != nil || m["x"] != 9 {
		t.Fatalf("SetPath(顶层) = %v,%v", m["x"], err)
	}

	// 错误场景
	if err := xmap.SetPath(nil, "a", 1); err == nil {
		t.Fatal("SetPath(nil) 应报错")
	}
	if err := xmap.SetPath(m, "", 1); err == nil {
		t.Fatal("SetPath(空路径) 应报错")
	}
	conflict := map[string]any{"a": "not-a-map"}
	if err := xmap.SetPath(conflict, "a.b", 1); err == nil {
		t.Fatal("中间层不是 map 应报错")
	}
	if got := xerr.CodeOf(xmap.SetPath(conflict, "a.b", 1)); got != xerr.CodeConflict {
		t.Fatalf("错误码 = %v, want CodeConflict", got)
	}
}

func TestDeletePath(t *testing.T) {
	m := map[string]any{"a": map[string]any{"b": 1, "c": 2}}
	if v, ok := xmap.DeletePath(m, "a.b"); !ok || v != 1 {
		t.Fatalf("DeletePath = %v,%v", v, ok)
	}
	if _, ok := xmap.GetPath(m, "a.b"); ok {
		t.Fatal("DeletePath 未生效")
	}
	// 未命中
	if _, ok := xmap.DeletePath(m, "a.zzz"); ok {
		t.Fatal("DeletePath 未命中应为 false")
	}
	if _, ok := xmap.DeletePath(m, "zzz.b"); ok {
		t.Fatal("DeletePath 中间节点不存在应为 false")
	}
	if _, ok := xmap.DeletePath(nil, "a"); ok {
		t.Fatal("DeletePath(nil) 应为 false")
	}
	if _, ok := xmap.DeletePath(m, ""); ok {
		t.Fatal("DeletePath(空路径) 应为 false")
	}
}

func TestFlattenUnflatten(t *testing.T) {
	m := map[string]any{
		"a": map[string]any{"b": 1, "c": map[string]any{"d": 2}},
		"e": 3,
		"f": map[string]any{}, // 空 map 保留为叶子
	}
	flat := xmap.FlattenKeys(m, ".")
	if flat["a.b"] != 1 || flat["a.c.d"] != 2 || flat["e"] != 3 {
		t.Fatalf("FlattenKeys = %v", flat)
	}
	if _, ok := flat["f"]; !ok {
		t.Fatalf("空 map 应保留为叶子: %v", flat)
	}

	back, err := xmap.UnflattenKeys(flat, ".")
	if err != nil {
		t.Fatalf("UnflattenKeys: %v", err)
	}
	if got, _ := xmap.GetPath(back, "a.c.d"); got != 2 {
		t.Fatalf("UnflattenKeys 往返失败: %v", back)
	}
	if back["e"] != 3 {
		t.Fatalf("UnflattenKeys e = %v", back["e"])
	}

	// 默认分隔符
	if got := xmap.FlattenKeys(map[string]any{"a": map[string]any{"b": 1}}, ""); got["a.b"] != 1 {
		t.Fatalf("FlattenKeys(默认 sep) = %v", got)
	}

	// 冲突场景
	_, err = xmap.UnflattenKeys(map[string]any{"a": 1, "a.b": 2}, ".")
	if err == nil {
		t.Fatal("键冲突应返回错误")
	}
	if xerr.CodeOf(err) != xerr.CodeConflict {
		t.Fatalf("冲突错误码 = %v", xerr.CodeOf(err))
	}
	// 逆序冲突：先建子 map，再遇到叶子
	if _, err := xmap.UnflattenKeys(map[string]any{"a.b": 1, "a": 2}, "."); err == nil {
		t.Fatal("另一方向的键冲突也应返回错误")
	}
}

// ---------------------------------------------------------------------------
// 克隆与构建
// ---------------------------------------------------------------------------

func TestCloneAndBuild(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2}
	c := xmap.Clone(m)
	c["a"] = 99
	if m["a"] != 1 {
		t.Fatal("Clone 应解耦")
	}
	if xmap.Clone[string, int](nil) != nil {
		t.Fatal("Clone(nil) 应为 nil")
	}

	// CloneDeep 解耦嵌套结构
	nested := map[string]any{"list": []int{1, 2}}
	deep := xmap.CloneDeep(nested)
	deep["list"].([]int)[0] = 99
	if nested["list"].([]int)[0] != 1 {
		t.Fatal("CloneDeep 未解耦嵌套切片")
	}

	dst := map[string]int{"z": 0}
	got := xmap.Copy(dst, m)
	if got["a"] != 1 || got["z"] != 0 || len(got) != 3 {
		t.Fatalf("Copy = %v", got)
	}
	if created := xmap.Copy[string, int](nil, m); created["a"] != 1 {
		t.Fatalf("Copy(nil dst) = %v", created)
	}

	type user struct {
		ID   int
		Name string
	}
	users := []user{{1, "a"}, {2, "b"}}
	byID := xmap.FromSlice(users, func(u user) int { return u.ID }, func(u user) string { return u.Name })
	if byID[1] != "a" || byID[2] != "b" {
		t.Fatalf("FromSlice = %v", byID)
	}
	self := xmap.FromSliceSelf(users, func(u user) int { return u.ID })
	if self[1].Name != "a" {
		t.Fatalf("FromSliceSelf = %v", self)
	}
	if got := xmap.FromSlice([]user(nil), func(u user) int { return u.ID }, func(u user) string { return u.Name }); got != nil {
		t.Fatal("FromSlice(空) 应为 nil")
	}

	pairs := []xmap.Pair[string, int]{{Key: "a", Val: 1}, {Key: "b", Val: 2}}
	if got := xmap.FromEntries(pairs); got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("FromEntries = %v", got)
	}
	if got := xmap.FromEntries[string, int](nil); got != nil {
		t.Fatalf("FromEntries(nil) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 并发安全（配合 -race）
// ---------------------------------------------------------------------------

func TestConcurrentReadOnly(t *testing.T) {
	m := make(map[int]int, 100)
	for i := 0; i < 100; i++ {
		m[i] = i
	}
	done := make(chan struct{})
	for g := 0; g < 20; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 100; i++ {
				_ = xmap.Keys(m)
				_ = xmap.EntriesSorted(m)
				_ = xmap.Filter(m, func(k, v int) bool { return v%2 == 0 })
				_ = xmap.Clone(m)
				xmap.ForEachSorted(m, func(int, int) {})
			}
		}()
	}
	for g := 0; g < 20; g++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkKeysSorted(b *testing.B) {
	m := make(map[int]int, 1000)
	for i := 0; i < 1000; i++ {
		m[i] = i
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xmap.KeysSorted(m)
	}
}

func BenchmarkMerge(b *testing.B) {
	a := map[string]int{"a": 1, "b": 2}
	c := map[string]int{"b": 3, "c": 4}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xmap.Merge(a, c)
	}
}

func BenchmarkGetPath(b *testing.B) {
	m := map[string]any{"a": map[string]any{"b": map[string]any{"c": 1}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = xmap.GetPath(m, "a.b.c")
	}
}
