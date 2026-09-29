# 更新日志

本文件记录 gokit 的阶段性变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [未发布]

### 计划中（后续阶段）

- `xdistlock` —— 分布式锁：接口抽象 + memory/redis/etcd 实现 + 看门狗续约 + fencing token
- `xevent` —— 进程内消息订阅/事件总线：主题通配、同步/异步分发、背压、死信、按 key 保序
- `xkeyed` —— 同 key 串行化管道（MapPipe）：按 key 路由到独立管道，保证同 key 顺序
- `xpipe` —— 流式管道：source → stage → sink，支持中间件、并发 stage、背压与优雅关闭
- `xcache` —— 本地缓存：TTL / LRU / 多级缓存 / singleflight 防击穿
- `xconcurrency` —— Future、WorkerPool、Semaphore、限流器、熔断器、重试退避、Debounce/Throttle、Batcher
- `xlog` / `xconfig` / `xid` / `xcodec` / `xcrypto` / `xfile` / `xnet` / `xvalidate`
- `xcontainer` 后续子包：`xset`（集合）、`xlru`（LRU/LFU）、`xring`（环形缓冲）、`xbit`（位集合）、`xheap`（优先队列）

---

## [0.8.0] - Stage 8：进程内唯一资源锁（xlock）

### 新增

- **`xlock`** —— 按 key 的唯一资源锁，四种实现共享同一套契约：

  | 类型 | 语义 | 适用场景 |
  |---|---|---|
  | `KeyedLocker` | 每个 key 一个锁内核，引用计数归零即回收 | 通用；key 之间需严格隔离 |
  | `ShardedLocker` | 固定 N 个锁内核，key 按 FNV-1a 哈希落分片 | key 数量巨大、访问分散；表大小恒定 |
  | `ReentrantLocker` | 同一 owner 重复加锁不阻塞 | 无法拆分的递归/回调（有明确警告） |
  | `ResourcePool` | 不区分 key，只限总数（信号量） | 外部接口限流、并发闸门 |

  - 统一接口：`Locker`（`Lock`/`LockCtx`/`TryLock`/`RLock`/`RLockCtx`/`TryRLock`）
  - 作用域用法：`With` / `WithR` / `WithValue[T]`（panic 也会释放锁）
  - 多 key 加锁：`LockAll`（**去重 + 字典序排序**，从根本消除交叉死锁；失败回滚）
  - 观测：`Stats`（等待次数/时长、持有总量与峰值、竞争次数、无效释放次数）、
    `ActiveKeys` / `ActiveKeysSnapshot` / `EntryState`
  - 选项：`WithMaxKeys` / `WithWaitTimeout` / `WithOnContention` / `WithContentionThreshold`
  - 选型辅助：`HashKey`（FNV-1a，刻意不用 maphash 以保证可复现）、`ShardOf`
- **自研可取消读写锁内核**（`entry`）：写者 FIFO 队列 + 按序交接所有权 + 读者广播，
  实现「写者互斥 + 读者共享 + 写者优先（防写饥饿）」，
  且 `ctx` 取消能**真正把自己从等待队列摘除**（`sync.RWMutex` 做不到，会造成 goroutine 堆积）。
- 模糊测试：`FuzzLockCtxNoLeak`（任意超时下不泄漏锁/条目）、
  `FuzzLockAllKeys`（多 key 加锁的去重排序不变式）、`FuzzShardOf`（分片值域与可复现性）

### 修复（本阶段开发过程中发现并修复的缺陷）

- **已取消的 ctx 仍会抢到锁**：`core.acquire` 原本直接走「无人竞争」快速路径，
  导致 `LockCtx(已取消)` / `With(已取消 ctx)` 拿到了锁并执行了业务函数。
  改为**先判 ctx 再取锁**，与 `golang.org/x/sync/semaphore` 的约定一致。
- **`WithR` 静默忽略 ctx**：内部误用了无 ctx 的 `RLock` 而非 `RLockCtx`。
- **nil 接收者 panic**：嵌入式 `core` 在各实现偏移量不确定，
  改为在每个公开方法入口显式守卫（nil 时返回 `ErrNilLocker` / 零值）。
- **`ReentrantLocker.LockFor` 错误类型不一致**：无 ctx 入口却返回 `context.DeadlineExceeded`，
  改为与 `Lock` 一致地映射为 `ErrLockTimeout`（抽出 `lockForBlocking` 复用）。
- 设计补充：`Locker` 接口增加 `RLockCtx`（DESIGN.md 5.10 只列了 `RLock`，
  但「读锁等待无法取消」同样是 goroutine 堆积来源）；`ShardedLocker` 补 `EntryState` 观测能力。

### 工程

- `NewSharded` 返回独立类型 `*ShardedLocker`（DESIGN.md 中声明为 `*KeyedLocker`）——
  两者语义不同（假冲突、`Stats.Keys` 含义），共用类型会掩盖差异；二者都满足 `Locker` 接口。

---

## [0.7.0] - Stage 6：容器层与测试工具

### 新增

- **`xcontainer`** —— 容器共享词汇：`OverflowPolicy`（`Block` / `DropOldest` / `DropNewest` / `Fail`）、
  `Collection[T]` / `Queue[T]` 接口、四个带错误码的哨兵错误
  （`ErrFull` / `ErrWouldBlock` / `ErrClosed` / `ErrEmpty`）。覆盖率 100%。
- **`xcontainer/xstack`** —— 栈：
  - `Stack[T]`：切片实现，非并发安全，性能优先
  - `ConcurrentStack[T]`：读写锁保护，并发安全
  - `BoundedStack[T]`：有界栈，支持四种溢出策略
  - 能力：`Push/Pop/Peek/PeekN/PopUntil/Search/Values/ForEach/Clone/Clear`，`Values` 为「底→顶」，
    `ForEach` 与 `Pop` 顺序一致；`Search` 对不可比较类型安全（内部用 `DeepEqual`）。覆盖率 96%。
- **`xcontainer/xqueue`** —— 队列：
  - `SliceQueue[T]`：无界，`head` 索引 + 惰性压缩，避免 `Pop` 的 O(n) 搬移
  - `Ring[T]`：定容环形队列，元素预分配、运行期零分配
  - `Bounded[T]`：有界并发队列，channel 实现，四种溢出策略 + `PushCtx`/`PopCtx` 阻塞与取消
  - `Sync[T]`：无界并发队列
  - 能力：`Drain`、`Dropped`（丢弃计数）、`Close`/`IsClosed`、`Peer` 顺序保证。覆盖率 96%。
- **`xtest`** —— 零依赖测试工具：
  - 断言：`Equal` / `NotEqual` / `Equiv`（nil 与空容器等价）/ `True` / `False` / `Nil` / `NotNil` /
    `Zero` / `NotZero` / `NoError` / `Error` / `ErrorIs` / `ErrorContains` / `Panics` / `NotPanics` /
    `Contains` / `NotContains` / `ElementsMatch`（与顺序无关）/ `Len` / `HasKey` / `Greater` / `Less` / `InDelta`
  - 等待：`Eventually` / `EventuallyEqual` / `Never`（替代裸 `time.Sleep` 防偶发失败）
  - golden：`Golden` / `GoldenJSON` / `EqualJSON` / `JSONEqual` / `DiffText`（内置行差分）
  - 假数据：`RandString` / `RandDigits` / `RandInt`（极值安全）/ `RandFloat` / `RandUUID` /
    `RandEmail` / `RandPhone` / `RandName` / `RandIPv4` / `RandTime` / `RandSlice` / `RandMap` / `Pick` / `Shuffle`
  - 其它：`Fill`（按类型自动填充结构体）、`NoGoroutineLeak`（goroutine 泄漏检测）、`TempDir`、`SkipShort`
  - 覆盖率 93%。
- **模型式模糊测试**（用朴素参考模型逐步比对被测实现）：
  - `xcontainer/xstack`：`FuzzStackLIFO`、`FuzzBoundedStackConservation`（元素守恒：入栈 = 剩余 + 丢弃 + 已弹出）
  - `xcontainer/xqueue`：`FuzzSliceQueueFIFO`（覆盖压缩阈值）、`FuzzRingCapacityAndFIFO`、`FuzzBoundedDropPolicies`

### 工程

- `Makefile` 重构：新增 `cover-check`（覆盖率门禁）、`fuzz-all`（全量 fuzz）、`lint`、`ci` 目标
- 新增 `scripts/cover-gate.sh`：两级覆盖率门禁（总分 ≥ 85%、单包 ≥ 75%），阈值可用环境变量覆盖
- 新增 `.golangci.yml`（可选使用）与 `.github/workflows/ci.yml`（门禁 / 跨平台 / fuzz / 可选 lint 四作业）

### 修复（本阶段开发过程中发现并修复的缺陷）

- **`xqueue.Bounded` 多生产者 panic 风险**：原实现用 `close(data channel)` 表示关闭，
  与并发 `Push` 的 "check-then-send" 竞争会触发 `send on closed channel` panic。
  改为独立的 `done` channel + 数据 channel 永不关闭，并保证「关闭后已入队元素仍可完整消费」。
- **`xqueue.Bounded` 关闭后语义不确定**：`select` 在「缓冲区有空位」与「done 已关闭」同时就绪时
  会随机选择分支，导致 `Close` 后 `Push` 时成时败。改为先做确定性的关闭判定，再进入 `select`。
- **`xstack.Stack.Search` 对不可比较类型 panic**：原实现用 `any(x) == any(y)`，
  当元素是切片/map 时动态类型不可比较 → 运行时 panic。改为 `reflect.DeepEqual`。
- **`xtest.format` 超长值未被截断**：字符串 / 字节切片 / error 分支在截断逻辑之前就 `return`，
  导致 1000 字符的值会刷屏。改为所有分支统一走截断。
- **`xtest.DiffText` 行号列宽写死**：`%4d` 使小文件出现大片空白、大文件错位。
  改为按最大行号动态计算列宽。
- **`xtest.RandTime` 与文档不一致**：文档写半开区间 `[from, to)`，实现却是闭区间。
  改为半开区间，并保证 1ns 极小区间返回 `from`。
- **`xtest.Fill` 未处理不可设置的 `reflect.Value`**：已增加 `IsValid`/`CanSet` 守卫与深度上限（8 层），
  自引用结构体不会无限递归。

---

## [0.5.0] - Stage 3-5：字符串 / 数值 / 时间 / map / slice / 转换

### 新增

- **`xstr`** —— 命名风格互转（camel/snake/kebab/constant/dot）、截断（按 rune / 按字节）、
  脱敏（手机号 / 邮箱 / 身份证 / 银行卡 / 姓名）、随机串与字符集、Levenshtein 相似度
- **`xnum`** —— `Decimal`（基于 `big.Rat` 的精确十进制）、浮点舍入（含银行家舍入）、
  字节单位换算（1024/1000）、2~62 进制转换、千分位 / 百分比 / 序数词 / 罗马数字格式化
- **`xtime`** —— `Layouts` 布局嗅探（全工具链唯一来源）、时间边界（起止 / 跨月加减钳位）、
  `HumanizeAgo`、`AutoFromTimestamp`（秒/毫秒/微秒/纳秒自动识别）、Stopwatch
- **`xmap`** —— 合并 / 集合运算 / `ForEachSorted`（稳定遍历）/ `GetPath` / `SetPath` / `FromEntries`
- **`xslice`** —— 去重（保序）/ 变换 / 分组 / 集合运算 / 稳定排序 / 滑窗 / `Chunk` / `Flatten` / `Zip`
- **`xconv`** —— 类型转换枢纽：`Cast`/`CastErr`、`V(x).Path("a.b.c").Int(def)` 容错链、
  map↔slice↔string↔struct↔JSON 五向互转、进制转换
- 模糊测试：`xconv`（`FuzzToInt` / `FuzzIntToBaseRoundTrip` / `FuzzValueChain`）、
  `xslice`（`FuzzDedup` / `FuzzSetOperations` / `FuzzRotateAndWindow`）

### 修复

- `xconv.IntToBase` 对 `MinInt64` 缓冲区越界 panic（`[64]byte` → `[72]byte`）
- `xnum.Decimal.Sign()` 零值空指针崩溃（补 nil 守卫）
- `xmap` 手写 quicksort 分区无进展导致死循环的风险（统一改为稳定归并排序）
- `xstr.IsFloat(".")` 误判为 true（要求至少一个数字）
- `xslice.Clone` 对非 nil 空切片返回 nil（改为 `make` + `copy`）

---

## [0.2.0] - Stage 0-2：底座

### 新增

- **`xconstraints`** —— 泛型约束：`Signed` / `Unsigned` / `Integer` / `Float` / `Complex` /
  `Number` / `Ordered` / `Key`。全工具链唯一约束来源。
- **`xerr`** —— 错误码体系（`Code` + `NewC` / `WrapC`）、包装与分类（`CodeOf` / `MessageOf`）、
  多错误聚合 `MultiError`、校验器 `Validate`。关键设计：`New/Wrap` 不携带错误码，
  `CodeOf` 沿链找第一个**显式设置**码的错误，保证包装不丢业务码。
- **`xsafe`** —— panic 安全：`Go` / `Recover` / `Try` / `Caller` / `ShortFuncName`，
  可配置 `SetPanicHandler` / `SetPanicWriter`。
- **`xcontext`** —— 类型安全的 context 键（`Key[T]`）、`Merge` / `Detach`、HTTP 链路提取。
- **`xtype`** —— 泛型类型操作：`Ptr`（取地址）/ `Addr` / `Add` / `Div` / `Sum`（带除零守卫）/
  比较 / 条件选择 / `Abs`（对 min int 溢出已文档化并测试锁定）。
- **`xreflect`** —— 深拷贝（保留别名与循环引用）、字段元数据缓存（`sync.Map`）、
  **弱类型赋值 `Assign`**（全工具链唯一弱类型转换底座）、struct↔map 互转。
- 工程基线：`go.mod`（零依赖）、`README.md`、`Makefile`、`.gitignore`

### 修复

- `xerr`：`Wrap` 误赋 `CodeUnknown` 导致丢业务码；`CodeOf`/`MessageOf` 只取最外层；
  `MultiError.Error()` 把 nil 计入总数；`Validate` 的 `[]Validator` 不能展开
- `xsafe`：`ShortFuncName` 多算栈帧；默认输出写死 `os.Stderr`（新增 `SetPanicWriter`）
- `xreflect`：变参调用须用 `CallSlice`；`FillDefaults` 不递归无 tag 的嵌套结构体；
  `SetByPath` 中间 nil 指针不分配；`Tag.Has` 不匹配主值；`MapToStruct` 不处理 `json:"-"`
- `xtype`：`Div`/`Mod` 除零守卫

---

## 设计约定（全版本适用）

- **零第三方依赖**：核心包只用标准库，`go.mod` 不含任何 `require`
- **永不 panic**：除 `MustXxx` 变体外，`nil` / 空输入返回零值或 error
- **并发语义显式**：每个类型的 godoc 首行标注「并发安全 / 非并发安全」
- **不可变优先**：返回新集合；原地修改的显式以 `InPlace` 结尾
- **失败可区分**：提供带 error 的变体，以及容错链式取值
- **泛型约束统一**：全部从 `xconstraints` 引用，不在各包重复定义
