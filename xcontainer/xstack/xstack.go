// Package xstack 提供后进先出（LIFO）栈实现。
//
// 提供三种实现：
//
//	Stack[T]            —— 切片实现，非并发安全，性能最好
//	ConcurrentStack[T]  —— 读写锁保护，并发安全
//	BoundedStack[T]     —— 有界栈，满时按 OverflowPolicy 处理
//
// 语义约定：
//   - **空栈操作永不 panic**：Pop / Peek 返回 (零值, false)，仅 MustPop 会在空栈时 panic；
//   - Values 返回「栈底 → 栈顶」，ForEach 按「栈顶 → 栈底」遍历（与弹出顺序一致）；
//   - 所有入参为 nil 的调用都被安全处理。
//
// 并发安全：Stack 与 BoundedStack **非并发安全**；ConcurrentStack 并发安全。
package xstack

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/zhangwenxing/gokit/xcontainer"
)

// ---------------------------------------------------------------------------
// Stack
// ---------------------------------------------------------------------------

// Stack 是切片实现的后进先出栈。
//
// **非并发安全**：多 goroutine 访问请使用 ConcurrentStack 或自行加锁。
type Stack[T any] struct {
	items []T
}

// New 创建空栈；capacity 用于预分配底层数组（可省略）。
func New[T any](capacity ...int) *Stack[T] {
	capHint := 0
	if len(capacity) > 0 && capacity[0] > 0 {
		capHint = capacity[0]
	}
	return &Stack[T]{items: make([]T, 0, capHint)}
}

// NewFrom 由切片创建栈，切片最后一个元素成为栈顶。
//
// ⚠️ 会复制入参，不与调用方共享底层数组。
func NewFrom[T any](items []T) *Stack[T] {
	s := &Stack[T]{items: make([]T, 0, len(items))}
	s.items = append(s.items, items...)
	return s
}

// Push 压入若干元素（先传的在下）。
func (s *Stack[T]) Push(vs ...T) {
	if s == nil {
		return
	}
	s.items = append(s.items, vs...)
}

// Pop 弹出栈顶；空栈返回 (零值, false)。
func (s *Stack[T]) Pop() (T, bool) {
	var zero T
	if s == nil || len(s.items) == 0 {
		return zero, false
	}
	i := len(s.items) - 1
	v := s.items[i]
	s.items[i] = zero // 释放引用，避免内存泄漏
	s.items = s.items[:i]
	return v, true
}

// MustPop 弹出栈顶；空栈时 panic。仅用于「栈非空是程序不变式」的场景与测试。
func (s *Stack[T]) MustPop() T {
	v, ok := s.Pop()
	if !ok {
		panic("xstack: pop from empty stack")
	}
	return v
}

// Peek 查看栈顶但不出栈。
func (s *Stack[T]) Peek() (T, bool) {
	if s == nil || len(s.items) == 0 {
		var zero T
		return zero, false
	}
	return s.items[len(s.items)-1], true
}

// PeekN 查看距栈顶第 n 层的元素（n = 0 表示栈顶）。
func (s *Stack[T]) PeekN(n int) (T, bool) {
	var zero T
	if s == nil || n < 0 || n >= len(s.items) {
		return zero, false
	}
	return s.items[len(s.items)-1-n], true
}

// Len 返回元素个数。
func (s *Stack[T]) Len() int {
	if s == nil {
		return 0
	}
	return len(s.items)
}

// Cap 返回底层数组容量。
func (s *Stack[T]) Cap() int {
	if s == nil {
		return 0
	}
	return cap(s.items)
}

// IsEmpty 判断是否为空。
func (s *Stack[T]) IsEmpty() bool { return s.Len() == 0 }

// Clear 清空并释放元素引用（保留底层数组容量）。
func (s *Stack[T]) Clear() {
	if s == nil {
		return
	}
	var zero T
	for i := range s.items {
		s.items[i] = zero
	}
	s.items = s.items[:0]
}

// Values 返回「栈底 → 栈顶」的元素副本。
func (s *Stack[T]) Values() []T {
	if s == nil || len(s.items) == 0 {
		return nil
	}
	out := make([]T, len(s.items))
	copy(out, s.items)
	return out
}

// Clone 返回栈的副本。
func (s *Stack[T]) Clone() *Stack[T] {
	if s == nil {
		return New[T]()
	}
	return NewFrom(s.items)
}

// ForEach 按「栈顶 → 栈底」顺序遍历（即 Pop 的顺序）。
func (s *Stack[T]) ForEach(fn func(T)) {
	if s == nil || fn == nil {
		return
	}
	for i := len(s.items) - 1; i >= 0; i-- {
		fn(s.items[i])
	}
}

// PopUntil 依次弹出元素直到 pred 返回 true（该元素也会被弹出），返回被弹出的元素。
//
// 用于「表达式求值」「回溯到某个标记」等场景。
func (s *Stack[T]) PopUntil(pred func(T) bool) []T {
	if s == nil || pred == nil {
		return nil
	}
	var out []T
	for {
		v, ok := s.Pop()
		if !ok {
			return out
		}
		out = append(out, v)
		if pred(v) {
			return out
		}
	}
}

// Search 返回元素距栈顶的距离；未找到返回 -1。
//
// eq 省略时使用 reflect.DeepEqual 比较（对切片 / map 等不可比较类型同样安全）。
func (s *Stack[T]) Search(v T, eq ...func(a, b T) bool) int {
	if s == nil {
		return -1
	}
	var equal func(a, b T) bool
	if len(eq) > 0 {
		equal = eq[0]
	}
	for i := len(s.items) - 1; i >= 0; i-- {
		if equal != nil {
			if equal(s.items[i], v) {
				return len(s.items) - 1 - i
			}
			continue
		}
		// 不能用 any(x) == any(y)：动态类型不可比较时会 panic
		if reflect.DeepEqual(s.items[i], v) {
			return len(s.items) - 1 - i
		}
	}
	return -1
}

// String 实现 fmt.Stringer，按「栈底 → 栈顶」输出，便于调试。
func (s *Stack[T]) String() string {
	if s == nil {
		return "Stack(nil)"
	}
	parts := make([]string, 0, len(s.items))
	for _, v := range s.items {
		parts = append(parts, fmt.Sprint(v))
	}
	return "Stack([" + strings.Join(parts, " ") + "])"
}

// ---------------------------------------------------------------------------
// ConcurrentStack
// ---------------------------------------------------------------------------

// ConcurrentStack 是读写锁保护的并发安全栈。
type ConcurrentStack[T any] struct {
	mu    sync.RWMutex
	items []T
}

// NewConcurrent 创建并发安全栈。
func NewConcurrent[T any](capacity ...int) *ConcurrentStack[T] {
	capHint := 0
	if len(capacity) > 0 && capacity[0] > 0 {
		capHint = capacity[0]
	}
	return &ConcurrentStack[T]{items: make([]T, 0, capHint)}
}

// Push 压入若干元素。
func (s *ConcurrentStack[T]) Push(vs ...T) {
	if s == nil || len(vs) == 0 {
		return
	}
	s.mu.Lock()
	s.items = append(s.items, vs...)
	s.mu.Unlock()
}

// Pop 弹出栈顶；空栈返回 (零值, false)。
func (s *ConcurrentStack[T]) Pop() (T, bool) {
	var zero T
	if s == nil {
		return zero, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return zero, false
	}
	i := len(s.items) - 1
	v := s.items[i]
	s.items[i] = zero
	s.items = s.items[:i]
	return v, true
}

// Peek 查看栈顶。
func (s *ConcurrentStack[T]) Peek() (T, bool) {
	var zero T
	if s == nil {
		return zero, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.items) == 0 {
		return zero, false
	}
	return s.items[len(s.items)-1], true
}

// Len 返回元素个数。
func (s *ConcurrentStack[T]) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// IsEmpty 判断是否为空。
func (s *ConcurrentStack[T]) IsEmpty() bool { return s.Len() == 0 }

// Clear 清空。
func (s *ConcurrentStack[T]) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero T
	for i := range s.items {
		s.items[i] = zero
	}
	s.items = s.items[:0]
}

// Values 返回「栈底 → 栈顶」的副本。
func (s *ConcurrentStack[T]) Values() []T {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.items) == 0 {
		return nil
	}
	out := make([]T, len(s.items))
	copy(out, s.items)
	return out
}

// ForEach 按「栈顶 → 栈底」遍历。
//
// 遍历期间 fn 内**不可**再调用本栈的方法（会死锁）；如需访问请先 Values 取快照。
func (s *ConcurrentStack[T]) ForEach(fn func(T)) {
	if s == nil || fn == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.items) - 1; i >= 0; i-- {
		fn(s.items[i])
	}
}

// Clone 返回栈的快照副本。
func (s *ConcurrentStack[T]) Clone() *ConcurrentStack[T] {
	out := NewConcurrent[T]()
	out.Push(s.Values()...)
	return out
}

// ---------------------------------------------------------------------------
// BoundedStack
// ---------------------------------------------------------------------------

// BoundedStack 是有界栈，满时按 OverflowPolicy 处理。
//
// **非并发安全**。
//
// 策略语义（栈的「最旧」指栈底）：
//
//	Block      —— Push 返回 ErrWouldBlock（提示改用别的策略）
//	DropOldest —— 丢弃栈底元素以腾出空间（O(n) 搬移，n 上限为 maxSize）
//	DropNewest —— 拒绝新元素，返回 ErrFull 之外的成功语义：保留原栈，返回 nil
//	Fail       —— 返回 ErrFull
type BoundedStack[T any] struct {
	items   []T
	max     int
	policy  xcontainer.OverflowPolicy
	dropped int64
}

// NewBounded 创建有界栈；maxSize <= 0 时按 1 处理，非法策略归一化为 DropNewest。
func NewBounded[T any](maxSize int, policy xcontainer.OverflowPolicy) *BoundedStack[T] {
	if maxSize <= 0 {
		maxSize = 1
	}
	return &BoundedStack[T]{
		items:  make([]T, 0, maxSize),
		max:    maxSize,
		policy: policy.Norm(),
	}
}

// Push 压入一个元素，按策略处理溢满情况。
//
// 返回 nil 表示成功或「已按策略丢弃」；只有在 Fail 策略满时返回 ErrFull。
func (s *BoundedStack[T]) Push(v T) error {
	if s == nil {
		return nil
	}
	if len(s.items) < s.max {
		s.items = append(s.items, v)
		return nil
	}

	switch s.policy {
	case xcontainer.DropOldest:
		// 丢弃栈底（最旧）
		copy(s.items, s.items[1:])
		var zero T
		s.items[len(s.items)-1] = zero
		s.items = s.items[:len(s.items)-1]
		s.items = append(s.items, v)
		s.dropped++
		return nil

	case xcontainer.DropNewest, xcontainer.Block:
		// 保留原数据，拒绝新元素
		s.dropped++
		if s.policy == xcontainer.Block {
			return xcontainer.ErrWouldBlock
		}
		return nil

	default: // Fail
		return xcontainer.ErrFull
	}
}

// Pop 弹出栈顶。
func (s *BoundedStack[T]) Pop() (T, bool) {
	var zero T
	if s == nil || len(s.items) == 0 {
		return zero, false
	}
	i := len(s.items) - 1
	v := s.items[i]
	s.items[i] = zero
	s.items = s.items[:i]
	return v, true
}

// Peek 查看栈顶。
func (s *BoundedStack[T]) Peek() (T, bool) {
	if s == nil || len(s.items) == 0 {
		var zero T
		return zero, false
	}
	return s.items[len(s.items)-1], true
}

// Len 返回元素个数。
func (s *BoundedStack[T]) Len() int {
	if s == nil {
		return 0
	}
	return len(s.items)
}

// Cap 返回容量上限。
func (s *BoundedStack[T]) Cap() int {
	if s == nil {
		return 0
	}
	return s.max
}

// IsEmpty 判断是否为空。
func (s *BoundedStack[T]) IsEmpty() bool { return s.Len() == 0 }

// IsFull 判断是否已满。
func (s *BoundedStack[T]) IsFull() bool { return s.Len() >= s.max }

// Clear 清空（不重置丢弃计数）。
func (s *BoundedStack[T]) Clear() {
	if s == nil {
		return
	}
	var zero T
	for i := range s.items {
		s.items[i] = zero
	}
	s.items = s.items[:0]
}

// Values 返回「栈底 → 栈顶」的副本。
func (s *BoundedStack[T]) Values() []T {
	if s == nil || len(s.items) == 0 {
		return nil
	}
	out := make([]T, len(s.items))
	copy(out, s.items)
	return out
}

// ForEach 按「栈顶 → 栈底」遍历。
func (s *BoundedStack[T]) ForEach(fn func(T)) {
	if s == nil || fn == nil {
		return
	}
	for i := len(s.items) - 1; i >= 0; i-- {
		fn(s.items[i])
	}
}

// Dropped 返回被丢弃（或被拒绝）的元素总数。
func (s *BoundedStack[T]) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped
}

// Policy 返回溢出策略。
func (s *BoundedStack[T]) Policy() xcontainer.OverflowPolicy {
	if s == nil {
		return xcontainer.DropNewest
	}
	return s.policy
}
