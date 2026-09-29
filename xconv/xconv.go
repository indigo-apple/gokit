// Package xconv 是本工具链的「类型转换枢纽」：把任意输入转成目标类型，永不 panic。
//
// 设计要点：
//   - **统一底座**：所有转换都建立在 xreflect.Assign 的弱类型语义之上，
//     因此 `ToInt("42")`、`ToBool("yes")`、`ToString(42)` 与
//     `xreflect.SetValue(&dst, src)` 的行为完全一致，不会出现「两套规则」；
//   - **永不 panic**：失败时返回目标类型的零值；需要区分「转换失败」与
//     「结果本来就是零值」时，请使用 CastErr / ToInt64 等带 error 的变体；
//   - **容错链式取值**：`xconv.V(m["age"]).Int(18)` 一行拿到带默认值的值，
//     并可用 `.Path("user.profile.age")` 深入嵌套 map / struct；
//   - **map ↔ slice ↔ string ↔ struct ↔ JSON** 的互转集中在本包，
//     底层实现分别复用 xmap / xslice / xreflect，不重复造轮子；
//   - **零值语义**：nil 输入一律返回目标类型零值，绝不产生「意外的非零值」。
//
// 并发安全：本包全部为纯函数，可并发调用。
package xconv

import (
	"encoding/json"
	"math/bits"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/zhangwenxing/gokit/xconstraints"
	"github.com/zhangwenxing/gokit/xerr"
	"github.com/zhangwenxing/gokit/xmap"
	"github.com/zhangwenxing/gokit/xnum"
	"github.com/zhangwenxing/gokit/xreflect"
	"github.com/zhangwenxing/gokit/xslice"
)

// ---------------------------------------------------------------------------
// 核心：Cast
// ---------------------------------------------------------------------------

// Cast 把 v 尽力转换为 T，失败时返回 T 的零值（不 panic、不返回 error）。
//
// 这是本包所有 ToXxx 函数的统一实现，语义与 xreflect.Assign 一致：
//
//	Cast[int]("42")        // 42
//	Cast[int]("abc")       // 0
//	Cast[string](42)       // "42"
//	Cast[bool]("yes")      // true
//	Cast[time.Duration]("1h30m")  // 90 * time.Minute
//	Cast[[]int]([]any{1, "2"})    // []int{1, 2}
//
// 需要感知失败原因时请使用 CastErr。
func Cast[T any](v any) T {
	out, _ := CastErr[T](v)
	return out
}

// CastErr 把 v 转换为 T，返回转换错误。
//
// 与 Cast 的区别：本函数能区分「转换失败」与「结果恰为零值」。
func CastErr[T any](v any) (T, error) {
	var out T
	if v == nil {
		return out, nil
	}
	dst := reflect.ValueOf(&out).Elem()
	if err := xreflect.Assign(dst, v); err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 标量转换（永不 panic，失败返回零值）
// ---------------------------------------------------------------------------

// ToString 转为字符串。
//
//	nil → ""，[]byte → string，数字 / bool → 十进制文本，
//	time.Time → RFC3339Nano，time.Duration → "1h30m0s"。
func ToString(v any) string { return Cast[string](v) }

// ToInt 转为 int。
//
//	"42" → 42，"0x1F" → 31，"1.9" → 1（截断），"abc" → 0
func ToInt(v any) int { return Cast[int](v) }

// ToInt64 转为 int64，返回转换错误。
func ToInt64(v any) (int64, error) { return CastErr[int64](v) }

// ToUint64 转为 uint64（负数会失败）。
func ToUint64(v any) uint64 { return Cast[uint64](v) }

// ToFloat64 转为 float64。
func ToFloat64(v any) float64 { return Cast[float64](v) }

// ToFloat32 转为 float32。
func ToFloat32(v any) float32 { return Cast[float32](v) }

// ToBool 转为 bool。
//
// 宽松映射（大小写不敏感）：
//
//	true : "1" "true" "yes" "y" "on" "t" 及任意非零数字
//	false: "0" "false" "no" "n" "off" "f" 及空串
func ToBool(v any) bool { return Cast[bool](v) }

// ToDuration 转为 time.Duration。
//
//	"1h30m" → 90 分钟；纯数字按纳秒处理（与 time.Duration 底层单位一致）。
func ToDuration(v any) time.Duration { return Cast[time.Duration](v) }

// ToTime 转为 time.Time。
//
// 支持常见布局自动嗅探，以及按数值量级自动识别的时间戳（秒/毫秒/微秒/纳秒）。
func ToTime(v any) time.Time { return Cast[time.Time](v) }

// ToBytes 转为 []byte。
//
// []byte / string 直接转换；其它类型先转字符串再取字节。
func ToBytes(v any) []byte {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return x
	case string:
		return []byte(x)
	}
	return []byte(ToString(v))
}

// ToStringSlice 转为 []string（支持任意可迭代的源）。
func ToStringSlice(v any) []string { return Cast[[]string](v) }

// ToIntSlice 转为 []int。
func ToIntSlice(v any) []int { return Cast[[]int](v) }

// ToStringMap 转为 map[string]any。
func ToStringMap(v any) map[string]any { return Cast[map[string]any](v) }

// ---------------------------------------------------------------------------
// 容错链式取值
// ---------------------------------------------------------------------------

// Value 是「容错取值链」的载体。
//
// 所有 Xxx 方法在值缺失或无法转换时返回调用方给定的默认值：
//
//	port := xconv.V(cfg["port"]).Int(8080)
//	host := xconv.V(cfg).Path("db.host").String("localhost")
type Value struct {
	v any
}

// V 包装一个任意值，开始容错取值链。
func V(v any) Value { return Value{v: v} }

// Raw 返回原始值。
func (x Value) Raw() any { return x.v }

// Exists 判断值是否存在（非 nil，且非「接口持有 nil」）。
func (x Value) Exists() bool {
	if x.v == nil {
		return false
	}
	switch reflect.ValueOf(x.v).Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice,
		reflect.Chan, reflect.Func:
		return !reflect.ValueOf(x.v).IsNil()
	}
	return true
}

// IsEmpty 判断值是否为空（nil / 零值 / 空串 / 空集合）。
func (x Value) IsEmpty() bool { return IsEmpty(x.v) }

// String 转为字符串，缺失时返回默认值（默认 ""）。
func (x Value) String(def ...string) string {
	d := ""
	if len(def) > 0 {
		d = def[0]
	}
	if !x.Exists() {
		return d
	}
	s := ToString(x.v)
	if s == "" && d != "" {
		// 空串视为缺失，便于配置场景
		return d
	}
	return s
}

// Int 转为 int，缺失时返回默认值（默认 0）。
func (x Value) Int(def ...int) int { return withDefault(x, Cast[int], def) }

// Int64 转为 int64。
func (x Value) Int64(def ...int64) int64 { return withDefault(x, Cast[int64], def) }

// Float64 转为 float64。
func (x Value) Float64(def ...float64) float64 { return withDefault(x, Cast[float64], def) }

// Bool 转为 bool。
func (x Value) Bool(def ...bool) bool { return withDefault(x, Cast[bool], def) }

// Duration 转为 time.Duration。
func (x Value) Duration(def ...time.Duration) time.Duration {
	return withDefault(x, Cast[time.Duration], def)
}

// Time 转为 time.Time。
func (x Value) Time(def ...time.Time) time.Time { return withDefault(x, Cast[time.Time], def) }

// Slice 转为 []any。
func (x Value) Slice() []any { return Cast[[]any](x.v) }

// StringSlice 转为 []string。
func (x Value) StringSlice() []string { return Cast[[]string](x.v) }

// IntSlice 转为 []int。
func (x Value) IntSlice() []int { return Cast[[]int](x.v) }

// Map 转为 map[string]any。
func (x Value) Map() map[string]any { return Cast[map[string]any](x.v) }

// withDefault 是「缺失或零值则取默认值」的公共实现。
//
// 注意语义：当转换结果恰为目标类型的零值（含解析失败）时，也会返回默认值。
// 这正是「容错配置读取」想要的行为；需要区分失败请使用 CastErr。
//
// 之所以写成包级泛型函数而非 Value 的方法：Go 的方法不支持自带类型参数。
func withDefault[T any](x Value, convert func(any) T, def []T) T {
	var zero T
	if !x.Exists() {
		if len(def) > 0 {
			return def[0]
		}
		return zero
	}
	got := convert(x.v)
	if reflect.DeepEqual(got, zero) && len(def) > 0 {
		return def[0]
	}
	return got
}

// Struct 把当前值（map[string]any / JSON / 可赋值类型）填充到 T。
//
// 用法：`u, err := xconv.ValueStruct[User](xconv.V(rawMap))`
func ValueStruct[T any](x Value) (T, error) { return ToStruct[T](x.Raw()) }

// Path 深入嵌套结构取值，支持：
//
//   - map[string]any 的点分路径与下标：`"user.tags[0].name"`
//   - struct 的字段路径：`"User.Profile.Age"`（嵌入字段可用短名）
//
// 未命中时返回「空 Value」，后续 Xxx 方法会返回默认值。
func (x Value) Path(path string) Value {
	if x.v == nil || path == "" {
		return Value{}
	}
	// map 场景
	if m, ok := x.v.(map[string]any); ok {
		if v, ok := xmap.GetPath(m, path); ok {
			return Value{v}
		}
		return Value{}
	}
	// struct 场景
	if v, ok := xreflect.Get(x.v, path); ok {
		return Value{v}
	}
	// 兜底：先转成 map[string]any（例如 map[string]string / struct）
	if x.v != nil {
		if m, err := xreflect.StructToMap(x.v); err == nil {
			if v, ok := xmap.GetPath(m, path); ok {
				return Value{v}
			}
		}
		if m := Cast[map[string]any](x.v); m != nil {
			if v, ok := xmap.GetPath(m, path); ok {
				return Value{v}
			}
		}
	}
	return Value{}
}

// Get 是 Path 的别名，语义更直观。
func (x Value) Get(path string) Value { return x.Path(path) }

// ---------------------------------------------------------------------------
// map ↔ slice
// ---------------------------------------------------------------------------

// MapToSlice 把 map 的每个键值对映射为切片元素（顺序随机）。
//
// 需要稳定输出请使用 MapToSliceSorted。
func MapToSlice[T any, K comparable, V any](m map[K]V, fn func(K, V) T) []T {
	if len(m) == 0 {
		return nil
	}
	out := make([]T, 0, len(m))
	for k, v := range m {
		out = append(out, fn(k, v))
	}
	return out
}

// MapToSliceSorted 把 map 按键升序映射为切片元素（输出顺序稳定）。
func MapToSliceSorted[T any, K xconstraints.Ordered, V any](m map[K]V, fn func(K, V) T) []T {
	if len(m) == 0 {
		return nil
	}
	out := make([]T, 0, len(m))
	for _, k := range xmap.KeysSorted(m) {
		out = append(out, fn(k, m[k]))
	}
	return out
}

// MapToSliceValues 把 map 的值转为切片（顺序随机）。
func MapToSliceValues[K comparable, V any](m map[K]V) []V { return xmap.Values(m) }

// MapToSliceValuesSorted 把 map 的值按键升序转为切片。
func MapToSliceValuesSorted[K xconstraints.Ordered, V any](m map[K]V) []V {
	return xmap.ValuesSortedByKey(m)
}

// MapToSliceKeys 把 map 的键转为切片。
func MapToSliceKeys[K comparable, V any](m map[K]V) []K { return xmap.Keys(m) }

// MapToSliceKeysSorted 把 map 的键按升序转为切片。
func MapToSliceKeysSorted[K xconstraints.Ordered, V any](m map[K]V) []K {
	return xmap.KeysSorted(m)
}

// MapToEntries 把 map 转为键值对切片（顺序随机）。
func MapToEntries[K comparable, V any](m map[K]V) []xmap.Pair[K, V] { return xmap.Entries(m) }

// MapToEntriesSorted 把 map 按键升序转为键值对切片。
func MapToEntriesSorted[K xconstraints.Ordered, V any](m map[K]V) []xmap.Pair[K, V] {
	return xmap.EntriesSorted(m)
}

// SliceToMap 由切片构建 map（键与值分别由 fn 提取）。
func SliceToMap[T any, K comparable, V any](s []T, keyFn func(T) K, valFn func(T) V) map[K]V {
	return xmap.FromSlice(s, keyFn, valFn)
}

// SliceToMapSelf 由切片构建「键 → 元素本身」的 map。
func SliceToMapSelf[T any, K comparable](s []T, keyFn func(T) K) map[K]T {
	return xmap.FromSliceSelf(s, keyFn)
}

// SliceToSet 把切片转为集合（用于快速存在性判断）。
func SliceToSet[T comparable](s []T) map[T]struct{} { return xslice.ToSet(s) }

// SliceToCountMap 统计每个元素的出现次数。
func SliceToCountMap[T comparable](s []T) map[T]int {
	if len(s) == 0 {
		return nil
	}
	out := make(map[T]int, len(s))
	for _, v := range s {
		out[v]++
	}
	return out
}

// ---------------------------------------------------------------------------
// slice ↔ string
// ---------------------------------------------------------------------------

// SliceToString 把切片拼成以 sep 分隔的字符串。
//
//	SliceToString([]int{1,2,3}, ",")  // "1,2,3"
//
// 空切片返回 ""（而不是分隔符）。
func SliceToString[T any](s []T, sep string) string {
	if len(s) == 0 {
		return ""
	}
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = ToString(v)
	}
	return strings.Join(parts, sep)
}

// SliceToStringFunc 用自定义函数把元素转成字符串再拼接（跳过返回空串的元素可选）。
func SliceToStringFunc[T any](s []T, sep string, fn func(T) string) string {
	if len(s) == 0 || fn == nil {
		return ""
	}
	parts := make([]string, 0, len(s))
	for _, v := range s {
		parts = append(parts, fn(v))
	}
	return strings.Join(parts, sep)
}

// SliceToStringQuoted 拼接并对每个元素加双引号，便于日志 / SQL IN 场景查看。
//
//	SliceToStringQuoted([]string{"a","b"}, ",")  // `"a","b"`
func SliceToStringQuoted[T any](s []T, sep string) string {
	return SliceToStringFunc(s, sep, func(v T) string {
		return `"` + strings.ReplaceAll(ToString(v), `"`, `\"`) + `"`
	})
}

// SliceToStringSkipEmpty 拼接并跳过「转换后为空串」的元素。
func SliceToStringSkipEmpty[T any](s []T, sep string) string {
	if len(s) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s))
	for _, v := range s {
		if str := ToString(v); str != "" {
			parts = append(parts, str)
		}
	}
	return strings.Join(parts, sep)
}

// StringToSlice 按 sep 切分（与 strings.Split 一致，保留空段）。
func StringToSlice(s, sep string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}

// StringToSliceClean 按 sep 切分、逐段 TrimSpace，并丢弃空段。
//
//	"a, b,,c" → ["a" "b" "c"]
func StringToSliceClean(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// StringToIntSlice 按 sep 切分并逐段解析为 int（空段被跳过）。
//
// 任一段无法解析时返回错误，并给出出错的下标与内容。
func StringToIntSlice(s, sep string) ([]int, error) {
	parts := StringToSliceClean(s, sep)
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]int, 0, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, xerr.Wrapf(err, "xconv: parse int slice: item %d (%q)", i, p)
		}
		out = append(out, n)
	}
	return out, nil
}

// StringToFloatSlice 按 sep 切分并解析为 float64。
func StringToFloatSlice(s, sep string) ([]float64, error) {
	parts := StringToSliceClean(s, sep)
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]float64, 0, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return out, xerr.Wrapf(err, "xconv: parse float slice: item %d (%q)", i, p)
		}
		out = append(out, f)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// struct ↔ map ↔ JSON
// ---------------------------------------------------------------------------

// ToMap 把结构体转为 map[string]any（默认使用 json tag）。
//
// 选项与 xreflect.StructToMap 完全一致，直接透传：
//
//	xconv.ToMap(u, xreflect.WithNested(), xreflect.WithOmitEmpty())
func ToMap(v any, opts ...xreflect.MapOption) (map[string]any, error) {
	return xreflect.StructToMap(v, opts...)
}

// ToMapString 把结构体转为 map[string]string（值走字符串化）。
func ToMapString(v any, opts ...xreflect.MapOption) (map[string]string, error) {
	return xreflect.StructToMapString(v, opts...)
}

// FromMap 把 map 中的值写入 dst 指向的结构体（弱类型转换）。
//
// 键匹配顺序：json tag 名 → Go 字段名；单字段失败不会中断其它字段。
func FromMap(m map[string]any, dst any, opts ...xreflect.MapOption) error {
	return xreflect.MapToStruct(m, dst, opts...)
}

// ToStruct 把任意输入转成结构体 T。
//
// 支持 map[string]any（弱类型）、JSON []byte / string，以及可赋值类型。
func ToStruct[T any](v any) (T, error) {
	var out T
	if v == nil {
		return out, nil
	}
	switch x := v.(type) {
	case map[string]any:
		if err := xreflect.MapToStruct(x, &out); err != nil {
			return out, err
		}
		return out, nil
	case []byte:
		if len(x) == 0 {
			return out, nil
		}
		if err := json.Unmarshal(x, &out); err != nil {
			return out, xerr.Wrap(err, "xconv: unmarshal to struct")
		}
		return out, nil
	case string:
		if x == "" {
			return out, nil
		}
		if err := json.Unmarshal([]byte(x), &out); err != nil {
			return out, err
		}
		return out, nil
	}
	if err := xreflect.SetValue(&out, v); err != nil {
		return out, err
	}
	return out, nil
}

// ToStructs 把切片形式的输入转成 []T。
//
// 支持 []any / [](map[string]any) / JSON 数组。
func ToStructs[T any](v any) ([]T, error) {
	if v == nil {
		return nil, nil
	}
	// JSON 数组
	switch x := v.(type) {
	case []byte:
		if len(x) == 0 {
			return nil, nil
		}
		var out []T
		if err := json.Unmarshal(x, &out); err != nil {
			return nil, xerr.Wrap(err, "xconv: unmarshal to structs")
		}
		return out, nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, nil
		}
		var out []T
		if err := json.Unmarshal([]byte(x), &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, xerr.NewfC(xerr.CodeInvalidParam, "xconv: ToStructs needs a slice, got %s", xreflect.TypeName(v))
	}
	out := make([]T, 0, rv.Len())
	var errs xerr.MultiError
	for i := 0; i < rv.Len(); i++ {
		item, err := ToStruct[T](rv.Index(i).Interface())
		if err != nil {
			errs = append(errs, xerr.WithMeta(err, "index", i))
			continue
		}
		out = append(out, item)
	}
	if len(errs) > 0 {
		return out, errs
	}
	return out, nil
}

// ToJSON 序列化为 JSON。
func ToJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, xerr.Wrap(err, "xconv: marshal json")
	}
	return b, nil
}

// ToJSONString 序列化为 JSON 字符串；失败时返回空串。
func ToJSONString(v any) string {
	b, err := ToJSON(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ToJSONIndent 序列化为带缩进的 JSON。
func ToJSONIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, xerr.Wrap(err, "xconv: marshal json indent")
	}
	return b, nil
}

// FromJSON 反序列化为 T。
func FromJSON[T any](data []byte) (T, error) {
	var out T
	if len(data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, xerr.Wrap(err, "xconv: unmarshal json")
	}
	return out, nil
}

// FromJSONString 反序列化为 T。
func FromJSONString[T any](s string) (T, error) {
	return FromJSON[T]([]byte(s))
}

// MustFromJSON 反序列化为 T，失败时 panic。仅用于初始化与测试。
func MustFromJSON[T any](data []byte) T {
	v, err := FromJSON[T](data)
	if err != nil {
		panic(err)
	}
	return v
}

// DeepCopyJSON 通过 JSON 往返做深拷贝。
//
// 优点：绝对安全、不受反射限制；缺点：仅适用于 JSON 可表达的数据，
// 会丢失未导出字段、time.Time 的单调时钟部分等。
// 需要完整保真请使用 xreflect.DeepCopy。
func DeepCopyJSON[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// ---------------------------------------------------------------------------
// 数值 ↔ 字节 / 进制
// ---------------------------------------------------------------------------

// BytesToHuman 把字节数转为可读字符串（1024 进制，如 "1.5 KB"）。
func BytesToHuman(n int64) string { return xnum.BytesToHuman(n) }

// BytesToHumanSI 把字节数转为可读字符串（1000 进制）。
func BytesToHumanSI(n int64) string { return xnum.BytesToHumanSI(n) }

// HumanToBytes 解析 "1.5MB" / "2GiB" / "1024" 形式的字符串为字节数。
func HumanToBytes(s string) (int64, error) { return xnum.HumanToBytes(s) }

// base62Alphabet 是 2–62 进制共用的数字表。
//
// 前 36 位与 strconv 的小写风格一致（10–35 为 a–z），
// 36–61 位使用大写字母，从而支持到 62 进制。
const base62Alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// IntToBase 把整数转为指定进制的字符串（支持 2–62，越界时回退为十进制）。
//
//	IntToBase(255, 16)  // "ff"
//	IntToBase(255, 2)   // "11111111"
func IntToBase(n int64, base int) string {
	if base < 2 || base > 62 {
		return strconv.FormatInt(n, 10)
	}
	if n == 0 {
		return "0"
	}
	neg := n < 0
	var u uint64
	if neg {
		u = uint64(-(n + 1)) + 1 // 避免 math.MinInt64 取负溢出
	} else {
		u = uint64(n)
	}
	b := uint64(base)
	// 最大情形：base=2 时 uint64 需要 64 位数字，再加可能的负号 → 预留 72 字节
	var buf [72]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = base62Alphabet[u%b]
		u /= b
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// BaseToInt 解析指定进制的字符串为整数。
func BaseToInt(s string, base int) (int64, error) {
	str := strings.TrimSpace(s)
	if str == "" {
		return 0, xerr.NewC(xerr.CodeInvalidParam, "xconv: empty number string")
	}
	if base < 2 || base > 62 {
		return 0, xerr.NewfC(xerr.CodeOutOfRange, "xconv: base %d out of range [2,62]", base)
	}
	neg := false
	if strings.HasPrefix(str, "-") {
		neg = true
		str = str[1:]
	} else if strings.HasPrefix(str, "+") {
		str = str[1:]
	}
	if str == "" {
		return 0, xerr.NewfC(xerr.CodeInvalidFormat, "xconv: invalid number %q", s)
	}
	b := uint64(base)
	// 用 uint64 累加，最后统一判溢出，避免中途回绕
	var u uint64
	for i := 0; i < len(str); i++ {
		d := strings.IndexByte(base62Alphabet, str[i])
		if d < 0 || d >= base {
			return 0, xerr.NewfC(xerr.CodeInvalidFormat,
				"xconv: invalid digit %q for base %d", string(str[i]), base)
		}
		hi, lo := bits.Mul64(u, b)
		if hi != 0 {
			return 0, xerr.NewfC(xerr.CodeOutOfRange, "xconv: number %q overflows int64", s)
		}
		sum := lo + uint64(d)
		if sum < lo {
			return 0, xerr.NewfC(xerr.CodeOutOfRange, "xconv: number %q overflows int64", s)
		}
		u = sum
	}
	if neg {
		switch {
		case u > 1<<63:
			return 0, xerr.NewfC(xerr.CodeOutOfRange, "xconv: number %q overflows int64", s)
		case u == 1<<63:
			// 显式处理 MinInt64，避免 int64(u) 的隐式回绕造成误读
			return -1 << 63, nil
		default:
			return -int64(u), nil
		}
	}
	if u > 1<<63-1 {
		return 0, xerr.NewfC(xerr.CodeOutOfRange, "xconv: number %q overflows int64", s)
	}
	return int64(u), nil
}

// ---------------------------------------------------------------------------
// 指针与空值
// ---------------------------------------------------------------------------

// Ptr 返回 v 的地址（点号取值场景的便捷写法）。
func Ptr[T any](v T) *T { return &v }

// PtrOrNil 在 v 为零值时返回 nil（适合可选字段的序列化）。
func PtrOrNil[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// Deref 解引用；nil 返回零值。
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// DerefOr 解引用；nil 返回默认值。
func DerefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// IsNil 判断 v 是否为 nil，能正确识别「接口持有 nil 指针」的 typed nil 陷阱。
func IsNil(v any) bool { return xreflect.IsNil(v) }

// IsEmpty 判断值是否为空：nil / 零值 / 空串 / 空集合 都返回 true。
func IsEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array, reflect.Chan:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Ptr, reflect.Interface, reflect.Func:
		return rv.IsNil()
	case reflect.Struct:
		return rv.IsZero()
	}
	return false
}

// Default 在 v 为零值时返回 def。
func Default[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// Coalesce 返回第一个非零值。
func Coalesce[T comparable](vals ...T) T {
	var zero T
	for _, v := range vals {
		if v != zero {
			return v
		}
	}
	return zero
}

// Or 在指针为 nil 时返回默认值（DerefOr 的可读别名）。
func Or[T any](p *T, def T) T { return DerefOr(p, def) }

// ---------------------------------------------------------------------------
// 辅助断言
// ---------------------------------------------------------------------------

// Zero 返回 T 的零值。
func Zero[T any]() T {
	var zero T
	return zero
}

// IsType 判断 v 的运行时类型是否恰好为 T。
func IsType[T any](v any) bool {
	var zero T
	return reflect.TypeOf(v) == reflect.TypeOf(&zero).Elem()
}
