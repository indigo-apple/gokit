package xreflect_test

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indigo-apple/gokit/xreflect"
)

// ---------------------------------------------------------------------------
// 测试用类型
// ---------------------------------------------------------------------------

type Embedded struct {
	Emb string `json:"emb"`
}

type Inner struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}

type Outer struct {
	ID     int64          `json:"id"`
	Name   string         `json:"name"`
	Inner  Inner          `json:"inner"`
	PtrVal *int           `json:"ptr_val"`
	NilPtr *int           `json:"nil_ptr"`
	Skip   string         `json:"-"`
	Tags   []string       `json:"tags"`
	M      map[string]int `json:"m"`
	When   time.Time      `json:"when"`
	Dur    time.Duration  `json:"dur"`
	Embedded
	hidden string
}

// Hidden 暴露未导出字段，用于验证深拷贝是否正确复制了未导出字段。
func (o Outer) Hidden() string { return o.hidden }

func newOuter() Outer {
	p := 7
	return Outer{
		ID:       1,
		Name:     "n",
		Inner:    Inner{A: 2, B: "b"},
		PtrVal:   &p,
		Skip:     "skip",
		Tags:     []string{"x", "y"},
		M:        map[string]int{"k": 1},
		When:     time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		Dur:      90 * time.Second,
		Embedded: Embedded{Emb: "e"},
		hidden:   "secret",
	}
}

type Node struct {
	Val  int
	Next *Node
}

// ---------------------------------------------------------------------------
// 基础判定
// ---------------------------------------------------------------------------

type stringer int

func (stringer) String() string { return "stringer" }

func TestIsNilTypedNilTrap(t *testing.T) {
	var p *int
	var i any = p
	if i == nil {
		t.Fatal("前提错误：接口持有 nil 指针时 != nil")
	}
	if !xreflect.IsNil(i) {
		t.Fatal("IsNil 应识别出 typed nil")
	}
	if xreflect.IsNil(0) || xreflect.IsNil("") {
		t.Fatal("非 nil 值不应判定为 nil")
	}
	if !xreflect.IsNil(nil) {
		t.Fatal("IsNil(nil) 应为 true")
	}
	var s []int
	if !xreflect.IsNil(s) {
		t.Fatal("nil 切片应为 true")
	}
	var m map[string]int
	if !xreflect.IsNil(m) {
		t.Fatal("nil map 应为 true")
	}
	var f func()
	if !xreflect.IsNil(f) {
		t.Fatal("nil func 应为 true")
	}
	var ch chan int
	if !xreflect.IsNil(ch) {
		t.Fatal("nil chan 应为 true")
	}
}

func TestIsZero(t *testing.T) {
	if !xreflect.IsZero(nil) || !xreflect.IsZero(0) || !xreflect.IsZero("") {
		t.Fatal("IsZero 判断错误")
	}
	if xreflect.IsZero(1) || xreflect.IsZero("x") {
		t.Fatal("IsZero 判断错误")
	}
	if !xreflect.IsZero(Inner{}) || xreflect.IsZero(Inner{A: 1}) {
		t.Fatal("IsZero struct 判断错误")
	}
	if !xreflect.IsZero([]int(nil)) {
		t.Fatal("IsZero nil slice 判断错误")
	}
}

func TestBasics(t *testing.T) {
	if got := xreflect.TypeName(1); got != "int" {
		t.Fatalf("TypeName = %q", got)
	}
	if got := xreflect.TypeName(nil); got != "<nil>" {
		t.Fatalf("TypeName(nil) = %q", got)
	}
	if got := xreflect.TypeOf(nil); got != nil {
		t.Fatal("TypeOf(nil) 应为 nil")
	}

	if got := xreflect.IndirectType(reflect.TypeOf(&Inner{})); got != reflect.TypeOf(Inner{}) {
		t.Fatalf("IndirectType = %v", got)
	}
	if got := xreflect.IndirectType(reflect.TypeOf(1)); got != reflect.TypeOf(1) {
		t.Fatalf("IndirectType(int) = %v", got)
	}
	if got := xreflect.IndirectType(nil); got != nil {
		t.Fatal("IndirectType(nil) 应为 nil")
	}

	if got := xreflect.New(reflect.TypeOf(Inner{})); reflect.TypeOf(got) != reflect.TypeOf(&Inner{}) {
		t.Fatalf("New = %T", got)
	}
	if xreflect.New(nil) != nil {
		t.Fatal("New(nil) 应为 nil")
	}
	if p := xreflect.NewOf[int](); p == nil || *p != 0 {
		t.Fatalf("NewOf = %v", p)
	}
	if got := xreflect.NewValueOf[string](); got != "" {
		t.Fatalf("NewValueOf = %q", got)
	}
	sl := xreflect.MakeSlice(reflect.TypeOf(0), 2, 4)
	if reflect.ValueOf(sl).Len() != 2 || reflect.ValueOf(sl).Cap() != 4 {
		t.Fatalf("MakeSlice = %v", sl)
	}
	if xreflect.MakeSlice(nil, 0, 0) != nil {
		t.Fatal("MakeSlice(nil) 应为 nil")
	}

	errType := reflect.TypeOf((*error)(nil)).Elem()
	if !xreflect.Implements(errors.New("x"), errType) {
		t.Fatal("Implements 判断错误")
	}
	if xreflect.Implements("s", errType) || xreflect.Implements(nil, errType) {
		t.Fatal("Implements 应为 false")
	}
	if !xreflect.ImplementsInterface[error](errors.New("x")) {
		t.Fatal("ImplementsInterface 判断错误")
	}
	if !xreflect.ImplementsInterface[interface{ String() string }](stringer(1)) {
		t.Fatal("ImplementsInterface(Stringer) 判断错误")
	}
	if !xreflect.IsAssignable[int](1) || xreflect.IsAssignable[int]("s") {
		t.Fatal("IsAssignable 判断错误")
	}
	if !xreflect.IsStruct(Inner{}) || !xreflect.IsStruct(&Inner{}) || xreflect.IsStruct(1) {
		t.Fatal("IsStruct 判断错误")
	}
}

// ---------------------------------------------------------------------------
// 深拷贝
// ---------------------------------------------------------------------------

func TestDeepCopyScalars(t *testing.T) {
	if got := xreflect.DeepCopy(42); got != 42 {
		t.Fatalf("DeepCopy(int) = %d", got)
	}
	if got := xreflect.DeepCopy("s"); got != "s" {
		t.Fatalf("DeepCopy(string) = %q", got)
	}
	if got := xreflect.DeepCopy[int](0); got != 0 {
		t.Fatalf("DeepCopy zero = %d", got)
	}
	if got := xreflect.Clone("c"); got != "c" {
		t.Fatalf("Clone = %q", got)
	}
	var nilPtr *Inner
	if got := xreflect.DeepCopy(nilPtr); got != nil {
		t.Fatalf("DeepCopy(nil ptr) = %v", got)
	}
	if got := xreflect.DeepCopyAny(nil); got != nil {
		t.Fatalf("DeepCopyAny(nil) = %v", got)
	}
}

func TestDeepCopyStructDeep(t *testing.T) {
	src := newOuter()
	cp := xreflect.DeepCopy(src)

	if cp.Name != src.Name || cp.Inner.A != 2 || cp.Embedded.Emb != "e" {
		t.Fatalf("标量字段复制错误: %+v", cp)
	}
	if cp.Hidden() != "secret" {
		t.Fatal("未导出字段未被复制")
	}

	// 切片 / map / 指针必须解耦
	cp.Tags[0] = "MUTATED"
	if src.Tags[0] != "x" {
		t.Fatal("切片未深拷贝（共享底层数组）")
	}
	cp.M["k"] = 999
	if src.M["k"] != 1 {
		t.Fatal("map 未深拷贝")
	}
	*cp.PtrVal = 12345
	if *src.PtrVal != 7 {
		t.Fatal("指针目标未深拷贝")
	}
	if cp.When != src.When {
		t.Fatal("time.Time 应保持相等")
	}
	if cp.Dur != 90*time.Second {
		t.Fatal("time.Duration 复制错误")
	}
}

func TestDeepCopyPointer(t *testing.T) {
	src := &Inner{A: 1, B: "b"}
	cp := xreflect.DeepCopy(src)
	if cp == src {
		t.Fatal("指针应指向新对象")
	}
	if cp.A != 1 || cp.B != "b" {
		t.Fatalf("内容错误: %+v", cp)
	}
	cp.A = 9
	if src.A != 1 {
		t.Fatal("原对象被修改")
	}
}

func TestDeepCopyPreservesAliasing(t *testing.T) {
	shared := &Inner{A: 1}
	src := []*Inner{shared, shared}
	cp := xreflect.DeepCopy(src)

	if cp[0] != cp[1] {
		t.Fatal("别名关系未保留：两个元素应指向同一个新对象")
	}
	if cp[0] == shared {
		t.Fatal("未复制，仍指向原对象")
	}
	if cp[0].A != 1 {
		t.Fatalf("内容错误: %+v", cp[0])
	}
}

func TestDeepCopyCycle(t *testing.T) {
	n1 := &Node{Val: 1}
	n2 := &Node{Val: 2, Next: n1}
	n1.Next = n2 // 形成环

	cp := xreflect.DeepCopy(n1)
	if cp == n1 {
		t.Fatal("应复制")
	}
	if cp.Val != 1 || cp.Next.Val != 2 {
		t.Fatalf("内容错误: %+v", cp)
	}
	if cp.Next.Next != cp {
		t.Fatal("环未被保留")
	}
}

func TestDeepCopyMapAndInterface(t *testing.T) {
	src := map[string][]int{"a": {1, 2}}
	cp := xreflect.DeepCopy(src)
	cp["a"][0] = 99
	if src["a"][0] != 1 {
		t.Fatal("map 内的切片未深拷贝")
	}

	src2 := map[string]any{"i": Inner{A: 5}, "s": []string{"x"}}
	cp2 := xreflect.DeepCopy(src2)
	inner, ok := cp2["i"].(Inner)
	if !ok || inner.A != 5 {
		t.Fatalf("接口值内的结构体复制错误: %#v", cp2["i"])
	}
	cp2["s"].([]string)[0] = "MUT"
	if src2["s"].([]string)[0] != "x" {
		t.Fatal("接口值内的切片未深拷贝")
	}
}

func TestDeepCopyArrayAndNested(t *testing.T) {
	type WithArr struct {
		Arr [3]*Inner
	}
	src := WithArr{}
	src.Arr[0] = &Inner{A: 1}
	cp := xreflect.DeepCopy(src)
	if cp.Arr[0] == src.Arr[0] {
		t.Fatal("数组内指针应深拷贝")
	}
	cp.Arr[0].A = 8
	if src.Arr[0].A != 1 {
		t.Fatal("原数组元素被修改")
	}
	if cp.Arr[1] != nil || cp.Arr[2] != nil {
		t.Fatal("nil 元素应保持 nil")
	}
}

func TestDeepCopyAny(t *testing.T) {
	got := xreflect.DeepCopyAny([]int{1, 2})
	if !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("DeepCopyAny = %#v", got)
	}
}

// ---------------------------------------------------------------------------
// 元数据
// ---------------------------------------------------------------------------

func TestDescribe(t *testing.T) {
	info, err := xreflect.Describe(Outer{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if info.Kind != reflect.Struct {
		t.Fatalf("Kind = %v", info.Kind)
	}

	names := info.FieldNames()
	for _, want := range []string{"ID", "Name", "Inner", "PtrVal", "Skip", "Tags", "M", "When", "Dur", "Emb"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少字段 %q，实际 %v", want, names)
		}
	}
	// 未导出字段不应出现
	for _, n := range names {
		if n == "hidden" {
			t.Fatal("未导出字段不应出现在元数据中")
		}
	}

	if f := info.Field("ID"); f == nil || f.Type.Kind() != reflect.Int64 {
		t.Fatalf("Field(ID) = %v", f)
	}
	if f := info.Field("ID"); f.MapKey("json") != "id" {
		t.Fatalf("MapKey = %q", f.MapKey("json"))
	}
	if f := info.Field("Emb"); f == nil || f.Depth != 1 || len(f.Index) != 2 {
		t.Fatalf("嵌入字段信息错误: %+v", f)
	}
	if f := info.Field("Name"); f.MapKey("yaml") != "Name" {
		t.Fatal("缺失 tag 时应回退到 Go 字段名")
	}
	if got := info.FieldByTag("json", "name"); got == nil || got.Name != "Name" {
		t.Fatalf("FieldByTag = %v", got)
	}
	if info.Field("NotExist") != nil || info.FieldByTag("json", "zzz") != nil {
		t.Fatal("不存在的字段应返回 nil")
	}
	if info.FieldNames() == nil {
		t.Fatal("FieldNames 不应为 nil")
	}

	// 指针 / 非结构体
	if _, err := xreflect.Describe(&Outer{}); err != nil {
		t.Fatalf("Describe(ptr) 应成功: %v", err)
	}
	if _, err := xreflect.Describe(nil); err == nil {
		t.Fatal("Describe(nil) 应报错")
	}
	if _, err := xreflect.Describe(1); err == nil {
		t.Fatal("Describe(int) 应报错")
	}
	if _, err := xreflect.DescribeType(nil); err == nil {
		t.Fatal("DescribeType(nil) 应报错")
	}
	if info, err := xreflect.DescribeType(reflect.TypeOf(&Outer{})); err != nil || info == nil {
		t.Fatalf("DescribeType(ptr) = %v,%v", info, err)
	}
}

func TestFieldAccessors(t *testing.T) {
	o := newOuter()

	if v, ok := xreflect.Get(o, "Name"); !ok || v != "n" {
		t.Fatalf("Get(Name) = %v,%v", v, ok)
	}
	if v, ok := xreflect.Get(o, "Inner.A"); !ok || v != 2 {
		t.Fatalf("Get(Inner.A) = %v,%v", v, ok)
	}
	if v, ok := xreflect.Get(o, "Emb"); !ok || v != "e" {
		t.Fatalf("Get(Emb 嵌入字段) = %v,%v", v, ok)
	}
	if v, ok := xreflect.Get(o, "PtrVal"); !ok || v != 7 {
		t.Fatalf("Get(PtrVal) = %v,%v", v, ok)
	}
	if v, ok := xreflect.Get(o, "NilPtr"); ok || v != nil {
		t.Fatalf("Get(NilPtr) = %v,%v", v, ok)
	}
	if _, ok := xreflect.Get(o, "NotExist"); ok {
		t.Fatal("不存在字段应为 false")
	}
	if _, ok := xreflect.Get(o, "hidden"); ok {
		t.Fatal("未导出字段应为 false")
	}
	if _, ok := xreflect.Get(nil, "Name"); ok {
		t.Fatal("nil 应为 false")
	}
	if _, ok := xreflect.Get(o, ""); ok {
		t.Fatal("空路径应为 false")
	}
	if _, ok := xreflect.Get(1, "X"); ok {
		t.Fatal("非结构体应为 false")
	}

	if !xreflect.Has(o, "Name") || xreflect.Has(o, "Nope") {
		t.Fatal("Has 判断错误")
	}
	if got := xreflect.KindOf(o, "ID"); got != reflect.Int64 {
		t.Fatalf("KindOf = %v", got)
	}
	if got := xreflect.KindOf(o, "Nope"); got != reflect.Invalid {
		t.Fatalf("KindOf(不存在) = %v", got)
	}

	all, ok := xreflect.GetAll(o)
	if !ok || all["Name"] != "n" || all["Emb"] != "e" {
		t.Fatalf("GetAll = %#v", all)
	}
	if _, exists := all["hidden"]; exists {
		t.Fatal("GetAll 不应包含未导出字段")
	}

	names := xreflect.Names(o)
	vals, ok := xreflect.GetAllValues(o)
	if !ok || len(names) != len(vals) {
		t.Fatalf("Names/GetAllValues 长度不一致: %d vs %d", len(names), len(vals))
	}
	if xreflect.Names(1) != nil || xreflect.Names(nil) != nil {
		t.Fatal("Names 对非结构体应返回 nil")
	}
	if _, ok := xreflect.GetAll(1); ok {
		t.Fatal("GetAll 对非结构体应为 false")
	}
	if _, ok := xreflect.GetAllValues(1); ok {
		t.Fatal("GetAllValues 对非结构体应为 false")
	}

	if f := xreflect.FieldInfoOf(o, "ID"); f == nil || f.Name != "ID" {
		t.Fatalf("FieldInfoOf = %v", f)
	}
	if f := xreflect.FieldInfoOf(o, "Nope"); f != nil {
		t.Fatal("FieldInfoOf(不存在) 应为 nil")
	}
	if f := xreflect.FieldInfoOf(nil, "ID"); f != nil {
		t.Fatal("FieldInfoOf(nil) 应为 nil")
	}
}

func TestSetField(t *testing.T) {
	var o Outer
	if err := xreflect.Set(&o, "Name", "hello"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if o.Name != "hello" {
		t.Fatalf("Name = %q", o.Name)
	}
	// 弱类型：字符串 → int64
	if err := xreflect.Set(&o, "ID", "42"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if o.ID != 42 {
		t.Fatalf("ID = %d", o.ID)
	}
	// 嵌套路径，且自动分配中间指针
	type Deep struct {
		Inner *Inner
	}
	var d Deep
	if err := xreflect.Set(&d, "Inner.A", 5); err != nil {
		t.Fatalf("Set(嵌套): %v", err)
	}
	if d.Inner == nil || d.Inner.A != 5 {
		t.Fatalf("嵌套赋值失败: %+v", d)
	}
	// SetByPath 等价
	if err := xreflect.SetByPath(&d, "Inner.B", "bb"); err != nil {
		t.Fatalf("SetByPath: %v", err)
	}
	if d.Inner.B != "bb" {
		t.Fatalf("SetByPath 失败: %+v", d.Inner)
	}

	// 错误场景
	if err := xreflect.Set(nil, "A", 1); err == nil {
		t.Fatal("Set(nil) 应报错")
	}
	if err := xreflect.Set(Inner{}, "A", 1); err == nil {
		t.Fatal("非指针应报错")
	}
	var np *Inner
	if err := xreflect.Set(np, "A", 1); err == nil {
		t.Fatal("nil 指针应报错")
	}
	if err := xreflect.Set(&o, "Nope", 1); err == nil {
		t.Fatal("不存在字段应报错")
	}
	if err := xreflect.Set(&o, "hidden", 1); err == nil {
		t.Fatal("未导出字段应报错")
	}
	if err := xreflect.Set(&o, "ID", "abc"); err == nil {
		t.Fatal("无法解析的字符串应报错")
	}
	if err := xreflect.Set(&o, "Inner.A", "abc"); err == nil {
		t.Fatal("嵌套字段错误应报错")
	}
}

// ---------------------------------------------------------------------------
// tag
// ---------------------------------------------------------------------------

func TestParseTag(t *testing.T) {
	cases := []struct {
		in       string
		value    string
		options  []string
		keyVals  map[string]string
		asString string
	}{
		{"", "", nil, map[string]string{}, ""},
		{"name", "name", nil, map[string]string{}, "name"},
		{"name,omitempty", "name", []string{"omitempty"}, map[string]string{}, "name,omitempty"},
		{"-,", "-", nil, map[string]string{}, "-"},
		{"req,min=1,max=10", "req", []string{"min=1", "max=10"}, map[string]string{"min": "1", "max": "10"}, "req,min=1,max=10"},
		{",omitempty", "", []string{"omitempty"}, map[string]string{}, ",omitempty"},
		{"a,,b", "a", []string{"b"}, map[string]string{}, "a,b"},
	}
	for _, c := range cases {
		got := xreflect.ParseTag(c.in)
		if got.Value != c.value {
			t.Fatalf("ParseTag(%q).Value = %q, want %q", c.in, got.Value, c.value)
		}
		if !equalStrings(got.Options, c.options) {
			t.Fatalf("ParseTag(%q).Options = %v, want %v", c.in, got.Options, c.options)
		}
		for k, v := range c.keyVals {
			if got.Values[k] != v {
				t.Fatalf("ParseTag(%q).Values[%q] = %q, want %q", c.in, k, got.Values[k], v)
			}
		}
		if got.String() != c.asString {
			t.Fatalf("ParseTag(%q).String() = %q, want %q", c.in, got.String(), c.asString)
		}
	}

	tg := xreflect.ParseTag("req,min=1,max=10")
	if v, ok := tg.Get("min"); !ok || v != "1" {
		t.Fatalf("Tag.Get = %q,%v", v, ok)
	}
	if _, ok := tg.Get("nope"); ok {
		t.Fatal("Tag.Get 不存在的 key 应为 false")
	}
	if !tg.Has("min") || !tg.Has("max") || !tg.Has("req") || tg.Has("nope") {
		t.Fatal("Tag.Has 判断错误")
	}
	var zero xreflect.Tag
	if !zero.Has("x") {
		// 零值 Tag 的 Options 为空，不应 panic
	}
}

func TestTagHelpers(t *testing.T) {
	o := newOuter()
	if got := xreflect.GetTag(o, "Name", "json"); got != "name" {
		t.Fatalf("GetTag = %q", got)
	}
	if got := xreflect.GetTagValue(o, "Inner", "json"); got != "inner" {
		t.Fatalf("GetTagValue = %q", got)
	}
	if got := xreflect.TagOptions(o, "Inner", "json"); len(got) != 0 {
		t.Fatalf("TagOptions = %v", got)
	}
	if !xreflect.HasTagOption(o, "Inner.B", "json", "omitempty") {
		t.Fatal("HasTagOption(omitempty) 应为 true")
	}
	if xreflect.HasTagOption(o, "Inner.A", "json", "omitempty") {
		t.Fatal("HasTagOption 应为 false")
	}
	if xreflect.GetTag(o, "Nope", "json") != "" {
		t.Fatal("不存在字段应返回空串")
	}
	if xreflect.GetTagValue(o, "Nope", "json") != "" ||
		xreflect.TagOptions(o, "Nope", "json") != nil ||
		xreflect.HasTagOption(o, "Nope", "json", "x") {
		t.Fatal("不存在字段应返回零值")
	}
}

func equalStrings(a, b []string) bool {
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

// ---------------------------------------------------------------------------
// 动态调用
// ---------------------------------------------------------------------------

func TestCall(t *testing.T) {
	add := func(a, b int) int { return a + b }
	rs, err := xreflect.Call(add, 1, 2)
	if err != nil || len(rs) != 1 || rs[0] != 3 {
		t.Fatalf("Call = %v,%v", rs, err)
	}

	// 参数自动转换
	rs, err = xreflect.Call(add, "3", int64(4))
	if err != nil || rs[0] != 7 {
		t.Fatalf("Call 自动转换 = %v,%v", rs, err)
	}

	// 多返回值
	div := func(a, b int) (int, error) {
		if b == 0 {
			return 0, errors.New("div by zero")
		}
		return a / b, nil
	}
	rs, err = xreflect.Call(div, 6, 2)
	if err != nil || len(rs) != 2 || rs[0] != 3 || rs[1] != nil {
		t.Fatalf("Call 多返回值 = %v,%v", rs, err)
	}

	// 变参
	sum := func(base int, rest ...int) int {
		s := base
		for _, v := range rest {
			s += v
		}
		return s
	}
	rs, err = xreflect.Call(sum, 1, 2, 3, 4)
	if err != nil || rs[0] != 10 {
		t.Fatalf("Call 变参 = %v,%v", rs, err)
	}
	rs, err = xreflect.Call(sum, 1)
	if err != nil || rs[0] != 1 {
		t.Fatalf("Call 变参(无额外) = %v,%v", rs, err)
	}
	// 弱类型转换：float64 截断为 int
	rs, err = xreflect.Call(sum, 1.5, 2.9)
	if err != nil || rs[0] != 3 {
		t.Fatalf("Call 弱类型 = %v,%v", rs, err)
	}

	// nil 参数
	withPtr := func(p *int) bool { return p == nil }
	rs, err = xreflect.Call(withPtr, nil)
	if err != nil || rs[0] != true {
		t.Fatalf("Call(nil) = %v,%v", rs, err)
	}

	// Call1
	v, err := xreflect.Call1(add, 2, 3)
	if err != nil || v != 5 {
		t.Fatalf("Call1 = %v,%v", v, err)
	}

	// 错误场景
	if _, err := xreflect.Call(nil); err == nil {
		t.Fatal("Call(nil) 应报错")
	}
	if _, err := xreflect.Call(42); err == nil {
		t.Fatal("Call(非函数) 应报错")
	}
	if _, err := xreflect.Call(add, 1); err == nil {
		t.Fatal("参数数量不符应报错")
	}
	if _, err := xreflect.Call(add, 1, 2, 3); err == nil {
		t.Fatal("参数过多应报错")
	}
	if _, err := xreflect.Call(withPtr, 42); err == nil {
		t.Fatal("无法转换的参数应报错")
	}
	if _, err := xreflect.Call(sum, struct{}{}); err == nil {
		t.Fatal("无法转换的参数应报错")
	}
	// panic 被捕获
	boom := func() { panic("boom") }
	if _, err := xreflect.Call(boom); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Call(panic) = %v", err)
	}
	// 无返回值
	noRet := func() {}
	if _, err := xreflect.Call(noRet); err != nil {
		t.Fatalf("Call(无返回) = %v", err)
	}
	if _, err := xreflect.Call1(noRet); err == nil {
		t.Fatal("Call1 无返回值应报错")
	}
	if _, err := xreflect.Call(func(a []int) {}, []int{1, 2}); err != nil {
		t.Fatalf("Call(切片参数) = %v", err)
	}
}

// ---------------------------------------------------------------------------
// FillDefaults
// ---------------------------------------------------------------------------

type CfgForDefaults struct {
	Port int           `default:"8080"`
	Name string        `default:"gokit"`
	Dur  time.Duration `default:"5s"`
	Zero int           `default:"0"`
	Nest NestedCfg
	Ptr  *WithDefaults
	Ptr2 *NoDefaults
	NoD  string
}

type NestedCfg struct {
	Host string `default:"localhost"`
	Port int    `default:"3306"`
}

// WithDefaults 内部含默认值：其指针字段应被自动分配并填充。
type WithDefaults struct {
	Host string `default:"localhost"`
}

// NoDefaults 内部无默认值：其指针字段不应被无谓分配。
type NoDefaults struct {
	V string
}

func TestFillDefaults(t *testing.T) {
	var c CfgForDefaults
	if err := xreflect.FillDefaults(&c); err != nil {
		t.Fatalf("FillDefaults: %v", err)
	}
	if c.Port != 8080 {
		t.Fatalf("Port = %d", c.Port)
	}
	if c.Name != "gokit" {
		t.Fatalf("Name = %q", c.Name)
	}
	if c.Dur != 5*time.Second {
		t.Fatalf("Dur = %v", c.Dur)
	}
	// 嵌套结构体（自身无 default tag）也应被递归填充
	if c.Nest.Host != "localhost" || c.Nest.Port != 3306 {
		t.Fatalf("Nest = %+v", c.Nest)
	}
	if c.NoD != "" {
		t.Fatalf("NoD = %q", c.NoD)
	}
	// 指针嵌套且内部含默认值：应被分配并填充
	if c.Ptr == nil || c.Ptr.Host != "localhost" {
		t.Fatalf("Ptr 应被分配并填充: %+v", c.Ptr)
	}
	// 指针嵌套且内部无默认值：不应被无谓分配
	if c.Ptr2 != nil {
		t.Fatalf("Ptr2 不应被分配: %+v", c.Ptr2)
	}

	// 已有值不被覆盖
	c2 := CfgForDefaults{Port: 9090, Name: "custom"}
	if err := xreflect.FillDefaults(&c2); err != nil {
		t.Fatalf("FillDefaults: %v", err)
	}
	if c2.Port != 9090 || c2.Name != "custom" {
		t.Fatalf("已有值被覆盖: %+v", c2)
	}

	// 自定义 tag key
	type Other struct {
		V int `def:"7"`
	}
	var o Other
	if err := xreflect.FillDefaults(&o, "def"); err != nil {
		t.Fatalf("FillDefaults(自定义 tag): %v", err)
	}
	if o.V != 7 {
		t.Fatalf("V = %d", o.V)
	}

	// 错误场景
	if err := xreflect.FillDefaults(nil); err == nil {
		t.Fatal("FillDefaults(nil) 应报错")
	}
	if err := xreflect.FillDefaults(NestedCfg{}); err == nil {
		t.Fatal("非指针应报错")
	}
	var np *NestedCfg
	if err := xreflect.FillDefaults(np); err == nil {
		t.Fatal("nil 指针应报错")
	}

	// 无法转换的默认值应返回错误
	type Bad struct {
		N int `default:"abc"`
	}
	var b Bad
	if err := xreflect.FillDefaults(&b); err == nil {
		t.Fatal("无法解析的默认值应报错")
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkDeepCopyStruct(b *testing.B) {
	src := newOuter()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xreflect.DeepCopy(src)
	}
}

func BenchmarkDescribe(b *testing.B) {
	v := newOuter()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = xreflect.Describe(v)
	}
}

func BenchmarkGetField(b *testing.B) {
	v := newOuter()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = xreflect.Get(v, "Inner.A")
	}
}

// 并发访问元数据缓存，配合 -race 验证无数据竞争
func TestConcurrentDescribe(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				info, err := xreflect.Describe(Outer{})
				if err != nil || info == nil {
					t.Errorf("Describe: %v", err)
					return
				}
				_ = info.FieldNames()
				_ = info.Field("ID")
				_, _ = xreflect.GetAll(Outer{})
			}
		}()
	}
	wg.Wait()
}

func TestClearCache(t *testing.T) {
	_, _ = xreflect.Describe(Inner{})
	xreflect.ClearCache()
	// 清空后仍应能正常工作
	info, err := xreflect.Describe(Inner{})
	if err != nil || len(info.Fields) != 2 {
		t.Fatalf("ClearCache 后 Describe = %v,%v", info, err)
	}
}
