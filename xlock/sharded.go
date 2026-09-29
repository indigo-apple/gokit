package xlock

import (
	"context"
)

// ---------------------------------------------------------------------------
// ShardedLocker
// ---------------------------------------------------------------------------

// defaultShards 是分片数默认值：32 片在低并发下足够，且固定占用极小。
const defaultShards = 32

// ShardedLocker 用**固定锁表**实现按 key 互斥。
//
// 与 KeyedLocker 的差别：
//
//	KeyedLocker   —— 每个 key 一个锁内核，key 之间精确隔离；表随活跃 key 增长（有上限回收）
//	ShardedLocker —— 只有 N 个锁内核，key 按哈希落分片；表大小恒定，无回收开销
//
// 代价：不同的 key 可能落到同一分片而产生**假冲突**（例如 `user:1` 与 `user:33`
// 在 32 片下会互相阻塞）。因此本实现的适用场景是「key 数量巨大且访问分散」，
// 若对隔离性有严格要求请使用 KeyedLocker。
//
// 由于锁内核常驻，ShardedLocker 天然不会内存泄漏，也不需要引用计数。
//
// 并发安全。
type ShardedLocker struct {
	core
	shards []*entry
}

// NewSharded 创建分片锁；shards<=0 时使用 defaultShards。
//
// 设计取舍：DESIGN.md 中该构造函数声明为返回 `*KeyedLocker`，
// 这里改为返回独立的 `*ShardedLocker` —— 二者语义（假冲突、Stats.Keys 含义）不同，
// 用同一类型会掩盖差异。两者都满足 Locker 接口，可互换使用。
func NewSharded(shards ...int) *ShardedLocker {
	n := defaultShards
	if len(shards) > 0 && shards[0] > 0 {
		n = shards[0]
	}
	l := &ShardedLocker{shards: make([]*entry, n)}
	for i := range l.shards {
		l.shards[i] = newEntry(&l.st)
	}
	return l
}

// take 实现 entrySource：按哈希取分片，归还回调为空操作（分片常驻）。
func (l *ShardedLocker) take(key string) (*entry, func(), error) {
	if l == nil || len(l.shards) == 0 {
		return nil, nil, ErrNilLocker
	}
	return l.shards[ShardOf(key, len(l.shards))], func() {}, nil
}

// Shards 返回分片数。
func (l *ShardedLocker) Shards() int {
	if l == nil {
		return 0
	}
	return len(l.shards)
}

// ---------------------------------------------------------------------------
// Locker 实现

// ---------------------------------------------------------------------------

// Lock 阻塞获取写锁；受 WithWaitTimeout 限制。
func (l *ShardedLocker) Lock(key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.lockBlocking(l, key, true)
}

// LockCtx 阻塞获取写锁，支持 ctx 超时 / 取消。
func (l *ShardedLocker) LockCtx(ctx context.Context, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.acquire(l, ctx, key, true, false)
}

// TryLock 尝试获取写锁；失败返回 (nil, false)。
//
// 注意：返回 false 也可能是因为**别的 key 落在同一分片**，而不仅仅是同 key 被占用。
func (l *ShardedLocker) TryLock(key string) (UnlockFunc, bool) {
	if l == nil {
		return nil, false
	}
	return l.tryAcquire(l, key, true)
}

// RLock 阻塞获取读锁。
func (l *ShardedLocker) RLock(key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.lockBlocking(l, key, false)
}

// RLockCtx 阻塞获取读锁，支持 ctx 超时 / 取消。
func (l *ShardedLocker) RLockCtx(ctx context.Context, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.acquire(l, ctx, key, false, false)
}

// TryRLock 尝试获取读锁；失败返回 (nil, false)。
func (l *ShardedLocker) TryRLock(key string) (UnlockFunc, bool) {
	if l == nil {
		return nil, false
	}
	return l.tryAcquire(l, key, false)
}

// Stats 返回统计快照；Keys 字段恒为分片数（固定锁表）。
func (l *ShardedLocker) Stats() Stats {
	if l == nil {
		return Stats{}
	}
	return l.snapshot(l.Shards())
}

// EntryState 返回 key 所落分片的状态：是否被写者持有、读者数、等待写者数、等待读者数。
//
// ⚠️ 由于存在假冲突，这里的数值反映的是**整个分片**，而非该 key 自身。
func (l *ShardedLocker) EntryState(key string) (writing bool, readers, waitingWriters, waitingReaders int) {
	if l == nil || len(l.shards) == 0 {
		return false, 0, 0, 0
	}
	return l.shards[ShardOf(key, len(l.shards))].snapshot()
}
