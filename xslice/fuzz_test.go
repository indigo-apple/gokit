package xslice_test

import (
	"testing"

	"github.com/indigo-apple/gokit/xslice"
)

// FuzzDedup 验证去重的三条不变式：
//  1. 结果内无重复元素；
//  2. 保持「首次出现」的顺序（稳定）；
//  3. 结果必须是输入的子集（不凭空产生元素）。
func FuzzDedup(f *testing.F) {
	f.Add([]byte{1, 2, 1, 3})
	f.Add([]byte{})
	f.Add([]byte{7, 7, 7})
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		s := make([]int, len(data))
		for i, b := range data {
			s[i] = int(b % 8)
		}

		got := xslice.Dedup(s)

		// 1. 无重复
		seen := make(map[int]struct{}, len(got))
		for _, v := range got {
			if _, dup := seen[v]; dup {
				t.Fatalf("Dedup 结果仍有重复: %v (输入 %v)", got, s)
			}
			seen[v] = struct{}{}
		}

		// 2. 幂等且稳定
		again := xslice.Dedup(got)
		if !xslice.Equal(got, again) {
			t.Fatalf("Dedup 不幂等: %v → %v", got, again)
		}

		// 3. 结果是输入的子集
		if !xslice.IsSubset(got, s) {
			t.Fatalf("Dedup 产生了输入中不存在的元素: 输入 %v 结果 %v", s, got)
		}

		// 4. 与 DedupInPlace 结果一致（后者会改写入参，因此用副本）
		cp := xslice.Clone(s)
		if !xslice.Equal(got, xslice.DedupInPlace(cp)) {
			t.Fatalf("Dedup 与 DedupInPlace 结果不一致: %v vs %v", got, cp)
		}

		// 5. DedupLast 的元素集合应与 Dedup 相同
		if !xslice.EqualUnordered(got, xslice.DedupLast(s)) {
			t.Fatalf("Dedup 与 DedupLast 元素集合不一致: %v vs %v", got, xslice.DedupLast(s))
		}
	})
}

// FuzzSetOperations 验证集合运算的基本代数性质。
func FuzzSetOperations(f *testing.F) {
	f.Add([]byte{1, 2, 3}, []byte{2, 3, 4})
	f.Add([]byte{}, []byte{1})
	f.Add([]byte{5}, []byte{5})
	f.Fuzz(func(t *testing.T, da, db []byte) {
		a := make([]int, len(da))
		for i, v := range da {
			a[i] = int(v % 6)
		}
		b := make([]int, len(db))
		for i, v := range db {
			b[i] = int(v % 6)
		}

		union := xslice.Union(a, b)
		inter := xslice.Intersect(a, b)

		// 交集中的元素必须同时出现在并集两侧
		if !xslice.IsSubset(inter, union) {
			t.Fatalf("交集不是并集的子集: a=%v b=%v union=%v inter=%v", a, b, union, inter)
		}
		if !xslice.IsSubset(inter, a) || !xslice.IsSubset(inter, b) {
			t.Fatalf("交集不是某一侧的子集: a=%v b=%v inter=%v", a, b, inter)
		}
		if !xslice.IsSubset(a, union) || !xslice.IsSubset(b, union) {
			t.Fatalf("并集未包含全部元素: a=%v b=%v union=%v", a, b, union)
		}

		// difference(a,b) 与 intersect(a,b) 互斥且覆盖 a
		diff := xslice.Difference(a, b)
		if xslice.Overlaps(diff, inter) {
			t.Fatalf("差集与交集应互斥: a=%v b=%v diff=%v inter=%v", a, b, diff, inter)
		}
		if !xslice.EqualUnordered(xslice.Union(diff, inter), xslice.Dedup(a)) {
			t.Fatalf("差集 ∪ 交集 != a 的去重: a=%v diff=%v inter=%v", a, diff, inter)
		}

		// 对称差 = (a\b) ∪ (b\a)
		sym := xslice.SymmetricDifference(a, b)
		want := xslice.Union(xslice.Difference(a, b), xslice.Difference(b, a))
		if !xslice.EqualUnordered(sym, want) {
			t.Fatalf("对称差不一致: %v vs %v", sym, want)
		}
	})
}

// FuzzRotateAndWindow 验证切片切分操作不越界、不丢元素。
func FuzzRotateAndWindow(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5}, 2)
	f.Add([]byte{1}, 0)
	f.Add([]byte{}, -1)
	f.Add([]byte{1, 2}, 100)
	f.Fuzz(func(t *testing.T, data []byte, n int) {
		s := make([]int, len(data))
		for i, v := range data {
			s[i] = int(v)
		}

		// Rotate 只改变顺序，不改变元素集合与长度
		rot := xslice.Rotate(s, n)
		if len(rot) != len(s) {
			t.Fatalf("Rotate 改变了长度: %d → %d", len(s), len(rot))
		}
		if !xslice.EqualUnordered(rot, s) {
			t.Fatalf("Rotate 改变了元素集合: %v → %v", s, rot)
		}
		if len(s) > 0 {
			if !xslice.Equal(xslice.Rotate(rot, -n), s) {
				t.Fatalf("Rotate 不满足可逆性: %v n=%d", s, n)
			}
		}

		// Chunk 拼接后应还原原切片（size 必须 >= 1：
		// Chunk 对非法 size 按文档返回 nil，因此不变式不适用于该分支）
		size := n % 5
		if size < 0 {
			size = -size
		}
		size++
		if joined := xslice.Flatten(xslice.Chunk(s, size)); !xslice.Equal(joined, s) {
			t.Fatalf("Chunk 拼接未还原: %v size=%d → %v", s, size, joined)
		}

		// size <= 0 时 Chunk 必须返回 nil 而不是 panic 或半截结果
		if got := xslice.Chunk(s, 0); got != nil {
			t.Fatalf("Chunk(size=0) 应为 nil, got %v", got)
		}
		if got := xslice.Chunk(s, -3); got != nil {
			t.Fatalf("Chunk(size<0) 应为 nil, got %v", got)
		}

		// SplitAt 两段拼接应还原
		l, r := xslice.SplitAt(s, n)
		if !xslice.Equal(xslice.Concat(l, r), s) {
			t.Fatalf("SplitAt 拼接未还原: %v n=%d", s, n)
		}

		// Window 每个窗口长度固定
		if w := xslice.Window(s, size); w != nil {
			for i, win := range w {
				if len(win) != size {
					t.Fatalf("Window[%d] 长度 = %d, want %d", i, len(win), size)
				}
			}
		}
	})
}
