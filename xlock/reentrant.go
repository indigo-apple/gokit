package xlock

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/indigo-apple/gokit/xerr"
)

// ---------------------------------------------------------------------------
// ReentrantLocker
// ---------------------------------------------------------------------------

// rentry 是可重入锁的 per-key 状态：互斥内核 + 持有者 + 重入计数。
type rentry struct {
	core  entry // 实际互斥（写者语义）
	mu    sync.Mutex
	owner string // 当前持有者标识
	depth int    // 重入深度（0 表示未持有）
}

// ReentrantLocker 是**可重入**锁：同一个 owner 重复获取同一 key 不会阻塞。
//
// ⚠️ 强烈建议先读完再使用：
//
//  1. Go 官方不推荐可重入锁 —— 它会掩盖「在持锁状态下递归调用」的设计问题。
//     只有在**无法拆分**的递归/回调场景（如 ORM 钩子、模板渲染回调）才应使用。
//  2. 「同一个 owner」默认按 **goroutine 标识**判定，这依赖 runtime 的非公开细节
//     （解析 `runtime.Stack` 头部）。若解析失败，Lock 返回 ErrNoGoroutineID，
//     此时请改用 LockFor 显式传入 owner（例如请求 ID / 事务 ID）。
//  3. 可重入语义只在**同一 owner** 内成立；不同 goroutine 之间仍然是互斥的。
//  4. 不支持读锁（可重入 + 读写语义的组合极易出错，本包刻意不提供）。
//
// 并发安全。
type ReentrantLocker struct {
	core

	mu      sync.Mutex
	entries map[string]*rentry
}

// NewReentrant 创建可重入锁。
func NewReentrant(opts ...Option) *ReentrantLocker {
	l := &ReentrantLocker{entries: make(map[string]*rentry)}
	l.cfg = newConfig(opts)
	return l
}

// take 取可重入条目并增加引用计数。
func (l *ReentrantLocker) take(key string) (*rentry, func(), error) {
	if l == nil {
		return nil, nil, ErrNilLocker
	}
	l.mu.Lock()
	if e, ok := l.entries[key]; ok {
		e.core.refs++
		l.mu.Unlock()
		return e, func() { l.release(key, e) }, nil
	}
	if l.cfg.maxKeys > 0 && len(l.entries) >= l.cfg.maxKeys {
		n := len(l.entries)
		l.mu.Unlock()
		return nil, nil, xerrWithMeta(ErrTooManyKeys, key, l.cfg.maxKeys, n)
	}
	e := &rentry{core: *newEntry(&l.st)}
	e.core.refs = 1
	l.entries[key] = e
	l.mu.Unlock()
	return e, func() { l.release(key, e) }, nil
}

func (l *ReentrantLocker) release(key string, e *rentry) {
	l.mu.Lock()
	e.core.refs--
	if e.core.refs <= 0 {
		e.core.refs = 0
		delete(l.entries, key)
	}
	l.mu.Unlock()
}

// GoroutineID 返回当前 goroutine 的标识。
//
// 实现方式：解析 `runtime.Stack` 输出的头部（形如 `goroutine 123 [running]:`）。
// 这是 Go 的非公开实现细节，因此本函数**保守设计**：
//   - 只读取 64 字节的栈头，开销极小；
//   - 任何格式不符都返回 (0, false)，绝不 panic、绝不猜测。
//
// 第二个返回值为 false 时，请改用 LockFor 显式传入 owner。
func GoroutineID() (uint64, bool) { return goroutineIDFn() }

// goroutineIDFn 是可替换的实现（测试注入用），生产路径即 goroutineID。
var goroutineIDFn = goroutineID

// goroutineID 是真正的实现：取栈头部并解析。
func goroutineID() (uint64, bool) {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	return parseGoroutineID(buf[:n])
}

// parseGoroutineID 解析形如 `goroutine 123 [running]:` 的栈头部。
//
// 这是防作弊式的防御解析：任何格式不符一律返回 (0, false)，绝不 panic、绝不猜测。
func parseGoroutineID(s []byte) (uint64, bool) {
	const prefix = "goroutine "
	if len(s) <= len(prefix) {
		return 0, false
	}
	for i := 0; i < len(prefix); i++ {
		if s[i] != prefix[i] {
			return 0, false
		}
	}
	s = s[len(prefix):]

	var id uint64
	digits := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
		digits++
		if id > 1<<40 { // 异常大的数值 → 视为解析失败
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	return id, true
}

// Owner 返回当前 owner 标识（goroutine id 的十进制文本）；不可得时返回空串。
func Owner() string {
	id, ok := goroutineIDFn()
	if !ok {
		return ""
	}
	return u64toa(id)
}

// u64toa 是 strconv.FormatUint 的极简实现（避免为一个调用引入 strconv）。
func u64toa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// acquireReentrant 是可重入获取的统一实现。
func (l *ReentrantLocker) acquireReentrant(ctx context.Context, owner, key string, try bool) (UnlockFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner == "" {
		owner = Owner()
		if owner == "" {
			return nil, xerr.WithMeta(ErrNoGoroutineID, "key", key)
		}
	}

	e, release, err := l.take(key)
	if err != nil {
		return nil, err
	}

	// 重入快速路径：同一 owner 已持有时直接加深
	e.mu.Lock()
	if e.depth > 0 && e.owner == owner {
		e.depth++
		e.mu.Unlock()
		return l.makeReentrantUnlock(key, e, owner, release), nil
	}
	e.mu.Unlock()

	// 非重入：走真正的互斥
	start := time.Now()
	if try {
		if !e.core.tryLockWrite() {
			release()
			l.st.contention.Add(1)
			l.cfg.notifyContention(key, 0)
			return nil, nil
		}
	} else {
		waited, werr := e.core.lockWrite(ctx)
		if werr != nil {
			release()
			l.st.contention.Add(1)
			l.cfg.notifyContention(key, time.Since(start))
			return nil, werr
		}
		if waited {
			d := time.Since(start)
			l.st.waitCount.Add(1)
			l.st.waitTotal.Add(int64(d))
			l.cfg.notifyContention(key, d)
		}
	}

	e.mu.Lock()
	e.owner = owner
	e.depth = 1
	e.mu.Unlock()
	return l.makeReentrantUnlock(key, e, owner, release), nil
}

// makeReentrantUnlock 构造幂等的可重入释放函数。
func (l *ReentrantLocker) makeReentrantUnlock(key string, e *rentry, owner string, release func()) UnlockFunc {
	done := new(atomic.Bool)
	return func() error {
		if !done.CompareAndSwap(false, true) {
			l.st.invalidUnlock.Add(1)
			return nil
		}
		e.mu.Lock()
		if e.depth == 0 || e.owner != owner {
			// 非持有者（或状态已错乱）调用：幂等忽略，不 panic、不改状态
			e.mu.Unlock()
			l.st.invalidUnlock.Add(1)
			return nil
		}
		e.depth--
		if e.depth > 0 {
			e.mu.Unlock()
			return nil
		}
		e.owner = ""
		e.mu.Unlock()

		e.core.unlockWrite()
		release()
		return nil
	}
}

// lockForBlocking 实现无 ctx 的获取入口：把 WithWaitTimeout 超时映射为 ErrLockTimeout。
//
// 之所以不让 LockFor 直接返回 ctx.Err()：无 ctx 参数的方法暴露 context 内部错误
// 会让调用方难以判定（它们并不知道内部用了 ctx）。带 ctx 的 *Ctx 方法才返回原始 ctx 错误。
func (l *ReentrantLocker) lockForBlocking(owner, key string) (UnlockFunc, error) {
	ctx, cancel := l.waitCtx()
	defer cancel()

	un, err := l.acquireReentrant(ctx, owner, key, false)
	if err == nil {
		return un, nil
	}
	if l.cfg.waitTimeout > 0 && ctx.Err() != nil {
		return nil, xerr.WithMeta(ErrLockTimeout, "key", key, "timeout", l.cfg.waitTimeout)
	}
	return nil, err
}

// Lock 获取写锁（可重入）；owner 取当前 goroutine 标识。
//
// 无法取得 goroutine 标识时返回 ErrNoGoroutineID，请改用 LockFor。
func (l *ReentrantLocker) Lock(key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.lockForBlocking("", key)
}

// LockCtx 获取写锁（可重入），支持 ctx 取消；owner 取当前 goroutine 标识。
func (l *ReentrantLocker) LockCtx(ctx context.Context, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.acquireReentrant(ctx, "", key, false)
}

// TryLock 尝试获取写锁（可重入），立即返回。
func (l *ReentrantLocker) TryLock(key string) (UnlockFunc, bool) {
	if l == nil {
		return nil, false
	}
	un, err := l.acquireReentrant(context.Background(), "", key, true)
	if err != nil || un == nil {
		return nil, false
	}
	return un, true
}

// LockFor 获取写锁（可重入），显式指定 owner 标识。
//
// 适用场景：
//   - 无法取得 goroutine id（如特殊运行时 / 嵌入式）；
//   - 重入判定应按**业务标识**而非 goroutine 划分（如 `requestID`、`txID`）。
//
// ⚠️ 同一 owner 标识被多个 goroutine 同时使用时，可重入语义会失效（互相误判为「自己」），
// 请确保 owner 在「可能重入的调用链」内唯一。
func (l *ReentrantLocker) LockFor(owner, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.lockForBlocking(owner, key)
}

// LockForCtx 与 LockFor 相同，支持 ctx 取消。
func (l *ReentrantLocker) LockForCtx(ctx context.Context, owner, key string) (UnlockFunc, error) {
	if l == nil {
		return nil, ErrNilLocker
	}
	return l.acquireReentrant(ctx, owner, key, false)
}

// Depth 返回 key 当前的重入深度；未持有时为 0。
func (l *ReentrantLocker) Depth(key string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	e, ok := l.entries[key]
	l.mu.Unlock()
	if !ok {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.depth
}

// Holder 返回 key 当前的持有者标识；未持有时返回空串。
func (l *ReentrantLocker) Holder(key string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	e, ok := l.entries[key]
	l.mu.Unlock()
	if !ok {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner
}

// Stats 返回统计快照。
func (l *ReentrantLocker) Stats() Stats {
	if l == nil {
		return Stats{}
	}
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	return l.snapshot(n)
}
