package xlock

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/zhangwenxing/gokit/xerr"
)

// ---------------------------------------------------------------------------
// core —— 各实现共用的「获取锁 → 构造释放函数」流程
// ---------------------------------------------------------------------------

// entrySource 提供「key → 锁内核」的映射。
//
// KeyedLocker 与 ShardedLocker 的差异只在这一个方法上：
// 前者按 key 建条目（引用计数回收），后者按哈希取固定分片（永不回收）。
type entrySource interface {
	// take 取得 key 对应的锁内核，并返回一个「归还内核」的回调。
	//
	// 无论后续是否成功拿到锁，调用方都必须调用归还回调，否则会造成条目泄漏。
	take(key string) (*entry, func(), error)
}

// core 是锁实现共用的配置 + 统计 + 获取流程。
type core struct {
	cfg config
	st  stats
}

// acquire 是 Lock / LockCtx / TryLock / RLock / TryRLock 的统一实现。
func (c *core) acquire(src entrySource, ctx context.Context, key string, write, try bool) (UnlockFunc, error) {
	// nil 接收者守卫：core 是各实现的 offset 0 嵌入字段，
	// 因此 nil 接收者这里拿到的正是 nil *core，可在此一次性兜住所有公开方法。
	if c == nil || src == nil {
		return nil, ErrNilLocker
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// ctx 优先：已结束的 ctx 一律快速失败，即使当前无人持锁。
	// 这跟随 golang.org/x/sync/semaphore 等标准实现的约定：**ctx 决定这次操作还算不算数**。
	// 若不做这一步，`LockCtx(已取消)` 会走「无人竞争」快速路径直接拿到锁，
	// 调用方拿到锁却以为操作已被取消 —— 这是很容易踩的坑。
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	e, release, err := src.take(key)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	// 快速路径：Try* 不排队
	if try {
		var ok bool
		if write {
			ok = e.tryLockWrite()
		} else {
			ok = e.tryLockRead()
		}
		if !ok {
			release()
			c.st.contention.Add(1)
			c.cfg.notifyContention(key, 0)
			return nil, nil
		}
		return c.makeUnlock(release, e, write, start), nil
	}

	var waited bool
	if write {
		waited, err = e.lockWrite(ctx)
	} else {
		waited, err = e.lockRead(ctx)
	}
	if err != nil {
		release()
		c.st.contention.Add(1)
		c.cfg.notifyContention(key, time.Since(start))
		return nil, err
	}
	if waited {
		d := time.Since(start)
		c.st.waitCount.Add(1)
		c.st.waitTotal.Add(int64(d))
		c.cfg.notifyContention(key, d)
	}
	return c.makeUnlock(release, e, write, start), nil
}

// makeUnlock 构造**幂等**的释放函数。
func (c *core) makeUnlock(release func(), e *entry, write bool, start time.Time) UnlockFunc {
	done := new(atomic.Bool)
	return func() error {
		if !done.CompareAndSwap(false, true) {
			// 幂等：重复释放不报错、不破坏状态，只记一笔观测数据
			c.st.invalidUnlock.Add(1)
			return nil
		}
		c.st.observeHold(time.Since(start))
		if write {
			e.unlockWrite()
		} else {
			e.unlockRead()
		}
		release()
		return nil
	}
}

// waitCtx 把 WithWaitTimeout 配置转成 context（未配置时返回 Background，零开销）。
func (c *core) waitCtx() (context.Context, context.CancelFunc) {
	if c == nil || c.cfg.waitTimeout <= 0 {
		return context.Background(), func() {}
	}
	if c.cfg.waitTimeout > 0 {
		return context.WithTimeout(context.Background(), c.cfg.waitTimeout)
	}
	return context.Background(), func() {}
}

// lockBlocking 实现无 ctx 的 Lock / RLock：把 WithWaitTimeout 超时映射为 ErrLockTimeout。
func (c *core) lockBlocking(src entrySource, key string, write bool) (UnlockFunc, error) {
	if c == nil {
		return nil, ErrNilLocker
	}
	ctx, cancel := c.waitCtx()
	defer cancel()

	un, err := c.acquire(src, ctx, key, write, false)
	if err == nil {
		return un, nil
	}
	// ctx 未配置超时 → ctx.Err() 必为 nil，直接返回底层错误
	if c.cfg.waitTimeout > 0 && ctx.Err() != nil {
		return nil, xerr.WithMeta(ErrLockTimeout, "key", key, "timeout", c.cfg.waitTimeout)
	}
	return nil, err
}

// tryAcquire 实现 TryLock / TryRLock。
func (c *core) tryAcquire(src entrySource, key string, write bool) (UnlockFunc, bool) {
	if c == nil {
		return nil, false
	}
	un, err := c.acquire(src, context.Background(), key, write, true)
	if err != nil || un == nil {
		return nil, false
	}
	return un, true
}

// snapshot 生成统计快照。
func (c *core) snapshot(keys int) Stats {
	if c == nil {
		return Stats{}
	}
	return c.st.snapshot(keys)
}
