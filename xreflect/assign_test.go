package xreflect_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/indigo-apple/gokit/xreflect"
)

// setGet 把 src 弱类型赋值给 T，返回结果与新值。
func setGet[T any](src any) (T, error) {
	var dst T
	err := xreflect.SetValue(&dst, src)
	return dst, err
}

// ---------------------------------------------------------------------------
// 标量弱类型赋值
// ---------------------------------------------------------------------------

func TestAssignInt(t *testing.T) {
	if v, err := setGet[int](42); err != nil || v != 42 {
		t.Fatalf("int←int: %v,%v", v, err)
	}
	if v, err := setGet[int](int64(42)); err != nil || v != 42 {
		t.Fatalf("int←int64: %v,%v", v, err)
	}
	if v, err := setGet[int](3.9); err != nil || v != 3 {
		t.Fatalf("int←float64 应截断: %v,%v", v, err)
	}
	if v, err := setGet[int]("42"); err != nil || v != 42 {
		t.Fatalf("int←string: %v,%v", v, err)
	}
	if v, err := setGet[int]("0x1F"); err != nil || v != 31 {
		t.Fatalf("int←hex string: %v,%v", v, err)
	}
	if v, err := setGet[int]("1_000"); err != nil || v != 1000 {
		t.Fatalf("int←下划线数字: %v,%v", v, err)
	}
	if v, err := setGet[int]("1.0"); err != nil || v != 1 {
		t.Fatalf("int←float string: %v,%v", v, err)
	}
	if v, err := setGet[int](true); err != nil || v != 1 {
		t.Fatalf("int←bool: %v,%v", v, err)
	}
	if v, err := setGet[int](nil); err != nil || v != 0 {
		t.Fatalf("int←nil: %v,%v", v, err)
	}
	if v, err := setGet[int](""); err != nil || v != 0 {
		t.Fatalf("int←空串应视为零值: %v,%v", v, err)
	}
	if v, err := setGet[int]("  7  "); err != nil || v != 7 {
		t.Fatalf("int←带空白: %v,%v", v, err)
	}
	if _, err := setGet[int]("abc"); err == nil {
		t.Fatal("int←非数字应报错")
	}
	// 数值收窄按 Go 语义截断，不报错（300 的低 8 位 = 44）
	if v, err := setGet[int8](300); err != nil || v != 44 {
		t.Fatalf("int8←300 应截断为 44: %v,%v", v, err)
	}
}

func TestAssignUintFloatStringBool(t *testing.T) {
	if v, err := setGet[uint8]("255"); err != nil || v != 255 {
		t.Fatalf("uint8←string: %v,%v", v, err)
	}
	if _, err := setGet[uint8](-1); err == nil {
		t.Fatal("uint←负数应报错")
	}
	if _, err := setGet[uint8]("-1"); err == nil {
		t.Fatal("uint←负数字符串应报错")
	}
	if v, err := setGet[uint16](3.9); err != nil || v != 3 {
		t.Fatalf("uint16←float: %v,%v", v, err)
	}
	if _, err := setGet[uint16](-1.5); err == nil {
		t.Fatal("uint←负浮点应报错")
	}
	if v, err := setGet[uint](1); err != nil || v != 1 {
		t.Fatalf("uint←int: %v,%v", v, err)
	}

	if v, err := setGet[float64]("1.25"); err != nil || v != 1.25 {
		t.Fatalf("float64←string: %v,%v", v, err)
	}
	if v, err := setGet[float32](2); err != nil || v != 2 {
		t.Fatalf("float32←int: %v,%v", v, err)
	}
	if v, err := setGet[float64](true); err != nil || v != 1 {
		t.Fatalf("float←bool: %v,%v", v, err)
	}
	if _, err := setGet[float64]("abc"); err == nil {
		t.Fatal("float←非数字应报错")
	}

	if v, err := setGet[string](42); err != nil || v != "42" {
		t.Fatalf("string←int: %v,%v", v, err)
	}
	if v, err := setGet[string](1.5); err != nil || v != "1.5" {
		t.Fatalf("string←float: %v,%v", v, err)
	}
	if v, err := setGet[string](true); err != nil || v != "true" {
		t.Fatalf("string←bool: %v,%v", v, err)
	}
	if v, err := setGet[string]([]byte("hi")); err != nil || v != "hi" {
		t.Fatalf("string←[]byte: %v,%v", v, err)
	}
	if v, err := setGet[string](uint64(9)); err != nil || v != "9" {
		t.Fatalf("string←uint: %v,%v", v, err)
	}

	for _, s := range []string{"true", "TRUE", "1", "yes", "Y", "on", "t"} {
		if v, err := setGet[bool](s); err != nil || !v {
			t.Fatalf("bool←%q: %v,%v", s, v, err)
		}
	}
	for _, s := range []string{"false", "0", "no", "n", "off", "f", ""} {
		if v, err := setGet[bool](s); err != nil || v {
			t.Fatalf("bool←%q: %v,%v", s, v, err)
		}
	}
	if v, err := setGet[bool](2); err != nil || !v {
		t.Fatalf("bool←非零整数: %v,%v", v, err)
	}
	if v, err := setGet[bool](0.0); err != nil || v {
		t.Fatalf("bool←零浮点: %v,%v", v, err)
	}
	if _, err := setGet[bool]("maybe"); err == nil {
		t.Fatal("bool←无法识别应报错")
	}
}

func TestAssignTimeAndDuration(t *testing.T) {
	if v, err := setGet[time.Duration]("1h30m"); err != nil || v != 90*time.Minute {
		t.Fatalf("Duration←string: %v,%v", v, err)
	}
	if v, err := setGet[time.Duration](100); err != nil || v != 100 {
		t.Fatalf("Duration←int(纳秒): %v,%v", v, err)
	}
	if v, err := setGet[time.Duration]("500"); err != nil || v != 500 {
		t.Fatalf("Duration←数字字符串: %v,%v", v, err)
	}
	if v, err := setGet[time.Duration](1.9); err != nil || v != 1 {
		t.Fatalf("Duration←float: %v,%v", v, err)
	}
	if v, err := setGet[time.Duration](""); err != nil || v != 0 {
		t.Fatalf("Duration←空串: %v,%v", v, err)
	}
	if _, err := setGet[time.Duration]("nope"); err == nil {
		t.Fatal("Duration←无法解析应报错")
	}

	layouts := []struct {
		in   string
		year int
	}{
		{"2024-01-02 15:04:05", 2024},
		{"2024-01-02T15:04:05Z", 2024},
		{"2024-01-02", 2024},
		{"2024/01/02", 2024},
		{"20240102150405", 2024},
	}
	for _, c := range layouts {
		v, err := setGet[time.Time](c.in)
		if err != nil || v.Year() != c.year {
			t.Fatalf("Time←%q: %v,%v", c.in, v, err)
		}
	}
	// Unix 时间戳自动识别秒 / 毫秒 / 微秒 / 纳秒
	for _, ts := range []int64{1700000000, 1700000000000, 1700000000000000, 1700000000000000000} {
		v, err := setGet[time.Time](ts)
		if err != nil || v.Unix() != 1700000000 {
			t.Fatalf("Time←时间戳 %d: %v,%v", ts, v.Unix(), err)
		}
	}
	if v, err := setGet[time.Time](0); err != nil || !v.IsZero() {
		t.Fatalf("Time←0 应为零值: %v,%v", v, err)
	}
	if v, err := setGet[time.Time](""); err != nil || !v.IsZero() {
		t.Fatalf("Time←空串应为零值: %v,%v", v, err)
	}
	if v, err := setGet[time.Time]("1700000000"); err != nil || v.Unix() != 1700000000 {
		t.Fatalf("Time←数字字符串: %v,%v", v, err)
	}
	if v, err := setGet[time.Time](1700000000.5); err != nil || v.Unix() != 1700000000 {
		t.Fatalf("Time←float: %v,%v", v, err)
	}
	if _, err := setGet[time.Time]("not-a-time"); err == nil {
		t.Fatal("Time←无法解析应报错")
	}
}

func TestAssignComposite(t *testing.T) {
	if v, err := setGet[[]int]([]any{1, "2", 3.0}); err != nil || len(v) != 3 || v[1] != 2 || v[2] != 3 {
		t.Fatalf("[]int←[]any: %v,%v", v, err)
	}
	if v, err := setGet[[]string]([]int{1, 2}); err != nil || v[0] != "1" {
		t.Fatalf("[]string←[]int: %v,%v", v, err)
	}
	if _, err := setGet[[]int]("x"); err == nil {
		t.Fatal("[]int←string 应报错")
	}
	if v, err := setGet[[]int](nil); err != nil || v != nil {
		t.Fatalf("[]int←nil: %v,%v", v, err)
	}
	if v, err := setGet[[]byte]("hi"); err != nil || string(v) != "hi" {
		t.Fatalf("[]byte←string: %v,%v", v, err)
	}
	if v, err := setGet[[]byte]([]byte("raw")); err != nil || string(v) != "raw" {
		t.Fatalf("[]byte←[]byte: %v,%v", v, err)
	}
	if v, err := setGet[[]byte]([2]byte{'a', 'b'}); err != nil || string(v) != "ab" {
		t.Fatalf("[]byte←[2]byte: %v,%v", v, err)
	}

	if v, err := setGet[[2]int]([]int{1, 2}); err != nil || v != [2]int{1, 2} {
		t.Fatalf("[2]int←[]int: %v,%v", v, err)
	}
	if _, err := setGet[[2]int]([]int{1, 2, 3}); err == nil {
		t.Fatal("数组长度不一致应报错")
	}

	if v, err := setGet[map[string]int](map[string]any{"a": 1, "b": "2"}); err != nil || v["a"] != 1 || v["b"] != 2 {
		t.Fatalf("map←map: %v,%v", v, err)
	}
	if v, err := setGet[map[string]any](nil); err != nil || v != nil {
		t.Fatalf("map←nil: %v,%v", v, err)
	}
	if _, err := setGet[map[string]int]("x"); err == nil {
		t.Fatal("map←string 应报错")
	}
	// map 的键也可弱类型转换
	if v, err := setGet[map[int]string](map[string]any{"1": "a"}); err != nil || v[1] != "a" {
		t.Fatalf("map[int]←map[string]: %v,%v", v, err)
	}

	// 指针自动分配
	if v, err := setGet[*int](5); err != nil || v == nil || *v != 5 {
		t.Fatalf("*int←int: %v,%v", v, err)
	}
	if v, err := setGet[*int](nil); err != nil || v != nil {
		t.Fatalf("*int←nil: %v,%v", v, err)
	}
	if v, err := setGet[**int](7); err != nil || v == nil || **v != 7 {
		t.Fatalf("**int←int: %v,%v", v, err)
	}

	// 接口
	if v, err := setGet[any](42); err != nil || v != 42 {
		t.Fatalf("any←int: %v,%v", v, err)
	}
	if v, err := setGet[any](nil); err != nil || v != nil {
		t.Fatalf("any←nil: %v,%v", v, err)
	}

	// 结构体 ← map
	if v, err := setGet[Inner](map[string]any{"a": "9", "b": 1}); err != nil || v.A != 9 || v.B != "1" {
		t.Fatalf("Inner←map: %+v,%v", v, err)
	}
	// 结构体 ← 结构体（按字段名）
	type Alias struct {
		A string
		B string
	}
	if v, err := setGet[Inner](Alias{A: "1", B: "2"}); err != nil || v.A != 1 || v.B != "2" {
		t.Fatalf("Inner←Alias: %+v,%v", v, err)
	}
	// 无法转换
	if _, err := setGet[Inner]("x"); err == nil {
		t.Fatal("struct←string 应报错")
	}
	if _, err := setGet[int](Inner{}); err == nil {
		t.Fatal("int←struct 应报错")
	}
}

func TestAssignReflectValueErrors(t *testing.T) {
	if err := xreflect.Assign(reflect.Value{}, 1); err == nil {
		t.Fatal("无效目标应报错")
	}
	if err := xreflect.Assign(reflect.ValueOf(1), 2); err == nil {
		t.Fatal("不可设置的目标应报错")
	}
	if err := xreflect.SetValue(nil, 1); err == nil {
		t.Fatal("SetValue(nil) 应报错")
	}
	if err := xreflect.SetValue(Inner{}, 1); err == nil {
		t.Fatal("SetValue(非指针) 应报错")
	}
	var np *Inner
	if err := xreflect.SetValue(np, 1); err == nil {
		t.Fatal("SetValue(nil 指针) 应报错")
	}
	// 非 nil 指针正常
	v := 0
	if err := xreflect.SetValue(&v, "3"); err != nil || v != 3 {
		t.Fatalf("SetValue(&int): %v,%v", v, err)
	}
	// Assign 直接对 reflect.Value 使用
	v2 := 0
	rv := reflect.ValueOf(&v2).Elem()
	if err := xreflect.Assign(rv, "8"); err != nil || v2 != 8 {
		t.Fatalf("Assign: %v,%v", v2, err)
	}
}

// ---------------------------------------------------------------------------
// StructToMap
// ---------------------------------------------------------------------------

type OmitTest struct {
	A string `json:"a,omitempty"`
	B string `json:"b"`
	C int    `json:"c,omitempty"`
	D string `json:"-"`
	E string `json:"e" yaml:"ee"`
}

func TestStructToMap(t *testing.T) {
	o := newOuter()
	m, err := xreflect.StructToMap(o)
	if err != nil {
		t.Fatalf("StructToMap: %v", err)
	}
	if m["id"] != int64(1) || m["name"] != "n" {
		t.Fatalf("基本字段错误: %#v", m)
	}
	if _, ok := m["Skip"]; ok {
		t.Fatal(`json:"-" 字段应被排除`)
	}
	if _, ok := m["hidden"]; ok {
		t.Fatal("未导出字段应被排除")
	}
	if v, ok := m["nil_ptr"]; !ok || v != nil {
		t.Fatalf("nil 指针字段应输出为 nil: %#v", v)
	}
	if m["ptr_val"] != 7 {
		t.Fatalf("非 nil 指针应解引用: %#v", m["ptr_val"])
	}
	if m["emb"] != "e" {
		t.Fatalf("嵌入字段错误: %#v", m)
	}
	if _, ok := m["inner"]; !ok {
		t.Fatalf("嵌套结构体应以原值输出: %#v", m)
	}
	if got, ok := m["tags"].([]string); !ok || len(got) != 2 {
		t.Fatalf("切片字段错误: %#v", m["tags"])
	}

	// 指针入参
	m2, err := xreflect.StructToMap(&o)
	if err != nil || m2["id"] != int64(1) {
		t.Fatalf("StructToMap(指针) = %#v,%v", m2, err)
	}

	// nil 指针 → 空 map
	var np *Outer
	m3, err := xreflect.StructToMap(np)
	if err != nil || len(m3) != 0 {
		t.Fatalf("StructToMap(nil 指针) = %#v,%v", m3, err)
	}

	// 嵌套展开
	m4, err := xreflect.StructToMap(o, xreflect.WithNested())
	if err != nil {
		t.Fatalf("WithNested: %v", err)
	}
	sub, ok := m4["inner"].(map[string]any)
	if !ok || sub["a"] != 2 || sub["b"] != "b" {
		t.Fatalf("嵌套展开错误: %#v", m4["inner"])
	}

	// 自定义 tag
	m5, err := xreflect.StructToMap(OmitTest{E: "x"}, xreflect.WithTag("yaml"))
	if err != nil || m5["ee"] != "x" {
		t.Fatalf("WithTag(yaml) = %#v,%v", m5, err)
	}

	// 错误场景
	if _, err := xreflect.StructToMap(nil); err == nil {
		t.Fatal("StructToMap(nil) 应报错")
	}
	if _, err := xreflect.StructToMap(1); err == nil {
		t.Fatal("StructToMap(int) 应报错")
	}
}

func TestStructToMapOmitEmpty(t *testing.T) {
	m, err := xreflect.StructToMap(OmitTest{})
	if err != nil {
		t.Fatalf("StructToMap: %v", err)
	}
	if _, ok := m["a"]; ok {
		t.Fatalf("tag 上的 omitempty 应生效: %#v", m)
	}
	if _, ok := m["c"]; ok {
		t.Fatalf("tag 上的 omitempty 应生效: %#v", m)
	}
	if v, ok := m["b"]; !ok || v != "" {
		t.Fatalf("无 omitempty 的零值字段应保留: %#v", m)
	}
	if _, ok := m["D"]; ok {
		t.Fatalf(`json:"-" 应排除: %#v`, m)
	}

	m2, err := xreflect.StructToMap(OmitTest{}, xreflect.WithOmitEmpty())
	if err != nil {
		t.Fatalf("StructToMap: %v", err)
	}
	if len(m2) != 0 {
		t.Fatalf("WithOmitEmpty 应剔除全部零值字段: %#v", m2)
	}

	m3, err := xreflect.StructToMap(OmitTest{A: "x", B: "y", C: 1, E: "z"})
	if err != nil || m3["a"] != "x" || m3["c"] != 1 {
		t.Fatalf("非零字段应保留: %#v,%v", m3, err)
	}

	// WithGoFieldName：忽略 tag，使用 Go 字段名，且不处理 "-"
	m4, err := xreflect.StructToMap(OmitTest{A: "x", D: "d"}, xreflect.WithGoFieldName())
	if err != nil {
		t.Fatalf("StructToMap: %v", err)
	}
	if m4["A"] != "x" || m4["D"] != "d" {
		t.Fatalf("WithGoFieldName = %#v", m4)
	}
	if _, ok := m4["a"]; ok {
		t.Fatalf("WithGoFieldName 不应使用 tag 名: %#v", m4)
	}
}

func TestStructToMapString(t *testing.T) {
	m, err := xreflect.StructToMapString(OmitTest{B: "b", C: 3, E: "e"})
	if err != nil {
		t.Fatalf("StructToMapString: %v", err)
	}
	if m["b"] != "b" || m["c"] != "3" || m["e"] != "e" {
		t.Fatalf("StructToMapString = %#v", m)
	}
	if _, err := xreflect.StructToMapString(nil); err == nil {
		t.Fatal("StructToMapString(nil) 应报错")
	}
}

// ---------------------------------------------------------------------------
// MapToStruct
// ---------------------------------------------------------------------------

func TestMapToStruct(t *testing.T) {
	var o Outer
	err := xreflect.MapToStruct(map[string]any{
		"id":      "42",
		"name":    "bob",
		"inner":   map[string]any{"a": "7", "b": "bb"},
		"ptr_val": 9,
		"tags":    []any{"x", "y"},
		"m":       map[string]any{"k": "1"},
		"when":    "2024-03-04 05:06:07",
		"dur":     "2m",
		"missing": "ignored",
		"Skip":    "should-not-set",
	}, &o)
	if err != nil {
		t.Fatalf("MapToStruct: %v", err)
	}
	if o.ID != 42 || o.Name != "bob" {
		t.Fatalf("基本字段: %+v", o)
	}
	if o.Inner.A != 7 || o.Inner.B != "bb" {
		t.Fatalf("嵌套: %+v", o.Inner)
	}
	if o.PtrVal == nil || *o.PtrVal != 9 {
		t.Fatalf("指针: %v", o.PtrVal)
	}
	if len(o.Tags) != 2 || o.Tags[0] != "x" {
		t.Fatalf("切片: %v", o.Tags)
	}
	if o.M["k"] != 1 {
		t.Fatalf("map: %v", o.M)
	}
	if o.When.Year() != 2024 || o.When.Hour() != 5 {
		t.Fatalf("时间: %v", o.When)
	}
	if o.Dur != 2*time.Minute {
		t.Fatalf("Duration: %v", o.Dur)
	}
	if o.Skip == "should-not-set" {
		t.Fatal(`json:"-" 字段不应被赋值`)
	}

	// Go 字段名回退匹配
	var o2 Outer
	if err := xreflect.MapToStruct(map[string]any{"Name": "fallback"}, &o2); err != nil {
		t.Fatalf("MapToStruct: %v", err)
	}
	if o2.Name != "fallback" {
		t.Fatalf("字段名回退匹配失败: %+v", o2)
	}

	// 空 map / nil map 不报错
	if err := xreflect.MapToStruct(nil, &o2); err != nil {
		t.Fatalf("MapToStruct(nil) = %v", err)
	}
	if err := xreflect.MapToStruct(map[string]any{}, &o2); err != nil {
		t.Fatalf("MapToStruct(空) = %v", err)
	}

	// 单字段失败不中断其它字段
	var o3 Inner
	err = xreflect.MapToStruct(map[string]any{"a": "abc", "b": "ok"}, &o3)
	if err == nil {
		t.Fatal("无法解析的字段应产生错误")
	}
	if o3.B != "ok" {
		t.Fatalf("单字段失败不应中断其它字段: %+v", o3)
	}
	if !strings.Contains(err.Error(), "field") {
		t.Fatalf("错误应带上字段上下文: %v", err)
	}

	// 错误场景
	if err := xreflect.MapToStruct(map[string]any{"a": 1}, nil); err == nil {
		t.Fatal("MapToStruct(nil) 应报错")
	}
	if err := xreflect.MapToStruct(map[string]any{"a": 1}, Inner{}); err == nil {
		t.Fatal("非指针应报错")
	}
	var np *Inner
	if err := xreflect.MapToStruct(map[string]any{"a": 1}, np); err == nil {
		t.Fatal("nil 指针应报错")
	}
	if err := xreflect.MapToStruct(map[string]any{"a": 1}, new(int)); err == nil {
		t.Fatal("非结构体应报错")
	}
	// 空结构体是合法结构体，不应报错（只是没有字段可写）
	if err := xreflect.MapToStruct(map[string]any{"a": 1}, &struct{}{}); err != nil {
		t.Fatalf("空结构体不应报错: %v", err)
	}

	// 指针嵌套自动分配（结构体 ← map 内层）
	type DeepCfg struct {
		Inner *Inner `json:"inner"`
	}
	var dc DeepCfg
	if err := xreflect.MapToStruct(map[string]any{"inner": map[string]any{"a": 3}}, &dc); err != nil {
		t.Fatalf("MapToStruct(嵌套指针): %v", err)
	}
	if dc.Inner == nil || dc.Inner.A != 3 {
		t.Fatalf("嵌套指针未分配: %+v", dc.Inner)
	}
}

// 往返：StructToMap → MapToStruct 应还原主要字段
func TestStructMapRoundTrip(t *testing.T) {
	src := newOuter()
	m, err := xreflect.StructToMap(src)
	if err != nil {
		t.Fatalf("StructToMap: %v", err)
	}
	var dst Outer
	if err := xreflect.MapToStruct(m, &dst); err != nil {
		t.Fatalf("MapToStruct: %v", err)
	}
	if dst.ID != src.ID || dst.Name != src.Name || dst.Inner != src.Inner ||
		dst.Emb != src.Emb || dst.Dur != src.Dur || !dst.When.Equal(src.When) {
		t.Fatalf("往返结果不一致:\nsrc=%+v\ndst=%+v", src, dst)
	}
	if dst.PtrVal == nil || *dst.PtrVal != *src.PtrVal {
		t.Fatalf("指针字段不一致: %v vs %v", dst.PtrVal, src.PtrVal)
	}
	if len(dst.Tags) != len(src.Tags) || dst.M["k"] != src.M["k"] {
		t.Fatalf("集合字段不一致: %v %v", dst.Tags, dst.M)
	}
	if dst.Skip != "" {
		t.Fatal(`json:"-" 字段不应被还原`)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkStructToMap(b *testing.B) {
	o := newOuter()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = xreflect.StructToMap(o)
	}
}

func BenchmarkMapToStruct(b *testing.B) {
	o := newOuter()
	m, _ := xreflect.StructToMap(o)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var dst Outer
		_ = xreflect.MapToStruct(m, &dst)
	}
}
