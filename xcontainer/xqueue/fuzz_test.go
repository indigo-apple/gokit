package xqueue_test

import (
	"testing"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xcontainer/xqueue"
)

// 模型式模糊测试：把同一串操作同时作用于「被测实现」与「朴素参考模型」（切片），
// 逐步比对顺序与长度。这类测试比手写用例更容易发现 head 索引 / 压缩 / 取模的边界错误。
//
// 操作编码：每个 byte 是一次操作。
//
//	偶数 op → Push(int(op))
//	奇数 op → Pop()
func FuzzSliceQueueFIFO(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 2, 4})
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	// 覆盖压缩阈值（head >= 64 且 head*2 >= len）
	long := make([]byte, 300)
	for i := range long {
		if i%3 == 0 {
			long[i] = 1 // Pop
		} else {
			long[i] = byte(i % 256)
		}
	}
	f.Add(long)

	f.Fuzz(func(t *testing.T, ops []byte) {
		q := xqueue.New[int]()
		var model []int

		for i, op := range ops {
			if op%2 == 0 {
				v := int(op)
				if !q.Push(v) {
					t.Fatalf("第 %d 步: 无界队列 Push 不应失败", i)
				}
				model = append(model, v)
				continue
			}

			got, ok := q.Pop()
			if len(model) == 0 {
				if ok {
					t.Fatalf("第 %d 步: 空队列 Pop 应返回 false, got %d", i, got)
				}
				continue
			}
			want := model[0]
			model = model[1:]
			if !ok || got != want {
				t.Fatalf("第 %d 步: FIFO 错乱 got=%d,ok=%v want=%d", i, got, ok, want)
			}
			if q.Len() != len(model) {
				t.Fatalf("第 %d 步: Len=%d want=%d", i, q.Len(), len(model))
			}
		}

		// 收尾：Peek / Values / Drain 必须与模型一致
		if q.Len() != len(model) {
			t.Fatalf("结束时 Len=%d want=%d", q.Len(), len(model))
		}
		if len(model) > 0 {
			if got, ok := q.Peek(); !ok || got != model[0] {
				t.Fatalf("Peek=%d,ok=%v want=%d", got, ok, model[0])
			}
		}
		vals := q.Values()
		if len(vals) != len(model) {
			t.Fatalf("Values 长度=%d want=%d", len(vals), len(model))
		}
		for i := range vals {
			if vals[i] != model[i] {
				t.Fatalf("Values[%d]=%d want=%d", i, vals[i], model[i])
			}
		}
		drained := q.Drain()
		if len(drained) != len(model) {
			t.Fatalf("Drain 长度=%d want=%d", len(drained), len(model))
		}
		for i := range drained {
			if drained[i] != model[i] {
				t.Fatalf("Drain[%d]=%d want=%d", i, drained[i], model[i])
			}
		}
		if !q.IsEmpty() {
			t.Fatal("Drain 后应为空")
		}
	})
}

// Ring 的定容不变式：永不超过容量，满时 Push 必须失败，出队顺序恒为 FIFO。
func FuzzRingCapacityAndFIFO(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	// 针对容量 8 的绕环边界（恰好绕一圈 + 出界一步）
	f.Add([]byte{0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 1, 20, 3, 22, 5, 24})
	// 长时间一进一出
	alt := make([]byte, 400)
	for i := range alt {
		if i%2 == 0 {
			alt[i] = byte(i)
		} else {
			alt[i] = 1
		}
	}
	f.Add(alt)

	f.Fuzz(func(t *testing.T, ops []byte) {
		const capacity = 8
		q := xqueue.NewRing[int](capacity)
		var model []int

		for i, op := range ops {
			if op%2 == 0 {
				v := int(op)
				pushed := q.Push(v)
				if len(model) < capacity {
					if !pushed {
						t.Fatalf("第 %d 步: 未满却 Push 失败 (Len=%d)", i, q.Len())
					}
					model = append(model, v)
				} else if pushed {
					t.Fatalf("第 %d 步: 已满却 Push 成功", i)
				}
				// 不变式：长度永不超容量
				if q.Len() > capacity {
					t.Fatalf("第 %d 步: Len=%d 超过容量 %d", i, q.Len(), capacity)
				}
				if q.Len() != len(model) {
					t.Fatalf("第 %d 步: Len=%d want=%d", i, q.Len(), len(model))
				}
				if (q.Len() == capacity) != q.IsFull() {
					t.Fatalf("第 %d 步: IsFull 与 Len 不一致", i)
				}
				continue
			}

			got, ok := q.Pop()
			if len(model) == 0 {
				if ok {
					t.Fatalf("第 %d 步: 空队列 Pop 应失败, got %d", i, got)
				}
				continue
			}
			want := model[0]
			model = model[1:]
			if !ok || got != want {
				t.Fatalf("第 %d 步: FIFO 错乱 got=%d,ok=%v want=%d", i, got, ok, want)
			}
		}

		vals := q.Values()
		if len(vals) != len(model) {
			t.Fatalf("Values 长度=%d want=%d", len(vals), len(model))
		}
		for i := range vals {
			if vals[i] != model[i] {
				t.Fatalf("Values[%d]=%d want=%d", i, vals[i], model[i])
			}
		}
	})
}

// Bounded 的容量与丢弃计数不变式（DropNewest / DropOldest 两种策略）。
//
// 操作编码：偶数 → Push，奇数 → Pop。
func FuzzBoundedDropPolicies(f *testing.F) {
	f.Add([]byte{0, 2, 4, 6, 8, 10, 12, 14})
	f.Add([]byte{0, 2, 4})
	f.Add([]byte{1, 1, 1})
	long := make([]byte, 200)
	for i := range long {
		long[i] = byte(i * 3)
	}
	f.Add(long)

	f.Fuzz(func(t *testing.T, ops []byte) {
		const capacity = 4

		// DropNewest：保留最早的 capacity 个元素
		qn := xqueue.NewBounded[int](capacity, xcontainer.DropNewest)
		// DropOldest：保留最新的 capacity 个元素
		qo := xqueue.NewBounded[int](capacity, xcontainer.DropOldest)

		for i, op := range ops {
			v := int(op)
			if op%2 == 0 {
				if err := qn.Push(v); err != nil {
					t.Fatalf("第 %d 步: DropNewest Push 出错: %v", i, err)
				}
				if err := qo.Push(v); err != nil {
					t.Fatalf("第 %d 步: DropOldest Push 出错: %v", i, err)
				}
			} else {
				qn.Pop()
				qo.Pop()
			}

			// 核心不变式：长度永不超容量
			if qn.Len() > capacity {
				t.Fatalf("第 %d 步: DropNewest Len=%d 超过容量", i, qn.Len())
			}
			if qo.Len() > capacity {
				t.Fatalf("第 %d 步: DropOldest Len=%d 超过容量", i, qo.Len())
			}
			// 丢弃计数单调不减
			if qn.Dropped() < 0 || qo.Dropped() < 0 {
				t.Fatalf("第 %d 步: 丢弃计数为负", i)
			}
		}

		// 排空后两个队列都应为空
		qn.Drain()
		qo.Drain()
		if !qn.IsEmpty() || !qo.IsEmpty() {
			t.Fatal("Drain 后应为空")
		}
	})
}
