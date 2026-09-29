package xcontainer_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/indigo-apple/gokit/xcontainer"
	"github.com/indigo-apple/gokit/xerr"
)

func TestOverflowPolicyString(t *testing.T) {
	cases := []struct {
		p    xcontainer.OverflowPolicy
		want string
	}{
		{xcontainer.Block, "Block"},
		{xcontainer.DropOldest, "DropOldest"},
		{xcontainer.DropNewest, "DropNewest"},
		{xcontainer.Fail, "Fail"},
		{xcontainer.OverflowPolicy(-1), "Unknown"},
		{xcontainer.OverflowPolicy(99), "Unknown"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Fatalf("OverflowPolicy(%d).String() = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestOverflowPolicyValidAndNorm(t *testing.T) {
	valid := []xcontainer.OverflowPolicy{
		xcontainer.Block, xcontainer.DropOldest, xcontainer.DropNewest, xcontainer.Fail,
	}
	for _, p := range valid {
		if !p.Valid() {
			t.Fatalf("%v 应合法", p)
		}
		if p.Norm() != p {
			t.Fatalf("Norm 不应修改合法值 %v", p)
		}
	}
	for _, bad := range []xcontainer.OverflowPolicy{-1, 4, 1000} {
		if bad.Valid() {
			t.Fatalf("%d 应非法", bad)
		}
		if got := bad.Norm(); got != xcontainer.DropNewest {
			t.Fatalf("非法值 %d 应归一化为 DropNewest, got %v", bad, got)
		}
	}
}

// 哨兵错误必须携带业务错误码，且能被 errors.Is 识别（含包装后）。
func TestSentinelErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code xerr.Code
		text string
	}{
		{"ErrFull", xcontainer.ErrFull, xerr.CodeResourceExhausted, "full"},
		{"ErrWouldBlock", xcontainer.ErrWouldBlock, xerr.CodeUnavailable, "would block"},
		{"ErrClosed", xcontainer.ErrClosed, xerr.CodeStateInvalid, "closed"},
		{"ErrEmpty", xcontainer.ErrEmpty, xerr.CodeNotFound, "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err == nil {
				t.Fatal("哨兵不得为 nil")
			}
			if got := xerr.CodeOf(c.err); got != c.code {
				t.Fatalf("错误码 = %v, want %v", got, c.code)
			}
			if !strings.Contains(c.err.Error(), c.text) {
				t.Fatalf("文案 %q 不含 %q", c.err.Error(), c.text)
			}
			if !strings.Contains(c.err.Error(), "xcontainer") {
				t.Fatalf("文案应带包名前缀: %q", c.err.Error())
			}
			// 自身可识别
			if !errors.Is(c.err, c.err) {
				t.Fatal("errors.Is 应识别自身")
			}
			// 包装后仍可识别
			wrapped := fmt.Errorf("上层上下文: %w", c.err)
			if !errors.Is(wrapped, c.err) {
				t.Fatal("包装后 errors.Is 应仍可识别")
			}
			if got := xerr.CodeOf(wrapped); got != c.code {
				t.Fatalf("包装后错误码 = %v, want %v", got, c.code)
			}
		})
	}

	// 哨兵互不相等
	all := []error{xcontainer.ErrFull, xcontainer.ErrWouldBlock, xcontainer.ErrClosed, xcontainer.ErrEmpty}
	for i := range all {
		for j := range all {
			if i != j && errors.Is(all[i], all[j]) {
				t.Fatalf("%v 与 %v 不应相等", all[i], all[j])
			}
		}
	}
}

// 策略数序必须稳定：Block 是零值，便于「未指定策略」时取到阻塞语义。
func TestPolicyZeroValue(t *testing.T) {
	var p xcontainer.OverflowPolicy
	if p != xcontainer.Block {
		t.Fatalf("零值策略应为 Block, got %v", p)
	}
	if p.String() != "Block" {
		t.Fatalf("零值 String = %q", p.String())
	}
}
