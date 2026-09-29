package xpipe

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 阶段选项（重试等）
// ---------------------------------------------------------------------------

// StageOption 是「可失败阶段」的选项（MapErr / MapCtx / FilterCtx / Parallel）。
type StageOption func(*stageOptions)

type stageOptions struct {
	retryTimes   int
	retryBackoff time.Duration
}

// WithRetry 让该阶段的函数失败时自动重试（指数退避 + 抖动，上限 5s）。
//
// 设计说明：设计文档把 Retry 列为**独立阶段**（`Retry(p, times)`）。本实现刻意不这么做 ——
// 拉取式管道里「重试一个位置」语义上是含糊的：重试上游 pull 时，它要么重复消费
// 一个元素、要么已经推进到下一个元素，两种都不对。**重试属于「会失败的那个操作」**，
// 因此改为挂在具体阶段上：`MapErr(p, fn, WithRetry(3, 10*time.Millisecond))`。
func WithRetry(times int, backoff ...time.Duration) StageOption {
	return func(o *stageOptions) {
		if times >= 0 {
			o.retryTimes = times
		}
		if len(backoff) > 0 && backoff[0] > 0 {
			o.retryBackoff = backoff[0]
		}
	}
}

func applyStageOpts(opts []StageOption) stageOptions {
	o := stageOptions{retryBackoff: 20 * time.Millisecond}
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	return o
}

// callRetry 执行可失败操作并按需重试。
func callRetry(ctx context.Context, o stageOptions, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = guardErr(fn)
		if err == nil {
			return nil
		}
		if attempt >= o.retryTimes {
			return err
		}
		d := backoffFor(o.retryBackoff, attempt)
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// backoffFor 计算第 attempt 次重试的退避：指数增长 + 50%~100% 抖动，上限 5s。
func backoffFor(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = 20 * time.Millisecond
	}
	d := base
	for i := 0; i < attempt && d < 5*time.Second; i++ {
		d *= 2
	}
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	half := d / 2
	return half + time.Duration(float64(half)*rand.Float64())
}

// callGuard 执行「返回 (值, error)」的用户函数并捕获 panic。
func callGuard[T, R any](fn func(T) (R, error), v T) (out R, err error) {
	if gerr := guard(func() { out, err = fn(v) }); gerr != nil {
		var zero R
		return zero, gerr // panic：用 panic 转换出的错误
	}
	return out, err // 正常返回：原样透出 fn 的错误（**绝不能被覆盖**）
}

// stageNext 是「一对一阶段」的公共骨架：取一个上游元素 → 跑 fn → 按错误策略处理。
//
// 复用它的好处：所有一对一阶段对 FailFast / Skip / Collect 的处理**只有一份实现**，
// 不会出现「某个阶段忘了遵守策略」的漂移。
func stageNext[T, R any](self *Pipe[R], up *Pipe[T], fn func(v T) (R, error)) (R, bool, error) {
	var zero R
	for {
		v, more, err := up.next()
		if err != nil || !more {
			return zero, false, err
		}
		out, ferr := callGuard(fn, v)
		if ferr == nil {
			return out, true, nil
		}
		z, cont, ferr2 := self.fail2(zero, ferr)
		if ferr2 != nil {
			return z, false, ferr2
		}
		if !cont {
			return zero, false, nil
		}
	}
}

// ---------------------------------------------------------------------------
// 一对一变换
// ---------------------------------------------------------------------------

// Map 把每个元素经 fn 变换（一对一，fn 自身不返回错误；panic 按错误策略处理）。
func Map[T, R any](p *Pipe[T], fn func(T) R) *Pipe[R] {
	if p == nil || fn == nil {
		return nil
	}
	out := newStage[R](p.header, "Map"+suffix(p.name), nil)
	out.pull = func() (R, bool, error) {
		return stageNext(out, p, func(v T) (R, error) { return fn(v), nil })
	}
	return out
}

// MapErr 把每个元素经 fn 变换（可能失败；失败按错误策略处理，可配 WithRetry）。
func MapErr[T, R any](p *Pipe[T], fn func(T) (R, error), opts ...StageOption) *Pipe[R] {
	if p == nil || fn == nil {
		return nil
	}
	o := applyStageOpts(opts)
	out := newStage[R](p.header, "MapErr"+suffix(p.name), nil)
	out.pull = func() (R, bool, error) {
		return stageNext(out, p, func(v T) (R, error) {
			var r R
			var e error
			// ★ 在**拉取时**读共享 ctx 槽位：这样 WithContext 才能即时影响重试的退避等待
			err := callRetry(p.ctx(), o, func() error {
				r, e = fn(v)
				return e
			})
			return r, err
		})
	}
	return out
}

// MapCtx 是带 ctx 的 MapErr（fn 内可做 IO/取消感知）。
func MapCtx[T, R any](p *Pipe[T], fn func(ctx context.Context, v T) (R, error), opts ...StageOption) *Pipe[R] {
	if p == nil || fn == nil {
		return nil
	}
	return MapErr(p, func(v T) (R, error) { return fn(p.ctx(), v) }, opts...)
}

// Filter 保留 pred 为真的元素。
func Filter[T any](p *Pipe[T], pred func(T) bool) *Pipe[T] {
	if p == nil || pred == nil {
		return nil
	}
	return newStage[T](p.header, "Filter"+suffix(p.name), func() (T, bool, error) {
		return p.filterNext(func(v T) (bool, error) {
			var ok bool
			if err := guard(func() { ok = pred(v) }); err != nil {
				return false, err
			}
			return ok, nil
		})
	})
}

// FilterCtx 是带 ctx 的 Filter（pred 可返回错误，按策略处理，可配 WithRetry）。
func FilterCtx[T any](p *Pipe[T], pred func(ctx context.Context, v T) (bool, error), opts ...StageOption) *Pipe[T] {
	if p == nil || pred == nil {
		return nil
	}
	o := applyStageOpts(opts)
	return newStage[T](p.header, "FilterCtx"+suffix(p.name), func() (T, bool, error) {
		return p.filterNext(func(v T) (bool, error) {
			var ok bool
			ctx := p.ctx() // 拉取时读共享槽位
			err := callRetry(ctx, o, func() error {
				var e error
				ok, e = pred(ctx, v)
				return e
			})
			return ok, err
		})
	})
}

// Reject 丢弃 pred 为真的元素（Filter 的补集）。
func Reject[T any](p *Pipe[T], pred func(T) bool) *Pipe[T] {
	if p == nil || pred == nil {
		return nil
	}
	return newStage[T](p.header, "Reject"+suffix(p.name), func() (T, bool, error) {
		return p.filterNext(func(v T) (bool, error) {
			var ok bool
			if err := guard(func() { ok = pred(v) }); err != nil {
				return false, err
			}
			return !ok, nil
		})
	})
}

// FlatMap 把每个元素展开为多个（fn 返回 nil/空切片表示不产出）。
func FlatMap[T, R any](p *Pipe[T], fn func(T) []R) *Pipe[R] {
	if p == nil || fn == nil {
		return nil
	}
	var (
		buf []R
		idx int
	)
	out := newStage[R](p.header, "FlatMap"+suffix(p.name), nil)
	out.pull = func() (R, bool, error) {
		var zero R
		for {
			if idx < len(buf) {
				v := buf[idx]
				idx++
				return v, true, nil
			}
			buf, idx = nil, 0

			v, more, err := p.next()
			if err != nil || !more {
				return zero, false, err
			}
			var items []R
			gerr := guard(func() { items = fn(v) })
			if gerr != nil {
				z, cont, ferr := out.fail2(zero, gerr)
				if ferr != nil {
					return z, false, ferr
				}
				if !cont {
					return zero, false, nil
				}
				continue // 跳过该元素的展开
			}
			buf = items
		}
	}
	return out
}

// Peek 旁路观察（打日志/埋点），不改变数据。
//
// 语义保证：**Peek 的失败绝不改变数据流** —— fn 的 panic 只被记入 Err()，
// 元素照样向下游传递（否则「打日志」这种副作用就可能吃掉业务数据）。
func Peek[T any](p *Pipe[T], fn func(T)) *Pipe[T] {
	if p == nil || fn == nil {
		return nil
	}
	return newStage[T](p.header, "Peek"+suffix(p.name), func() (T, bool, error) {
		var zero T
		v, more, err := p.next()
		if err != nil || !more {
			return zero, false, err
		}
		if gerr := guard(func() { fn(v) }); gerr != nil {
			p.box.add(gerr)
		}
		return v, true, nil
	})
}

// Distinct 去重（按 comparable 相等，保持首次出现顺序）。
//
// ⚠️ 内存随「不同取值个数」线性增长。数据量不可控时请改用 DistinctBy + 有界键，
// 或先分片/外部排序。
func Distinct[T comparable](p *Pipe[T]) *Pipe[T] {
	if p == nil {
		return nil
	}
	return DistinctBy(p, func(v T) T { return v })
}

// DistinctBy 按 key(v) 去重（保持首次出现顺序）。
func DistinctBy[T any, K comparable](p *Pipe[T], key func(T) K) *Pipe[T] {
	if p == nil || key == nil {
		return nil
	}
	seen := make(map[K]struct{})
	return newStage[T](p.header, "DistinctBy"+suffix(p.name), func() (T, bool, error) {
		return p.filterNext(func(v T) (bool, error) {
			var k K
			if err := guard(func() { k = key(v) }); err != nil {
				return false, err
			}
			if _, ok := seen[k]; ok {
				return false, nil
			}
			seen[k] = struct{}{}
			return true, nil
		})
	})
}

// Scan 累积变换：**为每个输入元素产出一个累积值**（与 RxJS 的 scan 一致，
// 不额外产出初始值；需要「先发初始值」请在前面接 `Just(init)`）。
func Scan[T, A any](p *Pipe[T], init A, fn func(acc A, v T) A) *Pipe[A] {
	if p == nil || fn == nil {
		return nil
	}
	acc := init
	out := newStage[A](p.header, "Scan"+suffix(p.name), nil)
	out.pull = func() (A, bool, error) {
		var zero A
		v, more, err := p.next()
		if err != nil || !more {
			return zero, false, err
		}
		if gerr := guard(func() { acc = fn(acc, v) }); gerr != nil {
			z, cont, ferr := out.fail2(zero, gerr)
			if ferr != nil {
				return z, false, ferr
			}
			if !cont {
				return zero, false, nil
			}
		}
		return acc, true, nil
	}
	return out
}

// ---------------------------------------------------------------------------
// 批处理与窗口
// ---------------------------------------------------------------------------

// BatchOption 是 Batch 的选项。
type BatchOption func(*batchOptions)

type batchOptions struct {
	timeout time.Duration
}

// WithBatchTimeout 设置「批最长等待时间」：一批从第一个元素开始计时，
// 超过 d 且已有元素就先发出（不足 size 也发）。
//
// ⚠️ 拉取式管道的固有限制：超时**只在元素取得到时被检查**，无法打断「等上游」的那次阻塞。
// 需要严格按时间切批，请把上游换成 FromChannel 并在源头用定时器。
func WithBatchTimeout(d time.Duration) BatchOption {
	return func(o *batchOptions) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// Batch 把元素按 size 切批（size <= 0 视为 1）。
//
// 语义：满 size 立即发出；流结束时发出尾批（可能不足 size）；**绝不产出空批**。
func Batch[T any](p *Pipe[T], size int, opts ...BatchOption) *Pipe[[]T] {
	if p == nil {
		return nil
	}
	if size <= 0 {
		size = 1
	}
	o := batchOptions{}
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}

	var (
		cur      []T
		started  time.Time
		finished bool
	)
	return newStage[[]T](p.header, "Batch("+itoa(size)+")"+suffix(p.name), func() ([]T, bool, error) {
		for {
			if len(cur) >= size {
				out := cur
				cur, started = nil, time.Time{}
				return out, true, nil
			}
			if finished {
				if len(cur) == 0 {
					return nil, false, nil
				}
				out := cur
				cur = nil
				return out, true, nil
			}
			if o.timeout > 0 && len(cur) > 0 && time.Since(started) >= o.timeout {
				out := cur
				cur, started = nil, time.Time{}
				return out, true, nil
			}

			v, more, err := p.next()
			if err != nil {
				return nil, false, err
			}
			if !more {
				finished = true
				continue
			}
			if len(cur) == 0 {
				started = time.Now()
			}
			cur = append(cur, v)
		}
	})
}

// Window 生成滑动窗口（窗口大小 size、步长 step，默认 step = 1）。
//
// 语义：输出 [0,size)、[step,step+size)、…；每个窗口内部保持原顺序；
// 流末尾不足 size 的残窗被丢弃（与常见流式窗口语义一致）。
//
// ⚠️ 内存 O(size)，步长越小重复读的元素越多。
func Window[T any](p *Pipe[T], size int, step ...int) *Pipe[[]T] {
	if p == nil {
		return nil
	}
	if size <= 0 {
		size = 1
	}
	st := 1
	if len(step) > 0 && step[0] > 0 {
		st = step[0]
	}

	var (
		buf []T
		eof bool
	)
	return newStage[[]T](p.header, "Window("+itoa(size)+","+itoa(st)+")"+suffix(p.name), func() ([]T, bool, error) {
		for {
			// 先把缓冲区补到一个完整窗口
			for len(buf) < size && !eof {
				v, more, err := p.next()
				if err != nil {
					return nil, false, err
				}
				if !more {
					eof = true
					break
				}
				buf = append(buf, v)
			}
			if len(buf) < size {
				return nil, false, nil // 残窗不足，丢弃
			}
			out := make([]T, size)
			copy(out, buf[:size])

			// 窗口向右滑 step：需要丢弃 step 个元素。
			//   - 缓冲区里已有的先丢；
			//   - 若 step > 缓冲区长度（步长大于窗口），差额必须**直接在上游跳过**，
			//     否则本该跳过的元素会被拼进下一个窗口（真实缺陷，被测试抓到）。
			if st < len(buf) {
				buf = append(buf[:0], buf[st:]...)
			} else {
				need := st - len(buf)
				buf = buf[:0]
				for i := 0; i < need; i++ {
					_, more, err := p.next()
					if err != nil {
						return nil, false, err
					}
					if !more {
						eof = true
						break
					}
				}
			}
			return out, true, nil
		}
	})
}

// Throttle 限制元素输出间隔（相邻元素至少间隔 interval），用于简单限速。
//
// 取消时立即返回 ctx 错误；interval <= 0 时原样返回（便于配置驱动时关闭限速）。
func Throttle[T any](p *Pipe[T], interval time.Duration) *Pipe[T] {
	if p == nil || interval <= 0 {
		return p
	}
	var last time.Time
	return newStage[T](p.header, "Throttle("+interval.String()+")"+suffix(p.name), func() (T, bool, error) {
		var zero T
		if !last.IsZero() {
			if wait := interval - time.Since(last); wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-t.C:
				case <-p.ctx().Done():
					t.Stop()
					return zero, false, p.ctx().Err()
				}
			}
		}
		v, more, err := p.next()
		if err != nil || !more {
			return zero, false, err
		}
		last = time.Now()
		return v, true, nil
	})
}

// ---------------------------------------------------------------------------
// 并行阶段
// ---------------------------------------------------------------------------

// Parallel 用 workers 个 goroutine 并行处理元素，**不保证输出顺序**。
//
// 实现：1 个 feed goroutine（读上游 → 带序号的作业队列）+ workers 个 worker
// + 1 个 closer goroutine（worker 全退出后关闭输出通道），共 workers+2 个 goroutine。
// 三者全部 select 管道 ctx，因此终结操作一 cancel 就整体退出（有泄漏测试覆盖）。
//
// 语义细节：
//   - 输出顺序 = 完成顺序（要保序用 ParallelOrdered）；
//   - FailFast 的中止是「尽力而为」：已启动的元素会跑完（绝不强杀 goroutine）；
//   - 元素级错误照样走错误策略（Skip 时该元素不产出，但顺序位仍被消耗）。
func Parallel[T, R any](p *Pipe[T], workers int, fn func(T) (R, error), opts ...StageOption) *Pipe[R] {
	return parallel(p, workers, false, fn, opts...)
}

// ParallelOrdered 与 Parallel 相同，但**输出顺序与输入顺序一致**（按序号重排）。
//
// 代价：需要缓存「先完成但序号靠后」的结果，内存上限约为在途元素数（workers 量级）。
func ParallelOrdered[T, R any](p *Pipe[T], workers int, fn func(T) (R, error), opts ...StageOption) *Pipe[R] {
	return parallel(p, workers, true, fn, opts...)
}

func parallel[T, R any](p *Pipe[T], workers int, ordered bool, fn func(T) (R, error), opts ...StageOption) *Pipe[R] {
	if p == nil || fn == nil {
		return nil
	}
	o := applyStageOpts(opts)
	if workers <= 0 {
		workers = p.cfg.workers
	}
	if workers < 1 {
		workers = 1
	}
	// 注意：Parallel 在**构造时**就启动 goroutine，因此这里取的是当时的 ctx。
	// 若需要在运行期替换 ctx，请保证 WithContext 在构建 Parallel 之前调用
	// （最稳妥：直接把 ctx 传给数据源）。
	ctx := p.ctx()

	name := "Parallel(" + itoa(workers) + ")"
	if ordered {
		name = "ParallelOrdered(" + itoa(workers) + ")"
	}
	out := newStage[R](p.header, name+suffix(p.name), nil)

	type job struct {
		seq int
		v   T
	}
	type result struct {
		seq int
		v   R
		err error
	}

	jobs := make(chan job, workers)
	outCh := make(chan result, workers)
	upErr := make(chan error, 1)

	// feed：读上游 → 带序号作业
	go func() {
		defer close(jobs)
		seq := 0
		for {
			v, more, err := p.next()
			if err != nil {
				select {
				case upErr <- err:
				default:
				}
				return
			}
			if !more {
				return
			}
			select {
			case jobs <- job{seq: seq, v: v}:
				seq++
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case j, ok := <-jobs:
					if !ok {
						return
					}
					var r R
					err := callRetry(ctx, o, func() error {
						var e error
						r, e = fn(j.v)
						return e
					})
					select {
					case outCh <- result{seq: j.seq, v: r, err: err}:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(outCh)
	}()

	var (
		pending map[int]result
		nextSeq int
	)
	if ordered {
		pending = make(map[int]result, workers)
	}

	out.pull = func() (R, bool, error) {
		var zero R
		for {
			if err := ctx.Err(); err != nil {
				return zero, false, err
			}
			// 保序模式：先看「轮到的那一个」是否已就绪
			if ordered {
				if r, ok := pending[nextSeq]; ok {
					delete(pending, nextSeq)
					nextSeq++
					if r.err != nil {
						z, cont, ferr := out.fail2(zero, r.err)
						if ferr != nil {
							return z, false, ferr
						}
						if !cont {
							return zero, false, nil
						}
						continue
					}
					return r.v, true, nil
				}
			}

			select {
			case r, ok := <-outCh:
				if !ok {
					// 全部 worker 已退出：上游错误优先上报
					select {
					case err := <-upErr:
						return zero, false, err
					default:
					}
					return zero, false, nil
				}
				if ordered {
					pending[r.seq] = r
					continue
				}
				if r.err != nil {
					z, cont, ferr := out.fail2(zero, r.err)
					if ferr != nil {
						return z, false, ferr
					}
					if !cont {
						return zero, false, nil
					}
					continue
				}
				return r.v, true, nil
			case <-ctx.Done():
				return zero, false, ctx.Err()
			}
		}
	}
	return out
}

// Unordered 是恒等阶段，仅为可读性提供（语义标记：此处不保证顺序）。
func Unordered[T any](p *Pipe[T]) *Pipe[T] { return p }

// ---------------------------------------------------------------------------
// 错误处理阶段
// ---------------------------------------------------------------------------

// OnError 拦截**上游**传上来的错误，交给 fn 决定：返回 nil 表示跳过该错误继续，
// 返回非 nil 表示换成该错误中止。
//
// 用途：把某一段的 FailFast 局部改成「记录下来继续」：
//
//	p2 := xpipe.OnError(xpipe.MapErr(src, risky), func(err error, _ Result) error {
//	    log.Println("跳过:", err)
//	    return nil
//	})
func OnError[T any](p *Pipe[T], fn func(err error, v T) error) *Pipe[T] {
	if p == nil || fn == nil {
		return nil
	}
	return newStage[T](p.header, "OnError"+suffix(p.name), func() (T, bool, error) {
		var zero T
		for {
			v, more, err := p.next()
			if err == nil {
				return v, more, nil
			}
			var handled error
			if gerr := guard(func() { handled = fn(err, v) }); gerr != nil {
				return zero, false, gerr
			}
			if handled == nil {
				p.box.add(err) // 记下来，但继续拉
				continue
			}
			return zero, false, handled
		}
	})
}
