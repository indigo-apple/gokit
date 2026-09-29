// Package xqueue 提供先进先出（FIFO）队列实现。
//
// 四种实现各有明确的适用场景：
//
//	SliceQueue[T]    —— 无界切片队列（head 索引 + 惰性压缩），默认选择
//	Ring[T]          —— 定容环形队列，零分配、无 GC 压力，适合高频短生命周期
//	Bounded[T]       —— 有界队列（channel 实现），支持 4 种溢出策略与 ctx 取消，**并发安全**
//	Sync[T]          —— 无界并发队列（互斥锁保护）
//
// 语义约定：
//   - **所有入参 nil 安全、空队列操作不 panic**；
//   - SliceQueue / Ring / Sync 的 Push 返回 bool（false 表示被拒绝）；
//   - Bounded 的 Push 返回 error，并提供 PushCtx / PopCtx 支持阻塞与取消；
//   - **生产环境请优先使用有界队列**：无界队列是内存泄漏的常见来源。
//
// 并发安全：Bounded 并发安全；其余实现**非并发安全**（Sync 除外）。
package xqueue

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/zhangwenxing/gokit/xcontainer"
)

// ---------------------------------------------------------------------------
// SliceQueue：无界切片队列
// ---------------------------------------------------------------------------

// SliceQueue 是无界 FIFO 队列。
//
// 内部用 head 索引避免 Pop 时 O(n) 搬移；当废弃区间过大时惰性压缩。
// **非并发安全**。
type SliceQueue[T any] struct {
	items []T
	head  int
}

// New 创建无界队列；capacity 用于预分配（可省略）。
func New[T any](capacity ...int) *SliceQueue[T] {
	capHint := 0
	if len(capacity) > 0 && capacity[0] > 0 {
		capHint = capacity[0]
	}
	return &SliceQueue[T]{items: make([]T, 0, capHint)}
}

// Push 入队（无界队列永不失败）。
func (q *SliceQueue[T]) Push(v T) bool {
	if q == nil {
		return false
	}
	q.items = append(q.items, v)
	return true
}

// PushAll 批量入队。
func (q *SliceQueue[T]) PushAll(vs ...T) {
	if q == nil || len(vs) == 0 {
		return
	}
	q.items = append(q.items, vs...)
}

// Pop 出队；空队列返回 (零值, false)。
func (q *SliceQueue[T]) Pop() (T, bool) {
	var zero T
	if q == nil || q.head >= len(q.items) {
		return zero, false
	}
	v := q.items[q.head]
	q.items[q.head] = zero // 释放引用
	q.head++
	q.maybeCompact()
	return v, true
}

// maybeCompact 在废弃区间过大时把有效数据搬回数组头部。
//
// 触发条件：head 超过 64 且超过总容量的一半 —— 兼顾「减少搬移次数」与「限制内存浪费」。
func (q *SliceQueue[T]) maybeCompact() {
	if q.head < 64 || q.head*2 < len(q.items) {
		return
	}
	n := copy(q.items, q.items[q.head:])
	q.head = 0
	q.items = q.items[:n]
}

// Peek 查看队首但不出队。
func (q *SliceQueue[T]) Peek() (T, bool) {
	if q == nil || q.head >= len(q.items) {
		var zero T
		return zero, false
	}
	return q.items[q.head], true
}

// Len 返回有效元素个数。
func (q *SliceQueue[T]) Len() int {
	if q == nil {
		return 0
	}
	return len(q.items) - q.head
}

// IsEmpty 判断是否为空。
func (q *SliceQueue[T]) IsEmpty() bool { return q.Len() == 0 }

// Clear 清空。
func (q *SliceQueue[T]) Clear() {
	if q == nil {
		return
	}
	var zero T
	for i := range q.items {
		q.items[i] = zero
	}
	q.items = q.items[:0]
	q.head = 0
}

// Values 返回元素副本（队首 → 队尾）。
func (q *SliceQueue[T]) Values() []T {
	if q == nil || q.Len() == 0 {
		return nil
	}
	out := make([]T, q.Len())
	copy(out, q.items[q.head:])
	return out
}

// Drain 取出全部元素并清空队列。
func (q *SliceQueue[T]) Drain() []T {
	out := q.Values()
	q.Clear()
	return out
}

// ForEach 按队首 → 队尾遍历。
func (q *SliceQueue[T]) ForEach(fn func(T)) {
	if q == nil || fn == nil {
		return
	}
	for i := q.head; i < len(q.items); i++ {
		fn(q.items[i])
	}
}

// Clone 返回队列副本。
func (q *SliceQueue[T]) Clone() *SliceQueue[T] {
	out := New[T](q.Len())
	out.PushAll(q.Values()...)
	return out
}

// ---------------------------------------------------------------------------
// Ring：定容环形队列
// ---------------------------------------------------------------------------

// Ring 是定容环形队列，元素全部预分配，运行期零分配。
//
// **非并发安全**。容量固定，Push 在满时返回 false。
type Ring[T any] struct {
	buf   []T
	head  int // 队首下标
	count int
}

// NewRing 创建容量为 capacity 的环形队列；capacity <= 0 时按 1 处理。
func NewRing[T any](capacity int) *Ring[T] {
	if capacity <= 0 {
		capacity = 1
	}
	return &Ring[T]{buf: make([]T, capacity)}
}

// Push 入队；队列已满返回 false。
func (q *Ring[T]) Push(v T) bool {
	if q == nil || q.count >= len(q.buf) {
		return false
	}
	q.buf[(q.head+q.count)%len(q.buf)] = v
	q.count++
	return true
}

// Pop 出队；空队列返回 (零值, false)。
func (q *Ring[T]) Pop() (T, bool) {
	var zero T
	if q == nil || q.count == 0 {
		return zero, false
	}
	v := q.buf[q.head]
	q.buf[q.head] = zero
	q.head = (q.head + 1) % len(q.buf)
	q.count--
	return v, true
}

// Peek 查看队首。
func (q *Ring[T]) Peek() (T, bool) {
	if q == nil || q.count == 0 {
		var zero T
		return zero, false
	}
	return q.buf[q.head], true
}

// Len 返回元素个数。
func (q *Ring[T]) Len() int {
	if q == nil {
		return 0
	}
	return q.count
}

// Cap 返回容量。
func (q *Ring[T]) Cap() int {
	if q == nil {
		return 0
	}
	return len(q.buf)
}

// IsEmpty 判断是否为空。
func (q *Ring[T]) IsEmpty() bool { return q.Len() == 0 }

// IsFull 判断是否已满。
func (q *Ring[T]) IsFull() bool {
	if q == nil {
		return true
	}
	return q.count >= len(q.buf)
}

// Clear 清空。
func (q *Ring[T]) Clear() {
	if q == nil {
		return
	}
	var zero T
	for i := range q.buf {
		q.buf[i] = zero
	}
	q.head = 0
	q.count = 0
}

// Values 返回副本（队首 → 队尾）。
func (q *Ring[T]) Values() []T {
	if q == nil || q.count == 0 {
		return nil
	}
	out := make([]T, q.count)
	for i := 0; i < q.count; i++ {
		out[i] = q.buf[(q.head+i)%len(q.buf)]
	}
	return out
}

// ForEach 按队首 → 队尾遍历。
func (q *Ring[T]) ForEach(fn func(T)) {
	if q == nil || fn == nil {
		return
	}
	for i := 0; i < q.count; i++ {
		fn(q.buf[(q.head+i)%len(q.buf)])
	}
}

// ---------------------------------------------------------------------------
// Bounded：有界并发队列
// ---------------------------------------------------------------------------

// Bounded 是有界队列，**并发安全**。
//
// 内部用带缓冲 channel 存储，因此：
//   - Push/Pop 天然并发安全；
//   - Block 策略可通过 PushCtx/PopCtx 实现「阻塞 + 取消」；
//   - len(q.ch) 可直接作为队列水位指标。
//
// 关闭语义（重要）：
//   - 内部**不关闭数据 channel**，而是单独关闭一个 done channel。
//     这是刻意的：多生产者场景下 close(data channel) 会与并发 send 竞争，
//     导致 "send on closed channel" panic；
//   - Close 之后 Push 返回 ErrClosed，但**已入队的元素仍可被完整消费**；
//   - 重复 Close 安全；Pop 在队列关闭且排空后返回 false。
type Bounded[T any] struct {
	ch        chan T
	done      chan struct{}
	closeOnce sync.Once
	policy    xcontainer.OverflowPolicy
	dropped   atomic.Int64
	mu        sync.Mutex // 串行化 DropOldest 的「腾位 + 写入」
}

// NewBounded 创建有界队列；capacity <= 0 时按 1 处理，非法策略归一化为 DropNewest。
func NewBounded[T any](capacity int, policy xcontainer.OverflowPolicy) *Bounded[T] {
	if capacity <= 0 {
		capacity = 1
	}
	return &Bounded[T]{
		ch:     make(chan T, capacity),
		done:   make(chan struct{}),
		policy: policy.Norm(),
	}
}

// Policy 返回溢出策略。
func (q *Bounded[T]) Policy() xcontainer.OverflowPolicy {
	if q == nil {
		return xcontainer.DropNewest
	}
	return q.policy
}

// IsClosed 判断是否已关闭。
func (q *Bounded[T]) IsClosed() bool {
	if q == nil {
		return true
	}
	return q.closedNow()
}

// closedNow 非阻塞判断 done 是否已关闭。
func (q *Bounded[T]) closedNow() bool {
	select {
	case <-q.done:
		return true
	default:
		return false
	}
}

// Push 非阻塞入队，按策略处理满队列。
//
//	Block      → 返回 ErrWouldBlock（请改用 PushCtx）
//	DropOldest → 丢弃队首后写入
//	DropNewest → 丢弃新元素，返回 nil（计入 Dropped）
//	Fail       → 返回 ErrFull
//
// 队列已关闭时返回 ErrClosed。
func (q *Bounded[T]) Push(v T) error {
	if q == nil {
		return nil
	}
	// 必须先做确定性的关闭判定：否则在「缓冲区有空位」与「done 已关闭」同时就绪时，
	// Go 的 select 会随机选择分支，导致 Close 后 Push 时成时败（不可预测）。
	if q.closedNow() {
		return xcontainer.ErrClosed
	}
	switch q.policy {
	case xcontainer.Block:
		select {
		case q.ch <- v:
			return nil
		case <-q.done:
			return xcontainer.ErrClosed
		default:
			return xcontainer.ErrWouldBlock
		}

	case xcontainer.DropNewest:
		select {
		case q.ch <- v:
			return nil
		default:
			q.dropped.Add(1)
			return nil
		}

	case xcontainer.Fail:
		select {
		case q.ch <- v:
			return nil
		default:
			return xcontainer.ErrFull
		}

	default: // DropOldest
		return q.pushDropOldest(v)
	}
}

// pushDropOldest 用互斥锁串行化「丢队首 + 写入」，避免并发下反复失败。
//
// 全程非阻塞：DropOldest 的语义是「永不因满而阻塞」。
func (q *Bounded[T]) pushDropOldest(v T) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	select {
	case <-q.done:
		return xcontainer.ErrClosed
	default:
	}

	// 快路径：还有空位
	select {
	case q.ch <- v:
		return nil
	default:
	}

	// 满：丢一个队首
	select {
	case <-q.ch:
		q.dropped.Add(1)
	default:
	}

	select {
	case q.ch <- v:
		return nil
	default:
		// 极端竞争下刚腾出的位置被别的生产者抢走，仍然丢弃新值
		q.dropped.Add(1)
		return nil
	}
}

// PushCtx 按策略入队；Block 策略下会等待直到有空位、队列关闭或 ctx 结束。
//
// ctx 为 nil 时视为 context.Background()。
func (q *Bounded[T]) PushCtx(ctx context.Context, v T) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 与 Push 同理：关闭判定必须先于 select，保证 Close 后语义确定。
	if q.closedNow() {
		return xcontainer.ErrClosed
	}
	if q.policy != xcontainer.Block {
		return q.Push(v)
	}
	select {
	case q.ch <- v:
		return nil
	case <-q.done:
		return xcontainer.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pop 非阻塞出队；无元素时返回 (零值, false)。
func (q *Bounded[T]) Pop() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	// 数据 channel 永不被关闭，因此即使队列已 Close，
	// 这里的 select 也能安全地把剩余元素读完。
	select {
	case v := <-q.ch:
		return v, true
	default:
		return zero, false
	}
}

// PopCtx 阻塞出队，直到取到元素、ctx 结束或队列关闭且已排空。
//
// 第二个返回值为 false 表示「已关闭且无剩余元素」或「ctx 结束」，
// 可用 xerr.Is(err, xcontainer.ErrClosed) / errors.Is(err, context.Canceled) 区分。
func (q *Bounded[T]) PopCtx(ctx context.Context) (T, bool, error) {
	var zero T
	if q == nil {
		return zero, false, xcontainer.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// 快路径 + 关闭后仍可排空
	select {
	case v := <-q.ch:
		return v, true, nil
	default:
	}
	select {
	case <-q.done:
		select {
		case v := <-q.ch:
			return v, true, nil
		default:
			return zero, false, xcontainer.ErrClosed
		}
	default:
	}

	select {
	case v := <-q.ch:
		return v, true, nil
	case <-q.done:
		select {
		case v := <-q.ch:
			return v, true, nil
		default:
			return zero, false, xcontainer.ErrClosed
		}
	case <-ctx.Done():
		return zero, false, ctx.Err()
	}
}

// Len 返回当前元素个数。
func (q *Bounded[T]) Len() int {
	if q == nil {
		return 0
	}
	return len(q.ch)
}

// Cap 返回容量。
func (q *Bounded[T]) Cap() int {
	if q == nil {
		return 0
	}
	return cap(q.ch)
}

// IsEmpty 判断是否为空。
func (q *Bounded[T]) IsEmpty() bool { return q.Len() == 0 }

// IsFull 判断是否已满。
func (q *Bounded[T]) IsFull() bool {
	if q == nil {
		return true
	}
	return len(q.ch) >= cap(q.ch)
}

// Close 关闭队列：后续 Push 返回 ErrClosed，已入队的元素仍可被完整消费。
//
// 重复调用安全。内部只关闭 done channel，绝不关闭数据 channel ——
// 多生产者场景下关闭数据 channel 会与并发 send 竞争并 panic。
func (q *Bounded[T]) Close() {
	if q == nil {
		return
	}
	q.closeOnce.Do(func() { close(q.done) })
}

// Dropped 返回被丢弃的元素总数（DropOldest 丢的旧值 + DropNewest 拒绝的新值）。
func (q *Bounded[T]) Dropped() int64 {
	if q == nil {
		return 0
	}
	return q.dropped.Load()
}

// Drain 取出当前全部剩余元素（非阻塞），队列保持可用。
//
// 适用于优雅关闭时把缓冲内容落盘 / 上报。
func (q *Bounded[T]) Drain() []T {
	if q == nil {
		return nil
	}
	var out []T
	for {
		select {
		case v := <-q.ch:
			out = append(out, v)
		default:
			return out
		}
	}
}

// 注意：本类型**刻意不提供 Values()**。
//
// channel 型队列无法在不消费的前提下安全快照：任何「抓取后回填」的实现都会在
// 并发写入下重排甚至丢失元素。需要快照请显式使用 Drain（语义明确），
// 或改用 Sync 队列。

// ---------------------------------------------------------------------------
// Sync：无界并发队列
// ---------------------------------------------------------------------------

// Sync 是互斥锁保护的无界并发队列。
//
// 由于无界，存在内存无限增长的风险：仅用于「生产速率天然受控」的场景，
// 否则请使用 Bounded。
type Sync[T any] struct {
	mu sync.Mutex
	q  *SliceQueue[T]
}

// NewSync 创建无界并发队列。
func NewSync[T any]() *Sync[T] {
	return &Sync[T]{q: New[T]()}
}

// Push 入队。
func (q *Sync[T]) Push(v T) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.q.Push(v)
}

// Pop 出队。
func (q *Sync[T]) Pop() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.q.Pop()
}

// Peek 查看队首。
func (q *Sync[T]) Peek() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.q.Peek()
}

// Len 返回元素个数。
func (q *Sync[T]) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.q.Len()
}

// IsEmpty 判断是否为空。
func (q *Sync[T]) IsEmpty() bool { return q.Len() == 0 }

// Clear 清空。
func (q *Sync[T]) Clear() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.q.Clear()
}

// Values 返回快照。
func (q *Sync[T]) Values() []T {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.q.Values()
}

// Drain 取出全部元素并清空。
func (q *Sync[T]) Drain() []T {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.q.Drain()
}
