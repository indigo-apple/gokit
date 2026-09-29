package xtest

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/indigo-apple/gokit/xstr"
)

// 本文件提供测试假数据生成器。
//
// 设计约定：
//   - 使用 math/rand 全局源（Go 1.20+ 自动随机播种），**不用于安全场景**；
//   - 输出只保证「形态正确、可读、便于定位失败」，不保证格式严格合规
//     （例如 RandPhone 不校验运营商号段归属，RandIDCard 不生成校验位）；
//   - 需要确定性时请在测试内自行固定数据，而非依赖本包的随机源。

// ---------------------------------------------------------------------------
// 字符串
// ---------------------------------------------------------------------------

// RandString 返回长度为 n 的随机字母数字串。
func RandString(n int) string { return xstr.RandAlnum(n) }

// RandDigits 返回长度为 n 的随机数字串。
func RandDigits(n int) string { return xstr.RandDigit(n) }

// RandLetters 返回长度为 n 的随机小写字母串。
func RandLetters(n int) string { return xstr.RandAlpha(n) }

// RandHexStr 返回长度为 n 的随机十六进制串。
func RandHexStr(n int) string { return xstr.RandHex(n) }

// ---------------------------------------------------------------------------
// 数值
// ---------------------------------------------------------------------------

// RandInt 返回 [min, max] 闭区间内的随机整数；min > max 时自动交换。
//
// 用无符号差值实现，因此 MinInt64 / MaxInt64 等极值输入也不会溢出。
func RandInt(min, max int) int {
	if min > max {
		min, max = max, min
	}
	if min == max {
		return min
	}
	// 无符号差值 = 跨度-1（跨符号边界也正确）
	span := uint64(max) - uint64(min)
	if span == ^uint64(0) {
		// 全范围：直接取随机 64 位
		return int(rand.Uint64())
	}
	return int(uint64(min) + rand.Uint64()%(span+1))
}

// RandIntN 返回 [0, n) 内的随机整数；n <= 0 时返回 0。
func RandIntN(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}

// RandFloat 返回 [min, max) 内的随机浮点数；min > max 时自动交换。
func RandFloat(min, max float64) float64 {
	if min > max {
		min, max = max, min
	}
	if min == max {
		return min
	}
	return rand.Float64()*(max-min) + min
}

// RandBool 返回随机布尔值。
func RandBool() bool { return rand.Intn(2) == 0 }

// ---------------------------------------------------------------------------
// 常用业务形态
// ---------------------------------------------------------------------------

// RandUUID 返回符合 v4 格式的随机 UUID 字符串（仅形态正确，非加密安全）。
func RandUUID() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RandEmail 返回随机邮箱地址。
func RandEmail() string {
	domains := []string{"example.com", "test.local", "example.org", "mail.test"}
	return fmt.Sprintf("%s.%s@%s", xstr.RandAlpha(RandInt(4, 8)), xstr.RandAlpha(RandInt(3, 6)), pickDomain(domains))
}

// pickDomain 是内部小工具（保持 RandEmail 可读）。
func pickDomain(ds []string) string { return ds[rand.Intn(len(ds))] }

// RandPhone 返回中国大陆形态的随机手机号（1[3-9] 开头共 11 位）。
func RandPhone() string {
	return fmt.Sprintf("1%d%s", 3+rand.Intn(7), xstr.RandDigit(9))
}

// RandName 返回随机的两到三字中文姓名。
func RandName() string {
	sur := []string{"张", "王", "李", "赵", "陈", "刘", "杨", "黄", "周", "吴", "徐", "孙", "马", "朱", "胡", "郭", "林", "何"}
	giv := []string{"伟", "芳", "娜", "敏", "静", "磊", "强", "洋", "艳", "勇", "军", "杰", "娟", "涛", "明", "超", "秀英", "志强", "晓明", "文博", "雨萌"}
	return sur[rand.Intn(len(sur))] + giv[rand.Intn(len(giv))]
}

// RandIPv4 返回形态正确的随机 IPv4 地址（每段 1~254）。
func RandIPv4() string {
	return fmt.Sprintf("%d.%d.%d.%d", RandInt(1, 254), RandInt(0, 255), RandInt(0, 255), RandInt(1, 254))
}

// RandTime 返回 [from, to) 内的随机时间；to <= from 时返回 from。
func RandTime(from, to time.Time) time.Time {
	if !to.After(from) {
		return from
	}
	span := to.Sub(from)
	// 半开区间 [from, to)：偏移上界不含 span 本身。
	// to.After(from) 已保证 span >= 1ns，因此 Int63n 的入参恒为正。
	off := time.Duration(rand.Int63n(int64(span)))
	return from.Add(off)
}

// RandBytes 返回长度为 n 的随机字节切片。
func RandBytes(n int) []byte {
	if n <= 0 {
		return nil
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	return b
}

// ---------------------------------------------------------------------------
// 泛型辅助
// ---------------------------------------------------------------------------

// RandSlice 生成长度为 n 的随机切片，元素由 gen 产生；gen 为 nil 或 n <= 0 时返回 nil。
func RandSlice[V any](n int, gen func() V) []V {
	if n <= 0 || gen == nil {
		return nil
	}
	out := make([]V, n)
	for i := range out {
		out[i] = gen()
	}
	return out
}

// Pick 从候选值中随机取一个；候选为空时 Fatalf（避免静默返回零值掩盖错误）。
func Pick[V any](t TestingT, vs ...V) V {
	t.Helper()
	var zero V
	if len(vs) == 0 {
		t.Fatalf("xtest.Pick: 候选列表为空")
		return zero
	}
	return vs[rand.Intn(len(vs))]
}

// Shuffle 返回打乱顺序的新切片（不修改入参）；入参为 nil 时返回 nil。
func Shuffle[V any](vs []V) []V {
	if len(vs) == 0 {
		return nil
	}
	out := make([]V, len(vs))
	copy(out, vs)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// RandMap 生成长度为 n 的随机 map，键值分别由 genK / genV 产生。
//
// 生成的键可能重复（重复时后者覆盖前者），因此实际长度可能小于 n；
// 需要精确长度时请自行生成唯一键（如 RandUUID）。
func RandMap[K comparable, V any](n int, genK func() K, genV func() V) map[K]V {
	if n <= 0 || genK == nil || genV == nil {
		return nil
	}
	m := make(map[K]V, n)
	for i := 0; i < n; i++ {
		m[genK()] = genV()
	}
	return m
}
