package xconv_test

import (
	"math"
	"testing"

	"github.com/indigo-apple/gokit/xconv"
)

// FuzzToInt 确保所有标量转换入口对任意字符串都不 panic。
func FuzzToInt(f *testing.F) {
	seeds := []string{
		"", "0", "42", "-42", "0x1F", "1_000", "1.9", "abc",
		"999999999999999999999999", "1e309", " 42 ", "+7",
		"2024-01-02 15:04:05", "1700000000000", "1h30m",
		"true", "yes", "off", "\x00", "中文",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// 任意输入都不应 panic
		_ = xconv.ToInt(s)
		_, _ = xconv.ToInt64(s)
		_ = xconv.ToFloat64(s)
		_ = xconv.ToBool(s)
		_ = xconv.ToDuration(s)
		_ = xconv.ToTime(s)
		_ = xconv.ToStringSlice(s)
		_ = xconv.ToIntSlice(s)
		_, _ = xconv.StringToIntSlice(s, ",")
		_, _ = xconv.StringToFloatSlice(s, ",")
		_ = xconv.IsEmpty(s)
	})
}

// FuzzIntToBaseRoundTrip 是进制的核心不变式：
// 对任意 int64 与 2–62 进制，IntToBase 之后再 BaseToInt 必须回到原值。
//
// 这条不变式能直接暴露缓冲区越界、符号处理错误、溢出判断错误。
func FuzzIntToBaseRoundTrip(f *testing.F) {
	f.Add(int64(0), 10)
	f.Add(int64(-1), 2)
	f.Add(int64(1), 62)
	f.Add(int64(math.MinInt64), 2)
	f.Add(int64(math.MaxInt64), 62)
	f.Add(int64(-255), 16)
	f.Fuzz(func(t *testing.T, n int64, base int) {
		if base < 2 || base > 62 {
			return
		}
		s := xconv.IntToBase(n, base)
		if s == "" {
			t.Fatalf("IntToBase(%d, %d) 返回空串", n, base)
		}
		back, err := xconv.BaseToInt(s, base)
		if err != nil {
			t.Fatalf("BaseToInt(%q, %d) 失败: %v", s, base, err)
		}
		if back != n {
			t.Fatalf("进制往返失败: n=%d base=%d → %q → %d", n, base, s, back)
		}
	})
}

// FuzzBaseToIntNoPanic 确保任意字符串输入下 BaseToInt 只返回错误，不会 panic。
func FuzzBaseToIntNoPanic(f *testing.F) {
	f.Add("ff", 16)
	f.Add("-", 10)
	f.Add("", 2)
	f.Add("zzz", 62)
	f.Add("999999999999999999999999", 10)
	f.Fuzz(func(t *testing.T, s string, base int) {
		if base < 2 || base > 62 {
			return
		}
		_, _ = xconv.BaseToInt(s, base)
	})
}

// FuzzValueChain 确保容错取值链对任意 map 结构都不 panic。
func FuzzValueChain(f *testing.F) {
	f.Add("a.b.c", "v")
	f.Add("a..b", "x")
	f.Add("a[0].b", "y")
	f.Add("", "z")
	f.Add("a.b[999999]", "w")
	f.Fuzz(func(t *testing.T, path, val string) {
		m := map[string]any{
			"a": map[string]any{
				"b": map[string]any{"c": val},
			},
			"": val,
		}
		_ = xconv.V(m).Path(path).String("def")
		_ = xconv.V(m).Path(path).Int(1)
		_ = xconv.V(m).Path(path).Bool()
	})
}
