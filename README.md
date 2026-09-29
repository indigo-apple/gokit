# gokit

Go 通用工具链 —— **泛型优先、零第三方依赖**（核心包 100% 标准库）。

设计文档见 [DESIGN.md](./DESIGN.md)，变更记录见 [CHANGELOG.md](./CHANGELOG.md)。

## 要求

- Go **1.21+**（依赖泛型、`log/slog`、`slices`/`maps`、内置 `min`/`max`/`clear`、`context.AfterFunc`、`context.WithoutCancel`）

## 安装

```bash
go get github.com/zhangwenxing/gokit
```

核心包无任何 `require`，不会污染你的依赖树。

## 包索引

### 底座

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `xconstraints` | 泛型约束（`Number` / `Integer` / `Ordered` / `Key`），全工具链唯一来源 | 纯类型 |
| `xerr` | 错误码、包装、分类判定、`MultiError` 聚合、`Validate` | 87% |
| `xsafe` | panic 安全（`Go` / `Recover` / `Try` / `Caller`） | 97% |
| `xcontext` | 类型安全的 context 键、`Merge` / `Detach`、HTTP 链路提取 | 94% |
| `xreflect` | 深拷贝（含循环引用）、字段元数据缓存、**弱类型赋值**、struct↔map | 81% |
| `xtype` | 泛型类型操作（取地址 `Ptr` / `Addr`、算术 `Add`/`Div`/`Sum`、比较、条件） | 99% |

### 数据类型与算法

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `xstr` | 字符串（命名风格 / 截断 / 脱敏 / 随机串 / 相似度） | 95% |
| `xnum` | 数值（`Decimal` 精度 / 字节单位 / 进制 / 格式化） | 90% |
| `xtime` | 时间（边界 / 解析 / 人类可读文案 / Stopwatch） | 98% |
| `xmap` | map 算法（合并 / 集合运算 / **稳定遍历** / 嵌套路径） | 93% |
| `xslice` | slice 算法（去重 / 变换 / 分组 / 集合运算 / 排序） | 97% |
| `xconv` | **类型转换枢纽**（容错链式取值 / map↔slice↔string↔struct↔JSON） | 92% |

### 容器

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `xcontainer` | 容器共享词汇：`OverflowPolicy`、`Collection`/`Queue` 接口、哨兵错误 | 100% |
| `xcontainer/xstack` | 栈：`Stack`（切片）/ `ConcurrentStack`（并发）/ `BoundedStack`（有界 + 4 种溢出策略） | 96% |
| `xcontainer/xqueue` | 队列：`SliceQueue` / `Ring`（零分配环形）/ `Bounded`（并发 + 阻塞 + 溢出策略）/ `Sync` | 96% |

### 协调控制

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `xlock` | **进程内唯一资源锁**：`KeyedLocker`（按 key 精确互斥）/ `ShardedLocker`（固定分片）/ `ReentrantLocker`（可重入）/ `ResourcePool`（N 并发闸门） | 94% |

### 测试工具

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `xtest` | 零依赖断言、`Eventually` 轮询、golden 文件与行差分、假数据、`Fill`、goroutine 泄漏检测 | 93% |

## 依赖分层

```
                         xconstraints
                              │
  xerr ──────────────────────> xtype
       ├─> xcontainer ──> xstack / xqueue
       └─> xlock
  xsafe                        │
  xcontext                 xreflect ──> xmap ──> xconv
  xtime ───────────────────────┘        ↑        ↑
  xstr  xnum ──────────────────────────> xslice ─┘
                                        │
                                      xtest
```

关键约束（避免「两套规则」）：

- `xreflect.Assign` 是**全工具链唯一的弱类型转换底座**，`xconv` 与 `xmap` 都建立在它之上。
- `xtime.Layouts` 是**唯一的时间布局嗅探列表**，`xreflect` 复用同一份，
  保证 `MapToStruct` 与 `xtime.Parse` 对同一字符串解析结果一致。
- `xlock` 的所有实现共用同一个「可取消读写锁内核」（`entry`），
  区别只在「key → 锁内核」的映射方式：按 key 建表并引用计数回收，或按哈希取固定分片。
  因此**不存在两套互斥实现**，两种实现的互斥语义由同一套契约测试覆盖。
- 泛型约束统一从 `xconstraints` 引用，**禁止各包重复定义**。

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/zhangwenxing/gokit/xcontainer"
	"github.com/zhangwenxing/gokit/xcontainer/xqueue"
	"github.com/zhangwenxing/gokit/xconv"
	"github.com/zhangwenxing/gokit/xnum"
	"github.com/zhangwenxing/gokit/xslice"
	"github.com/zhangwenxing/gokit/xstack"
)

func main() {
	// ── 切片：去重 + 排序 ──
	in := []int{3, 1, 3, 2, 1}
	fmt.Println(xslice.SortOrdered(xslice.Dedup(in))) // [1 2 3]

	// ── 转换：map → slice → string ──
	m := map[string]int{"b": 2, "a": 1}
	fmt.Println(xconv.SliceToString(
		xconv.MapToSliceSorted(m, func(k string, v int) string { return k }), ",")) // a,b

	// ── 容错链式取值（配置场景）──
	cfg := map[string]any{"db": map[string]any{"pool": map[string]any{"max": 20}}}
	fmt.Println(xconv.V(cfg).Path("db.pool.max").Int(5)) // 20

	// ── 金额精度：float64 会得到 0.30000000000000004 ──
	fmt.Println(xnum.NewDecimal("0.1").Add(xnum.NewDecimal("0.2"))) // 0.3

	// ── 栈 ──
	st := xstack.NewFrom([]int{1, 2, 3}) // 栈顶 3
	top, _ := st.Pop()
	fmt.Println(top, st.Len()) // 3 2

	// ── 有界并发队列（背压 + 溢出策略）──
	q := xqueue.NewBounded[int](4, xcontainer.DropOldest)
	for i := 1; i <= 6; i++ {
		_ = q.Push(i) // 4,5,6 挤掉最早的
	}
	fmt.Println(q.Drain(), q.Dropped()) // [3 4 5 6] 2

	// ── 阻塞式消费与取消 ──
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if v, ok, err := q.PopCtx(ctx); err == nil && ok {
		fmt.Println(v)
	}

	// ── 唯一资源锁：同一个订单号只允许一个处理者 ──
	locker := xlock.NewKeyed() // 或 xlock.NewSharded() / NewKeyed(WithMaxKeys(10000))
	err := xlock.With(ctx, locker, "order:123", func() error {
		// 同 key 串行、不同 key 并行；fn panic 也会释放锁
		return handleOrder("123")
	})
	fmt.Println(err)

	// ── 多 key 加锁：内部去重 + 排序，从根本上避免交叉死锁 ──
	if un, err := xlock.LockAll(ctx, locker, "acct:A", "acct:B"); err == nil {
		defer un()
	}

	// ── 并发闸门：同一次最多 8 个外部请求 ──
	pool := xlock.NewResourcePool(8, xlock.WithPoolName("upstream"))
	if err := pool.With(ctx, func() error { return callUpstream() }); err != nil {
		fmt.Println(err)
	}
}
```

### 用 xtest 写测试

```go
func TestService(t *testing.T) {
	xtest.SkipShort(t)

	svc := NewService()
	xtest.NoError(t, svc.Start())
	t.Cleanup(func() { _ = svc.Stop() })

	// 等异步收敛，而不是裸 time.Sleep
	xtest.Eventually(t, func() bool { return svc.Ready() }, time.Second, 10*time.Millisecond)

	got, err := svc.Do("x")
	xtest.NoError(t, err)
	xtest.Equal(t, Result{Code: 200}, got)

	// nil 切片与空切片视为等价
	xtest.Equiv(t, []string{}, svc.Tags())

	// 集合断言与顺序无关
	xtest.ElementsMatch(t, []string{"a", "b"}, svc.Names())

	// golden：GOKIT_UPDATE=1 go test ./... 生成/更新
	xtest.GoldenJSON(t, "testdata/resp.json", got)
}
```

## 开发

```bash
export PATH=/opt/go_1.21/go/bin:$PATH
export GOTOOLCHAIN=local

make test          # go test -race ./...
make cover         # 覆盖率报告（打印总分）
make cover-html    # 生成 cover.html
make cover-check   # 覆盖率门禁（总 85% / 单包 75%）
make lint          # gofmt -l + go vet
make bench         # 基准测试
make fuzz-all      # 全量模糊测试回归
make cross-build   # linux/darwin/windows × amd64/arm64 编译校验
make check         # build 前的完整门禁：fmt-check + vet + race 测试 + 覆盖率
make ci            # check + cross-build + fuzz-all
```

## 测试与质量

- `go vet` 零告警，`-race` 全绿；linux/amd64 + arm64、darwin/arm64、windows/amd64 交叉编译通过
- 所有包均为**表驱动测试 + 边界用例**（`nil` / 空 / 单元素 / 重复 / 极值 / 除零 / 溢出 / 并发）
- **模型式模糊测试**：用朴素参考模型逐步比对被测实现，共 11 个 target

  | 包 | 目标 | 校验的不变式 |
  |---|---|---|
  | `xconv` | `FuzzToInt` / `FuzzIntToBaseRoundTrip` / `FuzzValueChain` | 进制往返、容错链不 panic |
  | `xslice` | `FuzzDedup` / `FuzzSetOperations` / `FuzzRotateAndWindow` | 去重、集合运算、滑窗 |
  | `xcontainer/xstack` | `FuzzStackLIFO` / `FuzzBoundedStackConservation` | LIFO 顺序、元素守恒 |
  | `xcontainer/xqueue` | `FuzzSliceQueueFIFO` / `FuzzRingCapacityAndFIFO` / `FuzzBoundedDropPolicies` | FIFO 顺序、容量上限、绕环、溢出策略 |
  | `xlock` | `FuzzLockCtxNoLeak` / `FuzzLockAllKeys` / `FuzzShardOf` | 任意超时下不泄漏锁与条目、多 key 加锁去重排序、分片值域 |

- 覆盖率门禁：总分 ≥ 85%、有语句的包 ≥ 75%（`scripts/cover-gate.sh`，阈值可用 `TOTAL_MIN` / `PKG_MIN` 覆盖）
- CI：`.github/workflows/ci.yml` 含门禁 / 跨平台 / fuzz / 可选 golangci-lint 四个作业；
  门禁作业会**校验 `go.mod` 无 `require`**，防止悄悄引入第三方依赖

## 约定

- **零第三方依赖**：核心包只用标准库，`go.mod` 不含任何 `require`
- **永不 panic**：除 `MustXxx` 变体外，`nil` / 空输入返回零值或 error
- **Options 模式**：`WithXxx()` 函数式选项
- **并发语义显式**：每个类型的 godoc 首行标注「并发安全 / 非并发安全」
- **不可变优先**：返回新集合；需要原地修改的显式以 `InPlace` 结尾
- **失败可区分**：转换类函数提供 `CastErr` / `ToInt64` 等带 error 的变体，
  以及 `V(x).Int(def)` 这样的容错链
- **导出符号必须有中文 godoc**，并在注释中解释设计理由
- **Bug 修复必须补回归测试**，且 `-race` 全绿才允许合入
