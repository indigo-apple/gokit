package xstr_test

import (
	"strings"
	"testing"

	"github.com/indigo-apple/gokit/xstr"
)

func eq(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func eqSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 单词切分（命名风格转换的地基）
// ---------------------------------------------------------------------------

func TestSplitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"user", []string{"user"}},
		{"user_name", []string{"user", "name"}},
		{"userName", []string{"user", "Name"}},
		{"HTTPServer", []string{"HTTP", "Server"}},
		{"getHTTPResponseCode", []string{"get", "HTTP", "Response", "Code"}},
		{"user-id-2", []string{"user", "id", "2"}},
		{"ID", []string{"ID"}},
		{"version2", []string{"version", "2"}},
		{"2nd", []string{"2", "nd"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{"a.B/c\\d:e", []string{"a", "B", "c", "d", "e"}},
		{"版本号2更新", []string{"版本号", "2", "更新"}},
		{"already_snake_case", []string{"already", "snake", "case"}},
		{"SCREAMING_SNAKE", []string{"SCREAMING", "SNAKE"}},
	}
	for _, c := range cases {
		eqSlice(t, "SplitWords("+c.in+")", xstr.SplitWords(c.in), c.want)
	}
}

func TestNamingStyles(t *testing.T) {
	cases := []struct {
		in                   string
		camel, lower, snake  string
		kebab, constant, dot string
	}{
		{"user_name", "UserName", "userName", "user_name", "user-name", "USER_NAME", "user.name"},
		{"HTTPServer", "HTTPServer", "httpServer", "http_server", "http-server", "HTTP_SERVER", "http.server"},
		{"user_id", "UserId", "userId", "user_id", "user-id", "USER_ID", "user.id"},
		{"", "", "", "", "", "", ""},
		{"already", "Already", "already", "already", "already", "ALREADY", "already"},
	}
	for _, c := range cases {
		eq(t, "CamelCase("+c.in+")", xstr.CamelCase(c.in), c.camel)
		eq(t, "LowerCamel("+c.in+")", xstr.LowerCamel(c.in), c.lower)
		eq(t, "SnakeCase("+c.in+")", xstr.SnakeCase(c.in), c.snake)
		eq(t, "KebabCase("+c.in+")", xstr.KebabCase(c.in), c.kebab)
		eq(t, "ConstantCase("+c.in+")", xstr.ConstantCase(c.in), c.constant)
		eq(t, "DotCase("+c.in+")", xstr.DotCase(c.in), c.dot)
	}
}

func TestTitleAndInitials(t *testing.T) {
	eq(t, "Title", xstr.Title("hello world"), "Hello World")
	eq(t, "Title(空)", xstr.Title(""), "")
	eq(t, "Title(已大写)", xstr.Title("Hello World"), "Hello World")
	eq(t, "Title(多空格)", xstr.Title("a  b"), "A  B")

	eq(t, "Initials", xstr.Initials("zhang san"), "ZS")
	eq(t, "Initials(单)", xstr.Initials("bob"), "B")
	eq(t, "Initials(空)", xstr.Initials(""), "")
	eq(t, "Initials(中文)", xstr.Initials("张三"), "张")
}

// ---------------------------------------------------------------------------
// 判定
// ---------------------------------------------------------------------------

func TestPredicates(t *testing.T) {
	boolCases := []struct {
		name string
		got  bool
		want bool
	}{
		{"IsBlank(\"\")", xstr.IsBlank(""), true},
		{"IsBlank(\"  \")", xstr.IsBlank("  "), true},
		{"IsBlank(\"a\")", xstr.IsBlank("a"), false},
		{"IsNotBlank(\" a \")", xstr.IsNotBlank(" a "), true},

		{"IsNumeric(\"123\")", xstr.IsNumeric("123"), true},
		{"IsNumeric(\"\")", xstr.IsNumeric(""), false},
		{"IsNumeric(\"1.2\")", xstr.IsNumeric("1.2"), false},

		{"IsInteger(\"-12\")", xstr.IsInteger("-12"), true},
		{"IsInteger(\"+12\")", xstr.IsInteger("+12"), true},
		{"IsInteger(\"-\")", xstr.IsInteger("-"), false},

		{"IsFloat(\"1.5\")", xstr.IsFloat("1.5"), true},
		{"IsFloat(\"1.5.6\")", xstr.IsFloat("1.5.6"), false},
		{"IsFloat(\".\")", xstr.IsFloat("."), false},
		{"IsFloat(\"+.\")", xstr.IsFloat("+."), false},
		{"IsFloat(\".5\")", xstr.IsFloat(".5"), true},
		{"IsFloat(\"5.\")", xstr.IsFloat("5."), true},
		{"IsFloat(\"-0.5\")", xstr.IsFloat("-0.5"), true},

		{"IsAlpha(\"abc中文\")", xstr.IsAlpha("abc中文"), true},
		{"IsAlpha(\"a1\")", xstr.IsAlpha("a1"), false},
		{"IsAlnum(\"a1中\")", xstr.IsAlnum("a1中"), true},
		{"IsAlnum(\"a-1\")", xstr.IsAlnum("a-1"), false},

		{"IsASCII(\"abc\")", xstr.IsASCII("abc"), true},
		{"IsASCII(\"中\")", xstr.IsASCII("中"), false},

		{"IsChinese(\"中文\")", xstr.IsChinese("中文"), true},
		{"IsChinese(\"中a\")", xstr.IsChinese("中a"), false},
		{"IsChinese(\"\")", xstr.IsChinese(""), false},

		{"HasPrefixAny", xstr.HasPrefixAny("hello", "x", "he"), true},
		{"HasPrefixAny(无)", xstr.HasPrefixAny("hello", "x", "y"), false},
		{"HasSuffixAny", xstr.HasSuffixAny("hello.go", ".txt", ".go"), true},
		{"HasPrefixAnyFold", xstr.HasPrefixAnyFold("Hello", "he"), true},
		{"ContainsAny", xstr.ContainsAny("abc", "x", "b"), true},
		{"ContainsAll", xstr.ContainsAll("abc", "a", "c"), true},
		{"ContainsAll(缺)", xstr.ContainsAll("abc", "a", "z"), false},
		{"ContainsFold", xstr.ContainsFold("Hello", "ELL"), true},
	}
	for _, c := range boolCases {
		if c.got != c.want {
			t.Fatalf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 处理
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	cases := []struct {
		s      string
		max    int
		suffix []string
		want   string
	}{
		{"hello world", 8, nil, "hello..."},
		{"hello", 10, nil, "hello"},
		{"hello", 5, nil, "hello"},
		{"hello", 3, nil, "hel"},
		{"hello", 2, nil, "he"},
		{"hello", 0, nil, ""},
		{"hello", -1, nil, ""},
		{"hello world", 8, []string{"…"}, "hello w…"},
		{"hello", 3, []string{""}, "hel"},
		{"中文测试内容", 4, nil, "中..."},
		{"中文测试内容", 5, nil, "中文..."},
		{"中文测试内容更多", 6, nil, "中文测..."},
	}
	for _, c := range cases {
		got := xstr.Truncate(c.s, c.max, c.suffix...)
		if got != c.want {
			t.Fatalf("Truncate(%q,%d,%v) = %q, want %q", c.s, c.max, c.suffix, got, c.want)
		}
	}
}

func TestTruncateBytesNotSplitRune(t *testing.T) {
	// "中" 占 3 字节，截到 5 字节必须回退到 3 字节边界
	got := xstr.TruncateBytes("中文测试", 5)
	if got != "中" {
		t.Fatalf("TruncateBytes = %q, want 中", got)
	}
	if xstr.TruncateBytes("abc", 10) != "abc" {
		t.Fatal("未超出时应原样返回")
	}
	if xstr.TruncateBytes("abc", 0) != "" {
		t.Fatal("maxBytes<=0 应返回空串")
	}
	if xstr.TruncateBytes("abc", 3) != "abc" {
		t.Fatal("正好等于长度应原样返回")
	}
	// 结果必须始终是合法 UTF-8
	for n := 0; n <= 12; n++ {
		if out := xstr.TruncateBytes("中文测试", n); !isValidUTF8(out) {
			t.Fatalf("TruncateBytes(%d) = %q 不是合法 UTF-8", n, out)
		}
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestPadAndRepeat(t *testing.T) {
	eq(t, "PadLeft", xstr.PadLeft("5", 3, '0'), "005")
	eq(t, "PadLeft(超长)", xstr.PadLeft("12345", 3, '0'), "12345")
	eq(t, "PadRight", xstr.PadRight("5", 3, '-'), "5--")
	eq(t, "Center(偶)", xstr.Center("ab", 6, '-'), "--ab--")
	eq(t, "Center(奇)", xstr.Center("ab", 5, '-'), "-ab--")
	eq(t, "Center(超长)", xstr.Center("abcdef", 3, '-'), "abcdef")
	eq(t, "Repeat", xstr.Repeat("ab", 3), "ababab")
	eq(t, "Repeat(0)", xstr.Repeat("ab", 0), "")
	eq(t, "Repeat(负)", xstr.Repeat("ab", -1), "")
	eq(t, "Repeat(空)", xstr.Repeat("", 3), "")
}

func TestMiscHelpers(t *testing.T) {
	eq(t, "Trim", xstr.Trim("##a##", "#"), "a")
	eq(t, "Trim(空 cutset 走 TrimSpace)", xstr.Trim("  a  ", ""), "a")
	eq(t, "Default", xstr.Default("  ", "d"), "d")
	eq(t, "Default(有值)", xstr.Default("v", "d"), "v")

	// Reverse 必须 rune 安全
	eq(t, "Reverse(ASCII)", xstr.Reverse("abc"), "cba")
	eq(t, "Reverse(中文)", xstr.Reverse("abc中文"), "文中cba")
	eq(t, "Reverse(空)", xstr.Reverse(""), "")

	if xstr.Count("banana", "an") != 2 {
		t.Fatal("Count 错误")
	}
	if xstr.RuneLen("中a") != 2 {
		t.Fatal("RuneLen 错误")
	}
	if xstr.ByteLen("中a") != 4 {
		t.Fatal("ByteLen 错误")
	}

	eqSlice(t, "SplitAny", xstr.SplitAny("a,b;c", ",;"), []string{"a", "b", "c"})
	eqSlice(t, "SplitAny(空)", xstr.SplitAny("", ","), nil)
	eqSlice(t, "SplitAny(无分隔符集)", xstr.SplitAny("abc", ""), []string{"abc"})
	eqSlice(t, "SplitAndTrim", xstr.SplitAndTrim(" a , , b ", ","), []string{"a", "b"})
	eqSlice(t, "SplitAndTrim(空)", xstr.SplitAndTrim("", ","), nil)
	eqSlice(t, "Words", xstr.Words("hello, world! 你好"), []string{"hello", "world", "你好"})
	eqSlice(t, "Words(空)", xstr.Words(""), nil)
}

func TestSubstitute(t *testing.T) {
	vars := map[string]string{"name": "bob", "age": "3"}
	eq(t, "Substitute(${})", xstr.Substitute("hi ${name}, age ${age}", vars), "hi bob, age 3")
	eq(t, "Substitute({{}})", xstr.Substitute("hi {{name}}", vars), "hi bob")
	eq(t, "Substitute(未命中保留原样)", xstr.Substitute("hi ${missing}", vars), "hi ${missing}")
	eq(t, "Substitute(混排)", xstr.Substitute("${name}:{{age}}", vars), "bob:3")
	eq(t, "Substitute(空变量)", xstr.Substitute("x", nil), "x")
	eq(t, "Substitute(空模板)", xstr.Substitute("", vars), "")
	// 未闭合的占位符原样保留
	eq(t, "Substitute(未闭合)", xstr.Substitute("${name", vars), "${name")
}

func TestSnippet(t *testing.T) {
	s := "the quick brown fox jumps over the lazy dog"
	if got := xstr.Snippet(s, "brown", 6); got != "...quick brown fox j..." {
		t.Fatalf("Snippet = %q", got)
	}
	if got := xstr.Snippet(s, "the", 0); got != "the..." {
		t.Fatalf("Snippet(radius=0) = %q", got)
	}
	if got := xstr.Snippet(s, "dog", 5); got != "...lazy dog" {
		t.Fatalf("Snippet(尾部) = %q", got)
	}
	if got := xstr.Snippet(s, "missing", 5); got != "" {
		t.Fatalf("Snippet(未命中) = %q", got)
	}
	if got := xstr.Snippet("", "a", 3); got != "" {
		t.Fatalf("Snippet(空串) = %q", got)
	}
	if got := xstr.Snippet(s, "", 3); got != "" {
		t.Fatalf("Snippet(空关键词) = %q", got)
	}
	// 大小写不敏感
	if got := xstr.Snippet(s, "BROWN", 4); !strings.Contains(got, "brown") {
		t.Fatalf("Snippet(忽略大小写) = %q", got)
	}
	// 多字节安全
	got := xstr.Snippet("这是一段中文测试文本", "中文", 2)
	if got != "...一段中文测试..." {
		t.Fatalf("Snippet(中文) = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 脱敏
// ---------------------------------------------------------------------------

func TestMask(t *testing.T) {
	cases := []struct {
		name      string
		got, want string
	}{
		{"Mask", xstr.Mask("1234567890", 3, 2), "123*****90"},
		{"Mask(自定义字符)", xstr.Mask("1234567890", 3, 2, '#'), "123#####90"},
		{"Mask(全部遮蔽)", xstr.Mask("1234", 0, 0), "****"},
		{"Mask(过长保留)", xstr.Mask("abc", 5, 5), "abc"},
		{"Mask(空)", xstr.Mask("", 1, 1), ""},
		{"Mask(负参数)", xstr.Mask("abcd", -1, -1), "****"},

		{"MaskPhone", xstr.MaskPhone("13812345678"), "138****5678"},
		{"MaskPhone(带分隔符)", xstr.MaskPhone("138 1234 5678"), "138****5678"},
		{"MaskPhone(带短横)", xstr.MaskPhone("138-1234-5678"), "138****5678"},
		{"MaskPhone(过短)", xstr.MaskPhone("123"), "1**"},
		{"MaskPhone(空)", xstr.MaskPhone(""), ""},

		{"MaskEmail", xstr.MaskEmail("zhangsanfeng@x.com"), "zh**********@x.com"},
		{"MaskEmail(短本地)", xstr.MaskEmail("ab@x.com"), "a*@x.com"},
		{"MaskEmail(单词)", xstr.MaskEmail("a@x.com"), "a@x.com"},
		{"MaskEmail(无@)", xstr.MaskEmail("noatsign"), "n*******"},

		{"MaskIDCard", xstr.MaskIDCard("110101199001011234"), "110101********1234"},
		{"MaskBankCard", xstr.MaskBankCard("6222021234567890"), "************7890"},
		{"MaskBankCard(短)", xstr.MaskBankCard("123"), "***"},

		{"MaskName(中文2)", xstr.MaskName("张三"), "张*"},
		{"MaskName(中文3)", xstr.MaskName("张小三"), "张*三"},
		{"MaskName(中文4)", xstr.MaskName("欧阳小明"), "欧**明"},
		{"MaskName(英文)", xstr.MaskName("John"), "J***"},
		{"MaskName(单字)", xstr.MaskName("张"), "*"},
		{"MaskName(空)", xstr.MaskName(""), ""},

		{"MaskID", xstr.MaskID("abcdefgh", 2, 2), "ab****gh"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 随机串
// ---------------------------------------------------------------------------

func TestRand(t *testing.T) {
	if got := xstr.Rand(0); got != "" {
		t.Fatalf("Rand(0) = %q", got)
	}
	if got := xstr.Rand(-1); got != "" {
		t.Fatalf("Rand(-1) = %q", got)
	}
	if d := xstr.RandDigit(6); len(d) != 6 || !xstr.IsNumeric(d) {
		t.Fatalf("RandDigit = %q", d)
	}
	if h := xstr.RandHex(8); len(h) != 8 || !isHex(h) {
		t.Fatalf("RandHex = %q", h)
	}
	if a := xstr.RandAlnum(12); len(a) != 12 || !xstr.IsAlnum(a) {
		t.Fatalf("RandAlnum = %q", a)
	}
	if a := xstr.RandAlpha(5); len(a) != 5 || !xstr.IsAlpha(a) {
		t.Fatalf("RandAlpha = %q", a)
	}
	if got := xstr.Rand(10, ""); len(got) != 10 {
		t.Fatalf("Rand(空 charset 应回退默认) = %q", got)
	}
	if got := xstr.Rand(5, "a"); got != "aaaaa" {
		t.Fatalf("Rand(单字符集) = %q", got)
	}
	// 中文字符集也应可用
	if got := xstr.Rand(4, "中文"); len([]rune(got)) != 4 {
		t.Fatalf("Rand(中文 charset) = %q", got)
	}

	// 密码必须包含四类字符
	for i := 0; i < 20; i++ {
		pw := xstr.RandPassword(12)
		if len(pw) != 12 {
			t.Fatalf("RandPassword 长度 = %d", len(pw))
		}
		var hasU, hasL, hasD, hasS bool
		for _, r := range pw {
			switch {
			case r >= 'A' && r <= 'Z':
				hasU = true
			case r >= 'a' && r <= 'z':
				hasL = true
			case r >= '0' && r <= '9':
				hasD = true
			default:
				hasS = true
			}
		}
		if !hasU || !hasL || !hasD || !hasS {
			t.Fatalf("RandPassword(%q) 缺少字符类别 u=%v l=%v d=%v s=%v", pw, hasU, hasL, hasD, hasS)
		}
	}
	// 长度不足 4 时退化为通用字符集，长度仍正确
	if got := xstr.RandPassword(3); len(got) != 3 {
		t.Fatalf("RandPassword(3) = %q", got)
	}
}

func TestRandUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		s := xstr.Rand(16)
		if _, dup := seen[s]; dup {
			t.Fatalf("随机串重复: %q", s)
		}
		seen[s] = struct{}{}
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return len(s) > 0
}

// ---------------------------------------------------------------------------
// 相似度
// ---------------------------------------------------------------------------

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"中文", "中文字", 1},
		{"中文", "英文", 1}, // 中→英 一次替换
		{"中文", "中文字符", 2},
	}
	for _, c := range cases {
		if got := xstr.Levenshtein(c.a, c.b); got != c.want {
			t.Fatalf("Levenshtein(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
		// 距离应对称
		if got, want := xstr.Levenshtein(c.b, c.a), c.want; got != want {
			t.Fatalf("Levenshtein 不对称: (%q,%q) = %d, want %d", c.b, c.a, got, want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"abc", "abc", 1},
		{"", "", 1},
		{"abc", "xyz", 0},
		{"abc", "abd", 1.0 - 1.0/3.0},
		{"中文", "中文", 1},
	}
	for _, c := range cases {
		got := xstr.Similarity(c.a, c.b)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("Similarity(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 基准
// ---------------------------------------------------------------------------

func BenchmarkSplitWords(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xstr.SplitWords("getHTTPResponseCode")
	}
}

func BenchmarkSnakeCase(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xstr.SnakeCase("HTTPServerConfig")
	}
}

func BenchmarkRand16(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xstr.Rand(16)
	}
}

func BenchmarkLevenshtein(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = xstr.Levenshtein("kitten", "sitting")
	}
}
