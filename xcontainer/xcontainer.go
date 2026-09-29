// Package xcontainer 定义容器子包共享的词汇：溢出策略、集合接口与哨兵错误。
//
// 本包只放「跨容器共享的最小约定」，具体实现分散在子包中：
//
//	xcontainer/xstack  —— 栈
//	xcontainer/xqueue  —— 队列（切片 / 环形 / 有界）
//
// 并发安全：本包只有常量、接口与哨兵错误，可并发使用。
package xcontainer

import "github.com/zhangwenxing/gokit/xerr"

// OverflowPolicy 定义容器容量已满时的处理策略。
//
// 生产环境**必须显式选择策略**：无界容器是内存泄漏的常见来源。
type OverflowPolicy int

const (
	// Block 阻塞等待，直到有空位（仅对有阻塞语义的实现生效，如 BoundedQueue.PushCtx）。
	// 非阻塞调用（Push）在满时返回 ErrWouldBlock。
	Block OverflowPolicy = iota

	// DropOldest 丢弃最旧的元素以腾出空间。
	//
	// 对队列即丢弃队首；对栈即丢弃栈底（O(n) 搬移，因为这些实现底层是切片）。
	DropOldest

	// DropNewest 拒绝新元素（保留已有数据）。
	DropNewest

	// Fail 返回错误，由调用方决定如何处理。
	Fail
)

// String 实现 fmt.Stringer。
func (p OverflowPolicy) String() string {
	switch p {
	case Block:
		return "Block"
	case DropOldest:
		return "DropOldest"
	case DropNewest:
		return "DropNewest"
	case Fail:
		return "Fail"
	default:
		return "Unknown"
	}
}

// Valid 判断策略值是否合法。
func (p OverflowPolicy) Valid() bool {
	return p >= Block && p <= Fail
}

// Norm 把非法策略归一化为安全默认值（DropNewest），避免 panic。
func (p OverflowPolicy) Norm() OverflowPolicy {
	if p.Valid() {
		return p
	}
	return DropNewest
}

// Collection 是只读集合的最小接口。
type Collection[T any] interface {
	// Len 返回元素个数。
	Len() int
	// IsEmpty 判断是否为空。
	IsEmpty() bool
	// Clear 清空。
	Clear()
	// Values 返回全部元素（顺序由具体实现定义）。
	Values() []T
	// ForEach 遍历元素。
	ForEach(fn func(T))
}

// Queue 是先进先出队列的最小接口。
type Queue[T any] interface {
	Collection[T]
	// Push 入队；返回 false 表示被策略拒绝（如队列已满且策略为 DropNewest）。
	Push(v T) bool
	// Pop 出队；返回 false 表示队列为空。
	Pop() (T, bool)
	// Peek 查看队首但不出队。
	Peek() (T, bool)
}

// 容器相关的哨兵错误。可用 errors.Is 判定，也可用 xerr.CodeOf 提取错误码。
var (
	// ErrFull 表示容器已满且策略为 Fail。
	ErrFull = xerr.NewC(xerr.CodeResourceExhausted, "xcontainer: container is full")

	// ErrWouldBlock 表示容器已满且策略为 Block，但当前调用不支持阻塞。
	// 请改用带 context 的 PushCtx / PopCtx。
	ErrWouldBlock = xerr.NewC(xerr.CodeUnavailable, "xcontainer: would block (use PushCtx/PopCtx)")

	// ErrClosed 表示容器已关闭，无法再写入。
	ErrClosed = xerr.NewC(xerr.CodeStateInvalid, "xcontainer: container is closed")

	// ErrEmpty 表示容器为空（需要错误而非 bool 的场景使用）。
	ErrEmpty = xerr.NewC(xerr.CodeNotFound, "xcontainer: container is empty")
)
