package xconstraints_test

import (
	"testing"

	"github.com/indigo-apple/gokit/xconstraints"
)

// 用泛型函数实际实例化每个约束，确保约束可被外部包正常使用。

func sum[T xconstraints.Number](vs ...T) T {
	var r T
	for _, v := range vs {
		r += v
	}
	return r
}

func maxOf[T xconstraints.Ordered](vs ...T) (T, bool) {
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

func keysOf[K xconstraints.Key, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestNumber(t *testing.T) {
	if got := sum(1, 2, 3); got != 6 {
		t.Fatalf("int sum = %d, want 6", got)
	}
	if got := sum(1.5, 2.25); got != 3.75 {
		t.Fatalf("float sum = %v, want 3.75", got)
	}
	if got := sum[int8](1, 2); got != 3 {
		t.Fatalf("int8 sum = %d, want 3", got)
	}
	if got := sum[uint16](1, 2); got != 3 {
		t.Fatalf("uint16 sum = %d, want 3", got)
	}
	// 自定义底层类型也应满足约束（~ 语义）
	type MyInt int
	if got := sum(MyInt(4), MyInt(5)); got != 9 {
		t.Fatalf("MyInt sum = %d, want 9", got)
	}
	type MyFloat float64
	if got := sum(MyFloat(1.5), MyFloat(1.5)); got != 3 {
		t.Fatalf("MyFloat sum = %v, want 3", got)
	}
}

func TestOrdered(t *testing.T) {
	if got, ok := maxOf(3, 9, 2); !ok || got != 9 {
		t.Fatalf("maxOf int = %v,%v", got, ok)
	}
	if got, ok := maxOf(1.5, 9.25, 2.0); !ok || got != 9.25 {
		t.Fatalf("maxOf float = %v,%v", got, ok)
	}
	if got, ok := maxOf("apple", "zebra", "mango"); !ok || got != "zebra" {
		t.Fatalf("maxOf string = %v,%v", got, ok)
	}
	if _, ok := maxOf[int](); ok {
		t.Fatal("maxOf empty should report false")
	}
}

func TestKey(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2}
	if got := len(keysOf(m)); got != 2 {
		t.Fatalf("keys len = %d, want 2", got)
	}
	type Custom struct{ ID int }
	m2 := map[Custom]string{{ID: 1}: "x"}
	ks := keysOf(m2)
	if len(ks) != 1 || ks[0].ID != 1 {
		t.Fatalf("custom struct key failed: %+v", ks)
	}
	type MyString string
	m3 := map[MyString]int{"k": 1}
	if len(keysOf(m3)) != 1 {
		t.Fatal("underlying-type key failed")
	}
}
