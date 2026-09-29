// Package xnum 提供数值工具：浮点舍入、格式化、字节单位换算、罗马数字、随机数，
// 以及用于金额等精度敏感场景的 Decimal 类型。
//
// 设计要点：
//   - **金额 / 精度敏感计算必须用 Decimal**：float64 无法精确表示 0.1，
//     `0.1+0.2 != 0.3` 是浮点的固有问题；
//   - Decimal 基于 math/big.Rat 实现，NewDecimal(float64) 使用**最短十进制表示**
//     （0.1 会变成精确的 1/10，而不是二进制精确值），符合直觉；
//   - Decimal 运算永不 panic：除零返回零值；
//   - Round / Trunc 等浮点函数遇到 NaN / Inf 原样返回，不做无意义处理；
//   - RandInt / RandFloat 使用 math/rand，**仅供非安全用途**；
//     安全场景（token、验证码、密钥）请使用 crypto/rand 或 xstr.Rand。
//
// 并发安全：本包全部为纯函数（Decimal 为不可变值类型），可并发调用。
package xnum

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"math/rand"
	"strconv"
	"strings"

	"github.com/indigo-apple/gokit/xerr"
)

// ---------------------------------------------------------------------------
// 浮点舍入
// ---------------------------------------------------------------------------

func pow10(places int) float64 { return math.Pow(10, float64(places)) }

// Round 四舍五入到 places 位小数（0.5 远离零）。
//
// 注意：这是**浮点舍入**，1.005 这类无法精确表示的十进制数可能得到 1.00。
// 金额场景请使用 Decimal.Round。
func Round(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := pow10(places)
	return math.Round(v*p) / p
}

// RoundHalfEven 使用银行家舍入（0.5 向偶数）。
func RoundHalfEven(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := pow10(places)
	return math.RoundToEven(v*p) / p
}

// Floor 向下舍入到 places 位小数（朝 -Inf 方向）。
func Floor(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := pow10(places)
	return math.Floor(v*p) / p
}

// Ceil 向上舍入到 places 位小数（朝 +Inf 方向）。
func Ceil(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := pow10(places)
	return math.Ceil(v*p) / p
}

// Trunc 直接截断到 places 位小数（朝零方向）。
func Trunc(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := pow10(places)
	return math.Trunc(v*p) / p
}

// FloorTo 向下对齐到 unit 的整数倍（unit <= 0 时原样返回）。
//
//	xnum.FloorTo(97, 15) == 90
func FloorTo(v, unit float64) float64 {
	if unit <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return math.Floor(v/unit) * unit
}

// CeilTo 向上对齐到 unit 的整数倍。
//
//	xnum.CeilTo(91, 15) == 105
func CeilTo(v, unit float64) float64 {
	if unit <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return math.Ceil(v/unit) * unit
}

// RoundTo 最近对齐到 unit 的整数倍。
func RoundTo(v, unit float64) float64 {
	if unit <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return math.Round(v/unit) * unit
}

// Percent 返回 part/total 的百分比（0–100），保留 places 位小数。
//
// total 为 0 时返回 0。
func Percent(part, total float64, places int) float64 {
	if total == 0 {
		return 0
	}
	return Round(part/total*100, places)
}

// SafeDivide 除法，b 为 0 时返回 def（不产生 Inf/NaN）。
func SafeDivide(a, b, def float64) float64 {
	if b == 0 {
		return def
	}
	return a / b
}

// InDelta 判断 |a-b| <= delta。
func InDelta(a, b, delta float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return false
	}
	if delta < 0 {
		delta = -delta
	}
	return math.Abs(a-b) <= delta
}

// DefaultEpsilon 是 IsNearlyEqual 使用的默认容差。
const DefaultEpsilon = 1e-9

// IsNearlyEqual 判断两浮点数在 DefaultEpsilon 内相等。
func IsNearlyEqual(a, b float64) bool { return InDelta(a, b, DefaultEpsilon) }

// ---------------------------------------------------------------------------
// 格式化
// ---------------------------------------------------------------------------

// Thousands 给整数加千分位分隔符：1234567 → "1,234,567"。
func Thousands(v int64, sep string) string {
	if sep == "" {
		sep = ","
	}
	neg := v < 0
	s := strconv.FormatInt(v, 10)
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// FormatFloat 按固定小数位格式化：FormatFloat(1.5, 2) == "1.50"。
func FormatFloat(v float64, places int) string {
	return strconv.FormatFloat(v, 'f', places, 64)
}

// FormatThousands 按千分位 + 固定小数位格式化。
func FormatThousands(v float64, places int, sep string) string {
	s := FormatFloat(math.Abs(v), places)
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i:]
	}
	n, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return s
	}
	out := Thousands(n, sep) + fracPart
	if v < 0 {
		return "-" + out
	}
	return out
}

// FormatPercent 格式化为百分比字符串：FormatPercent(0.1234, 2) == "12.34%"。
func FormatPercent(v float64, places int) string {
	return FormatFloat(v*100, places) + "%"
}

// Ordinal 返回序数词：1 → "1st"，12 → "12th"，21 → "21st"。
func Ordinal(n int) string {
	suffix := "th"
	switch absInt(n) % 100 {
	case 11, 12, 13:
	default:
		switch absInt(n) % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// 罗马数字映射表（1–3999）。
var romanTable = []struct {
	v int
	s string
}{
	{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
	{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
	{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
}

// Roman 返回 n 的罗马数字表示；超出 1–3999 范围返回空串。
func Roman(n int) string {
	if n < 1 || n > 3999 {
		return ""
	}
	var b strings.Builder
	for _, e := range romanTable {
		for n >= e.v {
			b.WriteString(e.s)
			n -= e.v
		}
	}
	return b.String()
}

// NextPow2 返回 >= n 的最小 2 的幂；n <= 1 时返回 1。
func NextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(n-1))
}

// IsPow2 判断 n 是否为 2 的幂（n <= 0 返回 false）。
func IsPow2(n int) bool { return n > 0 && n&(n-1) == 0 }

// ReverseInt 反转十进制数字：1234 → 4321；负数保留符号。
func ReverseInt(n int) int {
	neg := n < 0
	if neg {
		n = -n
	}
	r := 0
	for n > 0 {
		r = r*10 + n%10
		n /= 10
	}
	if neg {
		return -r
	}
	return r
}

// Digits 返回十进制各位数字（低位在前）。Digits(123) == []int{3,2,1}
func Digits(n int) []int {
	if n < 0 {
		n = -n
	}
	if n == 0 {
		return []int{0}
	}
	var out []int
	for n > 0 {
		out = append(out, n%10)
		n /= 10
	}
	return out
}

// ---------------------------------------------------------------------------
// 字节单位
// ---------------------------------------------------------------------------

// 字节单位（两种进制共用同一套符号）。
var byteUnits = []string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}

// BytesToHuman 把字节数转为可读字符串，使用 1024 进制。
//
//	1536    → "1.5 KB"
//	1048576 → "1 MB"
func BytesToHuman(n int64) string { return humanBytes(n, 1024, byteUnits) }

// BytesToHumanSI 把字节数转为可读字符串，使用 1000 进制。
//
//	1500    → "1.5 KB"
//	1000000 → "1 MB"
func BytesToHumanSI(n int64) string { return humanBytes(n, 1000, byteUnits) }

func humanBytes(n int64, base float64, units []string) string {
	neg := n < 0
	if neg {
		n = -n
	}
	f := float64(n)
	i := 0
	for f >= base && i < len(units)-1 {
		f /= base
		i++
	}
	// 小数值保留最多 2 位并去掉尾随 0
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if neg {
		s = "-" + s
	}
	return s + " " + units[i]
}

// HumanToBytes 解析 "1.5MB" / "2GiB" / "1024" 形式的字符串为字节数。
//
// 单位大小写不敏感；K/KB/KiB 均按 1024 进制处理。
func HumanToBytes(s string) (int64, error) {
	str := strings.TrimSpace(s)
	if str == "" {
		return 0, xerr.NewC(xerr.CodeInvalidParam, "xnum: empty byte size")
	}
	i := 0
	for i < len(str) {
		c := str[i]
		if c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+' {
			i++
			continue
		}
		break
	}
	numPart := str[:i]
	unitPart := strings.TrimSpace(str[i:])
	if numPart == "" {
		return 0, xerr.NewfC(xerr.CodeInvalidFormat, "xnum: no number in %q", s)
	}
	v, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, xerr.Wrapf(err, "xnum: parse %q", numPart)
	}
	mult, err := byteMultiplier(unitPart)
	if err != nil {
		return 0, err
	}
	return int64(v * mult), nil
}

func byteMultiplier(unit string) (float64, error) {
	u := strings.ToUpper(strings.TrimSpace(unit))
	u = strings.TrimSuffix(u, "B")
	u = strings.TrimSuffix(u, "I")
	switch u {
	case "":
		return 1, nil
	case "K":
		return 1 << 10, nil
	case "M":
		return 1 << 20, nil
	case "G":
		return 1 << 30, nil
	case "T":
		return 1 << 40, nil
	case "P":
		return 1 << 50, nil
	case "E":
		return 1 << 60, nil
	}
	return 0, xerr.NewfC(xerr.CodeInvalidParam, "xnum: unknown byte unit %q", unit)
}

// ---------------------------------------------------------------------------
// 随机数（非安全用途）
// ---------------------------------------------------------------------------

// RandInt 返回 [min, max] 区间内的随机整数。
//
// max < min 时返回 min。仅供非安全用途。
func RandInt(min, max int) int {
	if max <= min {
		return min
	}
	return min + rand.Intn(max-min+1)
}

// RandFloat 返回 [min, max) 区间内的随机浮点数。
func RandFloat(min, max float64) float64 {
	if max <= min {
		return min
	}
	return min + rand.Float64()*(max-min)
}

// RandWeighted 按权重随机返回下标，权重非正的元素不会被选中。
//
// 全部权重非正时返回 -1；weights 为空时返回 -1。
func RandWeighted(weights []float64) int {
	total := 0.0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return -1
	}
	// 循环直到命中有效权重（有效权重总和 > 0，期望轮次很小）
	for i := 0; i < 100; i++ {
		r := rand.Float64() * total
		acc := 0.0
		for idx, w := range weights {
			if w <= 0 {
				continue
			}
			acc += w
			if r < acc {
				return idx
			}
		}
	}
	// 兜底：返回最后一个正权重下标
	for i := len(weights) - 1; i >= 0; i-- {
		if weights[i] > 0 {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Decimal
// ---------------------------------------------------------------------------

// Decimal 表示任意精度的十进制数，用于金额等精度敏感场景。
//
// 零值即 0，可直接使用；Decimal 是不可变值类型，运算返回新值。
//
//	d := xnum.NewDecimal("0.1")
//	fmt.Println(d.Add(xnum.NewDecimal("0.2")))   // 0.3（不是 0.30000000000000004）
type Decimal struct {
	r *big.Rat
}

// NewDecimal 把任意值转为 Decimal。
//
// 支持 int / uint / float / string / Decimal / *big.Rat；
// 浮点使用 shortest 十进制表示（0.1 → 1/10）；
// 无法解析时返回 0（可用 ParseDecimal 获取错误）。
func NewDecimal(v any) Decimal {
	switch x := v.(type) {
	case nil:
		return Decimal{}
	case Decimal:
		return x
	case *Decimal:
		if x == nil {
			return Decimal{}
		}
		return *x
	case string:
		d, err := ParseDecimal(x)
		if err != nil {
			return Decimal{}
		}
		return d
	case int:
		return decimalFromInt64(int64(x))
	case int8:
		return decimalFromInt64(int64(x))
	case int16:
		return decimalFromInt64(int64(x))
	case int32:
		return decimalFromInt64(int64(x))
	case int64:
		return decimalFromInt64(x)
	case uint:
		return decimalFromUint64(uint64(x))
	case uint8:
		return decimalFromUint64(uint64(x))
	case uint16:
		return decimalFromUint64(uint64(x))
	case uint32:
		return decimalFromUint64(uint64(x))
	case uint64:
		return decimalFromUint64(x)
	case float32:
		return decimalFromFloat(float64(x), 32)
	case float64:
		return decimalFromFloat(x, 64)
	case *big.Rat:
		if x == nil {
			return Decimal{}
		}
		return Decimal{new(big.Rat).Set(x)}
	case *big.Int:
		if x == nil {
			return Decimal{}
		}
		return Decimal{new(big.Rat).SetInt(x)}
	case fmt.Stringer:
		d, err := ParseDecimal(x.String())
		if err != nil {
			return Decimal{}
		}
		return d
	}
	return Decimal{}
}

func decimalFromInt64(n int64) Decimal { return Decimal{new(big.Rat).SetInt64(n)} }

func decimalFromUint64(n uint64) Decimal {
	return Decimal{new(big.Rat).SetInt(new(big.Int).SetUint64(n))}
}

func decimalFromFloat(f float64, bits int) Decimal {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Decimal{}
	}
	// 使用最短表示：0.1 → "0.1"，而不是二进制精确值
	s := strconv.FormatFloat(f, 'f', -1, bits)
	d, err := ParseDecimal(s)
	if err != nil {
		return Decimal{}
	}
	return d
}

// ParseDecimal 解析十进制字符串（支持 "1.23" / "-1e3" / "3/4"）。
func ParseDecimal(s string) (Decimal, error) {
	str := strings.TrimSpace(s)
	if str == "" {
		return Decimal{}, xerr.NewC(xerr.CodeInvalidParam, "xnum: empty decimal string")
	}
	r, ok := new(big.Rat).SetString(str)
	if !ok {
		return Decimal{}, xerr.NewfC(xerr.CodeInvalidFormat, "xnum: cannot parse decimal %q", s)
	}
	return Decimal{r}, nil
}

// MustParseDecimal 解析失败时 panic。仅用于初始化与测试。
func MustParseDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// rat 返回内部的 *big.Rat（零值安全）。
func (d Decimal) rat() *big.Rat {
	if d.r == nil {
		return new(big.Rat)
	}
	return d.r
}

// IsZero 判断是否为零。
func (d Decimal) IsZero() bool { return d.r == nil || d.r.Sign() == 0 }

// Sign 返回符号：-1 / 0 / 1。零值安全。
func (d Decimal) Sign() int { return d.rat().Sign() }

// IsNeg 判断是否为负。
func (d Decimal) IsNeg() bool { return d.Sign() < 0 }

// IsPos 判断是否为正。
func (d Decimal) IsPos() bool { return d.Sign() > 0 }

// Add 加法。
func (d Decimal) Add(o Decimal) Decimal {
	return Decimal{new(big.Rat).Add(d.rat(), o.rat())}
}

// Sub 减法。
func (d Decimal) Sub(o Decimal) Decimal {
	return Decimal{new(big.Rat).Sub(d.rat(), o.rat())}
}

// Mul 乘法。
func (d Decimal) Mul(o Decimal) Decimal {
	return Decimal{new(big.Rat).Mul(d.rat(), o.rat())}
}

// Div 除法；除数为零时返回零值（不 panic）。
func (d Decimal) Div(o Decimal) Decimal {
	if o.IsZero() {
		return Decimal{}
	}
	return Decimal{new(big.Rat).Quo(d.rat(), o.rat())}
}

// DivWithError 除法，除数为零时返回错误。
func (d Decimal) DivWithError(o Decimal) (Decimal, error) {
	if o.IsZero() {
		return Decimal{}, xerr.NewC(xerr.CodeInvalidParam, "xnum: decimal division by zero")
	}
	return Decimal{new(big.Rat).Quo(d.rat(), o.rat())}, nil
}

// Neg 取负。
func (d Decimal) Neg() Decimal { return Decimal{new(big.Rat).Neg(d.rat())} }

// Abs 取绝对值。
func (d Decimal) Abs() Decimal { return Decimal{new(big.Rat).Abs(d.rat())} }

// Cmp 比较：-1 / 0 / 1。
func (d Decimal) Cmp(o Decimal) int { return d.rat().Cmp(o.rat()) }

// Equal 判断相等。
func (d Decimal) Equal(o Decimal) bool { return d.rat().Cmp(o.rat()) == 0 }

// GreaterThan 判断 d > o。
func (d Decimal) GreaterThan(o Decimal) bool { return d.Cmp(o) > 0 }

// LessThan 判断 d < o。
func (d Decimal) LessThan(o Decimal) bool { return d.Cmp(o) < 0 }

// Float64 转为 float64（可能损失精度）。
func (d Decimal) Float64() float64 {
	f, _ := d.rat().Float64()
	return f
}

// Int64 截断为 int64；溢出时返回 math.MaxInt64 / math.MinInt64。
func (d Decimal) Int64() int64 {
	r := d.rat()
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if q.IsInt64() {
		return q.Int64()
	}
	if q.Sign() > 0 {
		return math.MaxInt64
	}
	return math.MinInt64
}

// 舍入模式。
const (
	roundHalfAway = iota
	roundHalfEven
	roundFloor
	roundCeil
	roundTrunc
)

// ratPow10 返回 10^places（places 可为负）。
func ratPow10(places int) *big.Rat {
	if places >= 0 {
		return new(big.Rat).SetInt(pow10Int(places))
	}
	return new(big.Rat).Inv(new(big.Rat).SetInt(pow10Int(-places)))
}

func pow10Int(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

func (d Decimal) round(places int, mode int) Decimal {
	p10 := ratPow10(places)
	scaled := new(big.Rat).Mul(d.rat(), p10)

	num, den := scaled.Num(), scaled.Denom()
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(num, den, rem)

	if rem.Sign() != 0 {
		one := big.NewInt(1)
		switch mode {
		case roundHalfAway:
			a := new(big.Int).Abs(rem)
			a.Lsh(a, 1)
			if a.Cmp(den) >= 0 {
				if num.Sign() >= 0 {
					q.Add(q, one)
				} else {
					q.Sub(q, one)
				}
			}
		case roundHalfEven:
			a := new(big.Int).Abs(rem)
			a.Lsh(a, 1)
			c := a.Cmp(den)
			if c > 0 || (c == 0 && q.Bit(0) == 1) {
				if num.Sign() >= 0 {
					q.Add(q, one)
				} else {
					q.Sub(q, one)
				}
			}
		case roundFloor:
			if num.Sign() < 0 {
				q.Sub(q, one)
			}
		case roundCeil:
			if num.Sign() > 0 {
				q.Add(q, one)
			}
		}
	}
	return Decimal{new(big.Rat).Quo(new(big.Rat).SetInt(q), p10)}
}

// Round 四舍五入到 places 位小数（0.5 远离零）。
func (d Decimal) Round(places int) Decimal { return d.round(places, roundHalfAway) }

// RoundHalfEven 银行家舍入到 places 位小数。
func (d Decimal) RoundHalfEven(places int) Decimal { return d.round(places, roundHalfEven) }

// Truncate 截断到 places 位小数（朝零方向）。
func (d Decimal) Truncate(places int) Decimal { return d.round(places, roundTrunc) }

// Floor 向下舍入到 places 位小数。
func (d Decimal) Floor(places int) Decimal { return d.round(places, roundFloor) }

// Ceil 向上舍入到 places 位小数。
func (d Decimal) Ceil(places int) Decimal { return d.round(places, roundCeil) }

// String 返回最短的精确十进制表示。
//
// 有限小数（含 1/2、1/4、0.1 等）精确输出；无限小数保留 10 位。
func (d Decimal) String() string {
	if d.r == nil {
		return "0"
	}
	for prec := 0; prec <= 20; prec++ {
		scaled := new(big.Rat).Mul(d.r, ratPow10(prec))
		if scaled.IsInt() {
			return trimDecimalString(d.r.FloatString(prec))
		}
	}
	return d.r.FloatString(10)
}

// StringFixed 按固定小数位输出（会四舍五入）：金额展示用。
func (d Decimal) StringFixed(places int) string {
	if places < 0 {
		places = 0
	}
	return d.rat().FloatString(places)
}

func trimDecimalString(s string) string {
	if !strings.ContainsRune(s, '.') {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	if s == "-0" {
		return "0"
	}
	return s
}

// MarshalJSON 输出为不带引号的 JSON 数字。
func (d Decimal) MarshalJSON() ([]byte, error) {
	s := d.String()
	if s == "" {
		s = "0"
	}
	return []byte(s), nil
}

// UnmarshalJSON 从 JSON 数字或字符串解析。
func (d *Decimal) UnmarshalJSON(b []byte) error {
	if d == nil {
		return xerr.NewC(xerr.CodeInvalidParam, "xnum: UnmarshalJSON on nil *Decimal")
	}
	s := strings.TrimSpace(string(b))
	s = strings.Trim(s, `"`)
	if s == "" || s == "null" {
		*d = Decimal{}
		return nil
	}
	v, err := ParseDecimal(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// 编译期断言：Decimal 实现 fmt.Stringer。
var _ fmt.Stringer = Decimal{}
