// Package xtime 提供时间工具：布局常量与多布局解析、时间边界、日期差、人类可读文案、
// 时间对齐、以及耗时统计。
//
// 设计要点：
//   - **Layouts 是全工具链唯一的「自动嗅探布局」列表**（xreflect.Assign 也复用它），
//     保证 `xreflect.MapToStruct` 与 `xtime.Parse` 对同一字符串的解析结果一致；
//   - 所有「取边界」函数都保持**原时区**语义（不偷偷转 UTC），
//     只有 StartOf 的大粒度对齐（>= 1 天）才基于 UTC epoch，并在文档中标注；
//   - HumanizeDuration 输出紧凑的英文中性格式（"1m30s" / "2d3h"），
//     面向 UI 的中文文案请在其上二次封装；
//   - 时区解析失败不 panic，返回错误。
//
// 并发安全：本包全部为纯函数（Stopwatch 非并发安全），可并发调用。
package xtime

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 常用时间布局。
const (
	LayoutDate        = "2006-01-02"
	LayoutDateSlash   = "2006/01/02"
	LayoutDateTime    = "2006-01-02 15:04:05"
	LayoutDateTimeMs  = "2006-01-02 15:04:05.000"
	LayoutTime        = "15:04:05"
	LayoutTimeShort   = "15:04"
	LayoutCompact     = "20060102150405"
	LayoutCompactDate = "20060102"
	LayoutCompactTime = "150405"
	LayoutISO8601     = time.RFC3339
	LayoutISO8601Ms   = time.RFC3339Nano
	LayoutCN          = "2006年01月02日"
	LayoutCNTime      = "2006年01月02日 15:04:05"
)

// Layouts 是自动嗅探解析时按顺序尝试的布局列表（从最具体到最宽松）。
//
// 顺序很重要：RFC3339Nano 必须先于 RFC3339，带时间的必须先于纯日期，
// 否则会先匹配到较宽松的布局而丢失精度。
var Layouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	time.RFC850,
	time.ANSIC,
	time.UnixDate,
	LayoutDateTimeMs,
	LayoutDateTime,
	"2006-01-02 15:04",
	LayoutDate,
	LayoutDateSlash,
	"2006/01/02 15:04:05",
	"2006-1-2 15:04:05",
	"2006-1-2",
	LayoutCompact,
	LayoutCompactDate,
	LayoutCNTime,
	LayoutCN,
}

// ---------------------------------------------------------------------------
// 当前时间
// ---------------------------------------------------------------------------

// Now 返回当前本地时间。
func Now() time.Time { return time.Now() }

// NowUTC 返回当前 UTC 时间。
func NowUTC() time.Time { return time.Now().UTC() }

// NowMillis 返回当前 Unix 毫秒时间戳。
func NowMillis() int64 { return time.Now().UnixMilli() }

// NowSeconds 返回当前 Unix 秒时间戳。
func NowSeconds() int64 { return time.Now().Unix() }

// NowNanos 返回当前 Unix 纳秒时间戳。
func NowNanos() int64 { return time.Now().UnixNano() }

// ---------------------------------------------------------------------------
// 边界
// ---------------------------------------------------------------------------

// BeginOfDay 返回当天 00:00:00（保持原时区）。
func BeginOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// EndOfDay 返回当天 23:59:59.999999999（保持原时区）。
func EndOfDay(t time.Time) time.Time {
	return BeginOfDay(t).AddDate(0, 0, 1).Add(-time.Nanosecond)
}

// BeginOfWeek 返回本周起始日 00:00:00；firstDay 省略时以周一为一周开始。
func BeginOfWeek(t time.Time, firstDay ...time.Weekday) time.Time {
	first := time.Monday
	if len(firstDay) > 0 {
		first = firstDay[0]
	}
	day := BeginOfDay(t)
	delta := (int(day.Weekday()) - int(first) + 7) % 7
	return day.AddDate(0, 0, -delta)
}

// EndOfWeek 返回本周最后一刻；firstDay 省略时以周一为一周开始。
func EndOfWeek(t time.Time, firstDay ...time.Weekday) time.Time {
	return BeginOfWeek(t, firstDay...).AddDate(0, 0, 7).Add(-time.Nanosecond)
}

// BeginOfMonth 返回当月 1 日 00:00:00。
func BeginOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// EndOfMonth 返回当月最后一刻。
func EndOfMonth(t time.Time) time.Time {
	return BeginOfMonth(t).AddDate(0, 1, 0).Add(-time.Nanosecond)
}

// BeginOfQuarter 返回当季首日 00:00:00。
func BeginOfQuarter(t time.Time) time.Time {
	y, m, _ := t.Date()
	q := (int(m) - 1) / 3
	return time.Date(y, time.Month(q*3+1), 1, 0, 0, 0, 0, t.Location())
}

// EndOfQuarter 返回当季最后一刻。
func EndOfQuarter(t time.Time) time.Time {
	return BeginOfQuarter(t).AddDate(0, 3, 0).Add(-time.Nanosecond)
}

// BeginOfYear 返回当年 1 月 1 日 00:00:00。
func BeginOfYear(t time.Time) time.Time {
	return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
}

// EndOfYear 返回当年最后一刻。
func EndOfYear(t time.Time) time.Time {
	return BeginOfYear(t).AddDate(1, 0, 0).Add(-time.Nanosecond)
}

// StartOf 把 t 对齐到 d 的整数倍边界（基于 UTC epoch）。
//
// 注意：对于 >= 1 小时的粒度，Truncate 以 epoch 为基准，
// **跨时区时不等于本地零点**；需要本地零点请使用 BeginOfDay / BeginOfMonth 等。
func StartOf(t time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return t
	}
	return t.Truncate(d)
}

// ---------------------------------------------------------------------------
// 运算与比较
// ---------------------------------------------------------------------------

// AddDays 加减天数。
func AddDays(t time.Time, n int) time.Time { return t.AddDate(0, 0, n) }

// AddMonths 加减月数。
//
// 与 time.AddDate 一致：结果会像 time.Date 一样**规范化**，
// 因此 2024-01-31 加 1 个月会得到 2024-03-02（而不是 2 月的最后一天）。
// 需要把日期夹到目标月的最后一天，请使用 AddMonthsClamp。
func AddMonths(t time.Time, n int) time.Time { return t.AddDate(0, n, 0) }

// AddMonthsClamp 加减月数，并把「日」夹到目标月的最后一天。
//
//	2024-01-31 + 1 月 → 2024-02-29
//	2023-01-31 + 1 月 → 2023-02-28
//
// 适合账单日、订阅续期等场景。
func AddMonthsClamp(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	firstOfTarget := time.Date(y, m, 1, 0, 0, 0, 0, t.Location()).AddDate(0, n, 0)
	if last := DaysInMonth(firstOfTarget.Year(), firstOfTarget.Month()); d > last {
		d = last
	}
	return time.Date(firstOfTarget.Year(), firstOfTarget.Month(), d,
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// AddYears 加减年数。
func AddYears(t time.Time, n int) time.Time { return t.AddDate(n, 0, 0) }

// DiffDays 返回两个时间相差的**日历天数**（按本地零点计算，忽略时刻）。
//
//	DiffDays(今天 23:00, 明天 01:00) == 1
func DiffDays(a, b time.Time) int {
	da := BeginOfDay(a)
	db := BeginOfDay(b)
	return int(db.Sub(da).Hours() / 24)
}

// DaysBetween 是 DiffDays 的别名，返回绝对值。
func DaysBetween(a, b time.Time) int {
	d := DiffDays(a, b)
	if d < 0 {
		return -d
	}
	return d
}

// WorkdaysBetween 返回 [a, b] 之间的工作日数量（仅排除周六周日，不考虑法定节假日）。
//
// 起止日均计入；a 晚于 b 时自动交换。
func WorkdaysBetween(a, b time.Time) int {
	from, to := BeginOfDay(a), BeginOfDay(b)
	if to.Before(from) {
		from, to = to, from
	}
	n := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			n++
		}
	}
	return n
}

// IsWeekend 判断是否为周六或周日。
func IsWeekend(t time.Time) bool {
	w := t.Weekday()
	return w == time.Saturday || w == time.Sunday
}

// IsSameDay 判断是否为同一天（按日期，不含时刻）。
func IsSameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// IsToday 判断是否为今天（本地时区）。
func IsToday(t time.Time) bool { return IsSameDay(t, time.Now()) }

// IsYesterday 判断是否为昨天（本地时区）。
func IsYesterday(t time.Time) bool { return IsSameDay(t, time.Now().AddDate(0, 0, -1)) }

// IsTomorrow 判断是否为明天（本地时区）。
func IsTomorrow(t time.Time) bool { return IsSameDay(t, time.Now().AddDate(0, 0, 1)) }

// Between 判断 t 是否落在 [start, end] 区间内（含端点）。
func Between(t, start, end time.Time) bool {
	if end.Before(start) {
		start, end = end, start
	}
	return !t.Before(start) && !t.After(end)
}

// IsLeapYear 判断是否为闰年。
func IsLeapYear(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

// DaysInMonth 返回指定年月的天数。
func DaysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// Age 按出生日期计算周岁年龄（未满生日则减一）。
func Age(birth, now time.Time) int {
	years := now.Year() - birth.Year()
	if now.Month() < birth.Month() ||
		(now.Month() == birth.Month() && now.Day() < birth.Day()) {
		years--
	}
	if years < 0 {
		return 0
	}
	return years
}

// RangeOfDays 返回 [start, end] 之间每一天的零点（含端点，最多 10000 天）。
//
// start 晚于 end 时返回 nil。
func RangeOfDays(start, end time.Time) []time.Time {
	from, to := BeginOfDay(start), BeginOfDay(end)
	if to.Before(from) {
		return nil
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > 10000 {
		days = 10000
	}
	out := make([]time.Time, 0, days)
	for i := 0; i < days; i++ {
		out = append(out, from.AddDate(0, 0, i))
	}
	return out
}

// ---------------------------------------------------------------------------
// 解析与格式化
// ---------------------------------------------------------------------------

// Parse 依次尝试 layouts 与内置 Layouts，返回第一个成功的解析结果。
//
// 全部失败时返回带原始输入的错误。
func Parse(s string, layouts ...string) (time.Time, error) {
	str := strings.TrimSpace(s)
	if str == "" {
		return time.Time{}, errors.New("xtime: empty time string")
	}
	for _, l := range layouts {
		if l == "" {
			continue
		}
		if t, err := time.Parse(l, str); err == nil {
			return t, nil
		}
	}
	for _, l := range Layouts {
		if t, err := time.Parse(l, str); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("xtime: cannot parse %q with %d layouts", s, len(layouts)+len(Layouts))
}

// MustParse 解析失败时 panic。仅用于初始化与测试。
func MustParse(s string, layouts ...string) time.Time {
	t, err := Parse(s, layouts...)
	if err != nil {
		panic(err)
	}
	return t
}

// ParseInLocation 在指定时区按 layout 解析。
func ParseInLocation(s, layout string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.Local
	}
	return time.ParseInLocation(layout, strings.TrimSpace(s), loc)
}

// ParseDate 只解析日期（不含时间），返回当地零点。
func ParseDate(s string) (time.Time, error) {
	str := strings.TrimSpace(s)
	for _, l := range []string{LayoutDate, LayoutDateSlash, LayoutCompactDate, "2006-1-2"} {
		if t, err := time.ParseInLocation(l, str, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("xtime: cannot parse date %q", s)
}

// Format 按 layout 格式化；layout 省略时使用 LayoutDateTime。
func Format(t time.Time, layout ...string) string {
	l := LayoutDateTime
	if len(layout) > 0 && layout[0] != "" {
		l = layout[0]
	}
	return t.Format(l)
}

// FormatDate 按 "2006-01-02" 格式化。
func FormatDate(t time.Time) string { return t.Format(LayoutDate) }

// FormatMillis 格式化为带毫秒的字符串。
func FormatMillis(t time.Time) string { return t.Format(LayoutDateTimeMs) }

// FormatCompact 格式化为 "20060102150405"。
func FormatCompact(t time.Time) string { return t.Format(LayoutCompact) }

// FormatCN 格式化为中文日期时间。
func FormatCN(t time.Time) string { return t.Format(LayoutCNTime) }

// ToTimestamp 返回 Unix 秒时间戳。
func ToTimestamp(t time.Time) int64 { return t.Unix() }

// ToTimestampMilli 返回 Unix 毫秒时间戳。
func ToTimestampMilli(t time.Time) int64 { return t.UnixMilli() }

// FromTimestamp 由 Unix 秒时间戳构造本地时间。
func FromTimestamp(sec int64) time.Time { return time.Unix(sec, 0) }

// FromTimestampMilli 由 Unix 毫秒时间戳构造本地时间。
func FromTimestampMilli(ms int64) time.Time { return time.UnixMilli(ms) }

// FromTimestampNano 由 Unix 纳秒时间戳构造本地时间。
func FromTimestampNano(ns int64) time.Time { return time.Unix(0, ns) }

// AutoFromTimestamp 按数值量级自动识别秒 / 毫秒 / 微秒 / 纳秒。
//
// 0 返回零值时间。
func AutoFromTimestamp(n int64) time.Time {
	switch {
	case n == 0:
		return time.Time{}
	case n > 1e17:
		return time.Unix(0, n)
	case n > 1e14:
		return time.Unix(0, n*1e3)
	case n > 1e11:
		return time.Unix(0, n*1e6)
	default:
		return time.Unix(n, 0)
	}
}

// Timezone 按名称加载时区；name 为 "Local" / "UTC" 或 IANA 名称。
func Timezone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// ---------------------------------------------------------------------------
// 人类可读文案
// ---------------------------------------------------------------------------

// HumanizeDuration 返回紧凑的时长文案，省略为零的尾部单位。
//
//	90 * time.Second      → "1m30s"
//	2*time.Hour + 30*time.Minute → "2h30m"
//	26 * time.Hour        → "1d2h"
//	500 * time.Millisecond→ "500ms"
func HumanizeDuration(d time.Duration) string {
	neg := d < 0
	if neg {
		d = -d
	}
	s := humanizeDurationPositive(d)
	if neg {
		return "-" + s
	}
	return s
}

func humanizeDurationPositive(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d < time.Second:
		return d.String()
	case d < time.Minute:
		sec := d.Seconds()
		if sec == float64(int64(sec)) {
			return fmt.Sprintf("%ds", int64(sec))
		}
		return fmt.Sprintf("%.1fs", sec)
	case d < time.Hour:
		total := int64(d / time.Second)
		m, s := total/60, total%60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	case d < 24*time.Hour:
		total := int64(d / time.Second)
		h, m := total/3600, (total%3600)/60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		total := int64(d / time.Second)
		days, h := total/86400, (total%86400)/3600
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// HumanizeAgo 把「已经过去多久」转成可读文案。
//
//	5s    → "just now"
//	30s   → "30s ago"
//	5m    → "5m ago"
//	3h    → "3h ago"
//	2d    → "2d ago"
//	90d   → "3mo ago"
//	400d  → "1y ago"
//	-5m   → "in 5m"（表示未来）
//
// 输出为语言中性的紧凑格式；面向 UI 的中文文案请在此基础上本地化。
func HumanizeAgo(d time.Duration) string {
	if d < 30*time.Second && d > -30*time.Second {
		return "just now"
	}
	if d < 0 {
		return "in " + humanizeAgoUnit(-d)
	}
	return humanizeAgoUnit(d) + " ago"
}

// humanizeAgoUnit 返回不带 "ago" 后缀的时长片段。
func humanizeAgoUnit(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int64(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int64(d/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int64(d/(365*24*time.Hour)))
	}
}

// HumanizeSince 返回 t 距现在多久的可读文案（t 在过去 → "3m ago"）。
func HumanizeSince(t time.Time) string { return HumanizeAgo(time.Since(t)) }

// ---------------------------------------------------------------------------
// 耗时统计
// ---------------------------------------------------------------------------

// Stopwatch 是简单的分段计时器。
//
// 非并发安全，每个 goroutine 应使用独立实例。
type Stopwatch struct {
	start time.Time
	last  time.Time
}

// NewStopwatch 创建并启动计时器。
func NewStopwatch() *Stopwatch {
	now := time.Now()
	return &Stopwatch{start: now, last: now}
}

// Lap 返回距上次 Lap（或创建）的时长。
func (s *Stopwatch) Lap() time.Duration {
	if s == nil {
		return 0
	}
	now := time.Now()
	d := now.Sub(s.last)
	s.last = now
	return d
}

// Elapsed 返回自创建（或最近一次 Reset）以来的总时长。
func (s *Stopwatch) Elapsed() time.Duration {
	if s == nil {
		return 0
	}
	return time.Since(s.start)
}

// Reset 重新开始计时。
func (s *Stopwatch) Reset() {
	if s == nil {
		return
	}
	now := time.Now()
	s.start, s.last = now, now
}
