// Package xstr 提供字符串处理工具：命名风格转换、判定、截断、填充、脱敏、随机串、相似度。
//
// 设计要点：
//   - **所有长度相关的操作都是 rune 安全的**（Truncate / Reverse / PadLeft …），
//     不会把中文或 emoji 截成半个字符；需要按字节切的场景显式使用 TruncateBytes；
//   - **随机串一律基于 crypto/rand**（不用 math/rand 做业务随机，
//     避免可预测的 token / 验证码）；crypto/rand 不可用时降级而非 panic；
//   - 命名风格转换会把 "HTTPServer" 正确切成 ["HTTP","Server"]，
//     而不是 ["H","T","T","P","Server"]；
//   - 空字符串 / nil 语义统一：判定类返回 false，变换类返回空串。
//
// 并发安全：本包全部为纯函数，可并发调用。
package xstr

import (
	crand "crypto/rand"
	"math/big"
	mrand "math/rand"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// 命名风格
// ---------------------------------------------------------------------------

// isSep 判断是否为单词分隔符。
func isSep(r rune) bool {
	switch r {
	case '_', '-', ' ', '.', '/', '\\', '\t', '\n', '\r', ':':
		return true
	}
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// SplitWords 把任意命名风格的字符串切分成单词。
//
//	"user_name"        → ["user","name"]
//	"userName"         → ["user","Name"]
//	"HTTPServer"       → ["HTTP","Server"]
//	"user-id-2"        → ["user","id","2"]
//	"版本号2更新"       → ["版本号","2","更新"]
func SplitWords(s string) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	words := make([]string, 0, 4)
	cur := make([]rune, 0, 8)

	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if isSep(r) {
			flush()
			continue
		}
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			curUpper := unicode.IsUpper(prev)
			curLower := unicode.IsLower(prev)
			curDigit := unicode.IsDigit(prev)

			switch {
			case unicode.IsUpper(r):
				// 小写/数字 → 大写：新词开始
				if curLower || curDigit {
					flush()
				} else if curUpper && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					// 连续大写后紧跟小写：缩写边界，如 HTTPServer → HTTP | Server
					flush()
				}
			case unicode.IsDigit(r) && !curDigit:
				// 字母 → 数字
				flush()
			case !unicode.IsDigit(r) && curDigit:
				// 数字 → 字母
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

// CamelCase 转为大驼峰："user_name" → "UserName"。
func CamelCase(s string) string { return joinWords(s, "", true) }

// LowerCamel 转为小驼峰："user_name" → "userName"。
func LowerCamel(s string) string {
	words := SplitWords(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	for i, w := range words {
		if i == 0 {
			b.WriteString(strings.ToLower(w))
			continue
		}
		b.WriteString(capitalize(w))
	}
	return b.String()
}

// SnakeCase 转为下划线小写："HTTPServer" → "http_server"。
func SnakeCase(s string) string { return joinWords(s, "_", false) }

// KebabCase 转为短横线小写："HTTPServer" → "http-server"。
func KebabCase(s string) string { return joinWords(s, "-", false) }

// ConstantCase 转为大写下划线："httpServer" → "HTTP_SERVER"。
func ConstantCase(s string) string {
	words := SplitWords(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w)
	}
	return strings.Join(words, "_")
}

// DotCase 转为点分小写："HTTPServer" → "http.server"。
func DotCase(s string) string { return joinWords(s, ".", false) }

func joinWords(s, sep string, title bool) string {
	words := SplitWords(s)
	if len(words) == 0 {
		return ""
	}
	out := make([]string, len(words))
	for i, w := range words {
		if title {
			out[i] = capitalize(w)
		} else {
			out[i] = strings.ToLower(w)
		}
	}
	return strings.Join(out, sep)
}

// capitalize 首字符大写，其余保持原样（中文/数字无变化）。
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// Title 把每个空白分隔的单词首字母大写，其余保持不变。
func Title(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevIsSpace := true
	for _, r := range s {
		if prevIsSpace {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(r)
		}
		prevIsSpace = unicode.IsSpace(r)
	}
	return b.String()
}

// Initials 返回各单词首字母的大写形式："zhang san" → "ZS"。
func Initials(s string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	for _, w := range words {
		r, _ := utf8.DecodeRuneInString(w)
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 判定
// ---------------------------------------------------------------------------

// IsBlank 判断是否为空或仅含空白字符。
func IsBlank(s string) bool { return strings.TrimSpace(s) == "" }

// IsNotBlank 判断是否含有非空白字符。
func IsNotBlank(s string) bool { return !IsBlank(s) }

// IsNumeric 判断是否全部为数字（不接受正负号与小数点）。
func IsNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// IsInteger 判断是否为可选符号的整数。
func IsInteger(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	return IsNumeric(s)
}

// IsFloat 判断是否为可选符号的浮点数（不接受科学计数法）。
//
// 要求至少含一个数字，因此 "." / "+." / "-" 均为 false。
func IsFloat(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	digits, dot := 0, 0
	for _, r := range s {
		if r == '.' {
			dot++
			if dot > 1 {
				return false
			}
			continue
		}
		if !unicode.IsDigit(r) {
			return false
		}
		digits++
	}
	return digits > 0
}

// IsAlpha 判断是否全部为字母（支持 Unicode，含中文）。
func IsAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// IsAlnum 判断是否全部为字母或数字。
func IsAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// IsASCII 判断是否全部为 ASCII 字符。
func IsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// IsChinese 判断是否全部为中日韩统一表意文字。
func IsChinese(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

// HasPrefixAny 判断是否以任一前缀开头。
func HasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// HasSuffixAny 判断是否以任一后缀结尾。
func HasSuffixAny(s string, suffixes ...string) bool {
	for _, p := range suffixes {
		if p != "" && strings.HasSuffix(s, p) {
			return true
		}
	}
	return false
}

// HasPrefixAnyFold 判断是否以任一前缀开头（忽略大小写）。
func HasPrefixAnyFold(s string, prefixes ...string) bool {
	ls := strings.ToLower(s)
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(ls, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// ContainsAny 判断是否包含任一子串。
func ContainsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ContainsAll 判断是否包含全部子串。
func ContainsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// ContainsFold 忽略大小写判断是否包含子串。
func ContainsFold(s, sub string) bool {
	if sub == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// ---------------------------------------------------------------------------
// 处理
// ---------------------------------------------------------------------------

// Trim 同时去掉首尾的多种字符（按 rune 集合处理，而非整串前缀）。
func Trim(s string, cutset string) string {
	if cutset == "" {
		return strings.TrimSpace(s)
	}
	return strings.Trim(s, cutset)
}

// Default 在 s 为空白时返回 def。
func Default(s, def string) string {
	if IsBlank(s) {
		return def
	}
	return s
}

// Truncate 截断到最多 maxRunes 个字符（含后缀），超出时追加后缀。
//
//	maxRunes <= 0 时返回空串；s 未超出时原样返回（不追加后缀）。
//	suffix 省略时默认 "..."。
func Truncate(s string, maxRunes int, suffix ...string) string {
	suf := "..."
	if len(suffix) > 0 {
		suf = suffix[0]
	}
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	keep := maxRunes - len([]rune(suf))
	if keep <= 0 {
		return string(r[:maxRunes])
	}
	return string(r[:keep]) + suf
}

// TruncateBytes 按字节截断，并回退到合法的 UTF-8 边界，绝不产生半个字符。
func TruncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	b := []byte(s)[:maxBytes]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}

// PadLeft 左填充到 n 个字符（按 rune 计）。
func PadLeft(s string, n int, pad rune) string { return padTo(s, n, pad, true) }

// PadRight 右填充到 n 个字符。
func PadRight(s string, n int, pad rune) string { return padTo(s, n, pad, false) }

// Center 两侧填充到 n 个字符；左侧少一个时优先右侧。
func Center(s string, n int, pad rune) string {
	cur := utf8.RuneCountInString(s)
	if cur >= n {
		return s
	}
	total := n - cur
	left := total / 2
	right := total - left
	return strings.Repeat(string(pad), left) + s + strings.Repeat(string(pad), right)
}

func padTo(s string, n int, pad rune, left bool) string {
	cur := utf8.RuneCountInString(s)
	if cur >= n {
		return s
	}
	fill := strings.Repeat(string(pad), n-cur)
	if left {
		return fill + s
	}
	return s + fill
}

// Repeat 重复 s 共 n 次；n <= 0 返回空串。
func Repeat(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	return strings.Repeat(s, n)
}

// Reverse 反转字符串（rune 安全，"abc中文" → "文中cba"）。
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// Count 统计子串出现次数（不重叠）。
func Count(s, sub string) int { return strings.Count(s, sub) }

// ByteLen 返回字节长度。
func ByteLen(s string) int { return len(s) }

// RuneLen 返回字符（rune）数量。
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

// SplitAny 按任意分隔符集合切分，丢弃空段。
func SplitAny(s, seps string) []string {
	if s == "" {
		return nil
	}
	if seps == "" {
		return []string{s}
	}
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) })
}

// SplitAndTrim 按 sep 切分，逐段 TrimSpace，并丢弃空段。
func SplitAndTrim(s, sep string) []string {
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

// Words 按非字母数字字符切分，保留中文等 Unicode 字母。
func Words(s string) []string {
	if s == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Substitute 用 vars 替换模板中的 ${key} 与 {{key}}。
//
// 未出现在 vars 中的占位符保持原样，便于排查遗漏。
func Substitute(tpl string, vars map[string]string) string {
	if tpl == "" || len(vars) == 0 {
		return tpl
	}
	var b strings.Builder
	b.Grow(len(tpl))
	for i := 0; i < len(tpl); {
		// ${key}
		if tpl[i] == '$' && i+1 < len(tpl) && tpl[i+1] == '{' {
			if end := strings.IndexByte(tpl[i+2:], '}'); end >= 0 {
				key := tpl[i+2 : i+2+end]
				if v, ok := vars[key]; ok {
					b.WriteString(v)
					i += 2 + end + 1
					continue
				}
			}
		}
		// {{key}}
		if i+1 < len(tpl) && tpl[i] == '{' && tpl[i+1] == '{' {
			if end := strings.Index(tpl[i+2:], "}}"); end >= 0 {
				key := tpl[i+2 : i+2+end]
				if v, ok := vars[key]; ok {
					b.WriteString(v)
					i += 2 + end + 2
					continue
				}
			}
		}
		b.WriteByte(tpl[i])
		i++
	}
	return b.String()
}

// Snippet 在 s 中定位 kw（忽略大小写），返回带上下文的摘要片段。
//
// 命中处两侧各保留 radius 个字符，被裁掉的一侧追加 "..."；未命中返回空串。
// 位置计算基于 rune，因此不会切断多字节字符。
func Snippet(s, kw string, radius int) string {
	if s == "" || kw == "" {
		return ""
	}
	byteIdx := strings.Index(strings.ToLower(s), strings.ToLower(kw))
	if byteIdx < 0 {
		return ""
	}
	if radius < 0 {
		radius = 0
	}
	startRune := utf8.RuneCountInString(s[:byteIdx])
	kwRunes := utf8.RuneCountInString(kw)
	total := utf8.RuneCountInString(s)

	from := startRune - radius
	if from < 0 {
		from = 0
	}
	to := startRune + kwRunes + radius
	if to > total {
		to = total
	}

	r := []rune(s)
	var b strings.Builder
	if from > 0 {
		b.WriteString("...")
	}
	b.WriteString(string(r[from:to]))
	if to < total {
		b.WriteString("...")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 脱敏
// ---------------------------------------------------------------------------

// Mask 保留首尾各 keepHead / keepTail 个字符，中间用 maskChar 替换。
//
// 若字符串过短无法同时保留首尾，则只保留首部。
func Mask(s string, keepHead, keepTail int, maskChar ...rune) string {
	mc := '*'
	if len(maskChar) > 0 {
		mc = maskChar[0]
	}
	r := []rune(s)
	n := len(r)
	if n == 0 {
		return ""
	}
	if keepHead < 0 {
		keepHead = 0
	}
	if keepTail < 0 {
		keepTail = 0
	}
	if keepHead+keepTail >= n {
		// 无法遮蔽，退化为只保留可保留的部分
		if keepHead >= n {
			return s
		}
		return string(r[:keepHead]) + strings.Repeat(string(mc), n-keepHead)
	}
	return string(r[:keepHead]) + strings.Repeat(string(mc), n-keepHead-keepTail) + string(r[n-keepTail:])
}

// MaskPhone 手机号脱敏：13812345678 → 138****5678（兼容 +86 前缀与分隔符）。
func MaskPhone(s string) string {
	digits := keepDigits(s)
	if len(digits) == 0 {
		return s
	}
	if len(digits) < 7 {
		return Mask(digits, 1, 0)
	}
	if len(digits) >= 11 {
		return digits[:3] + strings.Repeat("*", len(digits)-7) + digits[len(digits)-4:]
	}
	return digits[:2] + strings.Repeat("*", len(digits)-5) + digits[len(digits)-3:]
}

// MaskEmail 邮箱脱敏：zhangwenxing@x.com → zh*******@x.com。
//
// 本地部分保留前 2 位（不足 2 位保留 1 位），域名保持原样。
func MaskEmail(s string) string {
	at := strings.LastIndexByte(s, '@')
	if at <= 0 {
		return Mask(s, 1, 0)
	}
	local, domain := s[:at], s[at:]
	keep := 2
	if utf8.RuneCountInString(local) <= 2 {
		keep = 1
	}
	return Mask(local, keep, 0) + domain
}

// MaskIDCard 身份证脱敏：保留前 6 位与后 4 位。
func MaskIDCard(s string) string {
	return Mask(s, 6, 4)
}

// MaskBankCard 银行卡脱敏：保留后 4 位。
//
//	6222021234567890 → ************7890
func MaskBankCard(s string) string {
	digits := keepDigits(s)
	if len(digits) <= 4 {
		return strings.Repeat("*", len(digits))
	}
	return strings.Repeat("*", len(digits)-4) + digits[len(digits)-4:]
}

// MaskName 姓名脱敏：中文保留首尾（张三 → 张*，张小三 → 张*三），
// 非中文保留首字符。
func MaskName(s string) string {
	r := []rune(s)
	switch {
	case len(r) == 0:
		return ""
	case len(r) == 1:
		return "*"
	case IsChinese(s):
		if len(r) == 2 {
			return string(r[0]) + "*"
		}
		return string(r[0]) + strings.Repeat("*", len(r)-2) + string(r[len(r)-1])
	default:
		return Mask(s, 1, 0)
	}
}

// MaskID 通用的 ID 脱敏：保留前 head 位与后 tail 位。
func MaskID(s string, head, tail int) string { return Mask(s, head, tail) }

func keepDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 随机串
// ---------------------------------------------------------------------------

// 随机串字符集。
const (
	CharsetDigit   = "0123456789"
	CharsetLower   = "abcdefghijklmnopqrstuvwxyz"
	CharsetUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	CharsetAlpha   = CharsetLower + CharsetUpper
	CharsetAlnum   = CharsetDigit + CharsetAlpha
	CharsetHex     = "0123456789abcdef"
	CharsetHexUp   = "0123456789ABCDEF"
	CharsetSymbols = "!@#$%^&*()-_=+[]{}<>?,."
	CharsetAll     = CharsetAlnum + CharsetSymbols
)

// Rand 返回长度 n、字符取自 charset（默认 CharsetAlnum）的随机串。
//
// 使用 crypto/rand；不可用时降级到 math/rand（仍不 panic）。
func Rand(n int, charset ...string) string {
	if n <= 0 {
		return ""
	}
	cs := CharsetAlnum
	if len(charset) > 0 && charset[0] != "" {
		cs = charset[0]
	}
	runes := []rune(cs)
	if len(runes) == 0 {
		return ""
	}
	out := make([]rune, n)
	limit := big.NewInt(int64(len(runes)))
	for i := range out {
		idx, err := crand.Int(crand.Reader, limit)
		if err != nil {
			out[i] = runes[mrand.Intn(len(runes))]
			continue
		}
		out[i] = runes[idx.Int64()]
	}
	return string(out)
}

// RandDigit 返回 n 位随机数字串。
func RandDigit(n int) string { return Rand(n, CharsetDigit) }

// RandHex 返回 n 位小写十六进制随机串。
func RandHex(n int) string { return Rand(n, CharsetHex) }

// RandAlnum 返回 n 位随机字母数字串。
func RandAlnum(n int) string { return Rand(n, CharsetAlnum) }

// RandAlpha 返回 n 位随机字母串。
func RandAlpha(n int) string { return Rand(n, CharsetAlpha) }

// RandPassword 返回 n 位强随机密码，保证同时包含大写、小写、数字与符号。
//
// n < 4 时退化为 RandAll（无法保证四类都出现）。
func RandPassword(n int) string {
	if n < 4 {
		return Rand(n, CharsetAll)
	}
	buf := make([]rune, 0, n)
	for _, cs := range []string{CharsetUpper, CharsetLower, CharsetDigit, CharsetSymbols} {
		buf = append(buf, []rune(Rand(1, cs))...)
	}
	buf = append(buf, []rune(Rand(n-4, CharsetAll))...)
	// 洗牌，避免前 4 位固定顺序
	for i := len(buf) - 1; i > 0; i-- {
		idx, err := crand.Int(crand.Reader, big.NewInt(int64(i+1)))
		j := 0
		if err == nil {
			j = int(idx.Int64())
		} else {
			j = mrand.Intn(i + 1)
		}
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// ---------------------------------------------------------------------------
// 相似度
// ---------------------------------------------------------------------------

// Levenshtein 返回两个字符串的编辑距离（按 rune 计算）。
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

// Similarity 返回归一化相似度，取值 [0,1]（1 表示完全相同）。
//
// 两个空串视为 1。
func Similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	maxLen := utf8.RuneCountInString(a)
	if n := utf8.RuneCountInString(b); n > maxLen {
		maxLen = n
	}
	if maxLen == 0 {
		return 1
	}
	return 1 - float64(Levenshtein(a, b))/float64(maxLen)
}
