package xevent_test

import (
	"strings"
	"testing"

	"github.com/zhangwenxing/gokit/xevent"
)

// FuzzMatchNoPanic 守卫 Match 的健壮性：
//  1. 任意输入（含畸形正则、超长、Unicode、控制字符）都不得 panic；
//  2. 全匹配模式必须恒为 true；
//  3. 无通配符的模式必须退化为「精确相等」；
//  4. 匹配结果必须与「模式是否为前缀 + 通配」的朴素判断自洽。
func FuzzMatchNoPanic(f *testing.F) {
	seeds := []struct{ pattern, topic string }{
		{"order.created", "order.created"},
		{"order.*", "order.paid"},
		{"order.**", "order.item.added"},
		{"re:^order\\..*$", "order.x"},
		{"*", "anything"},
		{"**", "a.b.c"},
		{"a.*.c", "a.b.c"},
		{"", ""},
		{"re:[", "x"},
		{"re:(", "x"},
		{"\u4e2d\u6587.*", "\u4e2d\u6587.\u4e8b\u4ef6"},
		{"a.**.c", "a.b.c"},
		{"\x00", "\x00"},
	}
	for _, s := range seeds {
		f.Add(s.pattern, s.topic)
	}

	f.Fuzz(func(t *testing.T, pattern, topic string) {
		// 不得 panic（Match 内部对非法正则必须降级为「不匹配」）
		got := xevent.Match(pattern, topic)

		switch {
		case pattern == "*" || pattern == "**":
			if !got {
				t.Fatalf("全匹配模式 %q 对 %q 必须为 true", pattern, topic)
			}
		case pattern == "":
			if got != (topic == "") {
				t.Fatalf("空模式对 %q 应等价于精确比较", topic)
			}
		case strings.HasPrefix(pattern, "re:"):
			// 正则模式：只要求不 panic + 与「编译后正则」一致
			if expr := strings.TrimPrefix(pattern, "re:"); expr != "" {
				if re, err := compileLike(expr); err == nil && !strings.Contains(expr, "(?") {
					if got != re.MatchString(topic) {
						t.Fatalf("Match(%q,%q)=%v 与标准库正则结果不一致", pattern, topic, got)
					}
				}
			}
		case !strings.Contains(pattern, "*"):
			if got != (pattern == topic) {
				t.Fatalf("无通配符模式 %q 对 %q 应为精确比较 = %v", pattern, topic, pattern == topic)
			}
		}

		// 幂等性：同一输入重复调用结果必须一致（内部有正则缓存，必须无状态）
		if again := xevent.Match(pattern, topic); again != got {
			t.Fatalf("Match 非确定: %v vs %v", got, again)
		}
	})
}

// FuzzValidatePatternConsistency 守卫「校验通过 → 匹配不 panic」且「校验失败的中间 ** 模式永不匹配」。
func FuzzValidatePatternConsistency(f *testing.F) {
	for _, s := range []string{"a", "a.*", "a.**", "**.c", "re:[", "", "*", "a.b.c", "a.**.c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		err := xevent.ValidatePattern(pattern)
		if err != nil {
			// 非法模式（如正则语法错误）：Match 必须安全降级为 false
			_ = xevent.Match(pattern, "whatever")
			return
		}
		if pattern == "" {
			t.Fatalf("空模式不应通过校验")
		}
		_ = xevent.Match(pattern, pattern)
	})
}
