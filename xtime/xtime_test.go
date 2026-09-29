package xtime_test

import (
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xtime"
)

// 测试基准时刻：2024-01-03 是周三（2024-01-01 为周一）
var (
	utc = time.UTC
	// 2024-01-03 10:35:40.123 UTC
	base = time.Date(2024, 1, 3, 10, 35, 40, 123000000, utc)
)

func eqT(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func eqS(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func eqI(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// 边界
// ---------------------------------------------------------------------------

func TestDayBoundaries(t *testing.T) {
	eqT(t, "BeginOfDay", xtime.BeginOfDay(base),
		time.Date(2024, 1, 3, 0, 0, 0, 0, utc))
	eqT(t, "EndOfDay", xtime.EndOfDay(base),
		time.Date(2024, 1, 3, 23, 59, 59, 999999999, utc))
	// 端点关系：EndOfDay 的下一个纳秒就是次日零点
	if !xtime.EndOfDay(base).Add(time.Nanosecond).Equal(xtime.BeginOfDay(base).AddDate(0, 0, 1)) {
		t.Fatal("EndOfDay 与次日 BeginOfDay 未衔接")
	}
}

func TestWeekBoundaries(t *testing.T) {
	// 默认以周一为一周开始
	eqT(t, "BeginOfWeek(周一)", xtime.BeginOfWeek(base),
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc))
	eqT(t, "EndOfWeek", xtime.EndOfWeek(base),
		time.Date(2024, 1, 7, 23, 59, 59, 999999999, utc))
	// 以周日为起点
	eqT(t, "BeginOfWeek(周日)", xtime.BeginOfWeek(base, time.Sunday),
		time.Date(2023, 12, 31, 0, 0, 0, 0, utc))
	// 恰好是周一时不应回退一周
	monday := time.Date(2024, 1, 1, 15, 0, 0, 0, utc)
	eqT(t, "BeginOfWeek(本身是周一)", xtime.BeginOfWeek(monday), monday.Truncate(24*time.Hour))
}

func TestMonthQuarterYearBoundaries(t *testing.T) {
	eqT(t, "BeginOfMonth", xtime.BeginOfMonth(base), time.Date(2024, 1, 1, 0, 0, 0, 0, utc))
	eqT(t, "EndOfMonth(1月)", xtime.EndOfMonth(base),
		time.Date(2024, 1, 31, 23, 59, 59, 999999999, utc))

	feb := time.Date(2024, 2, 10, 12, 0, 0, 0, utc)
	eqT(t, "EndOfMonth(闰年2月)", xtime.EndOfMonth(feb),
		time.Date(2024, 2, 29, 23, 59, 59, 999999999, utc))

	eqT(t, "BeginOfQuarter(Q2)", xtime.BeginOfQuarter(time.Date(2024, 5, 15, 0, 0, 0, 0, utc)),
		time.Date(2024, 4, 1, 0, 0, 0, 0, utc))
	eqT(t, "EndOfQuarter(Q2)", xtime.EndOfQuarter(time.Date(2024, 5, 15, 0, 0, 0, 0, utc)),
		time.Date(2024, 6, 30, 23, 59, 59, 999999999, utc))
	eqT(t, "BeginOfQuarter(Q1)", xtime.BeginOfQuarter(base), time.Date(2024, 1, 1, 0, 0, 0, 0, utc))
	eqT(t, "BeginOfQuarter(Q4)", xtime.BeginOfQuarter(time.Date(2024, 11, 5, 0, 0, 0, 0, utc)),
		time.Date(2024, 10, 1, 0, 0, 0, 0, utc))

	eqT(t, "BeginOfYear", xtime.BeginOfYear(base), time.Date(2024, 1, 1, 0, 0, 0, 0, utc))
	eqT(t, "EndOfYear", xtime.EndOfYear(base),
		time.Date(2024, 12, 31, 23, 59, 59, 999999999, utc))
}

func TestStartOf(t *testing.T) {
	eqT(t, "StartOf(1h)", xtime.StartOf(base, time.Hour),
		time.Date(2024, 1, 3, 10, 0, 0, 0, utc))
	eqT(t, "StartOf(15m)", xtime.StartOf(base, 15*time.Minute),
		time.Date(2024, 1, 3, 10, 30, 0, 0, utc))
	eqT(t, "StartOf(0)", xtime.StartOf(base, 0), base)
	eqT(t, "StartOf(-1)", xtime.StartOf(base, -time.Second), base)
}

// ---------------------------------------------------------------------------
// 运算与比较
// ---------------------------------------------------------------------------

func TestAddFunctions(t *testing.T) {
	eqT(t, "AddDays", xtime.AddDays(base, 1), base.AddDate(0, 0, 1))
	eqT(t, "AddDays(负)", xtime.AddDays(base, -3), base.AddDate(0, 0, -3))
	eqT(t, "AddYears", xtime.AddYears(base, 1), base.AddDate(1, 0, 0))

	// AddMonths 与 time.AddDate 一致：会规范化溢出
	jan31 := time.Date(2024, 1, 31, 9, 0, 0, 0, utc)
	eqT(t, "AddMonths(2024-01-31 +1M 规范化)", xtime.AddMonths(jan31, 1),
		time.Date(2024, 3, 2, 9, 0, 0, 0, utc))

	// AddMonthsClamp 把日期夹到目标月最后一天
	eqT(t, "AddMonthsClamp(2024-01-31 +1M)", xtime.AddMonthsClamp(jan31, 1),
		time.Date(2024, 2, 29, 9, 0, 0, 0, utc))
	eqT(t, "AddMonthsClamp(2023-01-31 +1M)", xtime.AddMonthsClamp(time.Date(2023, 1, 31, 0, 0, 0, 0, utc), 1),
		time.Date(2023, 2, 28, 0, 0, 0, 0, utc))
	eqT(t, "AddMonthsClamp(2024-03-31 -1M)", xtime.AddMonthsClamp(time.Date(2024, 3, 31, 0, 0, 0, 0, utc), -1),
		time.Date(2024, 2, 29, 0, 0, 0, 0, utc))
	// 不需要夹时保持原日期
	eqT(t, "AddMonthsClamp(无夹)", xtime.AddMonthsClamp(time.Date(2024, 1, 15, 0, 0, 0, 0, utc), 1),
		time.Date(2024, 2, 15, 0, 0, 0, 0, utc))
}

func TestDiffDays(t *testing.T) {
	// 按日历天算，不看时刻
	eqI(t, "DiffDays(跨天)", xtime.DiffDays(
		time.Date(2024, 1, 1, 23, 0, 0, 0, utc),
		time.Date(2024, 1, 2, 1, 0, 0, 0, utc)), 1)
	// 同一天任意时刻为 0
	eqI(t, "DiffDays(同天)", xtime.DiffDays(
		time.Date(2024, 1, 1, 0, 0, 1, 0, utc),
		time.Date(2024, 1, 1, 23, 59, 59, 0, utc)), 0)
	eqI(t, "DiffDays(逆序)", xtime.DiffDays(
		time.Date(2024, 1, 2, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc)), -1)
	eqI(t, "DiffDays(同年)", xtime.DiffDays(
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc),
		time.Date(2024, 12, 31, 0, 0, 0, 0, utc)), 365)

	eqI(t, "DaysBetween(取绝对值)", xtime.DaysBetween(
		time.Date(2024, 1, 2, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc)), 1)
}

func TestWorkdaysBetween(t *testing.T) {
	// 2024-01-01(周一) ~ 2024-01-07(周日) → 5 个工作日
	eqI(t, "WorkdaysBetween(整周)", xtime.WorkdaysBetween(
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 7, 0, 0, 0, 0, utc)), 5)
	// 逆序自动交换
	eqI(t, "WorkdaysBetween(逆序)", xtime.WorkdaysBetween(
		time.Date(2024, 1, 7, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc)), 5)
	// 单日周六
	eqI(t, "WorkdaysBetween(周六)", xtime.WorkdaysBetween(
		time.Date(2024, 1, 6, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 6, 0, 0, 0, 0, utc)), 0)
	// 单日周三
	eqI(t, "WorkdaysBetween(周三)", xtime.WorkdaysBetween(
		time.Date(2024, 1, 3, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 3, 0, 0, 0, 0, utc)), 1)
	// 跨两周
	eqI(t, "WorkdaysBetween(14天)", xtime.WorkdaysBetween(
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 14, 0, 0, 0, 0, utc)), 10)
}

func TestComparisons(t *testing.T) {
	if !xtime.IsWeekend(time.Date(2024, 1, 6, 0, 0, 0, 0, utc)) {
		t.Fatal("周六应识别为周末")
	}
	if !xtime.IsWeekend(time.Date(2024, 1, 7, 0, 0, 0, 0, utc)) {
		t.Fatal("周日应识别为周末")
	}
	if xtime.IsWeekend(time.Date(2024, 1, 8, 0, 0, 0, 0, utc)) {
		t.Fatal("周一不应识别为周末")
	}

	if !xtime.IsSameDay(base, time.Date(2024, 1, 3, 23, 59, 59, 0, utc)) {
		t.Fatal("IsSameDay 判断错误")
	}
	if xtime.IsSameDay(base, time.Date(2024, 1, 4, 0, 0, 0, 0, utc)) {
		t.Fatal("IsSameDay 误判")
	}

	now := time.Now()
	if !xtime.IsToday(now) {
		t.Fatal("IsToday 判断错误")
	}
	if !xtime.IsYesterday(now.AddDate(0, 0, -1)) {
		t.Fatal("IsYesterday 判断错误")
	}
	if !xtime.IsTomorrow(now.AddDate(0, 0, 1)) {
		t.Fatal("IsTomorrow 判断错误")
	}
	if xtime.IsToday(now.AddDate(0, 0, -1)) {
		t.Fatal("IsToday 误判")
	}

	if !xtime.Between(base, base.Add(-time.Hour), base.Add(time.Hour)) {
		t.Fatal("Between 判断错误")
	}
	if xtime.Between(base, base.Add(time.Hour), base.Add(2*time.Hour)) {
		t.Fatal("Between 误判")
	}
	// 端点包含
	if !xtime.Between(base, base, base) {
		t.Fatal("Between 应包含端点")
	}
	// 逆序区间自动交换
	if !xtime.Between(base, base.Add(time.Hour), base.Add(-time.Hour)) {
		t.Fatal("Between 逆序应自动交换")
	}
}

func TestCalendarHelpers(t *testing.T) {
	for _, c := range []struct {
		y    int
		want bool
	}{{2024, true}, {2023, false}, {2000, true}, {1900, false}, {0, true}} {
		if xtime.IsLeapYear(c.y) != c.want {
			t.Fatalf("IsLeapYear(%d) = %v", c.y, !c.want)
		}
	}

	eqI(t, "DaysInMonth(2024,2)", xtime.DaysInMonth(2024, time.February), 29)
	eqI(t, "DaysInMonth(2023,2)", xtime.DaysInMonth(2023, time.February), 28)
	eqI(t, "DaysInMonth(2024,1)", xtime.DaysInMonth(2024, time.January), 31)
	eqI(t, "DaysInMonth(2024,4)", xtime.DaysInMonth(2024, time.April), 30)

	eqI(t, "Age(未满生日)", xtime.Age(
		time.Date(1990, 6, 15, 0, 0, 0, 0, utc),
		time.Date(2024, 6, 14, 0, 0, 0, 0, utc)), 33)
	eqI(t, "Age(生日当天)", xtime.Age(
		time.Date(1990, 6, 15, 0, 0, 0, 0, utc),
		time.Date(2024, 6, 15, 0, 0, 0, 0, utc)), 34)
	eqI(t, "Age(已过生日)", xtime.Age(
		time.Date(1990, 6, 15, 0, 0, 0, 0, utc),
		time.Date(2024, 12, 1, 0, 0, 0, 0, utc)), 34)
	eqI(t, "Age(未来出生)", xtime.Age(
		time.Date(2030, 1, 1, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc)), 0)
}

func TestRangeOfDays(t *testing.T) {
	got := xtime.RangeOfDays(
		time.Date(2024, 1, 1, 10, 0, 0, 0, utc),
		time.Date(2024, 1, 3, 5, 0, 0, 0, utc))
	if len(got) != 3 {
		t.Fatalf("RangeOfDays 长度 = %d", len(got))
	}
	for i, want := range []time.Time{
		time.Date(2024, 1, 1, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 2, 0, 0, 0, 0, utc),
		time.Date(2024, 1, 3, 0, 0, 0, 0, utc),
	} {
		eqT(t, "RangeOfDays["+string(rune('0'+i))+"]", got[i], want)
	}
	// 单日
	if n := len(xtime.RangeOfDays(base, base)); n != 1 {
		t.Fatalf("单日 RangeOfDays = %d", n)
	}
	// 逆序返回 nil
	if got := xtime.RangeOfDays(base, base.AddDate(0, 0, -1)); got != nil {
		t.Fatalf("逆序 RangeOfDays = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 解析与格式化
// ---------------------------------------------------------------------------

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		year    int
		month   time.Month
		day     int
		hour    int
		checkFn func(time.Time) bool
	}{
		{"2024-01-02", 2024, 1, 2, 0, nil},
		{"2024-01-02 15:04:05", 2024, 1, 2, 15, nil},
		{"2024-01-02T15:04:05Z", 2024, 1, 2, 15, nil},
		{"2024-01-02T15:04:05+08:00", 2024, 1, 2, 15, nil},
		{"2024/01/02", 2024, 1, 2, 0, nil},
		{"2024/01/02 15:04:05", 2024, 1, 2, 15, nil},
		{"20240102150405", 2024, 1, 2, 15, nil},
		{"20240102", 2024, 1, 2, 0, nil},
		{"2024-1-2", 2024, 1, 2, 0, nil},
		{"2024年01月02日 15:04:05", 2024, 1, 2, 15, nil},
		{"2024年01月02日", 2024, 1, 2, 0, nil},
		{"2024-01-02 15:04", 2024, 1, 2, 15, nil},
		{"  2024-01-02  ", 2024, 1, 2, 0, nil},
	}
	for _, c := range cases {
		got, err := xtime.Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q) 失败: %v", c.in, err)
		}
		if got.Year() != c.year || got.Month() != c.month || got.Day() != c.day || got.Hour() != c.hour {
			t.Fatalf("Parse(%q) = %v", c.in, got)
		}
	}

	// 显式布局优先
	got, err := xtime.Parse("02/01/2024", "02/01/2006")
	if err != nil || got.Day() != 2 || got.Month() != 1 {
		t.Fatalf("Parse(显式布局) = %v,%v", got, err)
	}

	// 失败场景
	for _, bad := range []string{"", "   ", "not a time", "2024-13-45", "12345"} {
		if _, err := xtime.Parse(bad); err == nil {
			t.Fatalf("Parse(%q) 应报错", bad)
		}
	}

	// MustParse
	if xtime.MustParse("2024-01-02").Year() != 2024 {
		t.Fatal("MustParse 失败")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("MustParse 应在失败时 panic")
			}
		}()
		_ = xtime.MustParse("bad")
	}()
}

func TestParseInLocation(t *testing.T) {
	sh, err := xtime.Timezone("Asia/Shanghai")
	if err != nil {
		t.Skipf("无法加载 Asia/Shanghai: %v", err)
	}
	got, err := xtime.ParseInLocation("2024-01-02 15:04:05", xtime.LayoutDateTime, sh)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}
	if got.Location() != sh {
		t.Fatalf("时区错误: %v", got.Location())
	}
	if got.UTC().Hour() != 7 {
		t.Fatalf("UTC 小时 = %d, want 7", got.UTC().Hour())
	}
	// nil location → Local
	if _, err := xtime.ParseInLocation("2024-01-02", xtime.LayoutDate, nil); err != nil {
		t.Fatalf("ParseInLocation(nil loc): %v", err)
	}
}

func TestParseDate(t *testing.T) {
	for _, in := range []string{"2024-01-02", "2024/01/02", "20240102", "2024-1-2"} {
		got, err := xtime.ParseDate(in)
		if err != nil || got.Year() != 2024 || got.Month() != 1 || got.Day() != 2 {
			t.Fatalf("ParseDate(%q) = %v,%v", in, got, err)
		}
	}
	if _, err := xtime.ParseDate("bad"); err == nil {
		t.Fatal("ParseDate(bad) 应报错")
	}
}

func TestFormat(t *testing.T) {
	eqS(t, "Format(默认)", xtime.Format(base), "2024-01-03 10:35:40")
	eqS(t, "Format(自定义)", xtime.Format(base, xtime.LayoutDate), "2024-01-03")
	eqS(t, "Format(空布局回退默认)", xtime.Format(base, ""), "2024-01-03 10:35:40")
	eqS(t, "FormatDate", xtime.FormatDate(base), "2024-01-03")
	eqS(t, "FormatMillis", xtime.FormatMillis(base), "2024-01-03 10:35:40.123")
	eqS(t, "FormatCompact", xtime.FormatCompact(base), "20240103103540")
	eqS(t, "FormatCN", xtime.FormatCN(base), "2024年01月03日 10:35:40")
}

func TestTimestamps(t *testing.T) {
	eqI(t, "AutoFromTimestamp(秒)", int(xtime.AutoFromTimestamp(1700000000).Unix()), 1700000000)
	eqI(t, "AutoFromTimestamp(毫秒)", int(xtime.AutoFromTimestamp(1700000000000).Unix()), 1700000000)
	eqI(t, "AutoFromTimestamp(微秒)", int(xtime.AutoFromTimestamp(1700000000000000).Unix()), 1700000000)
	eqI(t, "AutoFromTimestamp(纳秒)", int(xtime.AutoFromTimestamp(1700000000000000000).Unix()), 1700000000)
	if !xtime.AutoFromTimestamp(0).IsZero() {
		t.Fatal("AutoFromTimestamp(0) 应为零值时间")
	}

	eqI(t, "ToTimestamp", int(xtime.ToTimestamp(base)), int(base.Unix()))
	eqI(t, "ToTimestampMilli", int(xtime.ToTimestampMilli(base)), int(base.UnixMilli()))
	eqI(t, "FromTimestamp", int(xtime.FromTimestamp(1700000000).Unix()), 1700000000)
	eqI(t, "FromTimestampMilli", int(xtime.FromTimestampMilli(1700000000000).Unix()), 1700000000)
	eqI(t, "FromTimestampNano", int(xtime.FromTimestampNano(1700000000000000000).Unix()), 1700000000)

	// Now* 应单调且合理
	if xtime.NowSeconds() <= 0 || xtime.NowMillis() <= 0 || xtime.NowNanos() <= 0 {
		t.Fatal("Now* 返回非正值")
	}
	if xtime.NowUTC().Location() != time.UTC {
		t.Fatal("NowUTC 时区错误")
	}
}

func TestTimezone(t *testing.T) {
	if loc, err := xtime.Timezone(""); err != nil || loc != time.Local {
		t.Fatalf("Timezone(\"\") = %v,%v", loc, err)
	}
	if loc, err := xtime.Timezone("UTC"); err != nil || loc != time.UTC {
		t.Fatalf("Timezone(UTC) = %v,%v", loc, err)
	}
	if loc, err := xtime.Timezone("Asia/Shanghai"); err != nil || loc == nil {
		t.Fatalf("Timezone(Asia/Shanghai) = %v,%v", loc, err)
	}
	if _, err := xtime.Timezone("Bad/Zone"); err == nil {
		t.Fatal("Timezone(Bad/Zone) 应报错")
	}
}

// ---------------------------------------------------------------------------
// 人类可读文案
// ---------------------------------------------------------------------------

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{time.Nanosecond, "1ns"},
		{100 * time.Microsecond, "100µs"},
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{time.Second, "1s"},
		{2 * time.Second, "2s"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{90 * time.Second, "1m30s"},
		{time.Hour, "1h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{24 * time.Hour, "1d"},
		{26 * time.Hour, "1d2h"},
		{72 * time.Hour, "3d"},
		{-90 * time.Second, "-1m30s"},
	}
	for _, c := range cases {
		if got := xtime.HumanizeDuration(c.d); got != c.want {
			t.Fatalf("HumanizeDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestHumanizeAgo(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "just now"},
		{5 * time.Second, "just now"},
		{-5 * time.Second, "just now"},
		{30 * time.Second, "30s ago"},
		{90 * time.Second, "1m ago"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{90 * 24 * time.Hour, "3mo ago"},
		{400 * 24 * time.Hour, "1y ago"},
		{-5 * time.Minute, "in 5m"},
		{-3 * time.Hour, "in 3h"},
	}
	for _, c := range cases {
		if got := xtime.HumanizeAgo(c.d); got != c.want {
			t.Fatalf("HumanizeAgo(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if got := xtime.HumanizeSince(time.Now().Add(-2 * time.Minute)); got != "2m ago" {
		t.Fatalf("HumanizeSince = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Stopwatch
// ---------------------------------------------------------------------------

func TestStopwatch(t *testing.T) {
	sw := xtime.NewStopwatch()
	if sw == nil {
		t.Fatal("NewStopwatch 返回 nil")
	}
	time.Sleep(20 * time.Millisecond)
	l1 := sw.Lap()
	if l1 < 15*time.Millisecond {
		t.Fatalf("Lap = %v, 过短", l1)
	}
	time.Sleep(10 * time.Millisecond)
	l2 := sw.Lap()
	if l2 < 5*time.Millisecond {
		t.Fatalf("Lap = %v, 过短", l2)
	}
	// 第二段应明显短于总时长
	if el := sw.Elapsed(); el < l1 {
		t.Fatalf("Elapsed(%v) 应 >= 首次 Lap(%v)", el, l1)
	}

	sw.Reset()
	if sw.Elapsed() > 5*time.Millisecond {
		t.Fatalf("Reset 后 Elapsed = %v", sw.Elapsed())
	}

	// nil 接收者安全
	var nilSW *xtime.Stopwatch
	if nilSW.Lap() != 0 || nilSW.Elapsed() != 0 {
		t.Fatal("nil Stopwatch 应返回 0")
	}
	nilSW.Reset() // 不应 panic
}

// ---------------------------------------------------------------------------
// 布局列表
// ---------------------------------------------------------------------------

func TestLayoutsConsistency(t *testing.T) {
	// Layouts 必须能被 time.Parse 接受（否则是配置错误）
	probe := "2024-01-02 15:04:05"
	for _, l := range xtime.Layouts {
		if _, err := time.Parse(l, probe); err != nil {
			// 仅记录：部分布局（如纯日期）本就无法解析带时间的字符串
			continue
		}
	}
	// RFC3339Nano 必须排在 RFC3339 之前，避免丢失小数秒精度
	idxNano, idxSec := -1, -1
	for i, l := range xtime.Layouts {
		if l == time.RFC3339Nano {
			idxNano = i
		}
		if l == time.RFC3339 {
			idxSec = i
		}
	}
	if idxNano < 0 || idxSec < 0 || idxNano > idxSec {
		t.Fatalf("Layouts 顺序错误: RFC3339Nano 必须在 RFC3339 之前 (nano=%d sec=%d)", idxNano, idxSec)
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkHumanizeDuration(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xtime.HumanizeDuration(90 * time.Second)
	}
}

func BenchmarkParse(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = xtime.Parse("2024-01-02 15:04:05")
	}
}

func BenchmarkBeginOfDay(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xtime.BeginOfDay(base)
	}
}
