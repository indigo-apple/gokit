// Package xconstraints 汇总全工具链共用的泛型约束定义。
//
// 所有包禁止自行重复定义约束，统一从此处引用，保证语义一致：
//
//	xconstraints.Number  —— 可做 + - * / 的数值类型
//	xconstraints.Ordered —— 可比较大小的类型（含 string）
//	xconstraints.Key     —— 可用作 map 键的类型
package xconstraints

// Signed 有符号整型。
type Signed interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// Unsigned 无符号整型。
type Unsigned interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Integer 全部整型。
type Integer interface {
	Signed | Unsigned
}

// Float 浮点型。
type Float interface {
	~float32 | ~float64
}

// Complex 复数型。
type Complex interface {
	~complex64 | ~complex128
}

// Number 可进行四则运算的数值类型（不含复数）。
type Number interface {
	Integer | Float
}

// Ordered 可比较大小的类型（可用于 Min/Max/排序/Clamp）。
//
// 注意：Go 内置的 cmp.Ordered 语义相同，但这里显式定义以保证本工具链
// API 文档自洽，并避免使用者误以为 string 不参与运算。
type Ordered interface {
	Integer | Float | ~string
}

// Key 可用作 map 键的类型。
//
// 这是 comparable 的语义别名，用于表达「此泛型参数将作为 map 的键」。
type Key interface {
	comparable
}
