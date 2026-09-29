package xdistlock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xdistlock"
	"github.com/zhangwenxing/gokit/xdistlock/memory"
)

// 模糊测试：随机组合「租约 / 等待超时 / 看门狗 / 阻塞策略」，
// 验证客户端在任意参数下都不会泄漏锁或残留等待位。
//
// 校验的不变式：
//  1. 获取失败必须是「可解释」的失败（等待超时 / ctx 结束），且**不残留任何锁**；
//  2. 获取成功后无论走哪条释放路径（正常释放 / 已过期 / 已丢锁），
//     最终后端必须回到「零把锁」；
//  3. 任何参数组合都不得 panic。
func FuzzLockLifecycleNoLeak(f *testing.F) {
	f.Add(0, 0, true)
	f.Add(5, 30, false)
	f.Add(100, 0, false)
	f.Add(-7, -3, true)

	f.Fuzz(func(t *testing.T, ttlMs, waitMs int, autoRenew bool) {
		if ttlMs < 0 {
			ttlMs = -ttlMs
		}
		if waitMs < 0 {
			waitMs = -waitMs
		}
		// 归一化到很小的值，保证 fuzz 快速
		ttl := time.Duration(ttlMs%25+1) * time.Millisecond
		wait := time.Duration(waitMs%25) * time.Millisecond

		be := memory.New()
		c := xdistlock.New(be)
		defer c.Close()
		ctx := context.Background()

		lk, err := c.Lock(ctx, "k",
			xdistlock.WithTTL(ttl),
			xdistlock.WithWaitTimeout(wait),
			xdistlock.WithAutoRenew(autoRenew, time.Millisecond),
			xdistlock.WithRetry(time.Millisecond, 0),
		)
		if err != nil {
			if !errors.Is(err, xdistlock.ErrWaitTimeout) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("意外错误: %v（ttl=%v wait=%v auto=%v）", err, ttl, wait, autoRenew)
			}
			if n := be.ActiveKeys(); n != 0 {
				t.Fatalf("获取失败后残留 %d 把锁", n)
			}
			return
		}

		if lk.FencingToken() == 0 {
			t.Fatal("fencing token 不应为 0")
		}
		if lk.Token() == "" {
			t.Fatal("token 不应为空")
		}
		if exp := lk.Expiry(); exp.IsZero() {
			t.Fatal("Expiry 不应为零值")
		}

		// 释放：允许「已过期/已丢锁」导致的失败，但不允许其它错误
		if uerr := lk.Unlock(ctx); uerr != nil {
			if !errors.Is(uerr, xdistlock.ErrLockLost) && !errors.Is(uerr, xdistlock.ErrNotHeld) {
				t.Fatalf("Unlock 意外错误: %v", uerr)
			}
		}
		if lk.IsHeld() {
			t.Fatal("Unlock 之后 IsHeld 仍为 true")
		}
		if n := be.ActiveKeys(); n != 0 {
			t.Fatalf("结束后残留 %d 把锁（ttl=%v auto=%v）", n, ttl, autoRenew)
		}
	})
}

// 模糊测试：TryLock 的语义 —— 要么拿到锁，要么返回 false；两种结果都不得残留。
func FuzzTryLockEitherOr(f *testing.F) {
	f.Add(true, "k")
	f.Add(false, "k")
	f.Add(false, "")

	f.Fuzz(func(t *testing.T, preHeld bool, key string) {
		if key == "" {
			key = "k" // 空 key 走参数校验分支，另有用例覆盖
		}
		be := memory.New()
		c := xdistlock.New(be)
		defer c.Close()
		ctx := context.Background()

		var holder *xdistlock.Lock
		if preHeld {
			h, err := c.Lock(ctx, key, xdistlock.WithTTL(30*time.Second))
			if err != nil {
				t.Fatalf("预占失败: %v", err)
			}
			holder = h
		}

		lk, ok, err := c.TryLock(ctx, key)
		switch {
		case err != nil:
			t.Fatalf("TryLock 不应返回错误: %v", err)
		case ok:
			if preHeld {
				t.Fatal("锁已被持有时 TryLock 不应成功")
			}
			if lk == nil {
				t.Fatal("成功却返回 nil 句柄")
			}
			if lk.FencingToken() == 0 {
				t.Fatal("fencing token 为 0")
			}
			if uerr := lk.Unlock(ctx); uerr != nil {
				t.Fatalf("Unlock: %v", uerr)
			}
		default:
			if lk != nil {
				t.Fatal("未获取到却返回了非 nil 句柄")
			}
			if !preHeld {
				t.Fatal("锁空闲却 TryLock 失败")
			}
		}

		if holder != nil {
			if uerr := holder.Unlock(ctx); uerr != nil {
				t.Fatalf("Unlock holder: %v", uerr)
			}
		}
		if n := be.ActiveKeys(); n != 0 {
			t.Fatalf("结束后残留 %d 把锁", n)
		}
	})
}
