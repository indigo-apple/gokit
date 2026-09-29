package xlock

import (
	"context"
	"sort"
	"sync"
)

// KeyedLocker 是「按 key 精确互斥」的锁实现，最常用。
//
// 内部维护 `map[string]*entry`，用**引用计数**保证：
// 某个 key 的持有者与等待者全部离开后，条目立即从 map 中删除。
// 这是本类型不会随 key 数量无限增长内存的关键 —— 直接用 `map[string]*sync.Mutex`
// 实现是本领域最常见的错误做法。
//
// 并发安全。
type KeyedLocker struct {
	core

	mu      sync.Mutex
	entries map[string]*entry
}

// NewKeyed 创建按 key 的锁。
func NewKeyed(opts ...Option) *KeyedLocker {
	l := &KeyedLocker{entries: make(map[string]*entry)}
	l.cfg = newConfig(opts)
	return l
}

// take 实现 entrySource：按 key 取条目并增加引用计数。
func (l *KeyedLocker) take(key string) (*entry, func(), error) {
	if l == nil {
		return nil, nil, ErrNilLocker
	}
	l.mu.Lock()
	// 快路径：已存在
	if e, ok := l.entries[key]; ok {
		e.refs++
		l.mu.Unlock()
		return e, func() { l.releaseEntry(key, e) }, nil
	}
	if l.cfg.maxKeys > 0 && len(l.entries) >= l.cfg.maxKeys {
		n := len(l.entries)
		l.mu.Unlock()
		return nil, nil, xerrWithMeta(ErrTooManyKeys, key, l.cfg.maxKeys, n)
	}
	e := newEntry(&l.st)
	e.refs = 1
	l.entries[key] = e
	l.mu.Unlock()
	return e, func() { l.releaseEntry(key, e) }, nil
}

// releaseEntry 减少引用计数，归零时回收条目。
//
// 关键不变式：refs 只统计「已成功取得条目」的调用方（无论最终是否拿到锁），
// 因此 refs==0 时一定没有任何 goroutine 正在持有或等待该 key 的锁。
func (l *KeyedLocker) releaseEntry(key string, e *entry) {
	l.mu.Lock()
	e.refs--
	if e.refs <= 0 {
		e.refs = 0
		delete(l.entries, key)
	}
	l.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Locker 实现
// ---------------------------------------------------------------------------

// Lock 阻塞获取写锁；受 WithWaitTimeout 限制（超时返回 ErrLockTimeout）。
func (l *KeyedLocker) Lock(key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.lockBlocking(l, key, true)
}

// LockCtx 阻塞获取写锁，支持 ctx 超时 / 取消。
//
// ctx 结束返回 ctx.Err()（context.DeadlineExceeded / context.Canceled）。
func (l *KeyedLocker) LockCtx(ctx context.Context, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.acquire(l, ctx, key, true, false)
}

// TryLock 尝试获取写锁；失败返回 (nil, false)，不等待。
func (l *KeyedLocker) TryLock(key string) (UnlockFunc, bool) {
	if l == nil {
		return nil, false
	}
	return l.tryAcquire(l, key, true)
}

// RLock 阻塞获取读锁；受 WithWaitTimeout 限制。
func (l *KeyedLocker) RLock(key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.lockBlocking(l, key, false)
}

// RLockCtx 阻塞获取读锁，支持 ctx 超时 / 取消。
func (l *KeyedLocker) RLockCtx(ctx context.Context, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.acquire(l, ctx, key, false, false)
}

// TryRLock 尝试获取读锁；失败返回 (nil, false)，不等待。
func (l *KeyedLocker) TryRLock(key string) (UnlockFunc, bool) {
	if l == nil {
		return nil, false
	}
	return l.tryAcquire(l, key, false)
}

// ---------------------------------------------------------------------------
// 观测
// ---------------------------------------------------------------------------

// Stats 返回统计快照。
func (l *KeyedLocker) Stats() Stats {
	if l == nil {
		return Stats{}
	}
	return l.snapshot(l.ActiveKeys())
}

// ActiveKeys 返回当前活跃（有持有者或等待者）的 key 数量。
//
// 这个值会随负载上下波动；**长期单调增长**才说明存在泄漏。
func (l *KeyedLocker) ActiveKeys() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// ActiveKeysSnapshot 返回当前活跃 key 的快照（升序，便于断言与排查）。
//
// 快照与真实状态可能在并发下不一致，仅用于观测。
func (l *KeyedLocker) ActiveKeysSnapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	out := make([]string, 0, len(l.entries))
	for k := range l.entries {
		out = append(out, k)
	}
	l.mu.Unlock()
	sort.Strings(out)
	return out
}

// EntryState 返回某个 key 的内部状态：是否被写者持有、读者数、等待写者数、等待读者数。
//
// 主要用于测试与线上排查（例如「这个 key 是不是被卡住了」）。key 不存在时全部为零。
func (l *KeyedLocker) EntryState(key string) (writing bool, readers, waitingWriters, waitingReaders int) {
	if l == nil {
		return false, 0, 0, 0
	}
	l.mu.Lock()
	e, ok := l.entries[key]
	l.mu.Unlock()
	if !ok {
		return false, 0, 0, 0
	}
	return e.snapshot()
}
