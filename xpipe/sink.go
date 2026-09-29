package xpipe

import (
	"sort"
	"sync"

	"github.com/zhangwenxing/gokit/xmap"
)

// ---------------------------------------------------------------------------
// 终结操作（Sink）
// ---------------------------------------------------------------------------
//
// 统一约定：
//   - 每个终结操作都会**独占消费**这条管道（重复终结返回 ErrConsumed）；
//   - 返回前统一 cancel 管道内部 ctx，因此 Buffer/Parallel/FromFunc 起的 goroutine
//     保证在返回时已经退出（**无 goroutine 泄漏**是硬性保证，有测试覆盖）；
//   - Collect 错误策略下，终结操作会把过程中记录的错误一并返回。

// finisher 统一收尾：主错误优先，其次按策略返回聚合错误。
func finisher[T any](p *Pipe[T], main error) error {
	if main != nil {
		return main
	}
	if p.cfg.policy == PolicyCollect {
		return p.box.err()
	}
	return nil
}

// drainLoop 是「逐个消费直到流结束」的公共循环。
func drainLoop[T any](p *Pipe[T], visit func(v T) error) error {
	for {
		v, more, err := p.next()
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if err := visit(v); err != nil {
			return err
		}
	}
}

// Drain 逐个消费元素；fn 返回错误立即中止并返回该错误。
func Drain[T any](p *Pipe[T], fn func(T) error) error {
	release, err := p.begin()
	if err != nil {
		return err
	}
	defer release()
	if fn == nil {
		return ErrNilFunc
	}
	err = drainLoop(p, func(v T) error {
		var e error
		if gerr := guard(func() { e = fn(v) }); gerr != nil {
			return gerr // fn 内 panic → 走 guard 的包装
		}
		return e // 用户的错误**原样**返回，不改写也不包装（保住 errors.Is 语义）
	})
	return finisher(p, err)
}

// ForEach 是 Drain 的别名，但 fn 不返回错误（习惯命名）。
func ForEach[T any](p *Pipe[T], fn func(T)) error {
	if fn == nil {
		release, err := p.begin()
		if err != nil {
			return err
		}
		release()
		return ErrNilFunc
	}
	return Drain(p, func(v T) error {
		fn(v)
		return nil
	})
}

// Collect 收集全部元素（⚠️ 注意内存：无限流会 OOM，请用 CollectN 或 Drain）。
func Collect[T any](p *Pipe[T]) ([]T, error) {
	release, err := p.begin()
	if err != nil {
		return nil, err
	}
	defer release()

	var out []T
	err = drainLoop(p, func(v T) error {
		out = append(out, v)
		return nil
	})
	return out, finisher(p, err)
}

// ToSlice 是 Collect 的别名。
func ToSlice[T any](p *Pipe[T]) ([]T, error) { return Collect(p) }

// CollectN 最多收集 n 个元素后停止（n <= 0 返回空切片）。
func CollectN[T any](p *Pipe[T], n int) ([]T, error) {
	release, err := p.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	if n <= 0 {
		return []T{}, nil
	}

	out := make([]T, 0, n)
	for len(out) < n {
		v, more, err := p.next()
		if err != nil {
			return out, finisher(p, err)
		}
		if !more {
			break
		}
		out = append(out, v)
	}
	return out, finisher(p, nil)
}

// Reduce 用 fn 折叠全部元素（init 为初始值）。
func Reduce[T, A any](p *Pipe[T], init A, fn func(acc A, v T) A) (A, error) {
	release, err := p.begin()
	if err != nil {
		var zero A
		return zero, err
	}
	defer release()
	acc := init
	if fn == nil {
		return acc, ErrNilFunc
	}
	err = drainLoop(p, func(v T) error {
		return guard(func() { acc = fn(acc, v) })
	})
	if err != nil {
		var zero A
		return zero, finisher(p, err)
	}
	return acc, finisher(p, nil)
}

// Count 统计元素个数。
func Count[T any](p *Pipe[T]) (int, error) {
	release, err := p.begin()
	if err != nil {
		return 0, err
	}
	defer release()
	n := 0
	err = drainLoop(p, func(T) error { n++; return nil })
	return n, finisher(p, err)
}

// First 返回第一个元素。
//
// 拿到第一个元素后立即取消（不会白拉剩余元素 —— 对 Buffer/Parallel 上游尤其重要）。
func First[T any](p *Pipe[T]) (T, bool, error) {
	release, err := p.begin()
	if err != nil {
		var zero T
		return zero, false, err
	}
	defer release()

	v, more, err := p.next()
	if err != nil {
		var zero T
		return zero, false, finisher(p, err)
	}
	if !more {
		var zero T
		return zero, false, finisher(p, nil)
	}
	return v, true, finisher(p, nil)
}

// Last 返回最后一个元素（必须读完整个流）。
func Last[T any](p *Pipe[T]) (T, bool, error) {
	release, err := p.begin()
	if err != nil {
		var zero T
		return zero, false, err
	}
	defer release()

	var (
		last T
		got  bool
	)
	err = drainLoop(p, func(v T) error { last, got = v, true; return nil })
	return last, got, finisher(p, err)
}

// Any 是否存在满足 pred 的元素（命中即短路返回，不读完整个流）。
func Any[T any](p *Pipe[T], pred func(T) bool) (bool, error) {
	release, err := p.begin()
	if err != nil {
		return false, err
	}
	defer release()
	if pred == nil {
		return false, ErrNilFunc
	}

	err = drainLoop(p, func(v T) error {
		var ok bool
		if gerr := guard(func() { ok = pred(v) }); gerr != nil {
			return gerr
		}
		if ok {
			return errStop // 短路：命中即停，不读完整个流
		}
		return nil
	})
	if err == errStop {
		return true, nil
	}
	if err != nil {
		return false, finisher(p, err)
	}
	return false, finisher(p, nil)
}

// All 是否所有元素都满足 pred（遇到不满足即短路）。
func All[T any](p *Pipe[T], pred func(T) bool) (bool, error) {
	release, err := p.begin()
	if err != nil {
		return false, err
	}
	defer release()
	if pred == nil {
		return false, ErrNilFunc
	}

	err = drainLoop(p, func(v T) error {
		var ok bool
		if gerr := guard(func() { ok = pred(v) }); gerr != nil {
			return gerr
		}
		if !ok {
			return errStop
		}
		return nil
	})
	if err == errStop {
		return false, nil
	}
	if err != nil {
		return false, finisher(p, err)
	}
	return true, finisher(p, nil)
}

// GroupBy 按 key 分组（分组内部保持原顺序）。
//
// ⚠️ 需要保存全部元素（内存 O(n)）；键的遍历顺序请自行排序（map 无序）。
func GroupBy[T any, K comparable](p *Pipe[T], key func(T) K) (map[K][]T, error) {
	release, err := p.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	if key == nil {
		return nil, ErrNilFunc
	}

	out := make(map[K][]T)
	err = drainLoop(p, func(v T) error {
		var k K
		if gerr := guard(func() { k = key(v) }); gerr != nil {
			return gerr
		}
		out[k] = append(out[k], v)
		return nil
	})
	return out, finisher(p, err)
}

// GroupByCount 只统计每组的数量（不需要保存元素，内存 O(组数)）。
func GroupByCount[T any, K comparable](p *Pipe[T], key func(T) K) (map[K]int, error) {
	release, err := p.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	if key == nil {
		return nil, ErrNilFunc
	}

	out := make(map[K]int)
	err = drainLoop(p, func(v T) error {
		var k K
		if gerr := guard(func() { k = key(v) }); gerr != nil {
			return gerr
		}
		out[k]++
		return nil
	})
	return out, finisher(p, err)
}

// ToMap 按 key 把元素收成 map。
//
// 默认语义：**后到者覆盖先到者**。需要合并（如求和/拼接）请传 merge：
//
//	m, err := xpipe.ToMap(src, func(u User) int { return u.ID }, func(old, new User) User {
//	    old.Tags = append(old.Tags, new.Tags...); return old
//	})
//
// 需要保留重复项请用 GroupBy。
func ToMap[T any, K comparable](p *Pipe[T], key func(T) K, merge ...func(old, new T) T) (map[K]T, error) {
	release, err := p.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	if key == nil {
		return nil, ErrNilFunc
	}
	var merger func(old, new T) T
	if len(merge) > 0 {
		merger = merge[0]
	}

	out := make(map[K]T)
	err = drainLoop(p, func(v T) error {
		var k K
		if gerr := guard(func() { k = key(v) }); gerr != nil {
			return gerr
		}
		if merger != nil {
			if old, ok := out[k]; ok {
				var merged T
				if gerr := guard(func() { merged = merger(old, v) }); gerr != nil {
					return gerr
				}
				out[k] = merged
				return nil
			}
		}
		out[k] = v
		return nil
	})
	return out, finisher(p, err)
}

// errStop 是终结操作的内部短路信号（不对外暴露）。
var errStop = &stopError{}

type stopError struct{}

func (*stopError) Error() string { return "xpipe: stop" }

// ---------------------------------------------------------------------------
// 扇出 / 汇聚
// ---------------------------------------------------------------------------

// Broadcast 把每个元素**广播**给 n 个分支（分支由调用方用下标区分）。
//
// 为什么不是「返回 n 条 *Pipe」：拉取式管道是单次消费的，要把一条流复制成 n 条
// 就必须为每条分支缓存整个流（内存无界）。用回调形式可以在**常量内存**下完成广播。
// 需要各自独立处理，请在 sink 里把数据写入自己的 channel/队列。
func Broadcast[T any](p *Pipe[T], n int, sink func(branch int, v T) error) error {
	release, err := p.begin()
	if err != nil {
		return err
	}
	defer release()
	if sink == nil {
		return ErrNilFunc
	}
	if n <= 0 {
		n = 0
	}
	err = drainLoop(p, func(v T) error {
		for i := 0; i < n; i++ {
			i := i
			var e error
			if gerr := guard(func() { e = sink(i, v) }); gerr != nil {
				return gerr
			}
			if e != nil {
				return e
			}
		}
		return nil
	})
	return finisher(p, err)
}

// FanOutTo 把元素**轮询**分发给 n 个分支（每个元素只去一个分支）。
//
// 与 Consumer Group 同义：用于「同一批数据分给多个处理者但不能重复处理」。
func FanOutTo[T any](p *Pipe[T], n int, sink func(branch int, v T) error) error {
	release, err := p.begin()
	if err != nil {
		return err
	}
	defer release()
	if sink == nil {
		return ErrNilFunc
	}
	if n <= 0 {
		n = 1
	}
	idx := 0
	err = drainLoop(p, func(v T) error {
		b := idx % n
		idx++
		var e error
		if gerr := guard(func() { e = sink(b, v) }); gerr != nil {
			return gerr
		}
		return e
	})
	return finisher(p, err)
}

// SplitTo 按 pred 把元素分流到两个 sink（谓词为真的进 yes、为假的进 no）。
//
// 返回 (yes 计数, no 计数)。
func SplitTo[T any](p *Pipe[T], pred func(T) bool, yes, no func(T) error) (int, int, error) {
	release, err := p.begin()
	if err != nil {
		return 0, 0, err
	}
	defer release()
	if pred == nil {
		return 0, 0, ErrNilFunc
	}

	var cy, cn int
	err = drainLoop(p, func(v T) error {
		var ok bool
		if gerr := guard(func() { ok = pred(v) }); gerr != nil {
			return gerr
		}
		fn := no
		if ok {
			fn = yes
		}
		if fn != nil {
			var e error
			if gerr := guard(func() { e = fn(v) }); gerr != nil {
				return gerr
			}
			if e != nil {
				return e
			}
		}
		if ok {
			cy++
		} else {
			cn++
		}
		return nil
	})
	return cy, cn, finisher(p, err)
}

// Merge 把多条管道**并发**合并为一条（fan-in，顺序不确定）。
//
// 实现：为每条上游起 1 个 pump goroutine（带 ctx 取消），共 n 个 goroutine；
// 终结操作一 cancel 全部退出。
//
// 需要确定顺序请用 Chain（顺序串联）或先各自 ParallelOrdered 再 Merge。
func Merge[T any](ps ...*Pipe[T]) *Pipe[T] {
	ps = filterNonNil(ps)
	if len(ps) == 0 {
		return Empty[T]()
	}
	if len(ps) == 1 {
		return ps[0]
	}

	cb := newCtxBox(ps[0].ctx())
	out := &Pipe[T]{
		header: header{name: "Merge(" + itoa(len(ps)) + ")", cb: cb, box: ps[0].box, cfg: ps[0].cfg},
	}
	ctx := cb.get()

	type item struct {
		v    T
		more bool
		err  error
	}
	ch := make(chan item, out.cfg.buffer)

	var wg sync.WaitGroup
	for _, up := range ps {
		cb.adopt(up.cb) // 接管上游已有的派生 ctx（避免其 goroutine 泄漏）
		up.cb = cb      // 全部切到共享 ctx 槽位
		up.consumed.Store(true)
		wg.Add(1)
		go func(up *Pipe[T]) {
			defer wg.Done()
			for {
				v, more, err := up.next()
				if err != nil {
					// 错误用一条标记项带出（读侧据此上报），随后退出
					select {
					case ch <- item{err: err}:
					case <-ctx.Done():
					}
					return
				}
				if !more {
					// ★ 上游结束**不能**发「结束标记」给读侧 ——
					// 那会被理解成「整条合并流结束」，任何一个上游先耗完就截断全部
					// （真实缺陷：Merge 只输出了最后一个源的元素）。
					// 正确做法是直接返回，靠所有 pump 退出后 close(ch) 表示整体结束。
					return
				}
				select {
				case ch <- item{v: v, more: true}:
				case <-ctx.Done():
					return
				}
			}
		}(up)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	out.pull = func() (T, bool, error) {
		var zero T
		select {
		case it, ok := <-ch:
			if !ok {
				// 全部上游都结束了
				return zero, false, nil
			}
			if it.err != nil {
				return zero, false, it.err
			}
			return it.v, true, nil
		case <-ctx.Done():
			return zero, false, ctx.Err()
		}
	}
	return out
}

// Chain 顺序串联多条管道（前一条**完全耗尽**后再拉下一条）。
//
// 与 Merge 的区别：Chain 顺序确定、只用 1 个 goroutine（不并发）；
// Merge 并发但顺序不确定。二者名字刻意区分开，避免「以为有序其实无序」。
func Chain[T any](ps ...*Pipe[T]) *Pipe[T] {
	ps = filterNonNil(ps)
	if len(ps) == 0 {
		return Empty[T]()
	}
	if len(ps) == 1 {
		return ps[0]
	}

	cb := newCtxBox(ps[0].ctx())
	out := &Pipe[T]{
		header: header{
			name: "Chain(" + itoa(len(ps)) + ")", cb: cb,
			box: ps[0].box, cfg: ps[0].cfg,
		},
	}
	idx := 0
	for _, up := range ps {
		cb.adopt(up.cb)
		up.cb = cb
		up.consumed.Store(true)
	}
	out.pull = func() (T, bool, error) {
		var zero T
		for idx < len(ps) {
			v, more, err := ps[idx].next()
			if err != nil {
				return zero, false, err
			}
			if more {
				return v, true, nil
			}
			idx++ // 当前管道耗尽，切下一条
		}
		return zero, false, nil
	}
	return out
}

// Zip 把两条管道**按位配对**（a 的第 i 个配 b 的第 i 个）；任一侧先结束即结束。
//
// 实现说明：Zip 会接管两条上游的生命周期（共享同一个 ctx 并标记为已消费），
// 因此上游不能再被单独终结。
func Zip[A, B any](a *Pipe[A], b *Pipe[B]) *Pipe[xmap.Pair[A, B]] {
	if a == nil || b == nil {
		return nil
	}
	cb := newCtxBox(a.ctx())
	cb.adopt(a.cb)
	cb.adopt(b.cb)
	a.cb, b.cb = cb, cb
	a.consumed.Store(true)
	b.consumed.Store(true)

	z := &Pipe[xmap.Pair[A, B]]{
		header: header{name: "Zip", cb: cb, box: a.box, cfg: a.cfg},
	}
	z.pull = func() (xmap.Pair[A, B], bool, error) {
		var zero xmap.Pair[A, B]
		av, amore, err := a.next()
		if err != nil {
			return zero, false, err
		}
		if !amore {
			return zero, false, nil
		}
		bv, bmore, err := b.next()
		if err != nil {
			return zero, false, err
		}
		if !bmore {
			return zero, false, nil
		}
		return xmap.Pair[A, B]{Key: av, Val: bv}, true, nil
	}
	return z
}

// SortedKeys 返回 map 的键的稳定有序列表（便于对 GroupBy/ToMap 结果做确定性断言与输出）。
//
// 单独提供它的原因：Go 的 map 遍历顺序是随机的，任何「按分组输出」的场景
// 都需要显式排序，否则会出现「同样的数据、两次跑出不同顺序」。
func SortedKeys[K comparable, V any](m map[K]V, less func(a, b K) bool) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if less != nil {
		sort.Slice(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
	}
	return keys
}

// filterNonNil 去掉 nil 管道（链式拼接时允许传 nil 表示「无此段」）。
func filterNonNil[T any](ps []*Pipe[T]) []*Pipe[T] {
	out := make([]*Pipe[T], 0, len(ps))
	for _, p := range ps {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}
