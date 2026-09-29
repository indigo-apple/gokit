package xkeyed_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/indigo-apple/gokit/xkeyed"
)

// FuzzShardOf 守卫分片选型：
//  1. 结果必须落在 [0, shards) 内（越界会造成 index panic）；
//  2. 必须**可复现**（同一 key 永远同分片 —— 否则「同 key 串行」直接失效）；
//  3. shards <= 0 必须安全退化为 1。
func FuzzShardOf(f *testing.F) {
	for _, s := range []string{"", "a", "order:1", "中文 key", "\x00", "very-long-key-xxxxxxxxxxxxxxxxxxxxxxxx"} {
		f.Add(s, 1)
		f.Add(s, 16)
	}
	f.Fuzz(func(t *testing.T, key string, shards int) {
		if shards > 4096 {
			shards = 4096 // 限制规模，避免测试自身变慢
		}
		got := xkeyed.ShardOf(key, shards)

		eff := shards
		if eff <= 0 {
			eff = 1
		}
		if got < 0 || got >= eff {
			t.Fatalf("ShardOf(%q, %d) = %d 越界", key, shards, got)
		}
		if again := xkeyed.ShardOf(key, shards); again != got {
			t.Fatalf("ShardOf 不可复现: %d vs %d", got, again)
		}
		if xkeyed.HashKey(key) != xkeyed.HashKey(key) {
			t.Fatal("HashKey 不可复现")
		}
	})
}

// FuzzKeyedSerialOrder 是「同 key 串行管道」最核心的不变式：
//
//	对随机生成的 (key, 序号) 提交序列，必须满足
//	  ① 每个 key 上的执行顺序与该 key 的提交顺序完全一致；
//	  ② 每个 key 上任意时刻最多只有 1 个任务在执行；
//	  ③ 不丢不重（执行总数 == 提交总数）。
func FuzzKeyedSerialOrder(f *testing.F) {
	f.Add(1, 3, 12)
	f.Add(4, 8, 40)
	f.Add(16, 2, 20)

	f.Fuzz(func(t *testing.T, shards, numKeys, ops int) {
		if shards < 1 {
			shards = 1
		}
		if shards > 32 {
			shards = 32
		}
		if numKeys < 1 {
			numKeys = 1
		}
		if numKeys > 16 {
			numKeys = 16
		}
		if ops < 1 {
			ops = 1
		}
		if ops > 200 {
			ops = 200
		}

		p := xkeyed.New(xkeyed.WithShards(shards), xkeyed.WithQueueSize(ops+1))
		ctx := context.Background()

		type state struct {
			order    []int
			inFlight int
			maxOver  int
		}
		states := make([]*state, numKeys)
		mu := make([]sync.Mutex, numKeys)
		for i := range states {
			states[i] = &state{}
		}

		submitted := make([]int, numKeys)
		for i := 0; i < ops; i++ {
			ki := i % numKeys
			seq := submitted[ki]
			submitted[ki]++

			err := p.Submit(ctx, fmt.Sprintf("k%d", ki), func(context.Context) error {
				mu[ki].Lock()
				s := states[ki]
				s.inFlight++
				if s.inFlight > s.maxOver {
					s.maxOver = s.inFlight
				}
				s.order = append(s.order, seq)
				s.inFlight--
				mu[ki].Unlock()
				return nil
			})
			if err != nil {
				t.Fatalf("Submit 失败: %v", err)
			}
		}

		if err := p.Wait(ctx); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		for ki, s := range states {
			if s.maxOver > 1 {
				t.Fatalf("key k%d 并发重叠 = %d（同 key 串行保证被破坏）", ki, s.maxOver)
			}
			if len(s.order) != submitted[ki] {
				t.Fatalf("key k%d 执行 %d 次, 提交 %d 次（丢失或重复）", ki, len(s.order), submitted[ki])
			}
			for i, v := range s.order {
				if v != i {
					t.Fatalf("key k%d 顺序错乱: 第 %d 位是 %d", ki, i, v)
				}
			}
		}
		if st := p.Stats(); st.Completed+st.Failed+st.Panics != int64(ops) {
			t.Fatalf("统计对不上: %+v (ops=%d)", st, ops)
		}
	})
}
