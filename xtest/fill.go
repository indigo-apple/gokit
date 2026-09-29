package xtest

import (
	"fmt"
	"reflect"
	"time"
)

// Fill 用确定性的假数据填充 v 指向的值，供「字段遍历型」测试使用
// （如：序列化 / 反序列化往返、深拷贝、字段级权限、表单渲染）。
//
// 填充规则（按类型）：
//
//	bool        → true
//	整数        → 递增序号（1, 2, 3 …）
//	浮点        → 序号 + 0.5
//	string      → "s1" / "s2" …
//	切片/数组   → 长度 1 的元素（递归填充）
//	map         → 1 个键值对（递归填充；键不可构造时保持空 map）
//	指针        → 分配后递归填充
//	结构体      → 逐字段递归填充（时间类型填固定值）
//	空接口      → 填入字符串
//	chan/func   → 保持零值
//
// 其它约定：
//   - **未导出字段自动跳过**（反射无法设置），因此同包测试以外的填充范围与用户可见字段一致；
//   - 递归深度上限 8，可安全处理自引用结构；
//   - 确定性：同一个值结构每次填充结果相同（序号从 1 开始）；
//   - Fill(nil) 与不可设置的入参不会 panic，而是在 test 上直接报错。
func Fill[V any](t TestingT, v *V) {
	t.Helper()
	if v == nil {
		t.Fatalf("xtest.Fill: 入参为 nil 指针")
		return
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		t.Fatalf("xtest.Fill: 入参必须是可写的非 nil 指针，实际 %T", v)
		return
	}
	serial := 0
	fillValue(rv.Elem(), 0, &serial)
}

// FillValue 填充任意 reflect.Value（v 必须可设置）；供需要精细控制的场景使用。
//
// 与 Fill 共用填充规则与序号起点，返回值也为该值本身（便于链式使用）。
func FillValue(v reflect.Value) reflect.Value {
	serial := 0
	fillValue(v, 0, &serial)
	return v
}

// timeType 用于识别 time.Time（其内部字段未导出，无法逐字段填充）。
var timeType = reflect.TypeOf(time.Time{})

// fillTime 是填充时间字段时使用的固定时刻（UTC，便于断言）。
var fillTime = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

// fillValue 递归填充。
func fillValue(v reflect.Value, depth int, serial *int) {
	if !v.IsValid() || !v.CanSet() || depth > 8 {
		return
	}

	*serial = *serial + 1
	n := *serial

	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillValue(v.Elem(), depth+1, serial)

	case reflect.Bool:
		v.SetBool(true)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// time.Duration 也走这里：填成 n 纳秒，语义正确且可断言
		v.SetInt(int64(n))

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(uint64(n))

	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(n) + 0.5)

	case reflect.Complex64, reflect.Complex128:
		v.SetComplex(complex(float64(n), float64(n)+0.5))

	case reflect.String:
		v.SetString(fmt.Sprintf("s%d", n))

	case reflect.Slice:
		sl := reflect.MakeSlice(v.Type(), 1, 1)
		fillValue(sl.Index(0), depth+1, serial)
		v.Set(sl)

	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillValue(v.Index(i), depth+1, serial)
		}

	case reflect.Map:
		m := reflect.MakeMapWithSize(v.Type(), 1)
		key := reflect.New(v.Type().Key()).Elem()
		if fillKey(key, n) {
			val := reflect.New(v.Type().Elem()).Elem()
			fillValue(val, depth+1, serial)
			m.SetMapIndex(key, val)
		}
		v.Set(m)

	case reflect.Struct:
		if v.Type() == timeType {
			v.Set(reflect.ValueOf(fillTime))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			fillValue(v.Field(i), depth+1, serial)
		}

	case reflect.Interface:
		// 只处理空接口；非空接口无法凭空构造实现
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf(fmt.Sprintf("s%d", n)))
		}

	default:
		// chan / func / unsafe.Pointer：保持零值
	}
}

// fillKey 为 map 的键生成一个可比较的值；不支持的类型返回 false。
func fillKey(k reflect.Value, n int) bool {
	if !k.CanSet() {
		return false
	}
	switch k.Kind() {
	case reflect.String:
		k.SetString(fmt.Sprintf("k%d", n))
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		k.SetInt(int64(n))
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		k.SetUint(uint64(n))
		return true
	case reflect.Bool:
		k.SetBool(true)
		return true
	case reflect.Float32, reflect.Float64:
		k.SetFloat(float64(n))
		return true
	case reflect.Ptr:
		if k.IsNil() {
			k.Set(reflect.New(k.Type().Elem()))
		}
		return true
	case reflect.Interface:
		if k.NumMethod() == 0 {
			k.Set(reflect.ValueOf(fmt.Sprintf("k%d", n)))
			return true
		}
		return false
	default:
		// 结构体 / 数组做键：内容不可控，放弃填充该 map
		return false
	}
}
