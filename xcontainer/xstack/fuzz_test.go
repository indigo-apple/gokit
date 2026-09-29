package xstack_test

import (
	"testing"

	"github.com/indigo-apple/gokit/xcontainer"
	"github.com/indigo-apple/gokit/xcontainer/xstack"
)

// 模型式模糊测试：被测栈与朴素参考模型（切片，末尾即栈顶）逐步比对。
//
// 操作编码：
//
//	op%3 == 0 → Push(int(op))
//	op%3 == 1 → Pop()
//	op%3 == 2 → Peek()
func FuzzStackLIFO(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 3, 6})
	f.Add([]byte{0, 3, 1, 6, 9})
	long := make([]byte, 400)
	for i := range long {
		long[i] = byte(i)
	}
	f.Add(long)

	f.Fuzz(func(t *testing.T, ops []byte) {
		s := xstack.New[int]()
		var model []int

		for i, op := range ops {
			switch op % 3 {
			case 0:
				v := int(op)
				s.Push(v)
				model = append(model, v)

			case 1:
				got, ok := s.Pop()
				if len(model) == 0 {
					if ok {
						t.Fatalf("第 %d 步: 空栈 Pop 应返回 false, got %d", i, got)
					}
					continue
				}
				want := model[len(model)-1]
				model = model[:len(model)-1]
				if !ok || got != want {
					t.Fatalf("第 %d 步: LIFO 错乱 got=%d,ok=%v want=%d", i, got, ok, want)
				}

			default:
				got, ok := s.Peek()
				if len(model) == 0 {
					if ok {
						t.Fatalf("第 %d 步: 空栈 Peek 应返回 false", i)
					}
					continue
				}
				want := model[len(model)-1]
				if !ok || got != want {
					t.Fatalf("第 %d 步: Peek=%d,ok=%v want=%d", i, got, ok, want)
				}
			}

			if s.Len() != len(model) {
				t.Fatalf("第 %d 步: Len=%d want=%d", i, s.Len(), len(model))
			}
		}

		// 收尾：Values（底→顶）与 ForEach（顶→底）必须与模型一致
		vals := s.Values()
		if len(vals) != len(model) {
			t.Fatalf("Values 长度=%d want=%d", len(vals), len(model))
		}
		for i := range vals {
			if vals[i] != model[i] {
				t.Fatalf("Values[%d]=%d want=%d", i, vals[i], model[i])
			}
		}

		var asc []int
		s.ForEach(func(v int) { asc = append(asc, v) })
		if len(asc) != len(model) {
			t.Fatalf("ForEach 长度=%d want=%d", len(asc), len(model))
		}
		for i := range asc {
			if want := model[len(model)-1-i]; asc[i] != want {
				t.Fatalf("ForEach[%d]=%d want=%d", i, asc[i], want)
			}
		}

		// PeekN 边界
		for n := -1; n <= len(model); n++ {
			got, ok := s.PeekN(n)
			if n < 0 || n >= len(model) {
				if ok {
					t.Fatalf("PeekN(%d) 越界却返回 ok", n)
				}
				continue
			}
			if want := model[len(model)-1-n]; !ok || got != want {
				t.Fatalf("PeekN(%d)=%d,ok=%v want=%d", n, got, ok, want)
			}
		}
	})
}

// 有界栈不变式：长度永不超上限，且「输出长度 + 丢弃数 = 输入总数」守恒。
func FuzzBoundedStackConservation(f *testing.F) {
	f.Add([]byte{0, 2, 4, 6, 8})
	f.Add([]byte{1, 3, 5, 7, 9, 11})
	f.Add(seq(300))

	f.Fuzz(func(t *testing.T, ops []byte) {
		const maxSize = 5

		for _, policy := range []xcontainer.OverflowPolicy{xcontainer.DropNewest, xcontainer.DropOldest} {
			s := xstack.NewBounded[int](maxSize, policy)
			pushed := 0
			poppedInLoop := 0

			for i, op := range ops {
				if op%3 == 1 {
					if _, ok := s.Pop(); ok { // 只做入/出混合，不校验内容（上面已有 LIFO 模型测试）
						poppedInLoop++
					}
					continue
				}
				pushed++
				err := s.Push(int(op))
				// DropNewest / DropOldest 都不返回错误
				if err != nil {
					t.Fatalf("第 %d 步 (策略 %v): Push 出错: %v", i, policy, err)
				}
				if s.Len() > maxSize {
					t.Fatalf("第 %d 步 (策略 %v): Len=%d 超过上限 %d", i, policy, s.Len(), maxSize)
				}
			}

			// 守恒：入栈总数 = 仍在栈中 + 已丢弃 + 已弹出
			remaining := s.Len()
			dropped := s.Dropped()
			drained := 0
			for {
				if _, ok := s.Pop(); !ok {
					break
				}
				drained++
			}
			if drained != remaining {
				t.Fatalf("策略 %v: 排空数量 %d != 排空前 Len %d", policy, drained, remaining)
			}
			if int64(remaining)+dropped+int64(poppedInLoop) != int64(pushed) {
				t.Fatalf("策略 %v: 元素不守恒: 剩余 %d + 丢弃 %d + 已弹出 %d != 入栈 %d",
					policy, remaining, dropped, poppedInLoop, pushed)
			}
		}
	})
}

// seq 生成 0..n-1 的字节序列（用于 fuzz 种子）。
func seq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
