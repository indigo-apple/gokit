package xconv_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/indigo-apple/gokit/xconv"
	"github.com/indigo-apple/gokit/xmap"
	"github.com/indigo-apple/gokit/xreflect"
)

func eqI(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

func eqS(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func eqB(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func eqI64s(t *testing.T, name string, got, want []int64) {
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
// Cast 核心
// ---------------------------------------------------------------------------

func TestCastScalars(t *testing.T) {
	// 整数
	eqI(t, "Cast[int](\"42\")", xconv.Cast[int]("42"), 42)
	eqI(t, "Cast[int](42)", xconv.Cast[int](42), 42)
	eqI(t, "Cast[int](int64(42))", xconv.Cast[int](int64(42)), 42)
	eqI(t, "Cast[int](\"0x1F\")", xconv.Cast[int]("0x1F"), 31)
	eqI(t, "Cast[int](3.9)", xconv.Cast[int](3.9), 3)
	eqI(t, "Cast[int](true)", xconv.Cast[int](true), 1)
	eqI(t, "Cast[int](\"abc\")", xconv.Cast[int]("abc"), 0)
	eqI(t, "Cast[int](nil)", xconv.Cast[int](nil), 0)
	eqI(t, "Cast[int]([]int{1})", xconv.Cast[int]([]int{1}), 0)

	// 字符串
	eqS(t, "Cast[string](42)", xconv.Cast[string](42), "42")
	eqS(t, "Cast[string](true)", xconv.Cast[string](true), "true")
	eqS(t, "Cast[string]([]byte(\"hi\"))", xconv.Cast[string]([]byte("hi")), "hi")
	eqS(t, "Cast[string](nil)", xconv.Cast[string](nil), "")
	eqS(t, "Cast[string](1.5)", xconv.Cast[string](1.5), "1.5")

	// 布尔（宽松映射）
	for _, s := range []string{"true", "TRUE", "1", "yes", "Y", "on", "t"} {
		eqB(t, "Cast[bool]("+s+")", xconv.Cast[bool](s), true)
	}
	for _, s := range []string{"false", "0", "no", "n", "off", "f", ""} {
		eqB(t, "Cast[bool]("+s+")", xconv.Cast[bool](s), false)
	}
	eqB(t, "Cast[bool](2)", xconv.Cast[bool](2), true)
	eqB(t, "Cast[bool](\"maybe\")", xconv.Cast[bool]("maybe"), false)

	// 浮点
	if got := xconv.Cast[float64]("1.25"); got != 1.25 {
		t.Fatalf("Cast[float64] = %v", got)
	}
	if got := xconv.Cast[float32](2); got != 2 {
		t.Fatalf("Cast[float32] = %v", got)
	}

	// 时间与时长
	if got := xconv.Cast[time.Duration]("1h30m"); got != 90*time.Minute {
		t.Fatalf("Cast[Duration] = %v", got)
	}
	if got := xconv.Cast[time.Duration](100); got != 100 {
		t.Fatalf("Cast[Duration](纳秒) = %v", got)
	}
	if got := xconv.Cast[time.Time]("2024-01-02 15:04:05"); got.Year() != 2024 {
		t.Fatalf("Cast[time.Time] = %v", got)
	}

	// 复合类型
	eqI64s(t, "Cast[[]int]", toI64(xconv.Cast[[]int]([]any{1, "2", 3.0})), []int64{1, 2, 3})
	if got := xconv.Cast[[]string]([]any{"a", 1}); len(got) != 2 || got[1] != "1" {
		t.Fatalf("Cast[[]string] = %v", got)
	}
	if got := xconv.Cast[map[string]int](map[string]any{"a": "1"}); got["a"] != 1 {
		t.Fatalf("Cast[map[string]int] = %v", got)
	}
	if got := xconv.Cast[map[string]any](map[string]string{"k": "v"}); got["k"] != "v" {
		t.Fatalf("Cast[map[string]any] = %v", got)
	}
}

func toI64(s []int) []int64 {
	out := make([]int64, len(s))
	for i, v := range s {
		out[i] = int64(v)
	}
	return out
}

func TestCastErr(t *testing.T) {
	// 能区分「失败」与「零值」
	if v, err := xconv.CastErr[int]("42"); err != nil || v != 42 {
		t.Fatalf("CastErr(成功) = %v,%v", v, err)
	}
	if _, err := xconv.CastErr[int]("abc"); err == nil {
		t.Fatal("CastErr 应返回错误")
	}
	if _, err := xconv.CastErr[int]([]int{1}); err == nil {
		t.Fatal("类型不兼容应返回错误")
	}
	// nil 视为成功且零值
	if v, err := xconv.CastErr[int](nil); err != nil || v != 0 {
		t.Fatalf("CastErr(nil) = %v,%v", v, err)
	}
	// 目标为零值但转换成功时不应报错
	if v, err := xconv.CastErr[int]("0"); err != nil || v != 0 {
		t.Fatalf("CastErr(\"0\") = %v,%v", v, err)
	}
	if v, err := xconv.CastErr[uint8](-1); err == nil {
		t.Fatalf("负数转 uint8 应报错, got %v", v)
	}
}

func TestScalarHelpers(t *testing.T) {
	eqS(t, "ToString", xconv.ToString(42), "42")
	eqS(t, "ToString(nil)", xconv.ToString(nil), "")
	eqS(t, "ToString(time)", xconv.ToString(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))[:10], "2024-01-02")
	eqS(t, "ToString(duration)", xconv.ToString(90*time.Second), "1m30s")

	eqI(t, "ToInt", xconv.ToInt("42"), 42)
	if v, err := xconv.ToInt64("42"); err != nil || v != 42 {
		t.Fatalf("ToInt64 = %v,%v", v, err)
	}
	if _, err := xconv.ToInt64("abc"); err == nil {
		t.Fatal("ToInt64(abc) 应报错")
	}
	if got := xconv.ToUint64("42"); got != 42 {
		t.Fatalf("ToUint64 = %v", got)
	}
	if got := xconv.ToUint64("-1"); got != 0 {
		t.Fatalf("ToUint64(-1) = %v", got)
	}
	if got := xconv.ToFloat64("1.5"); got != 1.5 {
		t.Fatalf("ToFloat64 = %v", got)
	}
	if got := xconv.ToFloat32("1.5"); got != 1.5 {
		t.Fatalf("ToFloat32 = %v", got)
	}
	eqB(t, "ToBool", xconv.ToBool("yes"), true)
	if got := xconv.ToDuration("2m"); got != 2*time.Minute {
		t.Fatalf("ToDuration = %v", got)
	}
	if got := xconv.ToTime(int64(1700000000)); got.Unix() != 1700000000 {
		t.Fatalf("ToTime = %v", got)
	}
	eqS(t, "ToBytes(string)", string(xconv.ToBytes("hi")), "hi")
	eqS(t, "ToBytes([]byte)", string(xconv.ToBytes([]byte("hi"))), "hi")
	eqS(t, "ToBytes(int)", string(xconv.ToBytes(42)), "42")
	if xconv.ToBytes(nil) != nil {
		t.Fatal("ToBytes(nil) 应为 nil")
	}
	if got := xconv.ToStringSlice([]any{1, "a"}); len(got) != 2 || got[0] != "1" {
		t.Fatalf("ToStringSlice = %v", got)
	}
	if got := xconv.ToIntSlice([]any{"1", 2}); len(got) != 2 || got[1] != 2 {
		t.Fatalf("ToIntSlice = %v", got)
	}
	if got := xconv.ToStringMap(map[string]any{"a": 1}); got["a"] != 1 {
		t.Fatalf("ToStringMap = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 容错链式取值
// ---------------------------------------------------------------------------

func TestValueChain(t *testing.T) {
	cfg := map[string]any{
		"port": "8080",
		"host": "",
		"db": map[string]any{
			"dsn":     "root@tcp(localhost)",
			"timeout": "5s",
			"pool":    map[string]any{"max": 20},
		},
		"tags":  []any{"a", "b"},
		"debug": "true",
		"rate":  1.5,
	}

	eqI(t, "V(存在的值)", xconv.V(cfg["port"]).Int(), 8080)
	eqI(t, "V(缺失的键)", xconv.V(cfg["nope"]).Int(3306), 3306)
	eqI(t, "V(nil)", xconv.V(nil).Int(3306), 3306)
	eqI(t, "V(显式零值)", xconv.V(0).Int(3306), 3306)
	eqS(t, "V(空串走默认)", xconv.V(cfg["host"]).String("localhost"), "localhost")
	eqS(t, "V(空串无默认)", xconv.V(cfg["host"]).String(), "")
	eqB(t, "V(bool)", xconv.V(cfg["debug"]).Bool(), true)
	if got := xconv.V(cfg["rate"]).Float64(); got != 1.5 {
		t.Fatalf("V(rate) = %v", got)
	}
	if got := xconv.V(cfg).Path("db.timeout").Duration(); got != 5*time.Second {
		t.Fatalf("V.Path(db.timeout) = %v", got)
	}
	eqI(t, "Path 多层", xconv.V(cfg).Path("db.pool.max").Int(), 20)
	eqI(t, "Path 未命中走默认", xconv.V(cfg).Path("db.pool.min").Int(5), 5)
	eqI(t, "Get 是 Path 别名", xconv.V(cfg).Get("db.pool.max").Int(), 20)

	got := xconv.V(cfg["tags"]).StringSlice()
	if len(got) != 2 || got[0] != "a" {
		t.Fatalf("V(tags).StringSlice() = %v", got)
	}
	if got := xconv.V(cfg).Map()["port"]; got != "8080" {
		t.Fatalf("V().Map() = %v", got)
	}

	// Exists / IsEmpty
	eqB(t, "Exists(存在)", xconv.V(1).Exists(), true)
	eqB(t, "Exists(nil)", xconv.V(nil).Exists(), false)
	var np *int
	eqB(t, "Exists(nil 指针)", xconv.V(np).Exists(), false)
	eqB(t, "Exists(空 map)", xconv.V(map[string]int{}).Exists(), true)
	eqB(t, "IsEmpty(0)", xconv.V(0).IsEmpty(), true)
	eqB(t, "IsEmpty(空串)", xconv.V("").IsEmpty(), true)
	eqB(t, "IsEmpty(空切片)", xconv.V([]int{}).IsEmpty(), true)
	eqB(t, "IsEmpty(非空)", xconv.V([]int{1}).IsEmpty(), false)
	eqB(t, "IsEmpty(nil)", xconv.V(nil).IsEmpty(), true)

	// Raw
	if got := xconv.V(cfg).Raw(); got == nil {
		t.Fatal("Raw 应为原值")
	}
	// 空路径 / nil 路径安全
	eqI(t, "Path(空路径)", xconv.V(cfg).Path("").Int(9), 9)
	eqI(t, "Path(nil 值)", xconv.V(nil).Path("a").Int(9), 9)
}

type cfgStruct struct {
	Name string
	Port int
	DB   struct {
		Host string
	}
}

func TestValuePathOnStruct(t *testing.T) {
	var s cfgStruct
	s.Name = "svc"
	s.Port = 8080
	s.DB.Host = "localhost"

	eqS(t, "V(struct).Path(Name)", xconv.V(s).Path("Name").String(), "svc")
	eqI(t, "V(struct).Path(Port)", xconv.V(s).Path("Port").Int(), 8080)
	eqS(t, "V(struct).Path(DB.Host)", xconv.V(s).Path("DB.Host").String(), "localhost")
	eqI(t, "V(struct).Path(缺失)", xconv.V(s).Path("Nope").Int(1), 1)
	eqS(t, "V(&struct)", xconv.V(&s).Path("Name").String(), "svc")
	// map[string]string 也应可用
	eqS(t, "V(map[string]string)", xconv.V(map[string]string{"k": "v"}).Path("k").String(), "v")
}

// ---------------------------------------------------------------------------
// map ↔ slice
// ---------------------------------------------------------------------------

func TestMapToSlice(t *testing.T) {
	m := map[string]int{"b": 2, "a": 1, "c": 3}

	got := xconv.MapToSliceSorted(m, func(k string, v int) string { return k + ":" + xconv.ToString(v) })
	if strings.Join(got, ",") != "a:1,b:2,c:3" {
		t.Fatalf("MapToSliceSorted = %v", got)
	}
	// 非排序版本元素集合一致（顺序随机）
	unsorted := xconv.MapToSlice(m, func(k string, v int) string { return k })
	if len(unsorted) != 3 {
		t.Fatalf("MapToSlice = %v", unsorted)
	}
	if got := xconv.MapToSlice[string, string, int](nil, func(string, int) string { return "" }); got != nil {
		t.Fatalf("MapToSlice(nil) = %v", got)
	}
	if got := xconv.MapToSliceSorted[string, string, int](nil, func(string, int) string { return "" }); got != nil {
		t.Fatalf("MapToSliceSorted(nil) = %v", got)
	}

	if got := xconv.MapToSliceValuesSorted(m); len(got) != 3 || got[0] != 1 {
		t.Fatalf("MapToSliceValuesSorted = %v", got)
	}
	if got := xconv.MapToSliceValues(m); len(got) != 3 {
		t.Fatalf("MapToSliceValues = %v", got)
	}
	if got := xconv.MapToSliceKeysSorted(m); len(got) != 3 || got[0] != "a" {
		t.Fatalf("MapToSliceKeysSorted = %v", got)
	}
	if got := xconv.MapToSliceKeys(m); len(got) != 3 {
		t.Fatalf("MapToSliceKeys = %v", got)
	}

	entries := xconv.MapToEntriesSorted(m)
	if len(entries) != 3 || entries[0].Key != "a" || entries[0].Val != 1 {
		t.Fatalf("MapToEntriesSorted = %v", entries)
	}
	if got := xconv.MapToEntries(m); len(got) != 3 {
		t.Fatalf("MapToEntries = %v", got)
	}
}

func TestSliceToMap(t *testing.T) {
	type user struct {
		ID   int
		Name string
	}
	users := []user{{1, "a"}, {2, "b"}}

	byID := xconv.SliceToMap(users, func(u user) int { return u.ID }, func(u user) string { return u.Name })
	if byID[1] != "a" || byID[2] != "b" {
		t.Fatalf("SliceToMap = %v", byID)
	}
	self := xconv.SliceToMapSelf(users, func(u user) int { return u.ID })
	if self[2].Name != "b" {
		t.Fatalf("SliceToMapSelf = %v", self)
	}

	set := xconv.SliceToSet([]string{"a", "a", "b"})
	if len(set) != 2 {
		t.Fatalf("SliceToSet = %v", set)
	}
	if got := xconv.SliceToSet[string](nil); got != nil {
		t.Fatalf("SliceToSet(nil) = %v", got)
	}

	counts := xconv.SliceToCountMap([]int{1, 1, 2})
	if counts[1] != 2 || counts[2] != 1 {
		t.Fatalf("SliceToCountMap = %v", counts)
	}
	if got := xconv.SliceToCountMap[int](nil); got != nil {
		t.Fatalf("SliceToCountMap(nil) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// slice ↔ string
// ---------------------------------------------------------------------------

func TestSliceToString(t *testing.T) {
	eqS(t, "SliceToString(int)", xconv.SliceToString([]int{1, 2, 3}, ","), "1,2,3")
	eqS(t, "SliceToString(string)", xconv.SliceToString([]string{"a", "b"}, "-"), "a-b")
	eqS(t, "SliceToString(空)", xconv.SliceToString([]int{}, ","), "")
	eqS(t, "SliceToString(nil)", xconv.SliceToString[int](nil, ","), "")
	eqS(t, "SliceToStringFunc", xconv.SliceToStringFunc([]int{1, 2}, "|", func(v int) string {
		return xconv.ToString(v * 10)
	}), "10|20")
	eqS(t, "SliceToStringFunc(nil fn)", xconv.SliceToStringFunc([]int{1}, ",", nil), "")
	eqS(t, "SliceToStringQuoted", xconv.SliceToStringQuoted([]string{"a", "b"}, ","), `"a","b"`)
	eqS(t, "SliceToStringQuoted(含引号)", xconv.SliceToStringQuoted([]string{`a"b`}, ","), `"a\"b"`)
	eqS(t, "SliceToStringSkipEmpty", xconv.SliceToStringSkipEmpty([]string{"a", "", "b"}, ","), "a,b")

	got := xconv.StringToSlice("a,b", ",")
	if len(got) != 2 || got[0] != "a" {
		t.Fatalf("StringToSlice = %v", got)
	}
	if got := xconv.StringToSlice("", ","); got != nil {
		t.Fatalf("StringToSlice(空) = %v", got)
	}
	// 与 strings.Split 一致：保留空段
	if got := xconv.StringToSlice("a,,b", ","); len(got) != 3 {
		t.Fatalf("StringToSlice 应保留空段: %v", got)
	}
	got = xconv.StringToSliceClean("a, b,,c", ",")
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("StringToSliceClean = %v", got)
	}
	if got := xconv.StringToSliceClean("", ","); got != nil {
		t.Fatalf("StringToSliceClean(空) = %v", got)
	}

	ints, err := xconv.StringToIntSlice("1, 2,,3", ",")
	if err != nil || len(ints) != 3 || ints[2] != 3 {
		t.Fatalf("StringToIntSlice = %v,%v", ints, err)
	}
	if _, err := xconv.StringToIntSlice("1,abc", ","); err == nil {
		t.Fatal("StringToIntSlice 应报错")
	}
	if got, err := xconv.StringToIntSlice("", ","); err != nil || got != nil {
		t.Fatalf("StringToIntSlice(空) = %v,%v", got, err)
	}

	fs, err := xconv.StringToFloatSlice("1.5, 2", ",")
	if err != nil || len(fs) != 2 || fs[0] != 1.5 {
		t.Fatalf("StringToFloatSlice = %v,%v", fs, err)
	}
	if _, err := xconv.StringToFloatSlice("x", ","); err == nil {
		t.Fatal("StringToFloatSlice 应报错")
	}
}

// ---------------------------------------------------------------------------
// struct ↔ map ↔ JSON
// ---------------------------------------------------------------------------

type profile struct {
	Name    string            `json:"name"`
	Age     int               `json:"age"`
	Tags    []string          `json:"tags,omitempty"`
	Meta    map[string]string `json:"meta"`
	Skip    string            `json:"-"`
	Created time.Time         `json:"created"`
	Extra   *int              `json:"extra"`
}

func newProfile() profile {
	e := 7
	return profile{
		Name:    "bob",
		Age:     30,
		Tags:    []string{"x", "y"},
		Meta:    map[string]string{"k": "v"},
		Skip:    "hidden",
		Created: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		Extra:   &e,
	}
}

func TestStructMapJSON(t *testing.T) {
	p := newProfile()

	m, err := xconv.ToMap(p)
	if err != nil {
		t.Fatalf("ToMap: %v", err)
	}
	if m["name"] != "bob" || m["age"] != 30 {
		t.Fatalf("ToMap = %#v", m)
	}
	if _, ok := m["Skip"]; ok {
		t.Fatal(`json:"-" 字段应被排除`)
	}
	if m["extra"] != 7 {
		t.Fatalf("指针字段应解引用: %#v", m["extra"])
	}

	// 往返
	var back profile
	if err := xconv.FromMap(m, &back); err != nil {
		t.Fatalf("FromMap: %v", err)
	}
	if back.Name != p.Name || back.Age != p.Age || back.Skip != "" {
		t.Fatalf("FromMap 往返不一致: %+v", back)
	}
	if len(back.Tags) != 2 || !back.Created.Equal(p.Created) {
		t.Fatalf("FromMap 集合/时间字段不一致: %+v", back)
	}

	ms, err := xconv.ToMapString(p)
	if err != nil || ms["age"] != "30" {
		t.Fatalf("ToMapString = %v,%v", ms, err)
	}
	// 切片 / map 字段用 JSON 表达
	if ms["tags"] != `["x","y"]` {
		t.Fatalf("ToMapString(tags) = %q", ms["tags"])
	}
	if ms["meta"] != `{"k":"v"}` {
		t.Fatalf("ToMapString(meta) = %q", ms["meta"])
	}
	// 时长 / 时间使用可读格式
	if got := xconv.ToString(90 * time.Second); got != "1m30s" {
		t.Fatalf("ToString(duration) = %q", got)
	}
	if got := xconv.ToString(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)); !strings.HasPrefix(got, "2024-01-02T03:04:05") {
		t.Fatalf("ToString(time) = %q", got)
	}

	// ToStruct（map / JSON / string）
	got, err := xconv.ToStruct[profile](m)
	if err != nil || got.Name != "bob" {
		t.Fatalf("ToStruct(map) = %+v,%v", got, err)
	}
	b, _ := json.Marshal(p)
	if got, err := xconv.ToStruct[profile](b); err != nil || got.Age != 30 {
		t.Fatalf("ToStruct([]byte) = %+v,%v", got, err)
	}
	if got, err := xconv.ToStruct[profile](string(b)); err != nil || got.Name != "bob" {
		t.Fatalf("ToStruct(string) = %+v,%v", got, err)
	}
	if got, err := xconv.ToStruct[profile](nil); err != nil || got.Name != "" {
		t.Fatalf("ToStruct(nil) = %+v,%v", got, err)
	}
	if _, err := xconv.ToStruct[profile](map[string]any{}); err != nil {
		t.Fatalf("ToStruct(空 map) = %v", err)
	}

	// 同类型直接赋值
	if got, err := xconv.ToStruct[profile](p); err != nil || got.Name != "bob" {
		t.Fatalf("ToStruct(profile) = %+v,%v", got, err)
	}

	// ToStructs
	list, err := xconv.ToStructs[profile]([]any{m, m})
	if err != nil || len(list) != 2 || list[1].Name != "bob" {
		t.Fatalf("ToStructs([]any) = %+v,%v", list, err)
	}
	jsonArray := "[" + string(b) + "," + string(b) + "]"
	if got, err := xconv.ToStructs[profile](jsonArray); err != nil || len(got) != 2 {
		t.Fatalf("ToStructs(JSON) = %+v,%v", got, err)
	}
	if got, err := xconv.ToStructs[profile](nil); err != nil || got != nil {
		t.Fatalf("ToStructs(nil) = %v,%v", got, err)
	}
	if _, err := xconv.ToStructs[profile]("not-a-slice"); err == nil {
		t.Fatal("ToStructs(非切片) 应报错")
	}
	if _, err := xconv.ToStructs[profile](42); err == nil {
		t.Fatal("ToStructs(int) 应报错")
	}
}

func TestJSON(t *testing.T) {
	p := newProfile()

	b, err := xconv.ToJSON(p)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if !strings.Contains(string(b), `"name":"bob"`) {
		t.Fatalf("ToJSON = %s", b)
	}
	if s := xconv.ToJSONString(p); !strings.Contains(s, "bob") {
		t.Fatalf("ToJSONString = %s", s)
	}
	// 无法序列化的值 → 空串 / 错误
	type bad struct {
		C chan int
	}
	if got := xconv.ToJSONString(bad{}); got != "" {
		t.Fatalf("ToJSONString(不可序列化) = %q", got)
	}
	if _, err := xconv.ToJSON(bad{}); err == nil {
		t.Fatal("ToJSON 应报错")
	}
	if b2, err := xconv.ToJSONIndent(p); err != nil || !strings.Contains(string(b2), "\n") {
		t.Fatalf("ToJSONIndent = %s,%v", b2, err)
	}

	got, err := xconv.FromJSON[profile](b)
	if err != nil || got.Name != "bob" {
		t.Fatalf("FromJSON = %+v,%v", got, err)
	}
	if got, err := xconv.FromJSON[profile](nil); err != nil || got.Name != "" {
		t.Fatalf("FromJSON(nil) = %+v,%v", got, err)
	}
	if _, err := xconv.FromJSON[profile]([]byte("{bad}")); err == nil {
		t.Fatal("FromJSON 应报错")
	}
	if got, err := xconv.FromJSONString[profile](string(b)); err != nil || got.Age != 30 {
		t.Fatalf("FromJSONString = %+v,%v", got, err)
	}
	if got := xconv.MustFromJSON[profile](b); got.Name != "bob" {
		t.Fatalf("MustFromJSON = %+v", got)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("MustFromJSON 应在失败时 panic")
			}
		}()
		_ = xconv.MustFromJSON[profile]([]byte("{bad}"))
	}()

	// DeepCopyJSON
	cp := xconv.DeepCopyJSON(p)
	cp.Tags[0] = "MUT"
	if p.Tags[0] != "x" {
		t.Fatal("DeepCopyJSON 应深拷贝")
	}
	// 不可序列化时原样返回
	if got := xconv.DeepCopyJSON(bad{}); got != (bad{}) {
		t.Fatalf("DeepCopyJSON(不可序列化) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 数值 / 进制
// ---------------------------------------------------------------------------

func TestBytesHelpers(t *testing.T) {
	eqS(t, "BytesToHuman", xconv.BytesToHuman(1536), "1.5 KB")
	eqS(t, "BytesToHumanSI", xconv.BytesToHumanSI(1500), "1.5 KB")
	if v, err := xconv.HumanToBytes("1.5MB"); err != nil || v != 1572864 {
		t.Fatalf("HumanToBytes = %v,%v", v, err)
	}
	if _, err := xconv.HumanToBytes("garbage"); err == nil {
		t.Fatal("HumanToBytes 应报错")
	}
}

func TestBaseConversion(t *testing.T) {
	eqS(t, "IntToBase(255,16)", xconv.IntToBase(255, 16), "ff")
	eqS(t, "IntToBase(255,2)", xconv.IntToBase(255, 2), "11111111")
	eqS(t, "IntToBase(0,16)", xconv.IntToBase(0, 16), "0")
	eqS(t, "IntToBase(-255,16)", xconv.IntToBase(-255, 16), "-ff")
	eqS(t, "IntToBase(越界 base)", xconv.IntToBase(10, 99), "10")

	// 62 进制往返
	big := "zZ9"
	n, err := xconv.BaseToInt(big, 62)
	if err != nil {
		t.Fatalf("BaseToInt: %v", err)
	}
	eqS(t, "62 进制往返", xconv.IntToBase(n, 62), big)

	// 覆盖所有进制与多种数值
	values := []int64{0, 1, -1, 7, 255, -255, 4096, 123456789, -987654321,
		math.MaxInt64, math.MinInt64}
	for base := 2; base <= 62; base++ {
		for _, v := range values {
			s := xconv.IntToBase(v, base)
			back, err := xconv.BaseToInt(s, base)
			if err != nil {
				t.Fatalf("BaseToInt(%q, base=%d) 失败: %v", s, base, err)
			}
			if back != v {
				t.Fatalf("base=%d 往返失败: %d → %q → %d", base, v, s, back)
			}
		}
	}

	// 错误场景
	if _, err := xconv.BaseToInt("", 16); err == nil {
		t.Fatal("空串应报错")
	}
	if _, err := xconv.BaseToInt("g", 16); err == nil {
		t.Fatal("非法数字应报错")
	}
	if _, err := xconv.BaseToInt("-", 16); err == nil {
		t.Fatal("只有符号应报错")
	}
	if _, err := xconv.BaseToInt("1", 1); err == nil {
		t.Fatal("非法进制应报错")
	}
	if _, err := xconv.BaseToInt("1", 99); err == nil {
		t.Fatal("非法进制应报错")
	}
	if _, err := xconv.BaseToInt("99999999999999999999", 10); err == nil {
		t.Fatal("溢出应报错")
	}
	if _, err := xconv.BaseToInt("+ff", 16); err != nil {
		t.Fatalf("带正号应可解析: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 指针与空值
// ---------------------------------------------------------------------------

func TestPointerAndEmpty(t *testing.T) {
	p := xconv.Ptr(42)
	if p == nil || *p != 42 {
		t.Fatalf("Ptr = %v", p)
	}
	if xconv.PtrOrNil(0) != nil {
		t.Fatal("PtrOrNil(零值) 应为 nil")
	}
	if got := xconv.PtrOrNil(5); got == nil || *got != 5 {
		t.Fatalf("PtrOrNil = %v", got)
	}
	if xconv.PtrOrNil("") != nil {
		t.Fatal("PtrOrNil(空串) 应为 nil")
	}

	if got := xconv.Deref(p); got != 42 {
		t.Fatalf("Deref = %v", got)
	}
	if got := xconv.Deref[int](nil); got != 0 {
		t.Fatalf("Deref(nil) = %v", got)
	}
	if got := xconv.DerefOr[int](nil, 9); got != 9 {
		t.Fatalf("DerefOr(nil) = %v", got)
	}
	if got := xconv.Or(p, 9); got != 42 {
		t.Fatalf("Or = %v", got)
	}
	if got := xconv.Or[int](nil, 9); got != 9 {
		t.Fatalf("Or(nil) = %v", got)
	}

	// typed nil 陷阱
	var np *int
	var i any = np
	if i == nil {
		t.Fatal("前提错误")
	}
	eqB(t, "IsNil(typed nil)", xconv.IsNil(i), true)
	eqB(t, "IsNil(nil)", xconv.IsNil(nil), true)
	eqB(t, "IsNil(0)", xconv.IsNil(0), false)

	empties := []any{nil, "", 0, 0.0, false, []int{}, map[string]int{}, (*int)(nil)}
	for _, v := range empties {
		if !xconv.IsEmpty(v) {
			t.Fatalf("IsEmpty(%#v) 应为 true", v)
		}
	}
	nonEmpties := []any{"a", 1, 1.5, true, []int{1}, map[string]int{"a": 1}}
	for _, v := range nonEmpties {
		if xconv.IsEmpty(v) {
			t.Fatalf("IsEmpty(%#v) 应为 false", v)
		}
	}
	eqB(t, "IsEmpty(空结构体)", xconv.IsEmpty(struct{}{}), true)
	eqB(t, "IsEmpty(非空结构体)", xconv.IsEmpty(struct{ A int }{1}), false)

	eqI(t, "Default", xconv.Default(0, 9), 9)
	eqI(t, "Default(有值)", xconv.Default(1, 9), 1)
	eqS(t, "Default(string)", xconv.Default("", "d"), "d")
	eqS(t, "Coalesce", xconv.Coalesce("", "", "c"), "c")
	eqI(t, "Coalesce()", xconv.Coalesce[int](), 0)

	eqI(t, "Zero[int]", xconv.Zero[int](), 0)
	eqS(t, "Zero[string]", xconv.Zero[string](), "")
	eqB(t, "IsType[int]", xconv.IsType[int](1), true)
	eqB(t, "IsType[string]", xconv.IsType[string](1), false)
	eqB(t, "IsType(nil)", xconv.IsType[int](nil), false)
}

// ---------------------------------------------------------------------------
// 与 xreflect 选项透传
// ---------------------------------------------------------------------------

func TestMapOptionsPassthrough(t *testing.T) {
	type inner struct {
		A int `json:"a"`
	}
	type outer struct {
		Name string `json:"name"`
		In   inner  `json:"in"`
	}
	o := outer{Name: "n", In: inner{A: 1}}

	m, err := xconv.ToMap(o, xreflect.WithNested())
	if err != nil {
		t.Fatalf("ToMap: %v", err)
	}
	sub, ok := m["in"].(map[string]any)
	if !ok || sub["a"] != 1 {
		t.Fatalf("WithNested 未生效: %#v", m["in"])
	}

	m2, err := xconv.ToMap(&outer{}, xreflect.WithOmitEmpty())
	if err != nil {
		t.Fatalf("ToMap: %v", err)
	}
	if len(m2) != 0 {
		t.Fatalf("WithOmitEmpty 未生效: %#v", m2)
	}

	// FromMap 透传选项：用 Go 字段名匹配
	var o2 outer
	if err := xconv.FromMap(map[string]any{"Name": "x"}, &o2, xreflect.WithGoFieldName()); err != nil {
		t.Fatalf("FromMap: %v", err)
	}
	if o2.Name != "x" {
		t.Fatalf("WithGoFieldName 未生效: %+v", o2)
	}
}

// ---------------------------------------------------------------------------
// 综合场景：从原始 HTTP 表单（全字符串 map）构建配置
// ---------------------------------------------------------------------------

func TestRealisticFormParsing(t *testing.T) {
	// 模拟 query string 解析结果：所有值都是字符串
	form := map[string]any{
		"name":    "  my-service  ",
		"port":    "8080",
		"debug":   "on",
		"timeout": "1500ms",
		"ratio":   "0.75",
		"tags":    "a,b,c",
		"startAt": "2024-03-04 05:06:07",
		"limits":  map[string]any{"max": "100"},
	}

	type config struct {
		Name    string        `json:"name"`
		Port    int           `json:"port"`
		Debug   bool          `json:"debug"`
		Timeout time.Duration `json:"timeout"`
		Ratio   float64       `json:"ratio"`
		Tags    []string      `json:"tags"`
		StartAt time.Time     `json:"startAt"`
		Limits  struct {
			Max int `json:"max"`
		} `json:"limits"`
	}

	// 手工 + 链式两种方式都应得到正确结果
	cfg := config{
		Name:    strings.TrimSpace(xconv.V(form).Path("name").String()),
		Port:    xconv.V(form["port"]).Int(80),
		Debug:   xconv.V(form["debug"]).Bool(),
		Timeout: xconv.V(form["timeout"]).Duration(time.Second),
		Ratio:   xconv.V(form["ratio"]).Float64(),
		Tags:    xconv.StringToSliceClean(xconv.V(form["tags"]).String(), ","),
		StartAt: xconv.V(form["startAt"]).Time(),
	}
	cfg.Limits.Max = xconv.V(form).Path("limits.max").Int()

	if cfg.Name != "my-service" || cfg.Port != 8080 || !cfg.Debug {
		t.Fatalf("基础字段错误: %+v", cfg)
	}
	if cfg.Timeout != 1500*time.Millisecond || cfg.Ratio != 0.75 {
		t.Fatalf("数值字段错误: %+v", cfg)
	}
	if len(cfg.Tags) != 3 || cfg.Tags[2] != "c" {
		t.Fatalf("切片字段错误: %v", cfg.Tags)
	}
	if cfg.StartAt.Year() != 2024 || cfg.StartAt.Hour() != 5 {
		t.Fatalf("时间字段错误: %v", cfg.StartAt)
	}
	if cfg.Limits.Max != 100 {
		t.Fatalf("嵌套字段错误: %+v", cfg.Limits)
	}

	// 同一份数据走 MapToStruct 也应成功
	var cfg2 config
	if err := xconv.FromMap(form, &cfg2); err != nil {
		t.Fatalf("FromMap: %v", err)
	}
	if cfg2.Port != 8080 || cfg2.Timeout != 1500*time.Millisecond || !cfg2.Debug {
		t.Fatalf("FromMap 结果错误: %+v", cfg2)
	}
	if len(cfg2.Tags) != 3 || cfg2.Tags[2] != "c" {
		// Assign 会把字符串按逗号切分填进切片（env / 表单场景的通用约定）
		t.Fatalf("FromMap 切片切分: %v", cfg2.Tags)
	}
}

// 确保 JSON 数字（float64）场景下的转换一致
func TestJSONNumberRoundTrip(t *testing.T) {
	raw := `{"id":123,"price":19.99,"qty":3,"enabled":true,"tags":["a"]}`
	m, err := xconv.FromJSON[map[string]any]([]byte(raw))
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	eqI(t, "JSON 数字 → int", xconv.V(m["id"]).Int(), 123)
	eqI(t, "JSON 数字 → int64", int(xconv.V(m["qty"]).Int64()), 3)
	if got := xconv.V(m["price"]).Float64(); got != 19.99 {
		t.Fatalf("JSON 数字 → float64 = %v", got)
	}
	eqB(t, "JSON bool", xconv.V(m["enabled"]).Bool(), true)
	eqS(t, "JSON 数组 → string slice", xconv.V(m["tags"]).StringSlice()[0], "a")

	// 嵌套路径
	eqI(t, "嵌套 JSON 路径", xconv.V(map[string]any{
		"a": map[string]any{"b": map[string]any{"c": float64(7)}},
	}).Path("a.b.c").Int(), 7)
}

// 与 xmap 的协作
func TestInteropWithXmap(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2}
	pairs := xconv.MapToEntriesSorted(m)
	if len(pairs) != 2 {
		t.Fatalf("MapToEntriesSorted = %v", pairs)
	}
	if got := xmap.FromEntries(pairs); got["a"] != 1 {
		t.Fatalf("xmap.FromEntries = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkCastStringToInt(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xconv.ToInt("12345")
	}
}

func BenchmarkCastIntToString(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xconv.ToString(12345)
	}
}

func BenchmarkValueChain(b *testing.B) {
	cfg := map[string]any{"db": map[string]any{"pool": map[string]any{"max": 20}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xconv.V(cfg).Path("db.pool.max").Int(5)
	}
}

func BenchmarkSliceToString(b *testing.B) {
	s := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xconv.SliceToString(s, ",")
	}
}

func BenchmarkMapToSliceSorted(b *testing.B) {
	m := map[string]int{"c": 3, "a": 1, "b": 2}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xconv.MapToSliceSorted(m, func(k string, v int) string { return k })
	}
}
