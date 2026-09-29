package xnum_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/zhangwenxing/gokit/xnum"
)

func eqF(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

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

// ---------------------------------------------------------------------------
// 舍入
// ---------------------------------------------------------------------------

func TestRounding(t *testing.T) {
	cases := []struct {
		fn   func(float64, int) float64
		name string
		v    float64
		p    int
		want float64
	}{
		{xnum.Round, "Round", 1.5, 0, 2},   // 远离零
		{xnum.Round, "Round", -1.5, 0, -2}, // 远离零
		{xnum.Round, "Round", 2.5, 0, 3},
		{xnum.Round, "Round", 1.2345, 2, 1.23},
		{xnum.Round, "Round", 1.2355, 2, 1.24},
		{xnum.Round, "Round", 3.14159, 3, 3.142},
		{xnum.Round, "Round", -3.14159, 3, -3.142},
		{xnum.Round, "Round", 100, -1, 100},

		{xnum.RoundHalfEven, "RoundHalfEven", 2.5, 0, 2}, // 银行家舍入
		{xnum.RoundHalfEven, "RoundHalfEven", 3.5, 0, 4},
		{xnum.RoundHalfEven, "RoundHalfEven", -2.5, 0, -2},
		{xnum.RoundHalfEven, "RoundHalfEven", 0.125, 2, 0.12},
		{xnum.RoundHalfEven, "RoundHalfEven", 0.135, 2, 0.14},

		{xnum.Floor, "Floor", 1.9, 0, 1},
		{xnum.Floor, "Floor", -1.1, 0, -2},
		{xnum.Floor, "Floor", 1.999, 2, 1.99},
		{xnum.Ceil, "Ceil", 1.1, 0, 2},
		{xnum.Ceil, "Ceil", -1.9, 0, -1},
		{xnum.Ceil, "Ceil", 1.001, 2, 1.01},
		{xnum.Trunc, "Trunc", 1.9, 0, 1},
		{xnum.Trunc, "Trunc", -1.9, 0, -1},
		{xnum.Trunc, "Trunc", 1.999, 2, 1.99},
	}
	for _, c := range cases {
		got := c.fn(c.v, c.p)
		if math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s(%v,%d) = %v, want %v", c.name, c.v, c.p, got, c.want)
		}
	}
}

func TestRoundingSpecialValues(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	for _, fn := range []func(float64, int) float64{xnum.Round, xnum.RoundHalfEven, xnum.Floor, xnum.Ceil, xnum.Trunc} {
		if !math.IsNaN(fn(nan, 2)) {
			t.Fatal("NaN 应原样返回")
		}
		if !math.IsInf(fn(inf, 2), 1) {
			t.Fatal("Inf 应原样返回")
		}
	}
}

func TestUnitAlign(t *testing.T) {
	eqF(t, "FloorTo", xnum.FloorTo(97, 15), 90)
	eqF(t, "FloorTo(对齐)", xnum.FloorTo(90, 15), 90)
	eqF(t, "CeilTo", xnum.CeilTo(91, 15), 105)
	eqF(t, "CeilTo(对齐)", xnum.CeilTo(90, 15), 90)
	eqF(t, "RoundTo", xnum.RoundTo(97, 15), 90)
	eqF(t, "RoundTo(上)", xnum.RoundTo(98, 15), 105)
	// unit <= 0 时原样返回
	eqF(t, "FloorTo(unit=0)", xnum.FloorTo(97, 0), 97)
	eqF(t, "CeilTo(unit=-1)", xnum.CeilTo(97, -1), 97)
	eqF(t, "RoundTo(unit=0)", xnum.RoundTo(97, 0), 97)
}

func TestPercentAndDivide(t *testing.T) {
	eqF(t, "Percent", xnum.Percent(1, 4, 2), 25)
	eqF(t, "Percent(三分)", xnum.Percent(1, 3, 4), 33.3333)
	eqF(t, "Percent(total=0)", xnum.Percent(1, 0, 2), 0)
	eqF(t, "SafeDivide", xnum.SafeDivide(10, 4, -1), 2.5)
	eqF(t, "SafeDivide(除零)", xnum.SafeDivide(10, 0, -1), -1)
	if !xnum.InDelta(1.0, 1.0000001, 1e-6) {
		t.Fatal("InDelta 判断错误")
	}
	if xnum.InDelta(1.0, 1.1, 1e-6) {
		t.Fatal("InDelta 判断错误")
	}
	// 浮点经典问题：变量相加会引入误差（常量表达式会被编译器精确求值，所以必须用变量）
	var fa, fb, fc = 0.1, 0.2, 0.3
	if fa+fb == fc {
		t.Fatal("前提错误：float64 变量加法应产生误差")
	}
	if !xnum.IsNearlyEqual(fa+fb, fc) {
		t.Fatal("IsNearlyEqual 应容忍浮点误差")
	}
}

// ---------------------------------------------------------------------------
// 格式化
// ---------------------------------------------------------------------------

func TestThousands(t *testing.T) {
	cases := []struct {
		v    int64
		sep  string
		want string
	}{
		{0, ",", "0"},
		{12, ",", "12"},
		{123, ",", "123"},
		{1234, ",", "1,234"},
		{1234567, ",", "1,234,567"},
		{-1234, ",", "-1,234"},
		{-1234567, ",", "-1,234,567"},
		{1234, "_", "1_234"},
		{1234, "", "1,234"}, // 空 sep 回退默认
	}
	for _, c := range cases {
		eqS(t, "Thousands", xnum.Thousands(c.v, c.sep), c.want)
	}
}

func TestFormatting(t *testing.T) {
	eqS(t, "FormatFloat", xnum.FormatFloat(1.5, 2), "1.50")
	eqS(t, "FormatFloat(0)", xnum.FormatFloat(0, 0), "0")
	eqS(t, "FormatThousands", xnum.FormatThousands(1234567.891, 2, ","), "1,234,567.89")
	eqS(t, "FormatThousands(负)", xnum.FormatThousands(-1234.5, 1, ","), "-1,234.5")
	eqS(t, "FormatThousands(小)", xnum.FormatThousands(12.3, 2, ","), "12.30")
	eqS(t, "FormatPercent", xnum.FormatPercent(0.1234, 2), "12.34%")
	eqS(t, "FormatPercent(0)", xnum.FormatPercent(0, 0), "0%")
}

func TestOrdinal(t *testing.T) {
	cases := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 5: "5th",
		11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd",
		23: "23rd", 101: "101st", 111: "111th",
	}
	for n, want := range cases {
		eqS(t, "Ordinal", xnum.Ordinal(n), want)
	}
}

func TestRoman(t *testing.T) {
	cases := map[int]string{
		1: "I", 4: "IV", 5: "V", 9: "IX", 40: "XL", 58: "LVIII",
		1994: "MCMXCIV", 3999: "MMMCMXCIX",
	}
	for n, want := range cases {
		eqS(t, "Roman", xnum.Roman(n), want)
	}
	eqS(t, "Roman(0)", xnum.Roman(0), "")
	eqS(t, "Roman(4000)", xnum.Roman(4000), "")
	eqS(t, "Roman(负)", xnum.Roman(-1), "")
}

func TestBitAndDigitHelpers(t *testing.T) {
	cases := []struct{ n, want int }{
		{0, 1}, {1, 1}, {2, 2}, {3, 4}, {4, 4}, {5, 8}, {1000, 1024}, {1024, 1024},
	}
	for _, c := range cases {
		eqI(t, "NextPow2", xnum.NextPow2(c.n), c.want)
	}
	if !xnum.IsPow2(1) || !xnum.IsPow2(2) || !xnum.IsPow2(1024) {
		t.Fatal("IsPow2 应识别 2 的幂")
	}
	if xnum.IsPow2(0) || xnum.IsPow2(3) || xnum.IsPow2(-2) {
		t.Fatal("IsPow2 误判")
	}
	eqI(t, "ReverseInt", xnum.ReverseInt(1234), 4321)
	eqI(t, "ReverseInt(负)", xnum.ReverseInt(-1234), -4321)
	eqI(t, "ReverseInt(0)", xnum.ReverseInt(0), 0)
	eqI(t, "ReverseInt(尾零)", xnum.ReverseInt(1200), 21)

	got := xnum.Digits(123)
	want := []int{3, 2, 1}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("Digits = %v", got)
	}
	if d := xnum.Digits(0); len(d) != 1 || d[0] != 0 {
		t.Fatalf("Digits(0) = %v", d)
	}
}

// ---------------------------------------------------------------------------
// 字节单位
// ---------------------------------------------------------------------------

func TestBytesToHuman(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1048576, "1 MB"},
		{1073741824, "1 GB"},
		{1099511627776, "1 TB"},
		{-1536, "-1.5 KB"},
	}
	for _, c := range cases {
		eqS(t, "BytesToHuman", xnum.BytesToHuman(c.n), c.want)
	}
	eqS(t, "BytesToHumanSI(1500)", xnum.BytesToHumanSI(1500), "1.5 KB")
	eqS(t, "BytesToHumanSI(1000)", xnum.BytesToHumanSI(1000), "1 KB")
	eqS(t, "BytesToHumanSI(999)", xnum.BytesToHumanSI(999), "999 B")
}

func TestHumanToBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"1KB", 1024},
		{"1KiB", 1024},
		{"1kb", 1024},
		{"1.5MB", 1572864},
		{"2GiB", 2147483648},
		{"1TB", 1099511627776},
		{"512B", 512},
		{"512", 512},
		{"  1.5 MB  ", 1572864},
	}
	for _, c := range cases {
		got, err := xnum.HumanToBytes(c.in)
		if err != nil || got != c.want {
			t.Fatalf("HumanToBytes(%q) = %d,%v, want %d", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1XB", "MB", "1.2.3KB"} {
		if _, err := xnum.HumanToBytes(bad); err == nil {
			t.Fatalf("HumanToBytes(%q) 应报错", bad)
		}
	}
	// 往返一致性
	for _, n := range []int64{0, 512, 1024, 1536, 1048576, 1073741824} {
		s := xnum.BytesToHuman(n)
		back, err := xnum.HumanToBytes(s)
		if err != nil {
			t.Fatalf("往返解析 %q 失败: %v", s, err)
		}
		// 因展示保留 2 位小数，允许轻微误差
		diff := back - n
		if diff < 0 {
			diff = -diff
		}
		if diff > n/1000+1 {
			t.Fatalf("往返不一致: %d → %q → %d", n, s, back)
		}
	}
}

// ---------------------------------------------------------------------------
// 随机数
// ---------------------------------------------------------------------------

func TestRandNumbers(t *testing.T) {
	if got := xnum.RandInt(5, 5); got != 5 {
		t.Fatalf("RandInt(5,5) = %d", got)
	}
	if got := xnum.RandInt(3, 1); got != 3 {
		t.Fatalf("RandInt(max<min) = %d, 应返回 min", got)
	}
	for i := 0; i < 200; i++ {
		if v := xnum.RandInt(1, 6); v < 1 || v > 6 {
			t.Fatalf("RandInt 越界: %d", v)
		}
	}
	if got := xnum.RandFloat(1, 1); got != 1 {
		t.Fatalf("RandFloat(1,1) = %v", got)
	}
	for i := 0; i < 200; i++ {
		if v := xnum.RandFloat(0, 1); v < 0 || v >= 1 {
			t.Fatalf("RandFloat 越界: %v", v)
		}
	}

	if got := xnum.RandWeighted(nil); got != -1 {
		t.Fatalf("RandWeighted(nil) = %d", got)
	}
	if got := xnum.RandWeighted([]float64{0, 0}); got != -1 {
		t.Fatalf("RandWeighted(全零) = %d", got)
	}
	if got := xnum.RandWeighted([]float64{1, 0}); got != 0 {
		t.Fatalf("RandWeighted([1,0]) = %d", got)
	}
	if got := xnum.RandWeighted([]float64{0, 1}); got != 1 {
		t.Fatalf("RandWeighted([0,1]) = %d", got)
	}
	// 统计校验：[9,1] 的分布应明显偏向 0
	counts := [2]int{}
	for i := 0; i < 2000; i++ {
		idx := xnum.RandWeighted([]float64{9, 1})
		if idx < 0 || idx > 1 {
			t.Fatalf("RandWeighted 返回非法下标 %d", idx)
		}
		counts[idx]++
	}
	if counts[0] < 1500 {
		t.Fatalf("权重分布异常: %v", counts)
	}
}

// ---------------------------------------------------------------------------
// Decimal
// ---------------------------------------------------------------------------

func TestDecimalPrecision(t *testing.T) {
	// 这是 Decimal 存在的核心理由：float64 变量相加得不到 0.3
	var fa, fb, fc = 0.1, 0.2, 0.3
	if fa+fb == fc {
		t.Fatal("前提错误：float64 变量加法应产生误差")
	}
	got := xnum.NewDecimal("0.1").Add(xnum.NewDecimal("0.2"))
	eqS(t, "Decimal 0.1+0.2", got.String(), "0.3")
	if !got.Equal(xnum.NewDecimal("0.3")) {
		t.Fatal("Decimal 加法应精确")
	}

	// 浮点入参使用最短十进制表示
	eqS(t, "NewDecimal(0.1)", xnum.NewDecimal(0.1).String(), "0.1")
	eqS(t, "NewDecimal(0.3)", xnum.NewDecimal(0.3).String(), "0.3")
	// 极大值不应 panic，且保持正数语义
	big := xnum.NewDecimal(math.MaxFloat64)
	if !big.IsPos() || !big.GreaterThan(xnum.NewDecimal("1e300")) {
		t.Fatalf("MaxFloat64 Decimal = %s", big.String()[:20])
	}
	// 极小值同样安全
	if tiny := xnum.NewDecimal(math.SmallestNonzeroFloat64); !tiny.IsPos() {
		t.Fatal("最小正浮点数应保持正数")
	}
}

func TestDecimalConstruction(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "0"},
		{0, "0"},
		{42, "42"},
		{-42, "-42"},
		{int8(7), "7"},
		{int64(123), "123"},
		{uint(9), "9"},
		{uint64(18446744073709551615), "18446744073709551615"},
		{3.5, "3.5"},
		{float32(1.5), "1.5"},
		{"0.1", "0.1"},
		{"  2.5  ", "2.5"},
		{"1e3", "1000"},
		{"1.23e-2", "0.0123"},
		{"3/4", "0.75"},
		{"-0", "0"},
		{"bad", "0"}, // 无法解析 → 零值
		{true, "0"},  // 不支持的类型 → 零值
	}
	for _, c := range cases {
		if got := xnum.NewDecimal(c.in).String(); got != c.want {
			t.Fatalf("NewDecimal(%#v) = %q, want %q", c.in, got, c.want)
		}
	}

	if d := xnum.NewDecimal(xnum.NewDecimal("1.5")); d.String() != "1.5" {
		t.Fatalf("Decimal→Decimal = %q", d.String())
	}
	var pd *xnum.Decimal
	if d := xnum.NewDecimal(pd); d.String() != "0" {
		t.Fatalf("nil *Decimal = %q", d.String())
	}
	d := xnum.NewDecimal("1.5")
	if from := xnum.NewDecimal(&d); from.String() != "1.5" {
		t.Fatalf("*Decimal = %q", from.String())
	}

	if _, err := xnum.ParseDecimal(""); err == nil {
		t.Fatal("ParseDecimal(\"\") 应报错")
	}
	if _, err := xnum.ParseDecimal("abc"); err == nil {
		t.Fatal("ParseDecimal(abc) 应报错")
	}
	if xnum.MustParseDecimal("1.5").String() != "1.5" {
		t.Fatal("MustParseDecimal 失败")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("MustParseDecimal 应在解析失败时 panic")
			}
		}()
		_ = xnum.MustParseDecimal("abc")
	}()
}

func TestDecimalArithmetic(t *testing.T) {
	a, b := xnum.NewDecimal("1.5"), xnum.NewDecimal("0.25")
	eqS(t, "Add", a.Add(b).String(), "1.75")
	eqS(t, "Sub", a.Sub(b).String(), "1.25")
	eqS(t, "Mul", a.Mul(b).String(), "0.375")
	eqS(t, "Div", a.Div(b).String(), "6")
	eqS(t, "Neg", a.Neg().String(), "-1.5")
	eqS(t, "Abs", a.Neg().Abs().String(), "1.5")

	// 零值可直接参与运算
	var zero xnum.Decimal
	eqS(t, "零值 Add", zero.Add(a).String(), "1.5")
	eqS(t, "零值 String", zero.String(), "0")
	if !zero.IsZero() {
		t.Fatal("零值 IsZero 应为 true")
	}
	eqS(t, "零值 Sub", zero.Sub(a).String(), "-1.5")
	eqS(t, "零值 Mul", zero.Mul(a).String(), "0")

	// 除零不 panic，返回零值
	eqS(t, "Div(0)", a.Div(zero).String(), "0")
	eqS(t, "Div(zero/zero)", zero.Div(zero).String(), "0")
	if _, err := a.DivWithError(zero); err == nil {
		t.Fatal("DivWithError 除零应报错")
	}
	if v, err := a.DivWithError(xnum.NewDecimal("2")); err != nil || v.String() != "0.75" {
		t.Fatalf("DivWithError = %v,%v", v.String(), err)
	}
}

func TestDecimalCompare(t *testing.T) {
	a, b := xnum.NewDecimal("1.5"), xnum.NewDecimal("1.5")
	if a.Cmp(b) != 0 || !a.Equal(b) {
		t.Fatal("相等判断错误")
	}
	if !a.GreaterThan(xnum.NewDecimal("1.4")) || !a.LessThan(xnum.NewDecimal("1.6")) {
		t.Fatal("比较判断错误")
	}
	// 注意：复合字面量出现在 if 条件中必须加括号，否则会被解析为代码块
	if a.Sign() != 1 || xnum.NewDecimal("-1").Sign() != -1 || (xnum.Decimal{}).Sign() != 0 {
		t.Fatal("Sign 判断错误")
	}
	// 回归：零值必须在所有方法上都不 panic（曾因缺少 nil 守卫导致 SIGSEGV）
	var zero xnum.Decimal
	if zero.Sign() != 0 || zero.IsNeg() || zero.IsPos() || !zero.IsZero() {
		t.Fatal("零值符号判定错误")
	}
	if zero.String() != "0" || zero.StringFixed(2) != "0.00" || zero.Float64() != 0 {
		t.Fatal("零值格式化错误")
	}
	if zero.Int64() != 0 || zero.Cmp(zero) != 0 || !zero.Equal(zero) {
		t.Fatal("零值比较错误")
	}
	if _, err := zero.MarshalJSON(); err != nil {
		t.Fatalf("零值 MarshalJSON: %v", err)
	}
	if !xnum.NewDecimal("-1").IsNeg() || !xnum.NewDecimal("1").IsPos() {
		t.Fatal("IsNeg/IsPos 判断错误")
	}
	// 1.50 与 1.5 数值相等
	if !xnum.NewDecimal("1.50").Equal(xnum.NewDecimal("1.5")) {
		t.Fatal("1.50 应等于 1.5")
	}
}

func TestDecimalConversion(t *testing.T) {
	eqF(t, "Float64", xnum.NewDecimal("2.5").Float64(), 2.5)
	eqS(t, "Float64 非有限小数", xnum.NewDecimal("1").Div(xnum.NewDecimal("3")).String(), "0.3333333333")
	eqF(t, "Float64 近似", xnum.NewDecimal("1").Div(xnum.NewDecimal("3")).Float64(), 1.0/3.0)

	if got := xnum.NewDecimal("3.9").Int64(); got != 3 {
		t.Fatalf("Int64(3.9) = %d", got)
	}
	if got := xnum.NewDecimal("-3.9").Int64(); got != -3 {
		t.Fatalf("Int64(-3.9) = %d", got)
	}
	if got := xnum.NewDecimal("1e30").Int64(); got != math.MaxInt64 {
		t.Fatalf("Int64 溢出上界 = %d", got)
	}
	if got := xnum.NewDecimal("-1e30").Int64(); got != math.MinInt64 {
		t.Fatalf("Int64 溢出下界 = %d", got)
	}

	eqS(t, "StringFixed", xnum.NewDecimal("1.5").StringFixed(2), "1.50")
	eqS(t, "StringFixed(进位)", xnum.NewDecimal("1.567").StringFixed(2), "1.57")
	eqS(t, "StringFixed(负数位)", xnum.NewDecimal("1.567").StringFixed(-1), "2")
}

func TestDecimalRounding(t *testing.T) {
	cases := []struct {
		in     string
		mode   string
		places int
		want   string
	}{
		// 关键：float64 的 1.005 四舍五入会得到 1.00，Decimal 得到 1.01
		{"1.005", "round", 2, "1.01"},
		{"2.5", "round", 0, "3"},
		{"-2.5", "round", 0, "-3"},
		{"1.234", "round", 2, "1.23"},
		{"1.236", "round", 2, "1.24"},
		{"-1.236", "round", 2, "-1.24"},

		{"2.5", "halfEven", 0, "2"},
		{"3.5", "halfEven", 0, "4"},
		{"0.125", "halfEven", 2, "0.12"},
		{"0.135", "halfEven", 2, "0.14"},

		{"1.999", "trunc", 2, "1.99"},
		{"-1.999", "trunc", 2, "-1.99"},
		{"1.999", "floor", 2, "1.99"},
		{"-1.001", "floor", 2, "-1.01"},
		{"1.001", "ceil", 2, "1.01"},
		{"-1.999", "ceil", 2, "-1.99"},
	}
	for _, c := range cases {
		d := xnum.NewDecimal(c.in)
		var got xnum.Decimal
		switch c.mode {
		case "round":
			got = d.Round(c.places)
		case "halfEven":
			got = d.RoundHalfEven(c.places)
		case "trunc":
			got = d.Truncate(c.places)
		case "floor":
			got = d.Floor(c.places)
		case "ceil":
			got = d.Ceil(c.places)
		}
		if got.String() != c.want {
			t.Fatalf("%s(%s, %d) = %s, want %s", c.mode, c.in, c.places, got.String(), c.want)
		}
	}

	// 对照：float64 在 1.005 上会出错，这是 Decimal 的价值所在
	if xnum.Round(1.005, 2) == 1.01 {
		t.Log("注意：当前平台 float64 的 1.005 舍入恰好得到 1.01，属实现细节")
	}
}

func TestDecimalJSON(t *testing.T) {
	type payload struct {
		Amount xnum.Decimal `json:"amount"`
		Rate   xnum.Decimal `json:"rate"`
	}
	p := payload{Amount: xnum.NewDecimal("1234567.89"), Rate: xnum.NewDecimal("0.05")}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"amount":1234567.89,"rate":0.05}` {
		t.Fatalf("Marshal = %s", b)
	}

	var back payload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !back.Amount.Equal(p.Amount) || !back.Rate.Equal(p.Rate) {
		t.Fatalf("往返不一致: %+v", back)
	}

	// 字符串形式的数字也应可解析
	var d xnum.Decimal
	if err := json.Unmarshal([]byte(`"3.5"`), &d); err != nil || d.String() != "3.5" {
		t.Fatalf("Unmarshal(字符串) = %q,%v", d.String(), err)
	}
	// null → 零值
	if err := json.Unmarshal([]byte(`null`), &d); err != nil || !d.IsZero() {
		t.Fatalf("Unmarshal(null) = %q,%v", d.String(), err)
	}
	// 非法输入 → 错误
	if err := json.Unmarshal([]byte(`"abc"`), &d); err == nil {
		t.Fatal("Unmarshal(abc) 应报错")
	}
	// nil 接收者
	var nilD *xnum.Decimal
	if err := nilD.UnmarshalJSON([]byte("1")); err == nil {
		t.Fatal("nil *Decimal 应报错")
	}
	// 非有限小数也能被 JSON 编码（保留 10 位）
	third := xnum.NewDecimal("1").Div(xnum.NewDecimal("3"))
	b2, err := json.Marshal(third)
	if err != nil {
		t.Fatalf("Marshal(非有限小数): %v", err)
	}
	if !strings.HasPrefix(string(b2), "0.3333333333") {
		t.Fatalf("Marshal(非有限小数) = %s", b2)
	}
}

func TestDecimalMoneyScenario(t *testing.T) {
	// 典型金额场景：单价 × 数量 + 运费，按分四舍五入
	price := xnum.NewDecimal("19.99")
	qty := xnum.NewDecimal(3)
	shipping := xnum.NewDecimal("6.50")
	total := price.Mul(qty).Add(shipping).Round(2)
	eqS(t, "订单总额", total.String(), "66.47")
	eqS(t, "金额展示", total.StringFixed(2), "66.47")

	// 分期：总额 / 3，每期保留 2 位，余数补在首期
	per := total.Div(xnum.NewDecimal(3)).Round(2)
	eqS(t, "每期", per.StringFixed(2), "22.16")
	remainder := total.Sub(per.Mul(xnum.NewDecimal(3)))
	eqS(t, "余数", remainder.StringFixed(2), "-0.01")
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkRound(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xnum.Round(3.14159, 2)
	}
}

func BenchmarkBytesToHuman(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xnum.BytesToHuman(1234567)
	}
}

func BenchmarkDecimalAdd(b *testing.B) {
	a, c := xnum.NewDecimal("19.99"), xnum.NewDecimal("0.01")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Add(c)
	}
}
