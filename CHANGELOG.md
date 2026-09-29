# 更新日志

## [0.11.0] - 2026-09-29

### 新增：`xevent` —— 进程内消息订阅（事件总线）

对应需求「消息订阅」。零第三方依赖，仅复用本工具链的 `xcontainer` / `xerr` / `xkeyed`。

**事件模型**：`Event`（ID / Topic / Key / Payload / Data / Headers / Timestamp / Attempt / TraceID / Source
+ `WithHeader` 深拷贝 / `Clone` / `Header`）。事件 ID 为「时间戳36进制-进程内序号-随机4字节」三段式。

**主题匹配**：精确、`*`（单级通配 / 全匹配）、`**`（多级通配 / 全匹配）、`re:` 前缀正则。
   正则编译结果带缓存（上限 256），非法正则**降级为不匹配**而不是 panic。

**投递模式**（顺序与吞吐不可兼得，必须显式选择）：

| 模式 | 顺序保证 | 实现 |
|---|---|---|
| 默认异步 | 单订阅 FIFO（队列序） | 每订阅 1 个消费者 |
| `WithKey(k)` | 同 key 严格串行 | 复用 `xkeyed` 分片管道 |
| `WithOrdered(true)` | 全局严格串行 | 内部保留 key + 同一分片 |
| `WithConcurrency(n)` | 不保证（换吞吐） | 同订阅 n 个消费者 |
| `WithGroup(g)` | 组内只投一份（轮询），组间广播 | 发布时选组员 |

`WithSync(true)` / `PublishSync`：等所有匹配订阅的 handler 返回并聚合错误（`xerr.MultiError`），
`ErrorPolicy` 支持 `FailFast` / `Collect`。

**背压**：每订阅一条有界队列 + 4 种溢出策略（`Block` 默认阻塞发布 / `DropOldest` / `DropNewest` / `Fail`）。
`DropNewest` 静默丢弃**不报错**（语义是「宁可丢也不阻塞调用方」），`Fail` 才返回 `ErrQueueFull`。

**可靠性**：`WithRetry`（指数退避 + 50%~100% 抖动，上限 5s）→ `WithDeadLetter`（死信回调）
→ 总线级 `WithOnError` 兜底。失败事件的 `Attempt` 逐次递增。

**其它能力**：中间件（`Recover` / `Timeout` / `Validate` / `Skip` / `Tap` / `Chain`）、
`WithFilter`、`WithOnce`、`WithDelay`（延迟投递，关闭时停掉未到期定时器）、
`WithPull`（拉模式，事件流入 `Events()`）、`WithReplayBuffer` + `Replay`（最近 N 条重放）、
`Stats` / `Subscriptions` / `Topics` / `Subscribers`。

### 修复（写测试与压测时发现的真实缺陷）

1. **优雅关闭静默丢事件**：`deliver` 在「订阅 ctx 已被取消」时直接返回，导致**已出队的事件不调用 handler**
   就被丢弃（优雅关闭、`WithOnce` 都会命中）。修复：只有**重试**才因取消而放弃，
   首次投递必执行；排空阶段改用 `context.WithoutCancel(s.ctx)`，让已接收的事件真正处理完。
2. **`Close(ctx)` 的超时形同虚设**：等待逻辑写在 `closeOnce.Do` 内部，`ctx` 超时检查永远执行不到，
   Close 变成不可打断的挂起（实测挂 120s+）。修复：Once 内只做非阻塞通知，
   阻塞等待移到后台 goroutine，调用方通过 `b.drained` + `select ctx.Done()` 真正可超时返回。
3. **`timer.Stop()` 成功时的 WaitGroup 计数丢失**：`Stop()` 返回 true 表示回调永不执行，
   计数只能由调用方补 `Done()`，否则 `Close` 永久阻塞在 `timerWG.Wait()` 上。已修。
4. **`trace_id` 在带自定义头时丢失**：原实现仅在 `Headers == nil` 时注入 `trace_id`。
   改为恒定注入（未显式设置时），并统一走「拷贝一次 + 合并」，不修改调用方传入的 map。
5. **`WithOnce` 自等待死锁**：`Cancel()` 内含 `wg.Wait()`，被 worker 自身调用会等自己。
   拆分为 `stop()`（非阻塞通知）/ `wg.Wait()` / `detach()`，Once 触发走 `stop + 后台 detach`。
6. **订阅自动取消后发布误报 `ErrClosed`**：新增内部信号 `errSubStopped`，
   语义上「订阅自己消失了」不算发布失败 → 跳过并继续；同时把「总线关闭」判定提前，保证错误类型确定。
7. **`atomic.Value.Store(nil)` panic**：`lastErr` 改为互斥锁保护（该字段天然经常为 nil）。

### 变更

- `Bus` 接口在 DESIGN.md 基础上扩展：`PublishSync` / `Stats` / `Subscriptions` / `Topics` /
  `Replay` / `ReplayCount` / `IsClosed`（设计稿把它们写在具体类型上；提升到接口后调用方无需类型断言）。
- `Subscription` 接口补充 `QueueDepth()` / `Stats()`。
- 总线级溢出策略选项命名为 `WithDefaultOverflow`（Go 不支持重载，且与单订阅级 `WithOverflow` 语义不同；
  命名与 `xdistlock.WithDefaultTTL` 保持一致）。
- 新增 fuzz：`FuzzMatchNoPanic`（任意模式不得 panic + 与标准库正则结果一致 + 幂等）、
  `FuzzValidatePatternConsistency`。

---

## [0.10.0] - 2026-09-29

### 新增：`xkeyed` —— 按 key 串行管道（同 key 串行 / MapPipe）

对应需求「同一 key 的 mapPipe」。key 哈希到**固定分片**（默认 2*CPU，最少 8、最多 256），
每分片一条有界队列 + 单 worker：

- ✅ 同 key 必然同分片 → **严格串行**（强保证）；
- ✅ 固定分片表 → **不会随 key 数量增长而泄漏内存**（与逐 key 建队列的实现的关键差别）；
- ⚠️ 不同 key 可能同分片 → 偶发「假串行」（只影响吞吐，不影响正确性）。

API：`New` / `Submit`（不等待）/ `Do`（同步等待）/ `DoValue[T]`（带返回值，包级泛型函数）/
`Wait(ctx)` / `Close` / `CloseCtx(ctx)` / `Stats` / `Pending` / `Shards` / `HashKey` / `ShardOf`。
选项：`WithShards` / `WithQueueSize` / `WithOverflow` / `WithOnError` / `WithOnPanic`。

与 `xlock` 的分工：`xlock` 解决**互斥**（拿不到锁就等），`xkeyed` 解决**顺序**（不阻塞提交、无死锁风险）。

### 修复

1. **`Close` 与并发生产者的 send-on-closed-channel panic**：`Close` 直接关闭分片队列时，
   仍在 select 中阻塞发送的生产者会 panic。修复：引入 `prodMu`/`prodWG` 生产者登记，
   `Close` 的顺序变为「置标志 → 关 closeCh 唤醒阻塞的生产者 → 等生产者全部退出 → 才关队列」。
   （与 `xqueue.Bounded` 是同一类坑，这次以同样的思路根治。）
2. panic 保护刻意**手写**而非套用 `xsafe`：需要在 recover 时同时拿到「panic 值」与「任务 key」以便精准回调。

### 新增 fuzz

`FuzzShardOf`（分片结果必须在范围内 + 可复现 + `shards<=0` 安全退化）、
`FuzzKeyedSerialOrder`（随机提交序列下：同 key 顺序一致、同 key 不并发、不丢不重、统计自洽）。

---

本文件记录 gokit 的阶段性变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [未发布]

### 计划中（后续阶段）

- `xdistlock/etcd` —— 基于 etcd Lease + Txn 的实现（强一致、revision 天然单调、天然公平）。
  **未在本阶段实现**：etcd 官方客户端是强依赖（gRPC 全家桶），
  与本工具链「核心包零第三方依赖」目标冲突；如需要请参考 `xdistlock/redis` 的写法
  仅实现 3 个原语（Acquire/Refresh/Release）。
- `xdistlock` 的 **Redlock**（多节点 quorum）：设计文档明确标注「对时钟敏感、争议大、
  默认关闭」，且必须有多节点真实环境才能验证；在拿到可复现的多节点集成测试之前**不实现**，
  以免交付一个未经充分验证的脑裂防护。防脑裂请使用 fencing token + 资源侧校验。
- `xdistlock` 的 **公平锁**：仅 memory 后端支持；Redis 需 sorted set / Redlock 变种，不做。
- `xevent` —— 进程内消息订阅/事件总线：主题通配、同步/异步分发、背压、死信、按 key 保序
- `xkeyed` —— 同 key 串行化管道（MapPipe）：按 key 路由到独立管道，保证同 key 顺序
- `xpipe` —— 流式管道：source → stage → sink，支持中间件、并发 stage、背压与优雅关闭
- `xcache` —— 本地缓存：TTL / LRU / 多级缓存 / singleflight 防击穿
- `xconcurrency` —— Future、WorkerPool、Semaphore、限流器、熔断器、重试退避、Debounce/Throttle、Batcher
- `xlog` / `xconfig` / `xid` / `xcodec` / `xcrypto` / `xfile` / `xnet` / `xvalidate`
- `xcontainer` 后续子包：`xset`（集合）、`xlru`（LRU/LFU）、`xring`（环形缓冲）、`xbit`（位集合）、`xheap`（优先队列）

---

## [0.9.0] - Stage 9：分布式锁（xdistlock）

### 新增

- **`xdistlock`** —— 分布式锁的统一抽象与**通用机制**（零第三方依赖）：

  > 设计判断：分布式锁真正难的不是存储命令，而是**通用机制** ——
  > 租约续约、丢锁检测、安全释放、重试退避、fencing token。
  > 把这些写进各实现里必然出现 N 套实现与 N 套 bug。
  > 因此把存储压缩成 3 个原语（`Backend`），机制在客户端侧统一实现、
  > 由同一套契约测试覆盖。

  | 能力 | 说明 |
  |---|---|
  | `Locker` 接口 | `Lock`（阻塞）/ `TryLock`（一次）/ `Close` |
  | `Lock` 句柄 | `Key` / `Token` / `FencingToken` / `Expiry` / `TTL` / `IsHeld` / `Unlock` / `Refresh` / `Extend` |
  | **看门狗** | `WithAutoRenew`：按租约 1/3 周期续约；失败分「基础设施错误（继续重试）」与「锁已不属于我（判定丢锁）」 |
  | **安全释放** | 永远 compare-and-delete；锁已丢失时 `Unlock` 返回 `ErrLockLost` 且**绝不触碰后端** |
  | **fencing token** | 由后端发放、严格单调；文档明确「只发不校验 == 没做」 |
  | 重试策略 | `WithRetry`（含 50%~100% 抖动防惊群）/ `WithWaitTimeout` / `WithAttemptTimeout` / `WithBlocking` |
  | 观测 | `Metrics`（尝试/成功/等待时长/续约/丢锁/持有峰值）、`WithOnEvent`、`WithOnAcquired`、**`WithOnLost`** |
  | 排障 | `Snapshot(ctx, prefix)`（需后端实现 `Snapshotter`） |

- **`xdistlock/memory`** —— 进程内参考实现（★ 仅单进程有效，包注释含醒目警告）：
  完整实现安全释放、fencing、**公平 FIFO 队列**（`WithFair(true)`）、可注入时钟
  （`WithClock`，用于确定性地测试租约过期），并作为 `Backend` 语义的规范范例。
- **`xdistlock/redis`** —— Redis 实现，**自带极简 RESP2 客户端**（仅标准库），
  因此仍然零第三方依赖：
  - 加锁 = 一个 Lua 脚本内原子完成 `INCR`（分配 fencing）+ `SET NX PX`，单 RTT；
  - 释放 = Lua `GET == token` 才 `DEL`；续约 = Lua `GET == token` 才 `PEXPIRE`；
  - 连接池复用 + 协议错误自动丢弃坏连接 + `MOVED/ASK` 明确报 `ErrClusterNotSupported`；
  - `PoolStats` / `KeyFor` 便于排障。
  - 边界：**仅单节点**（含主从/哨兵代理），无 cluster、无 pipeline、无 pub/sub。
- **测试**：除常规契约测试外，`xdistlock/redis` 用**脚本化假 Redis 服务端**验证
  RESP 解析的每个分支（含各种畸形响应），并用**真实 Redis 集成测试**（自动探测
  6382/6379，可用 `GOKIT_REDIS_ADDR` 指定）验证 Lua 脚本语义、安全释放与并发互斥。
- 模糊测试：`FuzzLockLifecycleNoLeak`（任意租约/超时/看门狗组合不泄漏）、
  `FuzzTryLockEitherOr`、`FuzzMemoryTokenSafety`（模型式：不抢锁 / 不误删 / fencing 单调）。
- 公平模式的安全性质用**有界确定性压力测试**（`TestFairSafetyStress`）验证，而非 fuzz：
  每次「锁被持有」的 AcquireFair 都要等到 ctx 超时才返回，单次执行代价与输入长度线性相关
  （几千字节的输入要跑上百秒），且「交接 vs ctx 到期」的随机性会让 fuzz 变成 flaky 来源。
  这里改用有限循环 + 3ms 超时，性质覆盖不变、耗时确定为 0.6s。

### 修复（本阶段开发过程中发现并修复的缺陷）

- **`Snapshot` 死循环风险**：SCAN 游标用宽松转换接受了整数响应，
  一个返回整型游标的畸形成功响应会让游标永不推进 → 无限循环。
  改为游标**只接受 bulk**，并加「轮数上限 + 游标必须推进」双重终止保护。
- **`Snapshot` 把 fencing 计数器当成锁**：计数器 key 与锁 key 同前缀，
  被当成「永不释放的锁」列出（负 TTL）。已在扫描结果中显式排除。
- **memory 后端把已过期的租约仍算作「持有中」**：`Snapshot` 会列出负 TTL 的僵尸锁，
  `ActiveKeys` 也会把过期锁计入，与 Redis 的自动过期语义不一致。
  新增 `heldLocked`（token 匹配 **且** 未过期）统一过滤。
- **`AcquireFair` 的「授予 vs ctx 到期」竞态**：两者同时就绪时 `select` 随机选择；
  现在优先判定「是否已被交接」，避免白白浪费一次已到手的获取机会。
- **`conn.do` 的 socket deadline 泄漏风险**：`normCtx` 曾包一层 `WithTimeout` 却不调用
  `cancel`（timer 泄漏），而 `conn.do` 本身已用配置的 `ioTimeout` 作为兜底 deadline，
  该包装是多余的，已删除。

### 工程

- `Makefile` 的 `fuzz-all` 目标扩到 22 个 fuzz target。
- README 增补分布式锁的推荐用法（含「必须开看门狗」「必须校验 fencing token」两条硬要求）。

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
