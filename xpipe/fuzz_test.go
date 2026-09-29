package xpipe_test

import (
	"sort"
	"testing"

	"github.com/indigo-apple/gokit/xpipe"
)

// decode 把 fuzz 的 []byte 输入解码为 []int。
//
// ⚠️ Go 1.21 的 fuzz **不支持 []int 之类的切片参数**（只支持 string / []byte / 标量），
// 因此所有「序列类」输入统一用 []byte 承载再在用例内解码。
func decode(b []byte) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// FuzzPipelineMatchesModel 用「朴素切片实现」作为参考模型，验证
// Map + Filter + Distinct + Batch 组合后的结果与模型完全一致。
//
// 这是流水线类代码最有效的正确性手段：模型三行就能写对，实现则有惰性/缓冲/并行等
// 一堆容易出错的机制；两者逐元素比对能抓住「漏元素 / 重复 / 顺序错 / 分批错」。
func FuzzPipelineMatchesModel(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5}, 2, true, 2)
	f.Add([]byte{}, 1, false, 3)
	f.Add([]byte{0, 0, 0}, 3, true, 1)
	f.Add([]byte{7}, 4, false, 5)

	f.Fuzz(func(t *testing.T, raw []byte, batchSize int, distinct bool, mod int) {
		data := decode(raw)
		if len(data) > 300 {
			data = data[:300]
		}
		if batchSize < 1 {
			batchSize = 1
		}
		if batchSize > 8 {
			batchSize = 8
		}
		if mod == 0 {
			mod = 1
		}
		if mod < 0 {
			mod = -mod
		}
		if mod > 50 {
			mod = 50
		}

		// ── 参考模型（朴素实现）──
		var want []int
		seen := map[int]bool{}
		for _, v := range data {
			mapped := v * 2
			if mapped%mod != 0 { // Filter
				continue
			}
			if distinct { // Distinct
				if seen[mapped] {
					continue
				}
				seen[mapped] = true
			}
			want = append(want, mapped)
		}

		// ── 管道实现 ──
		p := xpipe.Filter(
			xpipe.Map(xpipe.FromSlice(data), func(v int) int { return v * 2 }),
			func(v int) bool { return v%mod == 0 },
		)
		if distinct {
			p = xpipe.Distinct(p)
		}
		batches, err := xpipe.Collect(xpipe.Batch(p, batchSize))
		if err != nil {
			t.Fatalf("管道错误: %v", err)
		}

		// 批次结构不变量：非空、不超长、最后一维可能不满
		var flat []int
		for i, b := range batches {
			if len(b) == 0 {
				t.Fatalf("第 %d 批为空", i)
			}
			if len(b) > batchSize {
				t.Fatalf("第 %d 批超长: %d > %d", i, len(b), batchSize)
			}
			if i != len(batches)-1 && len(b) != batchSize {
				t.Fatalf("第 %d 批不满却非尾批: %d", i, len(b))
			}
			flat = append(flat, b...)
		}

		if len(flat) != len(want) {
			t.Fatalf("元素数不符: got %v, want %v (data=%v)", flat, want, data)
		}
		for i := range want {
			if flat[i] != want[i] {
				t.Fatalf("第 %d 个元素不符: got %v, want %v", i, flat, want)
			}
		}
	})
}

// FuzzWindowMatchesModel 验证滑动窗口与朴素模型一致（含 size/step 的边界组合，
// 例如 step > size、step == size、size == 1）。
func FuzzWindowMatchesModel(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5}, 3, 1)
	f.Add([]byte{1, 2, 3, 4, 5}, 2, 3)
	f.Add([]byte{1, 2, 3, 4, 5, 6}, 3, 3)
	f.Add([]byte{1, 2}, 3, 1)

	f.Fuzz(func(t *testing.T, raw []byte, size, step int) {
		data := decode(raw)
		if len(data) > 200 {
			data = data[:200]
		}
		if size < 1 {
			size = 1
		}
		if size > 10 {
			size = 10
		}
		if step < 1 {
			step = 1
		}
		if step > 10 {
			step = 10
		}

		// 模型：起点 i 从 0 开始，每次 i += step，只要 i+size <= len(data) 就产出一窗
		var want [][]int
		for i := 0; i+size <= len(data); i += step {
			w := make([]int, size)
			copy(w, data[i:i+size])
			want = append(want, w)
		}

		got, err := xpipe.Collect(xpipe.Window(xpipe.FromSlice(data), size, step))
		if err != nil {
			t.Fatalf("Window 错误: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("窗口数不符: got %v, want %v (data=%v size=%d step=%d)", got, want, data, size, step)
		}
		for i := range want {
			if !eq(got[i], want[i]) {
				t.Fatalf("第 %d 窗不符: got %v, want %v (size=%d step=%d data=%v)",
					i, got[i], want[i], size, step, data)
			}
		}
	})
}

// FuzzParallelOrder_OrderAndConservation 验证并行阶段的两条硬性不变量：
//
//	① ParallelOrdered 的输出**必须与输入同序**（逐位置相等）；
//	② Parallel（无序）的输出**必须与输入同多重集**（不丢、不重、不变形）。
func FuzzParallelOrder_OrderAndConservation(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 1)
	f.Add([]byte{1, 2, 3}, 4)
	f.Add([]byte{9}, 3)

	f.Fuzz(func(t *testing.T, raw []byte, workers int) {
		data := decode(raw)
		if len(data) > 200 {
			data = data[:200]
		}
		if workers < 1 {
			workers = 1
		}
		if workers > 8 {
			workers = 8
		}

		fn := func(v int) (int, error) { return v * 3, nil }

		ordered, err := xpipe.Collect(xpipe.ParallelOrdered(xpipe.FromSlice(data), workers, fn))
		if err != nil {
			t.Fatalf("ParallelOrdered: %v", err)
		}
		if len(ordered) != len(data) {
			t.Fatalf("保序模式长度不符: %d vs %d", len(ordered), len(data))
		}
		for i, v := range data {
			if ordered[i] != v*3 {
				t.Fatalf("保序模式第 %d 位不符: got %d, want %d (workers=%d)", i, ordered[i], v*3, workers)
			}
		}

		unordered, err := xpipe.Collect(xpipe.Parallel(xpipe.FromSlice(data), workers, fn))
		if err != nil {
			t.Fatalf("Parallel: %v", err)
		}
		want := make([]int, len(data))
		for i, v := range data {
			want[i] = v * 3
		}
		got := make([]int, len(unordered))
		copy(got, unordered)
		sort.Ints(got)
		sort.Ints(want)
		if len(got) != len(want) {
			t.Fatalf("无序模式长度不符: %d vs %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("无序模式多重集不符: got %v, want %v", got, want)
			}
		}
	})
}

// FuzzBatchConservation 批次整体守恒：无论 size 如何，展开后必须与原序列逐位相等。
func FuzzBatchConservation(f *testing.F) {
	f.Add([]byte{1, 2, 3}, 2)
	f.Add([]byte{}, 3)

	f.Fuzz(func(t *testing.T, raw []byte, size int) {
		data := decode(raw)
		if len(data) > 200 {
			data = data[:200]
		}
		if size < -3 {
			size = -3
		}
		if size > 8 {
			size = 8
		}
		eff := size
		if eff <= 0 {
			eff = 1 // 实现约定：size <= 0 视为 1
		}

		batches, err := xpipe.Collect(xpipe.Batch(xpipe.FromSlice(data), size))
		if err != nil {
			t.Fatalf("Batch: %v", err)
		}
		var flat []int
		for _, b := range batches {
			flat = append(flat, b...)
		}
		if len(flat) != len(data) {
			t.Fatalf("批展开长度不符: %d vs %d (size=%d)", len(flat), len(data), size)
		}
		for i := range data {
			if flat[i] != data[i] {
				t.Fatalf("批展开第 %d 位不符 (size=%d, 有效 %d)", i, size, eff)
			}
		}
	})
}
