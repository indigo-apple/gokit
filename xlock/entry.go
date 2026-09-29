package xlock

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// entry —— per-key 的可取消读写锁内核
// ---------------------------------------------------------------------------

// entry 是单个 key（或分片）的锁状态，实现「写者互斥 + 读者共享 + 写者优先」。
//
// 为什么不用 sync.RWMutex？
//
//	sync.Mutex/RWMutex 的 Lock() **不可取消**：ctx 超时后等待者仍占着等待队列，
//	只能等真正拿到锁之后再释放，会造成 goroutine 堆积。这里用「每等待者一个 channel」
//	的显式队列，让 ctx 取消能真正把自己从队列里摘掉。
//
// 公平性：写者进入 FIFO 队列、按序交接所有权，因此不会有写者饥饿；
// 读者只在「无写者持有且无写者等待」时批量共享，从而保证写者优先。
//
// 归属：KeyedLocker 每个 key 一个 entry（引用计数归零回收）；
// ShardedLocker 固定 N 个 entry 常驻。
type entry struct {
	mu      sync.Mutex
	writing bool            // 是否有写者持有（含「已交接但未被唤醒」状态）
	readers int             // 当前读者数
	rwait   int             // 等待中的读者数（仅用于观测）
	wqueue  []chan struct{} // 写者等待队列（FIFO，按序交接所有权）
	changed chan struct{}   // 读者等待的状态变化广播
	refs    int             // 引用计数（持有者 + 等待者），归零后可回收
	st      *stats          // 共享统计（nil 表示不统计）
}

func newEntry(st *stats) *entry {
	return &entry{
		changed: make(chan struct{}),
		st:      st,
	}
}

// broadcastLocked 唤醒所有等待的读者（关闭旧 channel 并换新的）。
//
// 必须在持有 e.mu 时调用。读者「持有 e.mu 时读取 changed、再释放」，
// 因此不存在丢唤醒。
func (e *entry) broadcastLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

// handoffLocked 释放写锁时把所有权交给下一个写者；没有写者则开放给读者。
//
// 必须在持有 e.mu 时调用。
func (e *entry) handoffLocked() {
	if len(e.wqueue) > 0 {
		next := e.wqueue[0]
		e.wqueue = e.wqueue[1:]
		e.writing = true // 所有权直接交接，writing 保持 true
		close(next)
		return
	}
	e.writing = false
	e.broadcastLocked()
}

// lockWrite 获取写锁。返回 (是否发生过等待, error)。
//
// ctx 必须非 nil（由 core.acquire 保证）。
func (e *entry) lockWrite(ctx context.Context) (bool, error) {
	e.mu.Lock()
	if !e.writing && e.readers == 0 && len(e.wqueue) == 0 {
		e.writing = true
		e.mu.Unlock()
		return false, nil
	}

	// 需要排队：登记一个专属 channel，等待所有权交接
	ch := make(chan struct{})
	e.wqueue = append(e.wqueue, ch)
	e.mu.Unlock()

	select {
	case <-ch:
		// 已被交接：writing 已为 true，锁归我们
		return true, nil
	case <-ctx.Done():
		e.mu.Lock()
		removed := e.removeWaiterLocked(ch)
		e.mu.Unlock()
		if removed {
			return true, ctx.Err()
		}
		// 唤醒信号与 ctx 几乎同时到达：锁已交接给我们，
		// 必须立刻归还，否则这把锁就永久泄漏了。
		e.unlockWrite()
		return true, ctx.Err()
	}
}

// removeWaiterLocked 把自己从写者队列中摘除；返回是否成功摘除。
//
// 必须在持有 e.mu 时调用。
func (e *entry) removeWaiterLocked(ch chan struct{}) bool {
	for i, w := range e.wqueue {
		if w == ch {
			e.wqueue = append(e.wqueue[:i], e.wqueue[i+1:]...)
			return true
		}
	}
	return false
}

// lockRead 获取读锁。返回 (是否发生过等待, error)。
func (e *entry) lockRead(ctx context.Context) (bool, error) {
	waited := false
	for {
		e.mu.Lock()
		// 写者优先：有写者持有或写者在排队时，读者一律等待
		if !e.writing && len(e.wqueue) == 0 {
			e.readers++
			e.mu.Unlock()
			return waited, nil
		}
		waited = true
		ch := e.changed
		e.rwait++
		e.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			e.mu.Lock()
			e.rwait--
			e.mu.Unlock()
			return true, ctx.Err()
		}

		e.mu.Lock()
		e.rwait--
		e.mu.Unlock()
	}
}

// tryLockWrite 尝试立即获取写锁。
func (e *entry) tryLockWrite() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writing || e.readers > 0 || len(e.wqueue) > 0 {
		return false
	}
	e.writing = true
	return true
}

// tryLockRead 尝试立即获取读锁。
func (e *entry) tryLockRead() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writing || len(e.wqueue) > 0 {
		return false
	}
	e.readers++
	return true
}

// unlockWrite 释放写锁（交接给下一个等待者或开放给读者）。
func (e *entry) unlockWrite() {
	e.mu.Lock()
	e.handoffLocked()
	e.mu.Unlock()
}

// unlockRead 释放读锁；最后一个读者离开且写者在排队时交接写锁。
func (e *entry) unlockRead() {
	e.mu.Lock()
	e.readers--
	if e.readers <= 0 {
		e.readers = 0
		if len(e.wqueue) > 0 {
			e.handoffLocked()
		}
	}
	e.mu.Unlock()
}

// snapshot 返回当前状态（用于测试与观测）。
func (e *entry) snapshot() (writing bool, readers, waitingWriters, waitingReaders int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.writing, e.readers, len(e.wqueue), e.rwait
}
