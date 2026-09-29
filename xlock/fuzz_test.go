package xlock_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xlock"
)

// 模糊测试：任意的超时/取消时机下，「获取锁」要么真的拿到锁，要么干净失败。
//
// 校验的不变式：
//  1. 成功返回时必须真的持有锁（不能被别的 TryLock 拿走）；
//  2. 失败返回时不得泄漏锁，也不得在锁表里留下条目；
//  3. 无论走哪条路径，最终锁必须可用、条目必须归零。
//
// 这类不变式用固定用例很难覆盖全，因为「唤醒信号与 ctx 超时同时到达」的分支
// 取决于调度时序；用随机超时反复命中是最有效的验证手段。
func FuzzLockCtxNoLeak(f *testing.F) {
	f.Add(0)
	f.Add(1)
	f.Add(-5)
	f.Add(100)
	f.Add(3)
	f.Add(-1<<31 + 1)

	f.Fuzz(func(t *testing.T, ms int) {
		// 把输入映射到一个很小的等待时长，保证测试快速
		if ms < 0 {
			ms = -ms
		}
		wait := time.Duration(ms%15) * time.Millisecond

		l := xlock.NewKeyed()
		holder, err := l.Lock("k")
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if l.ActiveKeys() != 1 {
			t.Fatalf("持有时 ActiveKeys = %d", l.ActiveKeys())
		}

		ctx, cancel := context.WithTimeout(context.Background(), wait)
		un, lerr := l.LockCtx(ctx, "k")
		cancel()

		if lerr == nil {
			// 拿到了锁：必须真的互斥
			if un == nil {
				t.Fatal("成功但返回 nil 释放函数")
			}
			if _, ok := l.TryLock("k"); ok {
				t.Fatal("已成功获取，但 TryLock 仍能拿到 —— 互斥被破坏")
			}
			if err := un(); err != nil {
				t.Fatalf("释放出错: %v", err)
			}
		} else {
			if un != nil {
				t.Fatal("失败却返回了非 nil 释放函数")
			}
			// 失败路径不得留下等待位
			if _, _, waiting, _ := l.EntryState("k"); waiting != 0 {
				t.Fatalf("失败后仍有 %d 个等待写者", waiting)
			}
		}

		// 持有者释放后，锁必须可用于他人
		holder()
		un2, ok := l.TryLock("k")
		if !ok {
			t.Fatal("持有者释放后锁不可用（锁泄漏）")
		}
		un2()

		if n := l.ActiveKeys(); n != 0 {
			t.Fatalf("结束后残留 %d 个条目", n)
		}
	})
}

// 模糊测试：LockAll 的「去重 + 排序」不变式。
//
// 无论传入多少重复/乱序的 key，都必须全部拿到；否则说明排序或去重有误，
// 而排序正是防死锁的全部依据。
func FuzzLockAllKeys(f *testing.F) {
	f.Add("a", "b", "c")
	f.Add("", "", "")
	f.Add("z", "a", "m")
	f.Add("dup", "dup", "dup")

	f.Fuzz(func(t *testing.T, k1, k2, k3 string) {
		l := xlock.NewKeyed()
		keys := []string{k1, k2, k3, k1, k2}

		want := map[string]bool{}
		for _, k := range keys {
			want[k] = true
		}

		un, err := xlock.LockAll(context.Background(), l, keys...)
		if err != nil {
			t.Fatalf("LockAll: %v", err)
		}
		if got := l.ActiveKeys(); got != len(want) {
			t.Fatalf("持锁数 = %d, want %d（去重失效）", got, len(want))
		}
		for k := range want {
			if _, ok := l.TryLock(k); ok {
				t.Fatalf("key %q 应已被持有", k)
			}
		}
		if err := un(); err != nil {
			t.Fatalf("释放: %v", err)
		}
		if n := l.ActiveKeys(); n != 0 {
			t.Fatalf("释放后残留 %d 个条目", n)
		}
	})
}

// 模糊测试：分片取值必须在 [0, shards) 内，且对同一输入可复现。
func FuzzShardOf(f *testing.F) {
	f.Add("order:123", 32)
	f.Add("", 1)
	f.Add("x", -1)

	f.Fuzz(func(t *testing.T, key string, shards int) {
		n := shards
		if n <= 0 {
			n = 1
		}
		got := xlock.ShardOf(key, shards)
		if got < 0 || got >= n {
			t.Fatalf("ShardOf(%q, %d) = %d 越界（有效分片数 %d）", key, shards, got, n)
		}
		if again := xlock.ShardOf(key, shards); again != got {
			t.Fatalf("ShardOf 不可复现: %d vs %d", got, again)
		}
		if xlock.HashKey(key) != xlock.HashKey(key) {
			t.Fatal("HashKey 不可复现")
		}
	})
}
