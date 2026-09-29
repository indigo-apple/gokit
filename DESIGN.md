# gokit — Go 通用工具链设计文档

> 版本：v0.1（草案，用于后续开发参考）
> Go 基线：**Go 1.21**（依赖泛型、`log/slog`、内置 `min`/`max`/`clear`、`slices`/`maps` 标准库）
> 环境：`/opt/go_1.21/go/bin/go`（go1.21.4）
> 模块路径（占位，落地时替换）：`github.com/zhangwenxing/gokit`

---

## 0. 文档导航

| 章节 | 内容 |
|---|---|
| 1 | 项目概述、设计原则、非目标 |
| 2 | 包总览与目录结构 |
| 3 | 需求映射表（原始需求 → 包） |
| 4 | **补充的功能清单**（原始描述中缺失部分） |
| 5 | 各包详细设计（API 草案） |
| 6 | 横切关注点（context / panic / 可观测性） |
| 7 | API 设计规范 |
| 8 | 测试与质量门禁 |
| 9 | CI/CD 与版本发布 |
| 10 | 里程碑规划（P0–P3） |
| 11 | 风险与取舍 |
| 附录 A–D | 命名速查 / 依赖清单 / Go 陷阱清单 / 术语 |

---

## 1. 项目概述

### 1.1 目标

构建一套**生产可用、零或极少第三方依赖、泛型优先**的 Go 基础工具链，覆盖一个后端服务从「数据加工 → 并发控制 → 分布式协调 → 消息流转 → 可观测」的完整链路。定位类似 `samber/lo` + `gookit` + `go-kit` 的子集，但风格统一、可挑选使用（每个包可独立引入）。

**四条主线：**

1. **数据加工线** —— 类型转换、集合操作、泛型算法、基础类型工具
2. **结构容器线** —— stack / queue / deque / set / heap / ring / lru / pipe / keyed-pipe
3. **协调控制线** —— 唯一资源锁、分布式锁、并发原语、缓存
4. **消息与集成线** —— 进程内事件总线 + 外部 MQ 统一适配

**两条贯穿线：** 横切关注点（`xcontext` / `xsafe` / `xerr` / `xlog`）+ 工程质量（`xtest` / CI / 门禁）。

### 1.2 设计原则

| 原则 | 说明 |
|---|---|
| **零依赖优先** | 核心包只依赖标准库；`redis`、`etcd`、`kafka` 等一律放到 `contrib/` 或独立子包，通过接口解耦 |
| **泛型优先** | Go 1.21 基线，集合/容器/管道全部泛型化，杜绝 `interface{}` + 类型断言 |
| **永不 panic** | 除明确的 `MustXxx` 变体外，输入 `nil` / 类型不匹配返回零值或 error，绝不 panic |
| **context 一等公民** | 所有可能阻塞 / 有超时语义的 API，首参必须是 `context.Context` |
| **Options 模式** | 配置项一律 `WithXxx()` 函数式选项 + 合理默认值，禁止长参数列表 |
| **并发安全显式标注** | 每个类型的 godoc 首行注明「并发安全 / 非并发安全」 |
| **不可变优先** | 返回新集合；需要原地修改的提供显式 `XxxInPlace` 变体 |
| **可组合** | 小函数组合优于大而全的函数；管道/中间件可任意拼接 |
| **可替换** | 接口 + 实现分离（锁、缓存、日志、MQ、序列化器），默认实现可被替换 |
| **可测** | 时间、随机、UUID、网络均可注入；核心包覆盖率 ≥ 90% |

### 1.3 非目标（明确不做）

- ❌ 不做 Web 框架（路由/中间件栈交给 gin/echo/chi）
- ❌ 不做 ORM（交给 gorm/sqlx）
- ❌ 不做完整 DI 容器
- ❌ 不做微服务注册发现平台本身（只做客户端适配）
- ❌ 不追求 100% 覆盖所有开源工具，只覆盖「自研服务里反复手写」的那部分

### 1.4 命名与风格约定

- 包名统一 `x` + 短名：`xconv`、`xslice`、`xlock`。原因：**避免与标准库/第三方同名包冲突**（`slices`、`maps`、`sync`、`log` 均已被占用），且一眼可辨属于本工具链。
- 缩写统一大写：`ID`、`URL`、`JSON`、`HTTP`、`TTL`、`IP`。
- 避免 stutter：`xconv.Int(v)` 而非 `xconv.ConvInt(v)`。
- 约束（constraint）集中定义在 `xconstraints`，全工具链复用。

---

## 2. 包总览与目录结构

### 2.1 目录结构

```
gokit/
├── go.mod                      # module github.com/zhangwenxing/gokit
├── README.md
├── DESIGN.md                   # 本文档
├── CHANGELOG.md
├── Makefile                    # make test / lint / bench / cover / gen
├── .golangci.yml
├── .github/workflows/ci.yml
│
├── xconstraints/               # 泛型约束：Number / Integer / Float / Ordered / Comparable / Signed / Key
│
├── # ---------- 数据加工线 ----------
├── xconv/                      # 类型转换（标量、map↔slice、struct↔map、JSON、字节、指针）
├── xslice/                     # 切片算法（去重、过滤、分组、集合运算、排序、滑窗）
├── xmap/                       # 映射算法（合并、过滤、映射、路径取值、有序遍历）
├── xstr/                       # 字符串工具（命名风格、截断、掩码、随机、模板、模糊匹配）
├── xnum/                       # 数值工具（Decimal 精度、区间、随机、字节单位、进制）
├── xtime/                      # 时间工具（格式化、解析、时区、时间区间、节假日、耗时统计）
├── xtype/                      # 泛型类型操作（取地址、解引用、加法、比较、三元、Clamp）
│
├── # ---------- 结构容器线 ----------
├── xcontainer/
│   ├── xstack/                 # 栈（slice 版 / 并发版）
│   ├── xqueue/                 # 队列（slice 版 / 环形 / 有界带溢出策略）
│   ├── xdeque/                 # 双端队列
│   ├── xset/                   # 集合（Set / OrderedSet / 并发 Set）
│   ├── xheap/                  # 优先队列（最小/最大堆、自定义 less）
│   ├── xring/                  # 环形缓冲区（定长、覆盖策略）
│   ├── xlru/                   # LRU / LFU / TTL 缓存淘汰算法（纯算法，无存储）
│   ├── xbitset/                # 位图
│   └── xbloom/                 # 布隆过滤器
├── xpipe/                      # 流式管道（Source→Stage→Sink，并发/背压/错误）
├── xkeyed/                     # 同 key 管道（KeyedPipe / MapPipe，同 key 保序、跨 key 并行）
│
├── # ---------- 协调控制线 ----------
├── xlock/                      # 进程内唯一资源锁（Keyed / Sharded / 可重入 / 资源池）
├── xdistlock/                  # 分布式锁（接口 + 多实现）
│   ├── memory/                 #   内存实现（单测用）
│   ├── redis/                  #   Redis（SET NX PX + Lua 释放 + watchdog + Redlock）
│   ├── etcd/                   #   etcd（lease + revision 公平锁）
│   └── mysql/                  #   MySQL（唯一键 / SELECT FOR UPDATE / 号段）
├── xconcurrency/               # 并发原语（Future、WorkerPool、Semaphore、限流、熔断、重试、Singleflight）
├── xcache/                     # 缓存（内存 TTL/LRU、多级缓存、防击穿）
│
├── # ---------- 消息与集成线 ----------
├── xevent/                     # 进程内事件总线（pub/sub、通配主题、顺序、背压、死信、重放）
├── xmq/                        # 外部 MQ 统一抽象（接口 + 适配器）
│   ├── mqtt/
│   ├── kafka/
│   ├── redisstream/
│   └── rabbitmq/
│
├── # ---------- 基础支撑 ----------
├── xerr/                       # 错误（错误码、包装、分类、可重试判定、聚合、Must）
├── xcontext/                   # context 值传递（trace/user/tenant、typed key、合并）
├── xsafe/                      # panic 安全（Go、Recover、PanicToError）
├── xlog/                       # 结构化日志（slog 封装、trace 注入、脱敏、轮转、采样）
├── xconfig/                    # 配置（多源加载、结构体绑定、热加载、加密字段）
├── xid/                        # ID 生成（UUIDv4/v7、Snowflake、短 ID、号段）
├── xcodec/                     # 编解码（JSON、Base64/Hex/URL、压缩、msgpack/protobuf 适配、CSV）
├── xcrypto/                    # 哈希/加密（md5~sha512、bcrypt/argon2、AES-GCM、RSA、HMAC、随机）
├── xfile/                      # 文件 IO（原子写、遍历、文件锁、监视、归档、分片）
├── xnet/                       # 网络工具（HTTP client、IP/CIDR、连通性探测、URL 工具）
├── xvalidate/                  # 校验（规则链、结构体 tag、内置规则、i18n）
├── xreflect/                   # 反射工具（DeepCopy、字段元数据缓存、tag 解析、动态赋值）
├── xtest/                      # 测试工具（断言、golden、faker、时间冻结、httptest 封装）
│
├── contrib/                    # 可选集成（引入即带第三方依赖）
│   ├── xotel/                  #   OpenTelemetry 适配
│   ├── xprom/                  #   Prometheus metrics 适配
│   └── xzap/                   #   zap 日志适配
└── examples/                   # 可运行示例，每个包至少 1 个
```

### 2.2 包总览表

| 包 | 一句话职责 | 核心导出 | 依赖 |
|---|---|---|---|
| `xconstraints` | 泛型约束集合 | `Number` `Integer` `Ordered` `Signed` | std |
| `xconv` | 任意 → 目标类型的容错转换 | `ToString/ToInt/ToSlice/MapToSlice/SliceToString/ToStruct` | std |
| `xslice` | 切片算法全家桶 | `Dedup/Map/Filter/GroupBy/Union/Chunk/SortBy` | std |
| `xmap` | 映射算法全家桶 | `Merge/Keys/Values/Entries/GetPath/Filter` | std |
| `xstr` | 字符串工具 | `CamelCase/SnakeCase/Truncate/Mask/Rand` | std |
| `xnum` | 数值工具 | `Decimal/Round/Clamp/Bytes/Rand` | std |
| `xtime` | 时间工具 | `Format/Parse/BeginOfDay/Humanize/Timer` | std |
| `xtype` | 泛型类型操作 | `Ptr/Deref/Add/Sub/Compare/Ternary/Clamp` | std |
| `xcontainer/*` | 基础数据结构 | `Stack/Queue/Deque/Set/Heap/Ring/LRU/BitSet/Bloom` | std |
| `xpipe` | 流式管道 | `FromSlice/Map/Filter/Batch/Sink/Run` | std |
| `xkeyed` | 同 key 保序管道 | `New/Submit/Map/Merge/Shutdown` | std |
| `xlock` | 单机唯一资源锁 | `NewKeyed/NewSharded/With/NewResourcePool` | std |
| `xdistlock` | 分布式锁 | `Locker/Lock/Unlock/Refresh/FencingToken` | ✅ 实现层 |
| `xconcurrency` | 并发原语 | `Future/Pool/Semaphore/Limiter/Breaker/Retry/Singleflight` | std |
| `xcache` | 缓存 | `Cache/Memory/TwoLevel/GetOrLoad` | std |
| `xevent` | 进程内事件总线 | `Bus/Publish/Subscribe/Subscription` | std |
| `xmq` | MQ 统一抽象 | `Publisher/Subscriber/Message/Handler` | ✅ 适配层 |
| `xerr` | 错误体系 | `New/Wrap/Code/Is/Join/Must/MultiError` | std |
| `xcontext` | context 值 | `With/From/WithTraceID/TraceID` | std |
| `xsafe` | panic 安全 | `Go/Recover/PanicToError` | std |
| `xlog` | 结构化日志 | `L/F/Info/Error/With/SetLevel` | std(slog) |
| `xconfig` | 配置加载 | `Load/MustLoad/Watch/Get` | std + 可选 yaml |
| `xid` | ID 生成 | `UUID/Snowflake/Short/Segment` | std |
| `xcodec` | 编解码 | `JSON/Base64/Gzip/Msgpack/CSV` | std + 可选 |
| `xcrypto` | 哈希与加密 | `MD5/SHA256/Bcrypt/AESGCM/RSA/HMAC/Rand` | std |
| `xfile` | 文件 IO | `ReadAll/WriteAtomic/Walk/Lock/Watch/Zip` | std + 可选 fsnotify |
| `xnet` | 网络工具 | `GetJSON/PostJSON/IPInCIDR/ParseURL` | std |
| `xvalidate` | 校验 | `Field/Struct/Required/Email/Phone/IDCard` | std |
| `xreflect` | 反射工具 | `DeepCopy/Fields/SetField/ParseTag` | std |
| `xtest` | 测试工具 | `Equal/NoError/Golden/Fill/FreezeTime` | std |

---

## 3. 需求映射表

原始需求逐条落到具体包，便于开工时对号入座。

| # | 原始需求 | 归属包 | 关键 API |
|---|---|---|---|
| 1 | 类型转换 | `xconv` | `ToString/ToInt/ToBool/ToDuration/ToTime` |
| 2 | map 转 slice | `xconv` + `xmap` | `xconv.MapToSlice`、`xmap.Keys/Values/Entries` |
| 3 | sliceToString | `xconv` | `SliceToString/SliceToStringFunc` |
| 4 | slice 去重 | `xslice` | `Dedup/DedupBy/DedupLast/DedupInPlace` |
| 5 | 取地址符 | `xtype` | `Ptr/Addr/Ptrs/Deref/DerefOr` |
| 6 | 相加等运算 | `xtype` | `Add/Sub/Mul/Div/Mod/Sum/Min/Max/Abs` |
| 7 | stack | `xcontainer/xstack` | `NewStack/Push/Pop/Peek` |
| 8 | queue | `xcontainer/xqueue` | `NewSliceQueue/NewRingQueue/NewBoundedQueue` |
| 9 | pipe | `xpipe` | `FromSlice/Map/Filter/Batch/Sink/Run` |
| 10 | 同一 key 的 mapPipe | `xkeyed` | `New/Submit(key, v)/Shutdown` |
| 11 | 唯一资源锁 | `xlock` | `NewKeyed/With/NewResourcePool` |
| 12 | 分布式锁 | `xdistlock` | `Locker.Lock/Unlock/Refresh` |
| 13 | 消息订阅 | `xevent` + `xmq` | `Bus.Subscribe/Publish`、`xmq.Subscriber` |

---

## 4. 补充的功能清单（原始描述缺失部分）

以下是原始需求没提到、但「完整工具链」必须补齐的能力，按优先级排列。

**A. 数据加工线补齐**
1. `struct ↔ map` 双向转换（带 tag 与嵌套、切片、指针处理）
2. `struct ↔ JSON` 便捷转换（含 `must` 变体与流式编解码）
3. 三态转换（`bool` 的 `"1"/"true"/"yes"/"on"` 全兼容，杜绝「以为是 true 实际 false」）
4. **指针/空值工具**：`Ptr`、`Deref`、`DerefOr`、`Coalesce`、`Default`、`IsNil`、`FilterNil`
5. **集合运算**：并集/交集/差集/对称差、`Zip`、`Flatten`、`Chunk`、滑窗
6. **分组与聚合**：`GroupBy`、`Partition`、`CountBy`、`Sum/Avg/Min/Max`
7. **稳定排序**：`SortBy` 支持多字段 + 升降序，避免 map 遍历随机性问题
8. `map` 的**有序遍历**（`ForEachSorted`、`SortedKeys`）—— 直接解决「打印/写文件结果每次不一样」
9. **泛型约束体系**（`xconstraints`），避免每个包重复定义
10. 字符串**命名风格互转**（camel/snake/kebab/pascal）、截断、脱敏掩码、安全随机串
11. 数值 **Decimal 精度工具**（浮点金额计算），字节单位换算，进制转换

**B. 结构容器线补齐**
12. `Deque`、`Set`/`OrderedSet`、`PriorityQueue`、`BitSet`、`BloomFilter`
13. `RingBuffer` 定长环形缓冲（日志/指标/最近 N 条事件）
14. `LRU/LFU/TTL` 淘汰算法
15. **有界队列 + 溢出策略**（阻塞 / 丢最旧 / 丢最新 / 报错）—— 背压的基础
16. **容器统一接口**（`Collection[T]`、`Iterator[T]`）方便泛化使用

**C. 协调控制线补齐**
17. **分片锁**（降低热点 key 的锁竞争）
18. **可重入锁**
19. **资源池信号量**（「唯一资源」允许 N 个并发）
20. **锁的可观测性**：等待次数、持有时间、P99、死锁超时
21. 分布式锁的**看门狗自动续约**、**安全释放（CAS/Lua）**、**fencing token 防脑裂**
22. 分布式锁**多实现**：memory / redis / etcd / mysql
23. **并发原语**：`Future/Promise`、`WorkerPool`、`Semaphore`、`TokenBucket 限流`、`CircuitBreaker 熔断`、`Retry 退避+jitter`、`Singleflight`、`Debounce/Throttle`、`Batcher`
24. **缓存**：本地 TTL/LRU、多级缓存（L1 本地 + L2 Redis）、`GetOrLoad` 防击穿、TTL jitter 防雪崩、空值缓存防穿透
25. `ConcurrentMap[K,V]`（泛型 `sync.Map` 封装）、`AtomicValue[T]`、`ObjectPool`

**D. 消息订阅补齐**
26. **主题匹配**：精确 / 前缀 `user.*` / 通配 `user.**` / 正则
27. **投递模式**：同步（等所有 handler）/ 异步（worker pool）/ 混合
28. **顺序保证**：全局有序 / 按 key 有序（复用 `xkeyed`）/ 无序
29. **背压**：有界 channel + 溢出策略
30. **失败处理**：错误聚合、重试（次数 + 退避）、**死信队列**、fail-fast 开关
31. **中间件链**：日志 / recover / trace / 限流 / 度量，可自由叠加
32. **一次性订阅**（`Once`）、**延迟投递**（`Delay`）、**事件过滤链**
33. **事件重放**：保留最近 N 条 + `Replay`
34. **消费组**：广播 vs 分组消费（同组只投一次）
35. **外部 MQ 统一抽象**（`xmq`）：MQTT / Kafka / Redis Stream / RabbitMQ 用同一套 API，本地总线与远程总线可无缝切换

**E. 基础支撑补齐（原需求完全没提，但没有它们工具链不成立）**
36. **错误体系**：错误码、包装、分类、`IsRetryable/IsNotFound/IsConflict`、多错误聚合、`Must` 变体
37. **日志**：结构化日志、trace id 自动注入、字段化、脱敏 hook、级别热更新、轮转、采样
38. **配置**：多源合并（默认值→文件→env→flag→远程）、结构体绑定 + tag、热加载、加密字段、校验
39. **ID 生成**：UUIDv4/v7、Snowflake（含时钟回拨处理）、短 ID、号段模式
40. **时间工具**：格式化/解析、时区、时间区间、耗时统计、定时器/定时任务（cron）、时间轮
41. **编解码**：JSON（含 `jsoniter` 可选加速）、Base64/Hex/URL、gzip/zstd 压缩、msgpack/protobuf/CSV
42. **加密哈希**：md5~sha512、`bcrypt`/`argon2id` 密码、HMAC 签名、AES-GCM、RSA/ECDSA、安全随机、常量时间比较
43. **文件 IO**：原子写（临时文件 + rename + fsync）、大文件流式读写、目录遍历（支持 ignore 规则）、跨进程文件锁、文件监视、归档（zip/tar/gz）、分片合并、断点续传
44. **网络工具**：带重试/熔断/trace 的 HTTP client、`GetJSON[T]`/`PostJSON[T]` 泛型便捷方法、IP↔int、CIDR 判断/合并、URL 解析与规范化、连通性探测
45. **校验**：函数式规则链 + 结构体 tag，内置邮箱/手机号/身份证（校验位）/银行卡（Luhn）/密码强度等
46. **反射工具**：`DeepCopy`（零依赖）、字段元数据缓存（避免热路径反射）、`ParseTag`、动态取值赋值
47. **测试工具**：轻量断言、golden file、faker 随机数据、时间冻结、httptest 封装 —— 让工具链自身的测试成本可控
48. **横切三件套**：`xcontext`（值传递）、`xsafe`（panic 安全）、可观测性 hook（不发散地绑定 OTel/Prometheus）
49. **性能与质量**：benchmark 基准与回归门禁、`go test -race`、模糊测试（转换/解析类必备）、`golangci-lint`

---

## 5. 各包详细设计

> 以下签名是**设计草案**，用于对齐接口形态，落地时以实际实现为准。
> 约定：`T`/`K`/`V` 为泛型参数；`fn` 为回调；`…` 表示省略；`//` 后为语义说明。

### 5.1 `xconstraints` — 泛型约束

```go
package xconstraints

type Signed interface{ ~int | ~int8 | ~int16 | ~int32 | ~int64 }
type Unsigned interface{ ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr }
type Integer interface{ Signed | Unsigned }
type Float interface{ ~float32 | ~float64 }
type Complex interface{ ~complex64 | ~complex128 }

// Number = 可做 + - * / 的类型
type Number interface{ Integer | Float }

// Ordered = 可比较大小（可用于 Min/Max/排序/Clamp）
type Ordered interface{ Integer | Float | ~string }

// Comparable 直接复用内置 comparable，无需定义

type Key interface{ comparable }   // 语义别名，作为 map key 用
```

**要点**：全工具链禁止各自重复定义，统一从此包取。

---

### 5.2 `xconv` — 类型转换

设计目标：**任意输入 → 目标类型，永不 panic，失败给零值或 error**。

```go
package xconv

// ---------- 1. 标量转换（永不 panic，失败返回零值） ----------
func ToString(v any) string          // nil→"", []byte→string, fmt.Sprint 兜底
func ToInt(v any) int
func ToInt64(v any) (int64, error)
func ToUint64(v any) uint64
func ToFloat64(v any) float64
func ToFloat32(v any) float32
func ToBool(v any) bool              // 兼容 "1","true","yes","on","Y" / 1
func ToDuration(v any) time.Duration // "1h30m"、"90"(秒) 均可
func ToTime(v any) (time.Time, error)// 支持多种布局自动嗅探、Unix 秒/毫秒/纳秒
func ToBytes(v any) []byte
func ToStringSlice(v any) []string
func ToIntSlice(v any) []int
func ToStringMap(v any) map[string]any

// ---------- 2. 带默认值 / 容错链式取值 ----------
// 用法：xconv.V(m["age"]).Int(18)   xconv.V(cfg["host"]).String("localhost")
type Value struct{ v any }
func V(v any) Value
func (x Value) String(def ...string) string
func (x Value) Int(def ...int) int
func (x Value) Int64(def ...int64) int64
func (x Value) Float64(def ...float64) float64
func (x Value) Bool(def ...bool) bool
func (x Value) Duration(def ...time.Duration) time.Duration
func (x Value) Time(def ...time.Time) time.Time
func (x Value) Slice() []any
func (x Value) Map() map[string]any
// 嵌套取值：xconv.V(m).Path("user.profile.age").Int(0)
func (x Value) Path(path string) Value

// ---------- 3. map ↔ slice ----------
func MapToSlice[T any, K comparable, V any](m map[K]V, fn func(K, V) T) []T
func MapToSliceSorted[T any, K xconstraints.Ordered, V any](m map[K]V, fn func(K, V) T) []T // 按 key 稳定输出
func MapKeys[K comparable, V any](m map[K]V) []K
func MapValues[K comparable, V any](m map[K]V) []V
func MapKeysSorted[K xconstraints.Ordered, V any](m map[K]V) []K
func MapEntries[K comparable, V any](m map[K]V) []xmap.Entry[K, V]
func MapToPairs[K comparable, V any](m map[K]V) []xmap.Pair[K, V]   // 同上，别名

func SliceToMap[T any, K comparable, V any](s []T, keyFn func(T) K, valFn func(T) V) map[K]V
func SliceToMapSelf[T any, K comparable](s []T, keyFn func(T) K) map[K]T
func SliceToSet[T comparable](s []T) map[T]struct{}
func SliceToCountMap[T comparable](s []T) map[T]int     // 词频统计

// ---------- 4. slice ↔ string ----------
func SliceToString[T any](s []T, sep string) string                       // 等价 strings.Join + fmt
func SliceToStringFunc[T any](s []T, sep string, fn func(T) string) string
func SliceToStringQuoted[T any](s []T, sep string) string                 // 元素加引号，便于日志
func StringToSlice(s, sep string) []string
func StringToIntSlice(s, sep string) ([]int, error)
func StringToSliceClean(s, sep string) []string                          // 去空段 + trim

// ---------- 5. struct ↔ map / JSON ----------
func StructToMap(v any) (map[string]any, error)                           // 尊重 json/map tag
func StructToMapString(v any) (map[string]string, error)
func MapToStruct[T any](m map[string]any, opts ...Option) (*T, error)     // 支持弱类型转换
func ToStruct[T any](v any) (T, error)                                    // any/json.RawMessage/[]byte/map 均可
func ToStructs[T any](v any) ([]T, error)

func ToJSON(v any) ([]byte, error)
func ToJSONString(v any) string
func FromJSON[T any](data []byte) (T, error)
func FromJSONString[T any](s string) (T, error)
func MustFromJSON[T any](data []byte) T                                   // 失败 panic，仅用于初始化
func DeepCopyJSON[T any](v T) T                                           // JSON 往返深拷贝

// ---------- 6. 数值 ↔ 字节 / 进制 / 单位 ----------
func BytesToHuman(n int64) string         // 1.5MB / 2.3GiB
func HumanToBytes(s string) (int64, error)
func IntToBase(n int64, base int) string  // 2/8/16/36/62
func BaseToInt(s string, base int) (int64, error)

// ---------- 7. 指针 / 空值 ----------
func Ptr[T any](v T) *T
func PtrOrNil[T comparable](v T) *T       // 零值返回 nil，适合可选字段序列化
func Deref[T any](p *T) T
func DerefOr[T any](p *T, def T) T
func IsNil(v any) bool                    // 正确识别 typed nil（interface 持 nil 指针）
func IsEmpty(v any) bool                  // nil / 零值 / 空串 / 空集合 均 true
func Default[T comparable](v, def T) T
func Coalesce[T comparable](vals ...T) T  // 首个非零值
func Or[T any](v *T, def T) T
```

**不变量 / 约束**

- 所有 `ToXxx` 对 `nil` 输入返回零值，不 panic。
- 浮点 → 整型采用**截断**（不是四舍五入），并文档化；需要四舍五入用 `xnum.Round`。
- `ToString` 对 `time.Time` / `time.Duration` 有专门处理（ISO8601 / `1h30m0s`）。
- 数字字符串解析用 `strconv` 原语，禁止 `fmt.Sscanf`（慢且易错）。
- 热路径上避免 `reflect`：`StructToMap` 依赖 `xreflect` 的 `sync.Map` 元数据缓存。

---

### 5.3 `xslice` — 切片算法

```go
package xslice

// ---------- 去重 ----------
func Dedup[T comparable](s []T) []T                                   // 保序，保留首次出现
func DedupLast[T comparable](s []T) []T                               // 保序，保留最后一次出现位置
func DedupBy[T any, K comparable](s []T, key func(T) K) []T           // 按 key 去重
func DedupByLast[T any, K comparable](s []T, key func(T) K) []T
func DedupInPlace[T comparable](s []T) []T                            // 复用底层数组，零拷贝
func DedupKeepOrder...                                                // 别名，语义同上
func CountBy[T any, K comparable](s []T, key func(T) K) map[K]int     // 出现次数，便于找出重复项

// ---------- 遍历与变换 ----------
func Map[T, R any](s []T, fn func(T) R) []R
func MapIdx[T, R any](s []T, fn func(int, T) R) []R
func MapErr[T, R any](s []T, fn func(T) (R, error)) ([]R, error)      // 遇错即停
func MapConcat[R any, T any](s []T, fn func(T) []R) []R               // 扁平映射
func FilterMap[T, R any](s []T, fn func(T) (R, bool)) []R
func Filter[T any](s []T, pred func(T) bool) []T
func FilterNil[T any](s []*T) []T                                     // 剔除 nil 指针
func Reject[T any](s []T, pred func(T) bool) []T                      // Filter 取反
func Reduce[T, A any](s []T, init A, fn func(acc A, v T) A) A
func ReduceRight[T, A any](s []T, init A, fn func(acc A, v T) A) A
func ForEach[T any](s []T, fn func(T))
func ForEachIdx[T any](s []T, fn func(int, T))
func ForEachErr[T any](s []T, fn func(T) error) error

// ---------- 查找 ----------
func Contains[T comparable](s []T, v T) bool
func ContainsBy[T any](s []T, pred func(T) bool) bool
func IndexOf[T comparable](s []T, v T) int
func IndexOfBy[T any](s []T, pred func(T) bool) int
func LastIndexOf[T comparable](s []T, v T) int
func Find[T any](s []T, pred func(T) bool) (T, bool)
func FindIdx[T any](s []T, pred func(T) bool) (int, bool)
func FindLast[T any](s []T, pred func(T) bool) (T, bool)
func Any[T any](s []T, pred func(T) bool) bool
func All[T any](s []T, pred func(T) bool) bool
func None[T any](s []T, pred func(T) bool) bool
func Count[T any](s []T, pred func(T) bool) int

// ---------- 分组 / 切分 ----------
func GroupBy[T any, K comparable](s []T, key func(T) K) map[K][]T
func GroupByOrdered[T any, K comparable](s []T, key func(T) K) ([]K, map[K][]T) // 保序分组
func Partition[T any](s []T, pred func(T) bool) (yes, no []T)
func Chunk[T any](s []T, n int) [][]T                                 // 每 n 个一组
func Window[T any](s []T, n int, step ...int) [][]T                   // 滑窗
func SplitAt[T any](s []T, i int) (left, right []T)
func SplitBy[T any](s []T, pred func(T) bool) [][]T                   // 按谓词分段
func Flatten[T any](s [][]T) []T
func FlattenDeep... // 通过递归版 FlattenAny

// ---------- 集合运算 ----------
func Union[T comparable](lists ...[]T) []T
func Intersect[T comparable](lists ...[]T) []T
func Difference[T comparable](a, b []T) []T          // a 有 b 没有
func SymmetricDifference[T comparable](a, b []T) []T
func IsSubset[T comparable](a, b []T) bool
func Equal[T comparable](a, b []T) bool              // 顺序敏感
func EqualUnordered[T comparable](a, b []T) bool     // 顺序不敏感
func Overlaps[T comparable](a, b []T) bool

// ---------- 顺序操作 ----------
func Reverse[T any](s []T) []T
func ReverseInPlace[T any](s []T)
func Shuffle[T any](s []T)                           // 基于 xnum 的随机源，可注入种子
func Rotate[T any](s []T, n int) []T
func Take[T any](s []T, n int) []T
func TakeWhile[T any](s []T, pred func(T) bool) []T
func Skip[T any](s []T, n int) []T
func SkipWhile[T any](s []T, pred func(T) bool) []T
func Head[T any](s []T) (T, bool)
func Last[T any](s []T) (T, bool)
func Tail[T any](s []T) []T
func Compact[T comparable](s []T) []T                // 去零值

// ---------- 增删 ----------
func Insert[T any](s []T, i int, vs ...T) []T
func RemoveAt[T any](s []T, i int) []T
func Remove[T comparable](s []T, v T) []T
func RemoveAll[T comparable](s []T, vs ...T) []T
func RemoveBy[T any](s []T, pred func(T) bool) []T
func Replace[T comparable](s []T, old, new T, n ...int) []T
func Concat[T any](lists ...[]T) []T
func Repeat[T any](v T, n int) []T
func Pad[T any](s []T, n int, fill T) []T

// ---------- 排序（稳定） ----------
func SortBy[T any, K xconstraints.Ordered](s []T, key func(T) K, desc ...bool) []T
func SortStableBy[T any, K xconstraints.Ordered](s []T, key func(T) K, desc ...bool) []T
func SortMulti[T any](s []T, less ...func(a, b T) int) []T   // 多字段，依次比较
func MinBy[T any, K xconstraints.Ordered](s []T, key func(T) K) (T, bool)
func MaxBy[T any, K xconstraints.Ordered](s []T, key func(T) K) (T, bool)

// ---------- 数值聚合 ----------
func Sum[T xconstraints.Number](s []T) T
func Average[T xconstraints.Number](s []T) float64
func Min[T xconstraints.Ordered](s []T) (T, bool)
func Max[T xconstraints.Ordered](s []T) (T, bool)
func MinMax[T xconstraints.Ordered](s []T) (T, T, bool)

// ---------- 其他 ----------
func Clone[T any](s []T) []T                         // 浅拷贝（避免共享底层数组）
func CloneDeep[T any](s []T) []T                     // 深拷贝，依赖 xreflect
func Zip[A, B any](a []A, b []B) []xmap.Pair[A, B]
func Unzip[A, B any](pairs []xmap.Pair[A, B]) ([]A, []B)
func Rand[T any](s []T) (T, bool)
func Sample[T any](s []T, n int) []T
```

**关键不变量**

1. **所有函数输入 `nil` 切片必须返回 `nil` 或空切片，不得 panic。**
2. `Dedup` 系列**保序**（稳定），因为大量场景依赖「首次出现顺序」。
3. `SortBy` 系列一律**稳定排序**（`slices.SortStableFunc`），避免同 key 元素顺序抖动。
4. `InPlace` 变体与不可变变体严格区分，并在文档中警告「`InPlace` 会修改传入切片（含调用方可能仍持有引用的底层数组）」。
5. `Map`/`Filter` 尽可能**预分配容量**（`make([]R, 0, len(s))`），减少扩容。

---

### 5.4 `xmap` — 映射算法

```go
package xmap

type Pair[K comparable, V any] struct{ Key K; Val V }
type Entry[K comparable, V any] = Pair[K, V]           // 别名，兼容不同习惯

// ---------- 基础 ----------
func Keys[K comparable, V any](m map[K]V) []K
func KeysSorted[K xconstraints.Ordered, V any](m map[K]V) []K
func Values[K comparable, V any](m map[K]V) []V
func ValuesSortedByKey[K xconstraints.Ordered, V any](m map[K]V) []V
func Entries[K comparable, V any](m map[K]V) []Pair[K, V]
func EntriesSorted[K xconstraints.Ordered, V any](m map[K]V) []Pair[K, V]
func Has[K comparable, V any](m map[K]V, k K) bool
func Get[K comparable, V any](m map[K]V, k K) (V, bool)
func GetOr[K comparable, V any](m map[K]V, k K, def V) V
func GetOrSet[K comparable, V any](m map[K]V, k K, def V) V            // 不存在则写入并返回

// ---------- 变换 ----------
func MapValues[K comparable, V, R any](m map[K]V, fn func(V) R) map[K]R
func MapValuesKV[K comparable, V, R any](m map[K]V, fn func(K, V) R) map[K]R
func MapKeys[K, K2 comparable, V any](m map[K]V, fn func(K, V) K2) map[K2]V
func Filter[K comparable, V any](m map[K]V, pred func(K, V) bool) map[K]V
func FilterKeys[K comparable, V any](m map[K]V, pred func(K) bool) map[K]V
func FilterValues[K comparable, V any](m map[K]V, pred func(V) bool) map[K]V
func Invert[K, V comparable](m map[K]V) map[V]K
func Pick[K comparable, V any](m map[K]V, keys ...K) map[K]V
func Omit[K comparable, V any](m map[K]V, keys ...K) map[K]V
func GroupByValue[K comparable, V any, G comparable](m map[K]V, key func(K, V) G) map[G]map[K]V

// ---------- 合并 / 运算 ----------
func Merge[K comparable, V any](maps ...map[K]V) map[K]V               // 后者覆盖前者
func MergeKeepFirst[K comparable, V any](maps ...map[K]V) map[K]V      // 前者优先
func MergeWith[K comparable, V any](resolve func(old, new V) V, maps ...map[K]V) map[K]V
func MergeDeep[K comparable, V any](maps ...map[K]V) map[K]V           // 值也是 map 时递归合并
func Diff[K comparable, V comparable](a, b map[K]V) map[K]V            // a 中与 b 不同的项
func DiffKeys[K comparable, V any](a, b map[K]V) (onlyA, onlyB, both []K)
func IntersectKeys[K comparable, V any](a, b map[K]V) map[K]V

// ---------- 遍历（顺序确定性） ----------
func ForEach[K comparable, V any](m map[K]V, fn func(K, V))
func ForEachSorted[K xconstraints.Ordered, V any](m map[K]V, fn func(K, V))   // ★ 解决「打印顺序随机」
func ForEachSortedBy[K comparable, V any](m map[K]V, less func(a, b K) bool, fn func(K, V))

// ---------- 嵌套路径取值 ----------
func GetPath[V any](m map[string]any, path string) (V, bool)           // "a.b.c" / "a.b[0].c"
func GetPathOr[V any](m map[string]any, path string, def V) V
func SetPath(m map[string]any, path string, val any) error
func DeletePath(m map[string]any, path string) error
func FlattenKeys(m map[string]any, sep string) map[string]any          // 嵌套 → 扁平 a.b.c
func UnflattenKeys(m map[string]any, sep string) map[string]any        // 扁平 → 嵌套

// ---------- 克隆 ----------
func Clone[K comparable, V any](m map[K]V) map[K]V
func CloneDeep[K comparable, V any](m map[K]V) map[K]V
func Copy[K comparable, V any](dst, src map[K]V)                       // 合并进已有 map
```

**要点**

- 所有返回 map 的函数在输入 `nil` 时返回**空 map（非 nil）**或 nil，需在文档中二选一并统一 —— 建议返回 `nil`，与标准库习惯一致。
- `ForEachSorted` 是高频刚需：Go 的 map 遍历顺序随机，日志/报表/测试期望稳定输出时必须用它。
- `CloneDeep` 与 `xreflect.DeepCopy` 共用实现。

---

### 5.5 `xtype` — 泛型类型操作（取地址 / 相加 / 比较）

这是原始需求里「取地址符」「相加等」的落点。

```go
package xtype

// ---------- 取地址 / 解引用 ----------
func Ptr[T any](v T) *T                       // 取地址，最常用
func Addr[T any](v T) *T                      // 别名，语义更贴近「取地址符 &
func Ptrs[T any](s []T) []*T                  // 批量取地址（注意 v 的循环变量拷贝陷阱）
func Deref[T any](p *T) T                     // nil → 零值
func DerefOr[T any](p *T, def T) T
func Derefs[T any](s []*T) []T
func Swap[T any](a, b *T)                     // 交换两个指针指向的值
func IsNilPtr[T any](p *T) bool

// ---------- 泛型算术（对应「相加等」） ----------
func Add[T xconstraints.Number](a, b T) T
func Sub[T xconstraints.Number](a, b T) T
func Mul[T xconstraints.Number](a, b T) T
func Div[T xconstraints.Number](a, b T) T     // b==0 返回零值（浮点则 Inf/NaN）
func Mod[T xconstraints.Integer](a, b T) T
func Neg[T xconstraints.Signed | xconstraints.Float](a T) T
func Abs[T xconstraints.Signed | xconstraints.Float](a T) T
func Inc[T xconstraints.Number](a *T, delta T)
func Sum[T xconstraints.Number](vs ...T) T
func Average[T xconstraints.Number](vs ...T) float64
func Min[T xconstraints.Ordered](a, b T) T
func Max[T xconstraints.Ordered](a, b T) T
func MinN[T xconstraints.Ordered](vs ...T) T
func MaxN[T xconstraints.Ordered](vs ...T) T
func Clamp[T xconstraints.Ordered](v, lo, hi T) T
func ClampMin/ClampMax[T xconstraints.Ordered](v, bound T) T
func Between[T xconstraints.Ordered](v, lo, hi T) bool
func MapRange[T, R xconstraints.Number](v, inMin, inMax, outMin, outMax T) R   // 区间映射

// ---------- 比较 ----------
func Equal[T comparable](a, b T) bool
func EqualFunc[T any](a, b T, eq func(a, b T) bool) bool
func NotEqual[T comparable](a, b T) bool
func Compare[T xconstraints.Ordered](a, b T) int      // -1 / 0 / 1
func CompareFunc[T any](a, b T, less func(a, b T) bool) int
func Less/More[T xconstraints.Ordered](a, b T) bool
func InRange[T xconstraints.Ordered](v, min, max T) bool
func IsZero[T comparable](v T) bool
func Zero[T any]() T                                   // 返回类型零值
func ZeroOf[T any](_ T) T                              // 由样本推导类型零值
func DefaultIfZero[T comparable](v, def T) T
func Copy... // 见 xreflect.DeepCopy

// ---------- 条件与选择 ----------
func Ternary[T any](cond bool, ifTrue, ifFalse T) T
func If[T any](cond bool, fn func() T, elseFn ...func() T) T
func Unless[T any](cond bool, fn func() T, elseFn ...func() T) T
func Coalesce[T comparable](vals ...T) T
func FirstNonZero... // 别名
func OrDefault[T comparable](v, def T) T
func Must[T any](v T, err error) T                     // 一行吞掉 error，初始化用
func Try[T any](fn func() (T, error)) (T, error)

// ---------- 类型断言 / 判定 ----------
func As[T any](v any) (T, bool)                        // 安全类型断言
func MustAs[T any](v any) T                            // 断言失败 panic
func Is[T any](v any) bool                             // 是否可断言为 T
func TypeName(v any) string
func Cast[T any](v any) T                              // 尽力转换，走 xconv
func Switch[T any, R any](v any, cases map[reflect.Type]func(any) R, def func(any) R) R
```

**已识别的坑（务必在实现中避开）**

- `Ptr` 用在 `for _, v := range slice` 里会产生**所有指针指向同一变量**的经典 bug（Go 1.22 起循环变量按迭代新分配，但为兼容性仍建议提供 `Ptrs` 时显式拷贝并写注释）。
- `Div` 对整型除零：Go 会 panic，必须在实现里判零返回零值。
- `Deref(nil)` 必须返回零值，不能解引用。

---

### 5.6 `xstr` / `xnum` / `xtime` — 基础类型工具

```go
package xstr

// 命名风格
func CamelCase(s string) string      // user_name → UserName
func LowerCamel(s string) string     // → userName
func SnakeCase(s string) string      // UserName → user_name
func KebabCase(s string) string      // → user-name
func ConstantCase(s string) string   // → USER_NAME
func Title(s string) string
func Initials(s string) string

// 判定与处理
func IsBlank(s string) bool          // 空或全空白
func IsNumeric(s string) bool
func IsAlpha/IsAlnum/IsChinese(s string) bool
func HasPrefixAny/HasSuffixAny(s string, vs ...string) bool
func Trim(ss ...string) string       // 一次去多种字符
func Default(s, def string) string
func Truncate(s string, n int, suffix ...string) string
func TruncateBytes(s string, n int) string   // 按字节（防 UTF-8 截断，需回退到 rune 边界）
func PadLeft/PadRight/Center(s string, n int, pad byte) string
func Mask(s string, opts ...MaskOption) string  // 手机号 138****8888、邮箱 a***@x.com、身份证
func MaskLeft/MaskRight/MaskMiddle(s string, keep int) string
func Repeat(s string, n int) string
func Reverse(s string) string        // rune 安全
func Count(s, sub string) int
func SplitAny(s string, seps string) []string
func SplitAndTrim(s, sep string) []string
func Words(s string) []string
func Substitute(tpl string, vars map[string]string) string   // ${name} / {{name}}
func Rand(n int, opts ...RandOption) string   // 随机串（可选字符集：字母/数字/符号）
func RandHex(n int) string
func RandPassword(n int) string
func Similarity(a, b string) float64          // 编辑距离归一化
func Levenshtein(a, b string) int
func ContainsAny/ContainsAll(s string, subs ...string) bool
func Snippet(s string, kw string, radius int) string   // 关键词上下文摘要
func SplitKeywords(s string) []string                  // 分词（空格/标点分隔，非中文分词）
func DedupWords...
func ByteLen/RuneLen(s string) int
func Ellipsis...
func Masked...

package xnum

func Round(v float64, places int) float64
func RoundHalfUp/RoundHalfEven(v float64, places int) float64
func Floor/Ceil/Trunc(v float64) float64
func FloorTo/CeilTo(v float64, unit float64) float64   // 对齐到步长
func Percent(part, total float64, places int) float64
func Ratio...
func Clamp/Abs/Diff/Min/Max(...)                        // 见 xtype 亦可
func IsInt/IsFloat/IsNumeric(s string) bool
func Between/InRange(v, lo, hi float64) bool
func SafeDivide(a, b float64, def float64) float64
func Format(v float64, places int, opts...) string      // 千分位、百分比、货币
func Thousands(v int64, sep string) string
func Ordinal(n int) string                              // 1st/2nd/3rd
func Roman(n int) string
func RandInt(min, max int) int
func RandFloat(min, max float64) float64
func RandWeighted(weights []float64) int
func NextPow2(n int) int
func ReverseInt(n int) int

// 精度计算（金额）
type Decimal struct{ /* big.Rat 或 scaled int64 */ }
func NewDecimal(v any) Decimal
func (d Decimal) Add/Sub/Mul/Div(o Decimal) Decimal
func (d Decimal) Round(places int) Decimal
func (d Decimal) String() string
func (d Decimal) Float64() float64
func (d Decimal) Cmp(o Decimal) int
func (d Decimal) IsZero() bool

// 字节单位
func BytesToHuman(n int64, opts ...) string
func HumanToBytes(s string) (int64, error)

package xtime

// 常量与格式
const (
    LayoutDate     = "2006-01-02"
    LayoutDateTime = "2006-01-02 15:04:05"
    LayoutISO8601  = "2006-01-02T15:04:05Z07:00"
    LayoutCompact  = "20060102150405"
)

func Now() time.Time
func NowMillis/NowSeconds/NowNanos() int64
func BeginOfDay/EndOfDay(t time.Time) time.Time
func BeginOfWeek/EndOfWeek(t time.Time) time.Time      // 可配置周一/周日
func BeginOfMonth/EndOfMonth/BeginOfQuarter/BeginOfYear(t time.Time) time.Time
func AddDays/AddMonths(t time.Time, n int) time.Time
func DiffDays(a, b time.Time) int
func DaysBetween/WorkdaysBetween(a, b time.Time) int
func IsWeekend/IsSameDay/IsToday(t time.Time) bool
func IsLeapYear(y int) bool
func Age(birth, now time.Time) int
func Parse(s string, layouts ...string) (time.Time, error)   // 多布局自动尝试
func ParseInLocation(s, layout string, loc *time.Location) (time.Time, error)
func Format(t time.Time, layout ...string) string
func FormatMillis(t time.Time) string
func ToTimestamp(t time.Time) int64
func FromTimestamp(sec int64) time.Time
func FromTimestampMilli(ms int64) time.Time
func Humanize(d time.Duration) string          // 3 分钟前 / 2 天前
func HumanizeDuration(d time.Duration) string  // 1h30m
func HumanizeBytes... // 见 xnum
func RangeOfDays(start, end time.Time) []time.Time
func Timezone(name string) (*time.Location, error)
func StartOfDuration(d time.Duration, t time.Time) time.Time  // 对齐到分钟/小时边界，用于指标窗口

// 耗时统计
type Stopwatch struct{}
func NewStopwatch() *Stopwatch
func (s *Stopwatch) Lap() time.Duration
func (s *Stopwatch) Elapsed() time.Duration
func (s *Stopwatch) Reset()

// 定时器 / 定时任务（可选依赖 cron 表达式解析，内置 5 段式实现）
type Scheduler interface {
    Add(spec string, job func()) (JobID, error)   // "*/5 * * * *" 或 "@every 10s"
    Remove(JobID) error
    Start(); Stop(ctx context.Context) error
    Jobs() []JobInfo
}
func NewScheduler(opts ...SchedulerOption) Scheduler

// 时间轮（高精度大量定时任务）
type TimingWheel struct{}
func NewTimingWheel(tick time.Duration, wheelSize, level int) *TimingWheel
func (w *TimingWheel) AfterFunc(d time.Duration, fn func()) TimerID
func (w *TimingWheel) Cancel(id TimerID) bool
func (w *TimingWheel) Stop()
```

---

### 5.7 `xcontainer/*` — 数据结构

统一接口先行：

```go
package xcontainer

// 只读集合抽象
type Collection[T any] interface {
    Len() int
    IsEmpty() bool
    Clear()
    Values() []T
    ForEach(func(T))
}

// 迭代器（可选的惰性求值能力）
type Iterator[T any] interface {
    Next() (T, bool)
    Close() error
}
```

#### `xstack` — 栈

```go
package xstack

type Stack[T any] struct{ /* 非并发安全 */ }
func New[T any](capacity ...int) *Stack[T]
func (s *Stack[T]) Push(vs ...T)
func (s *Stack[T]) Pop() (T, bool)                  // 空栈返回零值,false，不 panic
func (s *Stack[T]) MustPop() T                      // 空栈 panic，仅测试用
func (s *Stack[T]) Peek() (T, bool)
func (s *Stack[T]) PeekN(n int) (T, bool)           // 第 n 层
func (s *Stack[T]) Len() int
func (s *Stack[T]) IsEmpty() bool
func (s *Stack[T]) Clear()
func (s *Stack[T]) Values() []T                     // 底→顶
func (s *Stack[T]) Clone() *Stack[T]
func (s *Stack[T]) ForEach(fn func(T))              // 顶→底
func (s *Stack[T]) PopUntil(pred func(T) bool) []T  // 弹出直到谓词为真（表达式求值常用）
func (s *Stack[T]) Search(v T, eq ...func(a,b T) bool) int

// 并发安全版本
type ConcurrentStack[T any] struct{ /* sync.RWMutex 或 lock-free */ }
func NewConcurrent[T any]() *ConcurrentStack[T]
// 同名方法，内部加锁；额外提供 TryPop() / PopTimeout(ctx)

// 有界栈（防内存膨胀）
func NewBounded[T any](maxSize int, policy OverflowPolicy) *Stack[T]
```

#### `xqueue` — 队列

```go
package xqueue

type Queue[T any] interface {
    Push(v T) bool          // 满时按策略返回 false
    Pop() (T, bool)
    Peek() (T, bool)
    Len() int
    IsEmpty() bool
    Clear()
    Close()                 // 关闭后 Push 失败、Pop 返回 false
}

// 无界切片队列（O(1) 摊还，低内存占用）
func NewQueue[T any](capacity ...int) *SliceQueue[T]

// 定容环形队列（零分配，高性能，容量固定）
func NewRing[T any](capacity int) *RingQueue[T]

// 有界队列 + 溢出策略
type OverflowPolicy int
const (
    Block OverflowPolicy = iota   // 阻塞直到有空位（默认）
    DropOldest                    // 丢最旧
    DropNewest                    // 丢最新
    Error                         // Push 返回错误
)
func NewBounded[T any](capacity int, policy OverflowPolicy) *BoundedQueue[T]
func (q *BoundedQueue[T]) Push(ctx context.Context, v T) error   // Block 策略下受 ctx 控制
func (q *BoundedQueue[T]) Pop(ctx context.Context) (T, bool, error)
func (q *BoundedQueue[T]) Drain() []T
func (q *BoundedQueue[T]) Dropped() int64                        // 统计丢弃数

// 并发队列
func NewConcurrent[T any](capacity ...int) *ConcurrentQueue[T]
func NewMPMC[T any](capacity int) *MPMCQueue[T]                  // 多生产者多消费者环形（无锁 CAS）
```

**要点**：队列是背压的第一道防线，**所有生产级队列都必须是「有界 + 策略显式」**。无界队列在生产中是内存泄漏的常见来源，文档中要明确警告。

#### `xdeque` / `xset` / `xheap` / `xring` / `xbitset` / `xbloom` / `xlru`

```go
package xdeque
func New[T any](cap ...int) *Deque[T]
// PushFront/PushBack/PopFront/PopBack/PeekFront/PeekBack/Get(i)/Set(i,v)/Insert/Remove/Rotate/Len/Values

package xset
type Set[T comparable] struct{}
func New[T comparable](items ...T) *Set[T]
func (s *Set[T]) Add(vs ...T); Remove(vs ...T); Has(v T) bool; Len() int; Clear()
func (s *Set[T]) Values() []T; Sorted() []T                  // Sorted 需 Ordered 约束（用 NewOrdered）
func (s *Set[T]) Union(o *Set[T]) *Set[T]
func (s *Set[T]) Intersect(o *Set[T]) *Set[T]
func (s *Set[T]) Difference(o *Set[T]) *Set[T]
func (s *Set[T]) SymmetricDifference(o *Set[T]) *Set[T]
func (s *Set[T]) IsSubset/IsSuperset(o *Set[T]) bool
func (s *Set[T]) Equal(o *Set[T]) bool
func (s *Set[T]) Pop() (T, bool)
func (s *Set[T]) ForEach(fn func(T))
func (s *Set[T]) Clone() *Set[T]
func (s *Set[T]) ToSlice() []T
func (s *Set[T]) UnionWith/IntersectWith/DifferenceWith(o *Set[T])   // 原地变体

type OrderedSet[T comparable] struct{}   // 保持插入顺序
type ConcurrentSet[T comparable] struct{}
type SyncSet[T comparable] struct{}

package xheap
type PriorityQueue[T any] struct{}       // 基于 container/heap，泛型包装
func New[T any](less func(a, b T) bool) *PriorityQueue[T]      // less(a,b)=true 表示 a 优先
func NewMin[T xconstraints.Ordered]() *PriorityQueue[T]
func NewMax[T xconstraints.Ordered]() *PriorityQueue[T]
func (h *PriorityQueue[T]) Push(v T); Pop() (T, bool); Peek() (T, bool); Len(); Fix(i int, v T)
func (h *PriorityQueue[T]) Init(items []T)
func (h *PriorityQueue[T]) Update(v T, less func(T, T) bool, eq func(T, T) bool) // 更新已有元素

package xring
type Buffer[T any] struct{}
func New[T any](size int) *Buffer[T]
func (b *Buffer[T]) Write(v T) (overwritten bool, old T)
func (b *Buffer[T]) WriteBatch(vs []T) []T   // 返回被覆盖元素
func (b *Buffer[T]) Read() (T, bool)
func (b *Buffer[T]) Snapshot() []T           // 最近 N 条（时间序）
func (b *Buffer[T]) Len/Cap/IsFull/IsEmpty/Clear
func (b *Buffer[T]) ForEach(fn func(T))
// 并发安全 + 可选持久化（WriteTo(io.Writer) / ReadFrom）

package xbitset
func New(n int) *BitSet                       // n 位
func (b *BitSet) Set(i int); Clear(i int); Flip(i int); Test(i int) bool
func (b *BitSet) And/Or/Xor/AndNot(o *BitSet) *BitSet
func (b *BitSet) Count() int                  // 基数
func (b *BitSet) Len() int
func (b *BitSet) String() string
func (b *BitSet) Union... // 别名
func (b *BitSet) ToBytes() []byte; FromBytes([]byte) *BitSet
func (b *BitSet) Iterate(fn func(i int) bool)

package xbloom
type Filter struct{}
func New(opts ...Option) *Filter                       // WithCapacity(n) / WithFalsePositiveRate(0.01) / WithHash(n)
func (f *Filter) Add(v []byte)
func (f *Filter) Test(v []byte) bool
func (f *Filter) MAdd/ MTest(vs [][]byte) []bool
func (f *Filter) Len/Count/Cap/IsFull
func (f *Filter) Reset()
func (f *Filter) MarshalBinary/UnmarshalBinary        // 可持久化到 Redis
func (f *Filter) EstimatedFalsePositiveRate() float64

package xlru
// 只提供淘汰算法本体，与「存储」解耦，供 xcache 复用
type Cache[K comparable, V any] interface {
    Get(k K) (V, bool)
    Add(k K, v V) (evictedK K, evictedV V, evicted bool)
    Remove(k K) bool
    Len() int
    Keys() []K
    Purge()
}
func NewLRU[K comparable, V any](size int, onEvict ...func(K, V)) *LRU[K, V]
func NewLFU[K comparable, V any](size int, onEvict ...func(K, V)) *LFU[K, V]
func NewFIFO[K comparable, V any](size int, onEvict ...func(K, V)) *FIFO[K, V]
func NewARC/New2Q/NewClock(...)                       // 可选进阶淘汰算法
type Expirable[K comparable, V any] struct{}          // 带 TTL 包装，懒删除 + 周期性清理
```

---

### 5.8 `xpipe` — 流式管道

#### 5.8.1 ⚠️ 关键设计约束：Go 不支持「方法级泛型」

`pipeline.Map(func(T) R) *Pipe[R]` 这种写法**在 Go 中无法编译** —— Go 的方法不能自带类型参数。因此 `xpipe` 采用 **「包级函数 + 泛型返回值」** 的组合风格（与 `samber/lo` 一致），类型推导体验最好：

```go
// ✅ 可行：包级函数，类型由 Go 自动推导
mapped  := xpipe.Map(src, func(i int) string { return strconv.Itoa(i) })   // *Pipe[string]
filtered := xpipe.Filter(mapped, func(s string) bool { return s != "2" })
err := xpipe.Drain(filtered, func(s string) error { fmt.Println(s); return nil })
```

同时提供**声明式 Stage 列表**风格，用于「管道由配置驱动」（YAML/DB 定义）的场景。

#### 5.8.2 API 草案

```go
package xpipe

// ---------- 数据源 ----------
func FromSlice[T any](s []T) *Pipe[T]
func FromChannel[T any](ch <-chan T) *Pipe[T]                       // 尊重 ch 关闭
func FromFunc[T any](fn func(ctx context.Context, emit func(T) error) error) *Pipe[T]
func Generate[T any](n int, fn func(i int) T) *Pipe[T]
func Just[T any](vs ...T) *Pipe[T]
func Range(start, end int) *Pipe[int]
func Empty[T any]() *Pipe[T]
func FromReader(r io.Reader) *Pipe[[]byte]        // 分块读取
func FromLines(r io.Reader) *Pipe[string]         // 按行

// ---------- 变换阶段（惰性） ----------
func Map[T, R any](p *Pipe[T], fn func(T) R) *Pipe[R]
func MapCtx[T, R any](p *Pipe[T], fn func(ctx context.Context, v T) (R, error)) *Pipe[R]
func MapErr[T, R any](p *Pipe[T], fn func(T) (R, error)) *Pipe[R]
func FlatMap[T, R any](p *Pipe[T], fn func(T) []R) *Pipe[R]
func Filter[T any](p *Pipe[T], pred func(T) bool) *Pipe[T]
func FilterCtx[T any](p *Pipe[T], pred func(ctx context.Context, v T) (bool, error)) *Pipe[T]
func Reject[T any](p *Pipe[T], pred func(T) bool) *Pipe[T]
func Distinct[T comparable](p *Pipe[T]) *Pipe[T]
func DistinctBy[T any, K comparable](p *Pipe[T], key func(T) K) *Pipe[T]
func Peek[T any](p *Pipe[T], fn func(T)) *Pipe[T]                  // 旁路观察（打日志），不改数据
func Tap...

// ---------- 批处理与窗口 ----------
func Batch[T any](p *Pipe[T], size int, opts ...BatchOption) *Pipe[[]T]   // 满 size 或超时（WithTimeout）即发
func Window[T any](p *Pipe[T], size int, step ...int) *Pipe[[]T]
func Throttle[T any](p *Pipe[T], interval time.Duration) *Pipe[T]
func Debounce[T any](p *Pipe[T], d time.Duration) *Pipe[T]

// ---------- 并发阶段 ----------
func Parallel[T, R any](p *Pipe[T], workers int, fn func(T) (R, error)) *Pipe[R]   // 保序可选
func ParallelOrdered[T, R any](p *Pipe[T], workers int, fn func(T) (R, error)) *Pipe[R]
func Unordered[T any](p *Pipe[T]) *Pipe[T]
func Buffer[T any](p *Pipe[T], n int) *Pipe[T]                     // 显式缓冲，调节背压

// ---------- 错误处理 ----------
func OnError[T any](p *Pipe[T], fn func(err error, v T) error) *Pipe[T]   // 返回 nil 表示跳过继续
func Recover[T any](p *Pipe[T], fn func(v T, panicVal any) (T, error)) *Pipe[T]
func Retry[T any](p *Pipe[T], times int, backoff ...time.Duration) *Pipe[T]
func Catch[T any](p *Pipe[T], fallback func(err error, emit func(T) error) error) *Pipe[T]

// ---------- 终结操作 ----------
func Drain[T any](p *Pipe[T], fn func(T) error) error             // 逐条消费
func Collect[T any](p *Pipe[T]) ([]T, error)                      // 全量收集（注意内存）
func CollectN[T any](p *Pipe[T], n int) ([]T, error)
func Reduce[T, A any](p *Pipe[T], init A, fn func(A, T) A) (A, error)
func Count[T any](p *Pipe[T]) (int, error)
func First[T any](p *Pipe[T]) (T, bool, error)
func Last[T any](p *Pipe[T]) (T, bool, error)
func Any[T any](p *Pipe[T], pred func(T) bool) (bool, error)
func All[T any](p *Pipe[T], pred func(T) bool) (bool, error)
func GroupBy[T any, K comparable](p *Pipe[T], key func(T) K) (map[K][]T, error)
func GroupByCount[T any, K comparable](p *Pipe[T], key func(T) K) (map[K]int, error)
func ForEach[T any](p *Pipe[T], fn func(T)) error                 // 兼容习惯命名
func Scan[T, A any](p *Pipe[T], init A, fn func(A, T) A) *Pipe[A] // 累积流

// ---------- 扇出 / 汇聚 ----------
func Tee[T any](p *Pipe[T], n int) []*Pipe[T]                      // 一分为 n，各分支都收到全部
func FanOut[T any](p *Pipe[T], n int) []*Pipe[T]                   // 轮询分发，元素只去一个分支
func Merge[T any](ps ...*Pipe[T]) *Pipe[T]                         // 合并多管道（fan-in）
func Chain[T any](ps ...*Pipe[T]) *Pipe[T]                         // 顺序串联（一个消费完再下一个）
func Zip[A, B any](a *Pipe[A], b *Pipe[B]) *Pipe[xmap.Pair[A, B]]
func SplitBy[T any](p *Pipe[T], pred func(T) bool) (yes, no *Pipe[T])

// ---------- 生命周期 ----------
func (p *Pipe[T]) Close() error
func (p *Pipe[T]) Err() error
func (p *Pipe[T]) Ctx() context.Context
func WithContext[T any](p *Pipe[T], ctx context.Context) *Pipe[T]
func WithName[T any](p *Pipe[T], name string) *Pipe[T]             // 用于 metrics / 日志

// ---------- 管道配置 ----------
type Option func(*config)
func WithBufferSize(n int) Option
func WithWorkers(n int) Option
func WithOrdered(ordered bool) Option
func WithErrorPolicy(p ErrorPolicy) Option   // FailFast / Skip / Collect
func WithMiddleware(mw ...Middleware) Option

// 配置化管道（YAML/JSON 驱动）
type StageSpec struct{ Type string; Params map[string]any }
func RegisterStage(name string, factory StageFactory)              // 注册内置/自定义阶段
func FromSpec[T any](p *Pipe[T], specs []StageSpec) (*Pipe[T], error)
```

#### 5.8.3 管道不变量

1. **关闭语义**：每个 `*Pipe[T]` 持有内部 channel；上游关闭 → 下游感知并关闭。任一阶段返回 error 且策略为 `FailFast` → 整条链取消（`ctx.Done()` + drain）。
2. **取消**：所有阶段必须同时监听 `ctx.Done()` 和输入 channel，**绝不裸读 channel**（否则 goroutine 泄漏）。
3. **背压**：每阶段有可选 buffer；`Buffer(0)` 为完全同步背压。默认 buffer 大小通过 `WithBufferSize` 统一配置。
4. **无 goroutine 泄漏**：提供 `xpipe` 专用的泄漏测试（`runtime.NumGoroutine` 前后对比 + `xtest.AssertNoGoroutineLeak`）。
5. **Panic 隔离**：每个 worker goroutine 外层 `xsafe.Recover`，panic 转为 error 上报而不是崩溃进程。
6. **幂等 Close**：多次 `Close()` 安全。

---

### 5.9 `xkeyed` — 同 key 管道（MapPipe）

> 对应需求「同一 key 的 mapPipe」。核心语义：**相同 key 的任务串行执行（保序），不同 key 并行执行（高吞吐）**。等价于 Kafka 的 partition 语义、或者「按用户的命令序列」。

#### 5.9.1 两种语义

| 语义 | 名称 | 用途 |
|---|---|---|
| **(A) 按 key 串行 + 跨 key 并行** | `KeyedPipe[K, V]` | 订单/用户维度的顺序处理；避免同一资源并发写 |
| **(B) map 的链式操作** | `xmap.Pipe` | fluent 风格连续对同一个 map 做多个操作 |

#### 5.9.2 API 草案

```go
package xkeyed

// ---------- (A) KeyedPipe：同 key 保序，跨 key 并行 ----------
type Config struct {
    Workers       int           // 总并发 worker 数（跨 key）
    QueueSize     int           // 每个 key 的待处理队列长度（有界！）
    IdleTTL       time.Duration // key 空闲多久后回收（防止 key 爆炸导致内存泄漏）
    MaxKeys       int           // 同时活跃 key 上限，超出按策略处理
    OverflowPolicy OverflowPolicy // Block / DropOldest / DropNewest / Error
    ShutdownTimeout time.Duration
}
type KeyedPipe[K comparable, V any] struct{}
func New[K comparable, V any](handler func(ctx context.Context, key K, v V) error, opts ...Option) *KeyedPipe[K, V]
func NewWithWorker[K comparable, V any](cfg Config, handler func(ctx, key K, v V) error) *KeyedPipe[K, V]

// 提交任务
func (p *KeyedPipe[K, V]) Submit(key K, v V) error                      // 非阻塞，队列满按策略
func (p *KeyedPipe[K, V]) SubmitCtx(ctx context.Context, key K, v V) error
func (p *KeyedPipe[K, V]) SubmitBatch(key K, vs ...V) error             // 同 key 批量，保证连续
func (p *KeyedPipe[K, V]) TrySubmit(key K, v V) (bool, error)

// 生命周期
func (p *KeyedPipe[K, V]) Shutdown(ctx context.Context) error           // 等待所有 key 排空
func (p *KeyedPipe[K, V]) Close() error                                 // 立即关闭，丢弃未处理
func (p *KeyedPipe[K, V]) FlushKey(ctx context.Context, key K) error    // 等待某 key 排空
func (p *KeyedPipe[K, V]) Wait() error

// 观测
func (p *KeyedPipe[K, V]) Stats() Stats    // {ActiveKeys, Queued, Processed, Failed, Dropped, MaxLatency}
func (p *KeyedPipe[K, V]) ActiveKeys() []K

// ---------- (A2) 变体：MapPipe（V 本身是 map） ----------
// 同一 key 的多条 record 按到达顺序合并成 map 处理
type MapPipe[K comparable] struct{}
func NewMap[K comparable](handler func(ctx context.Context, key K, m map[string]any) error, opts ...Option) *MapPipe[K]
func (p *MapPipe[K]) Submit(key K, field string, value any) error
func (p *MapPipe[K]) SubmitMap(key K, kv map[string]any) error
func (p *MapPipe[K]) Snapshot(key K) map[string]any          // 处理中途合并结果

// ---------- (B) map 的链式操作（fluent） ----------
type Pipe[K comparable, V any] struct{ /* 持有 map[K]V */ }
func Wrap[K comparable, V any](m map[K]V) *Pipe[K, V]
func Of[K comparable, V any](m map[K]V) *Pipe[K, V]
func (p *Pipe[K, V]) Filter(pred func(K, V) bool) *Pipe[K, V]
func (p *Pipe[K, V]) MapValues(fn func(V) V) *Pipe[K, V]
func (p *Pipe[K, V]) Pick(keys ...K) *Pipe[K, V]
func (p *Pipe[K, V]) Omit(keys ...K) *Pipe[K, V]
func (p *Pipe[K, V]) Merge(o map[K]V) *Pipe[K, V]
func (p *Pipe[K, V]) SortedByKey() *Pipe[K, V]
func (p *Pipe[K, V]) ToMap() map[K]V
func (p *Pipe[K, V]) Keys() []K
func (p *Pipe[K, V]) Values() []V
func (p *Pipe[K, V]) ForEach(fn func(K, V))
// 用法：xkeyed.Of(m).Filter(...).MapValues(...).ToMap()
```

#### 5.9.3 实现要点

- **分片**：`key → shardIndex = hash(key) % shardCount`。每个 shard 一条 goroutine + 一个队列；shard 内严格 FIFO 保序，shard 间并行。
- **key 级串行** 是天然保证，因为同 shard 同队列顺序消费。但注意：**同 key 必须映射到同 shard**（一致性哈希或取模均可，取模足够）。
- **必须带 IdleTTL 回收**：否则 key 无限增长（如按 userId 分 key 且用户量巨大）会 OOM。空闲 key 的队列 + 状态在 TTL 后被 GC。
- **失败处理**：handler 返回 error 时按策略处理（重试 N 次 / 进死信 / 跳过 / 关闭整个 pipe）。
- **shutdown 顺序**：拒绝新提交 → 等待所有 shard 队列排空 → 关闭 handler 资源 → 超时强制中断。
- **与 `xlock` 的关系**：`KeyedPipe` 解决「顺序」，`xlock` 解决「互斥」。如果用 `xlock` + 无序 worker 池来实现，会带来锁等待与 goroutine 堆积，`KeyedPipe` 是更优解。文档中要说明二者的取舍。

---

### 5.10 `xlock` — 唯一资源锁（进程内）

> 对应需求「唯一资源锁」。语义：**同一个资源 key，同一时刻只有一个持有者**。核心是 `KeyedMutex`（也叫 Named Lock / Striped Lock）。

```go
package xlock

// ---------- 统一接口 ----------
type Unlocker interface{ Unlock() error }
type UnlockFunc func() error
func (f UnlockFunc) Unlock() error { return f() }

type Locker interface {
    Lock(key string) (UnlockFunc, error)                        // 阻塞直到获取
    LockCtx(ctx context.Context, key string) (UnlockFunc, error) // 带超时/取消
    TryLock(key string) (UnlockFunc, bool)                      // 立即返回
    RLock(key string) (UnlockFunc, error)                       // 读锁
    TryRLock(key string) (UnlockFunc, bool)
}

// ---------- 实现 1：KeyedLocker（map + Mutex，最常用） ----------
func NewKeyed(opts ...Option) *KeyedLocker
// 内部：map[string]*entry{mu sync.RWMutex; refs int}
// 引用计数归零后从 map 中删除（★ 关键：防止 key 堆积内存泄漏）

// ---------- 实现 2：ShardedLocker（分片，降低热点竞争） ----------
func NewSharded(shards ...int) *KeyedLocker     // 默认 32 或 2*CPU
// key → hash(key) % shards 落到固定分片，分片内独立锁表

// ---------- 实现 3：ReentrantLocker（可重入） ----------
func NewReentrant(opts ...Option) *ReentrantLocker
// 基于 goroutine 标识（go1.21 可用 runtime 组合技巧或自建 token 传递）+ 持有计数
// 注意：Go 官方不推荐可重入锁，仅在「递归调用无法拆分」时使用，文档必须警告

// ---------- 实现 4：ResourcePool（唯一资源允许 N 并发） ----------
type ResourcePool struct{}
func NewResourcePool(size int) *ResourcePool
func (p *ResourcePool) AcquireCtx(ctx context.Context) (UnlockFunc, error)
func (p *ResourcePool) TryAcquire() (UnlockFunc, bool)
func (p *ResourcePool) Available() int
func (p *ResourcePool) InUse() int

// ---------- 便捷作用域用法（推荐，避免忘解锁） ----------
// 用法：xlock.With(ctx, locker, "order:123", func() error { ... })
func With(ctx context.Context, l Locker, key string, fn func() error) error
func WithR(ctx context.Context, l Locker, key string, fn func() error) error
func WithValue[T any](ctx context.Context, l Locker, key string, fn func() (T, error)) (T, error)

// ---------- 多 key 锁（按固定顺序加锁，防死锁） ----------
func LockAll(ctx context.Context, l Locker, keys ...string) (UnlockFunc, error)
// 内部对 keys 去重 + 排序后依次加锁，避免 A→B / B→A 交叉死锁

// ---------- 选型辅助 ----------
func ShardOf(key string, shards int) int
func HashKey(key string) uint64

// ---------- 观测 ----------
type Stats struct {
    Keys        int           // 当前活跃 key 数
    WaitCount   int64         // 累计等待次数
    WaitTotal   time.Duration // 累计等待时长
    HoldTotal   time.Duration // 累计持有时长
    HoldMax     time.Duration // 最长持有
    Contention  int64         // 竞争次数（重试/TryLock 失败）
}
func (l *KeyedLocker) Stats() Stats
type Option func(*config)
func WithMaxKeys(n int) Option                    // 活跃 key 上限，超出拒绝并报错
func WithIdleTTL(d time.Duration) Option           // 空闲 key 回收
func WithWaitTimeout(d time.Duration) Option       // 获取锁超时 → 返回 ErrLockTimeout
func WithOnContention(fn func(key string, waited time.Duration)) Option  // 竞争告警
func WithMetrics(h MetricsHook) Option
```

**实现要点 / 坑**

1. **`map[string]*sync.Mutex` 直接实现会导致内存无限增长** —— 必须用「引用计数 + 归零删除」或「分片固定锁表」。这是最常见的错误实现。
2. 分片实现更简单也更稳（不会增长），代价是不同 key 偶发假冲突；推荐默认用分片（32/64 片），热点场景再升级为 keyed。
3. **`TryLock` 必须在 `Unlock` 前记录持有状态**，防止 `sync.Mutex` 的「解锁未持有锁」panic。
4. **`Lock` 返回的 `UnlockFunc` 必须幂等**（二次调用不 panic）。
5. **死锁预防**：多 key 场景提供 `LockAll` 并强制排序；同时 `WithWaitTimeout` 作为兜底。
6. **不要用 `xlock` 做跨进程互斥** —— 那是 `xdistlock` 的职责，文档中明确边界。

---

### 5.11 `xdistlock` — 分布式锁

> 对应需求「分布式锁」。核心：**接口统一 + 多实现 + 看门狗续约 + 安全释放 + fencing token**。

#### 5.11.1 统一接口

```go
package xdistlock

type Locker interface {
    // 阻塞获取，直到成功或 ctx 结束
    Lock(ctx context.Context, key string, opts ...LockOption) (*Lock, error)
    // 尝试一次，不重试
    TryLock(ctx context.Context, key string, opts ...LockOption) (*Lock, bool, error)
    // 关闭，释放底层资源
    Close() error
}

type Lock struct{ /* opaques */ }
func (l *Lock) Key() string
func (l *Lock) Token() string                 // 唯一持有者标识（UUID/实例ID+序列）
func (l *Lock) FencingToken() uint64          // 单调递增，防脑裂（见下）
func (l *Lock) Expiry() time.Time             // 当前租约到期时间
func (l *Lock) Unlock(ctx context.Context) error   // 安全释放（CAS/compare-and-del）
func (l *Lock) Refresh(ctx context.Context) error  // 手动续约
func (l *Lock) IsHeld() bool
func (l *Lock) Extend(ttl time.Duration) error      // 调整租约

type LockOption func(*lockConfig)
func WithTTL(d time.Duration) LockOption            // 租约时长（默认 10s）
func WithWaitTimeout(d time.Duration) LockOption    // 最长等待时间
func WithRetry(interval time.Duration, max int) LockOption
func WithAutoRenew(enabled bool, interval ...time.Duration) LockOption // ★ 看门狗
func WithBlocking(block bool) LockOption
func WithAttemptTimeout(d time.Duration) LockOption
func WithFair(fair bool) LockOption                 // 公平锁（FIFO 等待队列）
func WithOnAcquired(fn func(*Lock)) LockOption
func WithOnLost(fn func(*Lock)) LockOption          // 续约失败（比如网络断了）时回调 → 用于主动放弃工作

var (
    ErrLocked         = xerr.New("xdistlock: already locked")
    ErrNotHeld        = xerr.New("xdistlock: lock not held by caller")
    ErrLockLost       = xerr.New("xdistlock: lock lost (lease expired)")
    ErrWaitTimeout    = xerr.New("xdistlock: wait timeout")
    ErrNotSupported   = xerr.New("xdistlock: operation not supported")
)
```

#### 5.11.2 实现清单

| 实现 | 加锁原语 | 安全释放 | 续约 | 公平 | 备注 |
|---|---|---|---|---|---|
| `memory` | `sync.Map` + 过期时间 | 天然安全 | 支持 | 支持 | **仅单测/单机**，绝不跨进程 |
| `redis` | `SET key token NX PX ttl` | Lua `if GET==token then DEL` | watchdog | **不支持**（需 sorted set 或 Redlock 变种） | 生产主力 |
| `redis`(Redlock) | N 个独立 Redis 节点 quorum | 多节点 Lua | 支持 | 不支持 | 对时钟敏感，争议大，**默认关闭**，需显式开启 |
| `etcd` | `Lease Grant` + `Txn(Compare CreateRevision==0)` | txn 比对 lease/token | `KeepAlive` | **天然公平**（revision 排序，`WithFirstRevision` 等待） | 强一致，推荐 |
| `mysql` | 唯一键 INSERT / `SELECT ... FOR UPDATE` | 按 token 删除 | 定时 UPDATE | 队列表 | 无中间件时的兜底 |
| `zookeeper` | 临时顺序节点 | 会话失效自动释放 | session | 天然公平 | 可选，实现复杂 |

```go
// 各实现构造函数（放在子包，避免主包强制引入第三方依赖）
package xdistlock/redis
func New(client redis.UniversalClient, opts ...Option) *Locker
func NewRedlock(clients []redis.UniversalClient, quorum int, opts ...Option) *Locker

package xdistlock/etcd
func New(client *clientv3.Client, opts ...Option) *Locker

package xdistlock/memory
func New() *Locker

package xdistlock/mysql
func New(db *sql.DB, table string, opts ...Option) *Locker
```

#### 5.11.3 关键机制说明

**1. 看门狗（Watchdog）自动续约**
```
Lock() 成功 → 启动后台 goroutine，每 ttl/3 续约一次
  ├─ 续约成功 → 继续
  └─ 续约失败（超时/锁被别人抢走）→
        1. 标记 lock.IsHeld() = false
        2. 调用 WithOnLost 回调
        3. 停止续约，Unlock() 变为 no-op（避免误删别人的锁）
Unlock() → 停止看门狗（★ 必须，否则 goroutine 泄漏）→ 安全释放
```
**坑**：`Unlock` 忘记 `stopWatchdog()` 会导致 goroutine 泄漏；`Refresh` 与 `Unlock` 并发竞态需用 `atomic.Bool` + `sync.Once` 保护。

**2. 安全释放（compare-and-delete）**
- Redis 必须用 Lua 保证原子性：
  ```lua
  if redis.call('get', KEYS[1]) == ARGV[1] then
      return redis.call('del', KEYS[1])
  else
      return 0
  end
  ```
- etcd 用 txn：`If(Value(key) == token) Then(Delete(key))`。
- **绝不裸 `DEL key`** —— 会删掉别人重新获取的锁。

**3. Fencing Token（防脑裂）**
- 场景：持锁者 GC 停顿 → 租约过期 → B 获得锁 → A 醒来继续写 → 数据损坏。
- 方案：锁服务提供**单调递增 token**（etcd 的 `CreateRevision` 天然满足；Redis 可用 `INCR` 计数器或 `SETNX` 时的全局序号）。
- 受保护资源侧必须校验：**只接受 token 比已见最大值更大的写入**（如 DB 加 `version` 乐观锁、存储层带 `fencing_token` 字段）。
- 文档必须写明：**fencing token 只有在「被保护资源主动校验」时才有意义**，只发不校验等于没做。

**4. 可重入分布式锁（Redis）**
- 用 Hash 存 `token → count`：`HINCRBY key token 1` + `EXPIRE`，释放时 `HINCRBY -1`，到 0 再 `DEL`。
- 注意：**同进程内重入**用 `xlock.ReentrantLocker` 更划算；跨进程重入才需要这个。

**5. 时钟漂移**
- Redlock 的正确性依赖各节点时钟，Redlock 作者与 Martin Kleppmann 的争论要写进文档「风险」一节。
- **结论：优先 etcd，其次 Redis + fencing token，Redlock 仅作最后选择。**

**6. 单机边界**
- `xdistlock/memory` 只在**单进程**内生效；同机多进程（如多副本 Docker）不生效。文档中必须用醒目警告标注。

**7. 观测**
- Metrics hook 暴露：加锁耗时、等待时长、续约失败次数、锁丢失次数、当前持有数。
- Debug 辅助：`Snapshot(ctx) ([]LockInfo, error)`（Redis：`SCAN` + `PTTL`；etcd：range）。

---

### 5.12 `xevent` — 进程内消息订阅（事件总线）

> 对应需求「消息订阅」。

#### 5.12.1 API 草案

```go
package xevent

// ---------- 事件模型 ----------
type Event struct {
    ID        string            // 事件唯一 ID（xid）
    Topic     string            // 主题
    Key       string            // 业务 key（用于保序路由）
    Payload   any               // 载荷
    Data      []byte            // 可选原始字节（跨进程场景）
    Headers   map[string]string // 元数据
    Timestamp time.Time
    Attempt   int               // 第几次投递（重试计数）
    TraceID   string
    Source    string            // 发布者标识
}
func (e Event) WithHeader(k, v string) Event

// ---------- 处理器 ----------
type Handler func(ctx context.Context, ev Event) error
type HandlerFunc = Handler

// ---------- 总线接口 ----------
type Bus interface {
    Publish(ctx context.Context, topic string, payload any, opts ...PublishOption) error
    PublishEvent(ctx context.Context, ev Event) error
    Subscribe(topic string, h Handler, opts ...SubscribeOption) (Subscription, error)
    Unsubscribe(s Subscription) error
    Close(ctx context.Context) error
    Subscribers(topic string) int
}

type Subscription interface {
    ID() string
    Topic() string
    Cancel() error
    Events() <-chan Event   // 拉模式（可选）
    Err() error
}

func New(opts ...Option) Bus

// ---------- 发布选项 ----------
func WithKey(key string) PublishOption               // ★ 按 key 保序（内部走 xkeyed）
func WithOrdered(ordered bool) PublishOption          // 全局保序（单 worker，吞吐低）
func WithDelay(d time.Duration) PublishOption         // 延迟投递
func WithHeaders(kv map[string]string) PublishOption
func WithSync(sync bool) PublishOption                // 是否等待所有 handler 完成

// ---------- 订阅选项 ----------
func WithBuffer(n int) SubscribeOption                // 有界缓冲区（背压）
func WithOverflow(p OverflowPolicy) SubscribeOption
func WithConcurrency(n int) SubscribeOption           // 并发消费该订阅
func WithRetry(times int, backoff ...time.Duration) SubscribeOption
func WithDeadLetter(dl func(ctx context.Context, ev Event, err error)) SubscribeOption  // ★ 死信
func WithOnce() SubscribeOption                       // 只消费一次后自动取消
func WithFilter(pred func(Event) bool) SubscribeOption
func WithMiddleware(mw ...Middleware) SubscribeOption
func WithGroup(group string) SubscribeOption          // 消费组：同组内只投一次
func WithName(name string) SubscribeOption            // 用于观测
func WithAckMode(mode AckMode) SubscribeOption        // Auto / Manual（handler 内显式 ack）

// ---------- 中间件 ----------
type Middleware func(next Handler) Handler
func Recover() Middleware
func Logging(...) Middleware
func Trace(...) Middleware
func Metrics(...) Middleware
func RateLimit(l xconcurrency.Limiter) Middleware
func Timeout(d time.Duration) Middleware
func Validate(fn func(Event) error) Middleware

// ---------- 总线选项 ----------
type Option func(*config)
func WithWorkers(n int) Option                        // 异步分发 worker 数
func WithGlobalBuffer(n int) Option
func WithErrorPolicy(p ErrorPolicy) Option             // FailFast / Continue / Collect
func WithReplayBuffer(n int) Option                    // 保留最近 n 条用于重放
func WithLogger(l xlog.Logger) Option

// ---------- 重放 / 观测 ----------
func (b *bus) Replay(ctx context.Context, topic string, h Handler, n int) error
type Stats struct {
    Published, Delivered, Failed, Dropped, Retried int64
    Subscribers int
    QueueDepth  int
}
func (b *bus) Stats() Stats

// ---------- 主题匹配 ----------
// 精确：      "order.created"
// 前缀：      "order.*"        → order.created / order.paid
// 多级通配：  "order.**"       → order.created / order.item.added
// 正则：      "re:^order\\..*$"
func Match(pattern, topic string) bool
func (b *bus) Topics() []string
```

#### 5.12.2 投递模式与顺序语义

| 模式 | 说明 | 顺序 | 吞吐 |
|---|---|---|---|
| **同步（Sync）** | `Publish` 阻塞直到所有 handler 返回 | 严格 | 低 |
| **异步（Async，默认）** | 入队后立即返回，worker 池消费 | 队列序（单 worker 时严格） | 高 |
| **按 key 保序** | `WithKey(k)`，同 key 走 `xkeyed` 串行 | 同 key 严格 | 高 |
| **全局保序** | `WithOrdered(true)`，单 worker | 全局严格 | 低 |
| **并发（Concurrency>1）** | 同一订阅多 goroutine 消费 | 不保证 | 最高 |
| **消费组（Group）** | 同组只投一份，组间广播 | 按组 | 高 |

**关键约束**：**顺序与吞吐不可兼得**，必须在 API 层让使用者显式选择，而不是默认给出「运行时才发现乱序」的隐式行为。

#### 5.12.3 背压与失败处理

- 每个订阅一条**有界 channel**，溢出策略：`Block`（默认，配合 ctx 超时）/ `DropOldest` / `DropNewest` / `Error`。
- handler 返回 error → 按 `WithRetry` 重试（指数退避 + jitter）→ 超限进入 `WithDeadLetter` 死信 → 死信也无则记 error 日志 + metric。
- `Publish` 返回 error 聚合所有 handler 的错误（`xerr.MultiError`）。

#### 5.12.4 与 `xmq` 的关系

`xevent` 是**进程内总线**；`xmq` 是**跨进程消息中间件**。二者共享同一套 `Message` / `Handler` / `Middleware` 抽象，便于本地开发用 `xevent`、上线切 `xmq`：

```go
package xmq

type Message struct {
    ID, Key, Topic string
    Payload []byte
    Headers map[string]string
    Timestamp time.Time
    Partition int32
    Offset int64
    Attempt int
}
type Handler func(ctx context.Context, msg Message) error
type Middleware func(next Handler) Handler

type Publisher interface {
    Publish(ctx context.Context, topic string, msgs ...Message) error
    Close() error
}
type Subscriber interface {
    Subscribe(ctx context.Context, topic string, group string, h Handler, opts ...Option) error
    Close() error
}
type Client interface {
    Publisher
    Subscriber
}

// 适配器
// xmq/mqtt         → MQTT（对应 conn-probe 场景）
// xmq/kafka        → Kafka
// xmq/redisstream  → Redis Stream（Consumer Group）
// xmq/rabbitmq     → RabbitMQ
// xmq/localbus     → 桥接到 xevent（本地开发/单机）
```
统一能力：自动重试、死信、消费组、手动 ack/nack、优雅关闭（drain + commit offset）、背压、trace 传播、批量发布/批量消费。

---

### 5.13 `xconcurrency` — 并发原语

```go
package xconcurrency

// ---------- Future / Promise ----------
type Future[T any] interface {
    Get(ctx context.Context) (T, error)     // 阻塞取结果
    GetTimeout(d time.Duration) (T, error)
    Done() <-chan struct{}
    IsDone() bool
    Cancel()
}
type Promise[T any] interface {
    Future[T]
    Resolve(T)
    Reject(error)
}
func NewPromise[T any]() Promise[T]
func Go[T any](fn func() (T, error)) Future[T]           // 立即启动异步任务
func GoCtx[T any](ctx context.Context, fn func(ctx context.Context) (T, error)) Future[T]
func All[T any](futures ...Future[T]) Future[[]T]         // 全部成功
func AllSettled[T any](fs ...Future[T]) Future[[]Settled[T]]
func Race[T any](fs ...Future[T]) Future[T]               // 最快返回
func Any[T any](fs ...Future[T]) Future[T]                // 最快成功
func Sequence[T any](fns ...func() (T, error)) Future[[]T]
func WaitAll[T any](ctx context.Context, fs ...Future[T]) ([]T, error)

// ---------- Worker 池 ----------
type Pool[T, R any] struct{}
func NewPool[T, R any](workers int, fn func(T) (R, error), opts ...PoolOption) *Pool[T, R]
func (p *Pool[T, R]) Submit(ctx context.Context, v T) error
func (p *Pool[T, R]) Results() <-chan Result[R]
func (p *Pool[T, R]) Run(ctx context.Context, in <-chan T) <-chan Result[R]
func (p *Pool[T, R]) Wait() error
func (p *Pool[T, R]) Shutdown(ctx context.Context) error
func (p *Pool[T, R]) Stats() PoolStats           // {Busy, Idle, Completed, Failed, QueueDepth}

type Result[R any] struct{ Value R; Err error; Index int }
func MapPool[T, R any](ctx context.Context, s []T, workers int, fn func(T) (R, error)) ([]R, error)
func MapPoolOrdered[T, R any](ctx context.Context, s []T, workers int, fn func(T) (R, error)) ([]R, error)
func ForEachPool[T any](ctx context.Context, s []T, workers int, fn func(T) error) error

// ---------- errgroup 封装（无需引入 x/sync） ----------
type Group struct{}
func WithGroup(ctx context.Context) (*Group, context.Context)
func (g *Group) Go(fn func() error)
func (g *Group) Wait() error
func (g *Group) SetLimit(n int)

// ---------- 信号量 ----------
type Semaphore struct{}
func NewSemaphore(n int) *Semaphore
func (s *Semaphore) Acquire(ctx context.Context) error
func (s *Semaphore) TryAcquire() bool
func (s *Semaphore) Release()
func (s *Semaphore) Available() int

// ---------- 限流 ----------
type Limiter interface {
    Wait(ctx context.Context) error
    Allow() bool
    AllowN(n int) bool
    Reserve() (time.Duration, bool)
    SetLimit(r float64)
}
func NewTokenBucket(rate float64, burst int, opts ...LimiterOption) Limiter
func NewLeakyBucket(rate float64, capacity int) Limiter
func NewSlidingWindow(rate int, window time.Duration) Limiter
func NewFixedWindow(rate int, window time.Duration) Limiter
func NewConcurrencyLimiter(n int) Limiter           // 并发数限制
func NewKeyedLimiter(factory func(key string) Limiter) *KeyedLimiter
func (k *KeyedLimiter) Wait(ctx context.Context, key string) error   // 按 key 限流（IP/用户）

// ---------- 熔断器 ----------
type Breaker interface {
    Do(fn func() error) error
    DoWithFallback(fn func() error, fallback func(error) error) error
    State() BreakerState      // Closed / Open / HalfOpen
    Reset()
    Stats() BreakerStats
}
func NewBreaker(opts ...BreakerOption) Breaker
func WithFailureThreshold(n int) BreakerOption        // 连续失败 n 次打开
func WithFailureRate(r float64, minReq int) BreakerOption
func WithOpenTimeout(d time.Duration) BreakerOption   // 打开后多久进半开
func WithHalfOpenMax(n int) BreakerOption
func WithIsSuccessful(fn func(err error) bool) BreakerOption  // 业务异常不算失败
// 泛型版：func Do[T any](b Breaker, fn func() (T, error)) (T, error)

// ---------- 重试 ----------
func Retry(ctx context.Context, fn func(ctx context.Context) error, opts ...RetryOption) error
func RetryValue[T any](ctx context.Context, fn func(ctx context.Context) (T, error), opts ...RetryOption) (T, error)
func WithMaxAttempts(n int) RetryOption                // 默认 3
func WithDelay(d time.Duration) RetryOption
func WithBackoff(fn func(attempt int) time.Duration) RetryOption
func WithExponentialBackoff(base, max time.Duration) RetryOption
func WithJitter(ratio float64) RetryOption             // ★ 必须有，防惊群
func WithRetryIf(fn func(err error) bool) RetryOption  // 只重试可重试错误（配合 xerr.IsRetryable）
func WithOnRetry(fn func(attempt int, err error, next time.Duration)) RetryOption
func WithMaxElapsed(d time.Duration) RetryOption

// ---------- Singleflight（防缓存击穿 / 请求合并） ----------
type Group[K comparable, V any] struct{}
func NewGroup[K comparable, V any]() *Group[K, V]
func (g *Group[K, V]) Do(key K, fn func() (V, error)) (V, error)
func (g *Group[K, V]) DoCtx(ctx context.Context, key K, fn func(ctx context.Context) (V, error)) (V, error)
func (g *Group[K, V]) Forget(key K)
func (g *Group[K, V]) Stats() SFStats                  // {Calls, Hits, SharedRatio}

// ---------- 防抖 / 节流 ----------
func Debounce(d time.Duration, fn func()) func()                 // 返回触发函数
func DebounceLeading(d time.Duration, fn func()) func()
func Throttle(d time.Duration, fn func()) func()
func ThrottleLatest[T any](d time.Duration, fn func(T)) func(T)

// ---------- 批量聚合（降低下游压力） ----------
type Batcher[T any] struct{}
func NewBatcher[T any](opts ...BatcherOption) *Batcher[T]
func (b *Batcher[T]) Add(v T) error
func (b *Batcher[T]) AddCtx(ctx context.Context, v T) error
func (b *Batcher[T]) Flush(ctx context.Context) error
func (b *Batcher[T]) Close(ctx context.Context) error
func WithBatchSize(n int) BatcherOption
func WithBatchTimeout(d time.Duration) BatcherOption      // 满 n 或超时即刷
func WithBatchHandler(fn func(ctx context.Context, items []T) error) BatcherOption
func WithBatchWorkers(n int) BatcherOption

// ---------- 其他 ----------
type AtomicValue[T any] struct{}          // 泛型 atomic.Value，避免 panic on inconsistent type
func NewAtomic[T any](v T) *AtomicValue[T]
func (a *AtomicValue[T]) Load() T; Store(v T); Swap(v T) T; CompareAndSwap(old, new T) bool

type Map[K comparable, V any] struct{}    // 泛型 sync.Map（类型安全 + Len + Range 有序）
func NewMap[K comparable, V any]() *Map[K, V]
func (m *Map[K, V]) Store/Load/LoadOrStore/Delete/Range/Keys/Values/Len/Clear
func (m *Map[K, V]) GetOrCompute(k K, fn func() (V, error)) (V, error)   // 单飞

type Pool[T any] struct{}                 // 对象池（泛型 sync.Pool 封装）
func NewPool[T any](new func() T) *Pool[T]
func (p *Pool[T]) Get() T; Put(T); With(fn func(T) error) error

type Barrier struct{}                     // 屏障（等 N 个协程到齐）
func NewBarrier(n int) *Barrier
func (b *Barrier) Wait(ctx context.Context) error

func Go(func())                           // 等价 go + recover（转发 xsafe.Go）
func GoWaitgroup(...)...
func WaitTimeout(wg *sync.WaitGroup, d time.Duration) bool
func SleepCtx(ctx context.Context, d time.Duration) error   // sleep 但可被 ctx 打断
func TickCtx(ctx context.Context, d time.Duration) <-chan time.Time  // 替代 time.Tick 防泄漏
func Timeout(ctx context.Context, d time.Duration, fn func() error) error
```

**要点**
- `WithJitter` 是重试的必须项，否则大量客户端同时重试会形成「惊群 / 重试风暴」。
- `SleepCtx` / `TickCtx` 用于替代 Go 新手常犯的 `time.Sleep` 不可中断、`time.Tick` 不释放定时器（内存泄漏）两个坑。
- `Retry` + `xerr.IsRetryable` 组合是标准姿势：只有网络类、5xx、超时才重试，参数错误绝不重试。

---

### 5.14 `xcache` — 缓存

```go
package xcache

type Cache[K comparable, V any] interface {
    Get(ctx context.Context, k K) (V, bool, error)
    GetOrLoad(ctx context.Context, k K, loader func(ctx context.Context) (V, error)) (V, error)
    Set(ctx context.Context, k K, v V, ttl ...time.Duration) error
    SetDefault(k K, v V) error
    Delete(ctx context.Context, k K) error
    Has(ctx context.Context, k K) (bool, error)
    Clear(ctx context.Context) error
    Len() int
    Stats() Stats          // {Hits, Misses, HitRate, Evictions, Items}
    Close() error
    Keys() []K             // 可选实现（大缓存下慎用）
}

type Option func(*config)
func WithCapacity(n int) Option                    // 最大条目数（LRU 淘汰）
func WithDefaultTTL(d time.Duration) Option
func WithTTLJitter(ratio float64) Option            // ★ 防雪崩：实际 TTL = ttl * (1 ± ratio)
func WithCleanupInterval(d time.Duration) Option     // 后台清理周期（0=懒删除）
func WithEvictionPolicy(p Policy) Option             // LRU / LFU / FIFO / CLOCK
func WithOnEvict(fn func(K, V)) Option
func WithSerializer(s Serializer) Option             // 多级缓存的 L2 需要序列化
func WithNegativeCache(d time.Duration) Option       // ★ 防穿透：空结果也缓存一小段时间

// 实现
func NewMemory[K comparable, V any](opts ...Option) Cache[K, V]                 // 单机
func NewSharded[K comparable, V any](shards int, opts ...Option) Cache[K, V]    // 分片降低锁竞争
func NewTwoLevel[K comparable, V any](l1 Cache[K, V], l2 Cache[K, V], opts ...Option) Cache[K, V]
// TwoLevel 语义：读 L1 命中即返回；未命中读 L2 并回填 L1；写同时写两级（可配置 WriteThrough/WriteBehind）

// 防击穿（用 singleflight 包装 GetOrLoad）
func WithSingleflight(enabled bool) Option

// 便捷顶层函数（全局默认缓存 + 泛型 type-assert 包装）
func Remember[T any](c Cache[string, any], ctx context.Context, key string, loader func() (T, error)) (T, error)
```

**三大缓存问题在 API 层解决**

| 问题 | 方案 | API 落点 |
|---|---|---|
| 缓存穿透 | 空值缓存（短 TTL）+ 可选布隆过滤器 | `WithNegativeCache` / `xbloom` |
| 缓存击穿 | `singleflight` 合并并发回源 | `WithSingleflight` |
| 缓存雪崩 | TTL 随机抖动 | `WithTTLJitter` |

---

### 5.15 `xerr` — 错误体系

```go
package xerr

// ---------- 基础 ----------
func New(msg string) error
func Newf(format string, args ...any) error
func Wrap(err error, msg string) error
func Wrapf(err error, format string, args ...any) error
func Is(err, target error) bool          // 转发 errors.Is
func As[T error](err error) (T, bool)    // 泛型 As
func Unwrap(err error) error
func Join(errs ...error) error           // 转发 errors.Join
func Ignore(err error)                   // 显式忽略

// ---------- 错误码 ----------
type Code int
const (
    CodeOK Code = 0
    CodeUnknown Code = 10000
    CodeInvalidParam Code = 10001
    CodeUnauthorized Code = 20001
    CodeForbidden Code = 20002
    CodeNotFound Code = 30001
    CodeConflict Code = 30002
    CodeTimeout Code = 40001
    CodeRateLimited Code = 40002
    CodeInternal Code = 50000
    CodeUnavailable Code = 50001
    // 业务码从 100000 起按模块分段
)
type Error struct {
    Code    Code
    Msg     string
    Cause   error
    Op      string            // 操作名
    Meta    map[string]any    // 附加上下文
    Stack   []byte            // 可选堆栈（Debug 模式）
    at      time.Time
}
func (e *Error) Error() string; Unwrap() error
func NewC(code Code, msg string) *Error
func NewfC(code Code, format string, args ...any) *Error
func WrapC(err error, code Code, msg string) *Error
func WithCode(err error, code Code) error
func WithMeta(err error, kv ...any) error
func WithOp(err error, op string) error
func CodeOf(err error) Code              // 未包装时返回 CodeUnknown
func MessageOf(err error) string
func MetaOf(err error) map[string]any

// ---------- 分类判定（供重试/熔断/HTTP 状态码映射使用） ----------
func IsRetryable(err error) bool         // 超时/连接失败/5xx/限流 → true；参数错误 → false
func IsNotFound/IsConflict/IsTimeout/IsCanceled/IsUnauthorized/IsForbidden(err error) bool
func IsTransient(err error) bool          // 同 IsRetryable，别名
func HTTPStatus(err error) int            // 错误码 → HTTP 状态码
func Is(err, target error) bool

// ---------- 聚合 ----------
type MultiError []error
func (m MultiError) Error() string
func (m MultiError) Unwrap() []error      // 支持 errors.Is/As 遍历
func (m MultiError) Errors() []error
func (m MultiError) First() error
func (m MultiError) Has(err error) bool
func (m MultiError) Len() int
func NewMulti(errs ...error) MultiError
func Append(m *MultiError, err error)     // 自动跳过 nil
func Collect(fns ...func() error) error   // 全部执行，聚合错误（对比 errgroup 的 fail-fast）

// ---------- 校验风格（Check） ----------
func Check(cond bool, format string, args ...any) error    // cond=false 生成 error
func Must(err error)                                       // err!=nil 则 panic，初始化用
func MustT[T any](v T, err error) T
func MustNoT...  // 见 xtype.Must
func Assert(cond bool, msg string)
func Catch(errp *error, fn func())                          // defer xerr.Catch(&err, func(){...})
type Validator func() error
func Validate(vs ...Validator) error
```

**错误码分段规范（建议）**

| 段 | 含义 | 示例 |
|---|---|---|
| 0 | 成功 | `CodeOK` |
| 10000–19999 | 客户端/参数类 | 参数非法、格式错误、缺少字段 |
| 20000–29999 | 认证授权类 | 未登录、无权限、Token 过期 |
| 30000–39999 | 资源/业务冲突 | 不存在、已存在、状态不允许 |
| 40000–49999 | 依赖/流量类 | 超时、限流、下游不可用 |
| 50000–59999 | 服务内部 | 内部错误、未实现 |
| 100000+ | 业务模块（按模块 ×1000 分段） | 100000 订单，101000 支付… |

---

### 5.16 `xcontext` / `xsafe` — 横切基础

```go
package xcontext

type key struct{ name string }        // 未导出的 typed key，杜绝字符串 key 冲突

var (
    KeyTraceID  = key{"trace_id"}
    KeySpanID   = key{"span_id"}
    KeyUserID   = key{"user_id"}
    KeyTenantID = key{"tenant_id"}
    KeyRequestID= key{"request_id"}
    KeyLocale   = key{"locale"}
)

func WithTraceID(ctx context.Context, id string) context.Context
func TraceID(ctx context.Context) string
func WithUserID/UserID(ctx, id)
func WithTenantID/TenantID(ctx)
func WithRequestID/RequestID(ctx)
func WithValue[T any](ctx context.Context, k key, v T) context.Context
func Value[T any](ctx context.Context, k key) (T, bool)
func GetOr[T any](ctx context.Context, k key, def T) T
func Merge(parent, child context.Context) context.Context   // child 的值优先，取消信号取两者
func FromRequest(*http.Request) context.Context             // 从 HTTP header 提取 trace/tenant
func Detach(ctx context.Context) context.Context            // 去掉取消信号只保留值（异步任务用）
func Copy(ctx context.Context) context.Context              // 浅拷贝值
func Values(ctx context.Context) map[string]any             // 用于日志注入

package xsafe

func Go(fn func())                                  // go + recover + 日志
func GoCtx(ctx context.Context, fn func())          // 同上，ctx 取消时不启动
func GoNamed(name string, fn func())
func GoWithError(errHandler func(error), fn func())  // 捕获 panic 转 error 回调
func Recover(fn func()) (err error)                  // 执行并捕获 panic 转为 error
func RecoverCtx(ctx context.Context, fn func()) (err error)
func PanicToError(v any) error
func IsPanicError(err error) bool
func Stack(skip int) []byte                          // 获取堆栈
func Caller(skip int) (file string, line int, fn string)
func FuncName() string
func IsTimeout(err error) bool                        // net.Error timeout / context.DeadlineExceeded
```

---

### 5.17 `xlog` — 结构化日志

```go
package xlog

type Level = slog.Level
const (LevelDebug = slog.LevelDebug; LevelInfo; LevelWarn; LevelError)

type Logger interface {
    Debug(msg string, args ...any); Info(msg string, args ...any)
    Warn(msg string, args ...any); Error(msg string, args ...any)
    DebugCtx(ctx context.Context, msg string, args ...any)   // ★ 自动注入 trace_id / user_id
    InfoCtx/ WarnCtx/ ErrorCtx(ctx, msg string, args ...any)
    With(args ...any) Logger
    WithGroup(name string) Logger
    Enabled(Level) bool
    Log(ctx context.Context, lv Level, msg string, args ...any)
}

func New(opts ...Option) Logger
func Default() Logger
func SetDefault(l Logger)
func Init(opts ...Option) error              // 从 xconfig 初始化

// 便捷函数（操作默认 logger）
func Debug/Info/Warn/Error(msg string, args ...any)
func F(k string, v any) slog.Attr            // 别名，适合快速书写
func Err(err error) slog.Attr                // F("err", err) 的语义化版本

type Option func(*config)
func WithLevel(lv Level) Option
func WithFormat(f Format) Option              // Text / JSON
func WithOutput(w io.Writer) Option
func WithFile(path string, opts ...FileOption) Option
func WithRotation(maxSizeMB, maxBackups, maxAgeDays int, compress bool) Option  // 自研 lumberjack
func WithAddSource(enabled bool) Option
func WithCallerSkip(n int) Option
func WithTimeFormat(layout string) Option
func WithSampler(n int, every time.Duration) Option        // 采样（高频日志降噪）
func WithMasker(fn func(k string, v any) any) Option       // ★ 脱敏 hook（password/token/phone）
func WithRedactKeys(keys ...string) Option
func WithHook(h Hook) Option                                // 分级 hook（告警通道等）
func WithService(name, version, env string) Option          // 每条日志附带服务元信息
func WithContextExtractor(fn func(ctx context.Context) []any) Option  // 自定义 ctx 字段提取

// 动态调整级别（配合 xconfig 热加载 / HTTP 管理接口）
func SetLevel(lv Level)
func GetLevel() Level

// 子 logger
func With(args ...any) Logger
func Named(name string) Logger
```

**要点**：基于 `log/slog`（Go 1.21 标准库），**零第三方依赖**即可获得结构化日志。轮转、脱敏、采样自行实现。

---

### 5.18 `xconfig` — 配置

```go
package xconfig

func Load(target any, opts ...Option) error          // 多源合并后反序列化到结构体
func MustLoad(target any, opts ...Option)            // 失败 panic
func LoadWithSchema(...)...
func Get[T any](key string) (T, error)               // 按点分路径取值
func GetString/GetInt/GetBool/GetDuration(key string, def ...) 
func Set(key string, v any) error
func All() map[string]any
func Watch(target any, onChange func(old, new any)) (StopFunc, error)   // 热加载
func Reload() error

type Option func(*config)
func WithFile(path string, formats ...string) Option      // yaml/json/toml/ini/properties
func WithEnv(prefix string) Option                        // APP_ 前缀 → 结构体字段
func WithFlags() Option                                   // 命令行 flag 优先
func WithDefaults(m map[string]any) Option
func WithRemote(p Provider) Option                        // etcd/nacos/apollo 适配接口
func WithStrict(unknownFieldError bool) Option             // 未知字段报错（防拼写错误）
func WithExpandEnv() Option                                // 值内支持 ${ENV}
func WithAutoWatch(interval time.Duration) Option
func WithValidators(vs ...Validator) Option

type Provider interface {                                  // 远程配置抽象
    Get(ctx context.Context, key string) ([]byte, error)
    Watch(ctx context.Context, key string, fn func([]byte)) error
    Close() error
}
```

**结构体 tag 设计**

```go
type Config struct {
    Host      string        `xcfg:"host,required" env:"APP_HOST" default:"0.0.0.0"`
    Port      int           `xcfg:"port" default:"8080" validate:"range:1,65535"`
    DSN       string        `xcfg:"db.dsn" secret:"true"`            // 打日志时脱敏
    Timeout   time.Duration `xcfg:"timeout" default:"5s"`
    Endpoints []string      `xcfg:"endpoints,split:,"`
    Nested    struct{ A int `xcfg:"a"` } `xcfg:"nested"`
}

cfg, err := xconfig.LoadFile[Config]("config.yaml")   // 泛型便捷版
```

**优先级（后者覆盖前者）**：默认值 → 配置文件 → 环境变量 → 远程配置 → 命令行 flag → 运行时 `Set`。

---

### 5.19 `xid` — ID 生成

```go
package xid

// UUID
func UUID() string                    // v4 随机
func UUIDv7() string                  // 时间有序（推荐做主键）
func UUIDNoDash() string
func UUIDBytes() [16]byte
func ParseUUID(s string) ([16]byte, error)
func IsUUID(s string) bool

// Snowflake（分布式有序 ID）
func NewSnowflake(workerID int64, opts ...SnowflakeOption) (*Snowflake, error)
func (s *Snowflake) Next() int64
func (s *Snowflake) NextString() string
func WithEpoch(t time.Time) SnowflakeOption       // 自定义起始纪元
func WithDatacenterID(id int64) SnowflakeOption
func WithClockBackwards(policy ClockBackwardPolicy) SnowflakeOption  // Wait / Error / Borrow
func (s *Snowflake) Parse(id int64) (t time.Time, dc, worker int64, seq int64)
// Worker ID 自动分配：从 k8s pod 名/主机名/环境变量/IP 推导
func WorkerIDFromHostname() (int64, error)

// 短 ID / 短链
func Short(n int) string                     // base62 随机
func ShortUnique(n int) string               // 带时间戳前缀防重复
func NanoID(n int) string

// 有序业务号
func OrderNo(prefix string, t time.Time, seq int64) string    // PREFIX + yyyyMMddHHmmss + 6位序列
func SerialNo(...)...

// 号段模式（DB 取一段，内存分发）
func NewSegmentAllocator(loader func(ctx context.Context, step int) (start, end int64, err error), opts ...Option) *SegmentAllocator
func (a *SegmentAllocator) Next(ctx context.Context) (int64, error)

interface Generator { Next() (string, error) }
```

**要点**：Snowflake 的**时钟回拨**必须显式处理（`Wait` 等待 / `Error` 报错 / `Borrow` 借用未来 ID），否则会生成重复 ID。

---

### 5.20 `xcodec` / `xcrypto` — 编解码与加密

```go
package xcodec

// JSON
func JSONMarshal(v any) ([]byte, error)
func JSONUnmarshal(data []byte, v any) error
func JSONMarshalIndent(v any) ([]byte, error)
func RegisterJSONCodec(c Codec)                    // 可替换为 jsoniter/sonic（contrib）
func NewJSONEncoder(w io.Writer) *json.Encoder     // 流式
func JSONMerge(a, b []byte) ([]byte, error)        // RFC 7396 JSON Merge Patch

// Base / Hex / URL
func Base64Encode(b []byte) string; Base64Decode(s string) ([]byte, error)
func Base64URLEncode/Decode
func HexEncode/Decode
func URLEncode/URLDecode(s string) (string, error)
func QueryString(v any) string                    // struct → a=1&b=2
func ParseQuery(s string) map[string][]string

// 压缩
func Gzip(b []byte, level ...int) ([]byte, error); Gunzip(b []byte) ([]byte, error)
func Zstd(b []byte) ([]byte, error); Unzstd(b []byte) ([]byte, error)     // 可选依赖
func Deflate/Inflate
func Snappy(...)  // 可选
func Compressor() Compressor                       // 接口，便于替换

// 序列化（可选依赖，放 contrib 或 build tag）
func MsgpackMarshal/Unmarshal(v any) ([]byte, error)
func ProtobufMarshal/Unmarshal(m proto.Message) ([]byte, error)
func YAMLToJSON / JSONToYAML(b []byte) ([]byte, error)

// CSV / 表格
func CSVWrite(w io.Writer, header []string, rows [][]string) error
func CSVRead(r io.Reader) ([][]string, error)
func StructToCSV[T any](rows []T, header ...string) ([]byte, error)
func CSVToStructs[T any](data []byte) ([]T, error)

// 二进制打包（大端/小端）
func PackInt/BigEndian/Unpack...

// Diff / Patch
func JSONPatch(a, b []byte) ([]byte, error)        // RFC 6902

package xcrypto

// 哈希
func MD5(data any) string; SHA1; SHA256; SHA512
func HashFile(path string, algo ...) (string, error)
func CRC32/CRC64/XXHash(data []byte) uint64          // xxhash 可选
func Checksum(data []byte) string                    // sha256 前 8 位，短指纹
func CompareHash(h1, h2 string) bool                 // 常量时间

// 密码
func HashPassword(pw string, opts ...PasswordOption) (string, error)   // 默认 bcrypt
func VerifyPassword(hash, pw string) bool
func WithArgon2id() PasswordOption
func WithScrypt() PasswordOption
func WithBcryptCost(c int) PasswordOption
func PasswordStrength(pw string) (score int, issues []string)          // 强度评分

// 对称加密
func AESGCMEncrypt(key, plaintext, aad []byte) ([]byte, error)
func AESGCMDecrypt(key, ciphertext, aad []byte) ([]byte, error)
func AESCBCEncrypt/Decrypt(key, iv, plaintext []byte) ([]byte, error)
func DeriveKey(password string, salt []byte, opts ...) []byte          // PBKDF2 / HKDF / Argon2
func NewRandomKey(bits int) ([]byte, error)

// 非对称
func RSAEncrypt/RSADecrypt(pub, priv, data []byte) ([]byte, error)     // 默认 OAEP
func RSASign(priv *rsa.PrivateKey, data []byte) ([]byte, error)        // PKCS1v15 / PSS
func RSAVerify(pub *rsa.PublicKey, data, sig []byte) bool
func ECDSASign/Verify(...)
func GenerateRSAKey(bits int) (*rsa.PrivateKey, error)
func GenerateECKey(curve elliptic.Curve) (*ecdsa.PrivateKey, error)
func ParsePrivateKeyPEM/ParsePublicKeyPEM([]byte) (...)
func MarshalPrivateKeyPEM/MarshalPublicKeyPEM(...)

// HMAC 与签名
func HMAC(key []byte, data []byte, algo ...) []byte
func HMACSign(secret, data string) string              // 默认 sha256 + hex
func HMACVerify(secret, data, sig string) bool         // 常量时间比较
func Sign(secret string, params map[string]string) string  // 参数排序拼接签名（支付网关常用）
func VerifySign(secret string, params map[string]string, sig string) bool

// 随机
func RandomBytes(n int) ([]byte, error)                 // crypto/rand
func RandomString(n int, charset ...string) string
func RandomInt(min, max int) int                        // crypto/rand，非 math/rand
func RandomAlnum/RandomHex/RandomBase62(n int) string
func RandomUUID() string
func Shuffle(...)

// 常量时间比较
func ConstantTimeCompare(a, b string) bool

// 脱敏
func MaskPhone/MaskEmail/MaskIDCard/MaskBankCard(s string) string
func MaskGeneric(s string, keepHead, keepTail int) string
```

**安全红线（必须写进文档）**
1. MD5/SHA1 **不得用于密码**，只可用于校验和。
2. 密码必须用 bcrypt/argon2id/scrypt（自带盐 + 慢哈希）。
3. 签名校验必须用 `crypto/subtle.ConstantTimeCompare`，禁止 `==`（时序攻击）。
4. AES 优先 **GCM**（带完整性校验），CBC 需自行加 HMAC。
5. 随机数凡涉及安全（token、验证码、密钥）必须用 `crypto/rand`，**禁止 `math/rand`**。
6. 密钥不得硬编码/入库明文，配合 `xconfig` 的 `secret` 脱敏与 KMS。

---

### 5.21 `xfile` — 文件 IO

```go
package xfile

// 路径
func Exists(p string) bool; IsDir/IsFile(p string) bool
func Abs/Rel(p string) (string, error)
func Join(parts ...string) string
func Home() string; Temp() string; TempDir(pattern string) (string, error)
func Expand(p string) string                      // ~ → 家目录
func Ext/BaseName/FileName(p string) string
func EnsureDir(dir string, perm ...os.FileMode) error
func SanitizeName(name string) string             // 去除非法字符（防路径穿越）
func IsSubPath(parent, child string) bool
func UniquePath(dir, name string) string          // 自动加 (1) (2)

// 读写
func ReadAll(p string) ([]byte, error)
func ReadString(p string) (string, error)
func ReadLines(p string) ([]string, error)
func ReadLinesFunc(p string, fn func(line string, n int) error) error   // 大文件流式
func ReadChunks(p string, size int) ([]byte, error)
func WriteAll(p string, data []byte, perm ...os.FileMode) error
func WriteString(p, s string) error
func WriteLines(p string, lines []string) error
func WriteAtomic(p string, data []byte, perm ...os.FileMode) error  // ★ 临时文件+fsync+rename
func Append(p string, data []byte) error
func AppendLine(p, line string) error
func CopyFile(src, dst string, perm ...os.FileMode) (int64, error)
func MoveFile/RenameFile(src, dst string) error
func Delete(p string) error; DeleteAll(p string) error
func Truncate(p string, size int64) error
func Chmod/Chown
func Size(p string) (int64, error)
func ModTime(p string) (time.Time, error)
func IsEmptyFile(p string) bool

// 遍历
type WalkOption func(*walkCfg)
func WithIgnore(patterns ...string) WalkOption          // .gitignore 风格
func WithDefaultIgnores() WalkOption                     // node_modules/.git/vendor
func WithMaxDepth(n int) WalkOption
func WithFollowSymlink(bool) WalkOption
func WithOnlyFiles/WithOnlyDirs() WalkOption
func Walk(root string, fn func(path string, info os.FileInfo, err error) error, opts ...WalkOption) error
func WalkFiles(root string, opts ...WalkOption) ([]string, error)
func Glob(patterns ...string) ([]string, error)
func GlobAll(root string, pattern string) ([]string, error)
func DirSize(root string) (int64, error)

// 文件锁（跨进程）
type FileLock struct{}
func NewLock(path string, opts ...LockOption) *FileLock
func (l *FileLock) TryLock() (bool, error)          // flock LOCK_EX|LOCK_NB
func (l *FileLock) Lock(ctx context.Context) error
func (l *FileLock) Unlock() error
func (l *FileLock) Close() error
func WithShared() LockOption                         // 共享读锁
func WithTimeout(d time.Duration) LockOption
func (l *FileLock) IsLocked() bool
func (l *FileLock) ReadLockInfo() (*LockInfo, error) // 记录 pid/host/时间，便于排查

// 监视（可选依赖 fsnotify；无依赖时降级为轮询）
type Watcher interface {
    Add(path string) error
    Remove(path string) error
    Events() <-chan Event      // Create/Write/Remove/Rename/Chmod
    Errors() <-chan error
    Close() error
}
func NewWatcher(opts ...WatchOption) (Watcher, error)
func WithPollInterval(d time.Duration) WatchOption

// 哈希与校验
func MD5File/SHA256File(p string) (string, error)
func CompareFiles(a, b string) (bool, error)

// 归档
func Zip(dst string, sources ...string) error
func Unzip(src, dst string) error
func Untar(src, dst string) error
func TarGz(dst string, sources ...string) error
func UntarGz(src, dst string) error
func GzFile(src, dst string) error
func ListZip/ListTar(...) ([]string, error)

// 分片
func SplitFile(src string, chunkSize int64) ([]string, error)
func MergeFiles(dst string, parts []string) error

// 临时目录自动清理
type TempDir struct{}
func NewTempDir(pattern string) (*TempDir, error)
func (t *TempDir) Path() string
func (t *TempDir) Cleanup() error
// 也可 defer xfile.CleanupTemp(dir)()
```

**要点**：`WriteAtomic` 是配置/状态落盘的唯一推荐方式（临时文件 → `fsync` → `rename`），避免断电/崩溃时的半截文件。

---

### 5.22 `xnet` — 网络工具

```go
package xnet

// ---------- HTTP Client ----------
type Client struct{}
func New(opts ...ClientOption) *Client
func WithTimeout(d time.Duration) ClientOption
func WithRetry(n int, opts ...) ClientOption
func WithRetryIf(fn func(*http.Response, error) bool) ClientOption
func WithBackoff(base, max time.Duration, jitter float64) ClientOption
func WithBreaker(b xconcurrency.Breaker) ClientOption
func WithLimiter(l xconcurrency.Limiter) ClientOption
func WithProxy(url string) ClientOption
func WithTLS(cfg *tls.Config) ClientOption
func WithInsecureSkipVerify() ClientOption         // 仅测试
func WithUserAgent(ua string) ClientOption
func WithMaxIdleConns(n int) ClientOption
func WithResponseHook(fn func(*http.Response)) ClientOption
func WithTracer(t Tracer) ClientOption             // OTel 适配
func WithLogger(l xlog.Logger) ClientOption
func WithDefaultHeaders(h map[string]string) ClientOption
func WithFollowRedirects(n int) ClientOption

// 便捷方法（泛型）
func GetJSON[T any](ctx context.Context, url string, opts ...) (T, error)
func DoJSON[Req, Resp any](ctx context.Context, method, url string, req Req, opts ...) (Resp, error)
func PostJSON(ctx, url, body any, resp any) error
func Get(ctx, url) (*http.Response, error)
func GetBytes/GetString(ctx, url) ([]byte/string, error)
func GetToFile(ctx, url, path string) error
func PostForm(ctx, url string, form url.Values) (*http.Response, error)
func PostMultipart(ctx, url string, fields map[string]string, files map[string]string) error
func DownloadWithResume(ctx, url, path string) error       // Range 断点续传
func StreamSSE(ctx, url string, onEvent func(event, data string) error) error
func ParseResponse[T any](resp *http.Response) (T, error)   // 自动处理状态码 → xerr
func StatusError(resp *http.Response) error                 // 4xx/5xx → 带 code 的错误

// URL / 查询参数
func JoinURL(base string, paths ...string) string
func AddQuery(u string, kv map[string]string) string
func ParseQuery(u string) (url.Values, error)
func NormalizeURL(u string) (string, error)          // 去默认端口/小写 host/sort query
func StripQuery(u string) string
func Domain(u string) string / TopDomain(u string)
func IsValidURL/IsAbsoluteURL(s string) bool

// IP / CIDR
func IPToInt(ip net.IP) uint32
func IntToIP(n uint32) net.IP
func IsPrivateIP(ip net.IP) bool                      // 10/172.16/192.168/169.254/loopback
func IsPublicIP/IsLoopback/IsIPv4/IsIPv6(ip) bool
func ParseCIDR(s string) (*net.IPNet, error)
func IPInCIDR(ip net.IP, cidr string) (bool, error)
func IPInAny(ip net.IP, cidrs []string) bool
func MergeCIDRs(cidrs []string) ([]*net.IPNet, error) // 合并重叠网段
func SplitCIDR(cidr string, subnets int) ([]*net.IPNet, error)
func LocalIPs() ([]net.IP, error)
func OutboundIP() (net.IP, error)                     // 出口 IP（探测）
func MACAddresses() ([]net.HardwareAddr, error)

// 连通性与探测
func IsPortOpen(host string, port int, timeout time.Duration) bool
func TCPPing(host string, port int, timeout time.Duration) (time.Duration, error)
func HTTPPing(ctx context.Context, url string) (time.Duration, int, error)
func LookupIPs(host string) ([]net.IP, error)
func ReverseLookup(ip string) ([]string, error)
func MTU(...)  // 可选

// 其他
func HTTPStatusText(code int) string
func StatusCodeClass(code int) string                // 2xx/4xx/5xx
func ParseBasicAuth/EncodeBasicAuth(...)
func ClientIP(r *http.Request) string                 // 处理 X-Forwarded-For / X-Real-IP
func ParseForwarded(r *http.Request) []string
```

---

### 5.23 `xvalidate` — 校验

```go
package xvalidate

// ---------- 函数式规则链 ----------
type Rule func(v any) error
func Field(name string, v any, rules ...Rule) error
func Validate(vs ...func() error) error

func Required(msg ...string) Rule
func MinLen(n int) Rule; MaxLen(n int) Rule; Len(n int) Rule
func Min(n float64) Rule; Max(n float64) Rule; Range(min, max float64) Rule
func Regexp(re *regexp.Regexp) Rule
func OneOf[T comparable](allowed ...T) Rule
func NotNil() Rule
func Email() Rule; URL() Rule; IP() Rule; IPv4() Rule; CIDR() Rule
func PhoneCN() Rule                                  // 中国大陆手机号
func IDCardCN() Rule                                  // 含校验位
func BankCardCN() Rule                                // Luhn
func Username() Rule; Password(minScore int) Rule
func Chinese() Rule; Alnum() Rule
func Date(layout string) Rule; TimeRange(from, to time.Time) Rule
func EQ[T comparable](other T) Rule                  // 与另一个字段相等（确认密码）
func InFuture() Rule; InPast() Rule
func Unique(fn func(v any) (bool, error)) Rule       // 异步唯一性校验（查库）
func Custom(fn func(v any) error) Rule
func When(cond func() bool, rules ...Rule) Rule      // 条件校验

// ---------- 结构体校验（tag 驱动） ----------
func Struct(v any) error
func StructCtx(ctx context.Context, v any) error
func RegisterRule(name string, fn func(val any, params string) error)   // 自定义规则
func RegisterMessage(locale string, msgs map[string]string)             // i18n

// tag 示例：
//   `validate:"required,min=3,max=20"`
//   `validate:"email"`
//   `validate:"eqfield=Password"`
//   `validate:"oneof=admin user guest"`
//   `validate:"gte=18,lte=120"`
//   `validate:"phone_cn"`
//   `validate:"required_if=Status active"`

// ---------- 错误 ----------
type FieldError struct{ Field, Tag, Value, Message string }
type Errors []FieldError
func (e Errors) Error() string
func (e Errors) Has(field string) bool
func (e Errors) Fields() []string
func (e Errors) First() FieldError
func (e Errors) AsMap() map[string]string        // 直接返回给前端
```

---

### 5.24 `xreflect` — 反射工具

```go
package xreflect

// ---------- 深拷贝（零依赖） ----------
func DeepCopy[T any](v T) T
func DeepCopyAny(v any) any
func Clone[T any](v T) T

// ---------- 字段元数据（★ 带缓存，避免热路径反射） ----------
type FieldInfo struct {
    Name      string
    Index     []int
    Type      reflect.Type
    Tag       reflect.StructTag
    JSONName  string
    OmitEmpty bool
    Anonymous bool
    Depth     int
}
type TypeInfo struct {
    Type   reflect.Type
    Kind   reflect.Kind
    Fields []FieldInfo
    FieldsByName map[string]*FieldInfo
    FieldsByPath map[string]*FieldInfo   // 支持 "a.b.c"
}
func Describe(v any) (*TypeInfo, error)
func DescribeType(t reflect.Type) (*TypeInfo, error)
func ClearCache()                        // 测试用

// ---------- 字段读写 ----------
func Field(v any, name string) (any, bool)
func FieldByPath(v any, path string) (any, bool)         // "User.Profile.Age"
func SetField(ptr any, name string, val any) error
func SetFieldByPath(ptr any, path string, val any) error
func Fields(v any) map[string]any
func FieldNames(v any) []string
func FieldValues(v any) []any
func HasField(v any, name string) bool
func IsStructKind(v any) bool

// ---------- Tag ----------
type Tag struct {
    Name    string
    Options []string
    Values  map[string]string
}
func ParseTag(tag string) Tag
func GetTag(v any, field, key string) string
func TagOptions(v any, field, key string) []string
func HasTagOption(v any, field, key, opt string) bool

// ---------- 类型工具 ----------
func TypeName(v any) string
func PkgPath(v any) string
func Indirect(t reflect.Type) (reflect.Type, bool)
func IsNil(v any) bool                   // ★ 正确处理 typed nil
func IsZero(v any) bool
func New(t reflect.Type) any
func NewOf[T any]() T                    // 泛型版：返回 T 的零值
func MakeSlice(t reflect.Type, n, cap int) any
func Implements(v any, iface reflect.Type) bool
func ImplementsInterface[T any](v any) bool
func IsAssignable[T any](v any) bool

// ---------- 调用 ----------
func Call(fn any, args ...any) ([]any, error)      // 反射调用，捕获 panic
func CallSafe(fn any, args ...any) (results []any, err error)

// ---------- 结构体填充 ----------
func FillDefaults(v any) error                     // 按 tag 填零值字段
func Merge(dst, src any) error                     // 非零字段覆盖
func MapToStructStrict[T any](m map[string]any) (*T, error)
func StructToMapWithTag(v any, tag string, omitEmpty bool) (map[string]any, error)
```

---

### 5.25 `xtest` — 测试工具

```go
package xtest

// ---------- 断言（轻量，不引第三方；也可适配 testify） ----------
func Equal[T comparable](t testing.TB, want, got T, msg ...any)
func NotEqual/True/False/Nil/NotNil/NoError/Error/ErrorIs
func Contains[T comparable](t, list []T, v T)
func ElementsMatch[T comparable](t, want, got []T)     // 顺序无关
func Len[T any](t, v []T, n int)
func Greater/Less/InDelta[T xconstraints.Number](t, want, got T, delta T)
func Zero/NotZero[T comparable](t, v T)
func EqualJSON(t, want, got []byte)                    // 忽略格式与字段顺序
func Panics(t testing.TB, fn func())
func Eventually(t, cond func() bool, timeout, interval time.Duration, msg ...any)
func Never(t, cond func() bool, d time.Duration)
func NoGoroutineLeak(t testing.TB, fn func())          // ★ runtime.NumGoroutine 前后对比
func UseTempDir(t testing.TB) string                   // 自动 t.Cleanup 删除
func FreezeTime(t testing.TB, ts time.Time)            // 替换 time.Now（通过注入的 clock）
func SetRandSeed(t testing.TB, seed int64)

// ---------- Golden File ----------
func Golden(t testing.TB, path string, got []byte)     // -update 时写回
func GoldenJSON(t testing.TB, path string, got any)
var Update = flag.Bool("update", false, "update golden files")

// ---------- 随机测试数据 ----------
func RandString(n int) string
func RandInt(min, max int) int
func RandFloat(min, max float64) float64
func RandEmail/RandPhone/RandName/RandIDCard/RandIPv4/RandURL/RandUUID() string
func RandTime(from, to time.Time) time.Time
func Pick[T any](t testing.TB, vs ...T) T
func RandSlice[T any](n int, gen func() T) []T
func Fill[T any](t testing.TB, v *T)                   // 按类型自动填充结构体全部字段
func Person() Person / Order() Order                   // 自定义样本

// ---------- HTTP 测试 ----------
func NewTestServer(t testing.TB, handler http.Handler) (*httptest.Server, *Client)
func NewTestClient(t testing.TB, baseURL string) *xnet.Client
func NewRecorder() *httptest.ResponseRecorder
func MockJSONResponse(t, recorder, code int, body any)

// ---------- Mock（接口桩） ----------
type Stub[T any] struct{}                              // 函数字段桩，避免 mockgen 依赖
func NewStub[T any]() *Stub[T]
func (s *Stub[T]) On(method string, fn any)
func (s *Stub[T]) Calls(method string) int

// ---------- 基准与性能 ----------
func Bench(b *testing.B, fn func())
func BenchParallel(b *testing.B, fn func())
func ReportAllocs(b *testing.B, fn func())
func CompareBench(...) 
```

---

## 6. 横切关注点

### 6.1 context 使用规范

1. **首参必须是 `context.Context`**，且命名为 `ctx`。函数签名 `func Do(ctx context.Context, ...)`。
2. **context 只传「请求链路元数据」，不传「可选参数」** —— 可选参数用 Options 模式。
3. **typed key（未导出结构体）**，禁止 `ctx.Value("user_id")` 字符串 key，避免包间冲突。
4. **异步任务要 `xcontext.Detach(ctx)`** —— 去取消信号保留值，否则请求结束就把后台任务取消了。
5. `Merge(parent, child)` 用于「保留父的取消信号 + 覆盖子值」的场景。

### 6.2 panic 安全规范

1. **所有 `go` 语句必须走 `xsafe.Go`**（内含 recover + 日志 + metric），禁止裸 `go func()`（除极少数核心无 panic 风险的内部循环）。
2. **库代码不 panic**，除 `MustXxx` 变体（文档标注「仅用于初始化/测试」）。
3. **HTTP/GRPC 中间件顶层 recover**，把 panic 转为 500 而不是进程崩溃。
4. **panic 转 error 时保留堆栈**（`xsafe.Stack`），否则排查无从下手。

### 6.3 可观测性三件套

统一提供 **hook 接口**，通过 `contrib/` 绑定具体实现，主库不硬依赖：

```go
package xobs

type Metrics interface {
    Counter(name string, delta float64, labels ...string)
    Gauge(name string, value float64, labels ...string)
    Histogram(name string, value float64, labels ...string)
    Timer(name string, d time.Duration, labels ...string)
}
type NoopMetrics struct{}      // 默认实现，零开销

// 各包的 MetricsHook 都接受 xobs.Metrics，contrib/xprom 提供 Prometheus 实现
```

**贯穿三个维度**：
- **Log**：`xlog` + trace_id 自动注入
- **Metric**：各核心包暴露 `Stats()` + `MetricsHook`（锁、队列、管道、事件总线、缓存、熔断器）
- **Trace**：`xcontext.KeyTraceID` 全链路透传；`contrib/xotel` 提供 OTel 适配

---

## 7. API 设计规范

### 7.1 命名

| 场景 | 规范 | 示例 |
|---|---|---|
| 转换函数 | `To<Type>` | `ToString`、`ToInt64` |
| 带默认值 | `XxxOr` / `Default` | `DerefOr`、`GetOr`、`Default` |
| 可能失败 | 返回 `(T, error)` | `ToInt64(v) (int64, error)` |
| 必须成功（初始化） | `Must<Xxx>` | `MustLoad`、`MustFromJSON` |
| 原地修改 | `<Xxx>InPlace` / `With` 后缀 | `DedupInPlace`、`RemoveAll` |
| 布尔判定 | `Is<Xxx>` / `Has<Xxx>` / `Can<Xxx>` | `IsEmpty`、`HasPrefix` |
| 构造函数 | `New<Xxx>` 返回指针；`New<Xxx>With...` 带参数 | `NewStack`、`NewWithWorker` |
| 变体构造 | `From<Xxx>` / `Of` | `FromSlice`、`FromChannel` |
| 选项 | `With<Xxx>` | `WithTTL`、WithCapacity` |
| 异步/上下文版 | 加 `Ctx` 后缀或独立函数 | `InfoCtx`、`SubmitCtx` |

### 7.2 四大铁律

1. **Options 模式**：超过 2 个可选参数就必须用 `func WithXxx() Option`，禁止 `bool` 参数（`NewThing(true, false)` 是灾难）。
2. **error 优先**：不用 panic 表达业务失败；不用 error 表达编程错误。
3. **nil 安全**：明确文档化 nil 输入的行为，且行为一致（建议：集合类返回空集合或 nil，标量返回零值）。
4. **并发语义显式**：类型文档首行标注 `// 并发安全。` 或 `// 非并发安全，调用方需自行同步。`

### 7.3 go.mod 与依赖策略

```
module github.com/zhangwenxing/gokit

go 1.21
```

- **根模块的 `go.mod` 只有 `go` 指令，没有任何 `require`** —— 核心包 100% 标准库。
- 需要第三方的实现放在**独立子模块**（各自 `go.mod`），或 `contrib/` 下用**构建标签 / build tag** 隔离。
  - 好处：使用者 `go get gokit` 不会被动拉入 redis/kafka 依赖树。
- 可选依赖清单（仅在对应子包使用）：`go-redis`、`etcd/clientv3`、`kafka-go`、`paho.mqtt.golang`、`fsnotify`、`gopkg.in/yaml.v3`、`klauspost/compress`（zstd）、`xxhash`、`json-iterator/go`、`google/uuid`。

### 7.4 API 兼容性

- `v0.x`：可破坏性变更，但每次变更写 CHANGELOG。
- `v1.0` 起：**严格遵守 semver**，禁止删除/改签名导出符号；废弃用 `// Deprecated: 用 Xxx 替代。`。
- CI 中跑 `apidiff` / `gorelease` 检查兼容性。

---

## 8. 测试与质量门禁

### 8.1 覆盖率要求

| 包类别 | 行覆盖率要求 |
|---|---|
| 核心数据加工（xconv/xslice/xmap/xtype/xstr/xnum/xtime） | ≥ 90% |
| 容器（xcontainer/*、xpipe、xkeyed） | ≥ 85% |
| 协调控制（xlock/xdistlock/xconcurrency/xcache） | ≥ 80%（分布式实现用容器化集成测试） |
| 横切（xerr/xcontext/xsafe/xlog/xconfig） | ≥ 80% |
| 网络/文件/crypto（xnet/xfile/xcrypto/xcodec） | ≥ 85% |
| 全仓 | **≥ 85%** |

### 8.2 必跑的检查

```bash
#!/bin/bash
# Makefile 目标示例
set -e

# 1. 格式
gofmt -l . | grep . && echo "gofmt failed" && exit 1
goimports -l . | grep . && exit 1

# 2. 静态检查
go vet ./...
staticcheck ./...                    # 或 golangci-lint run
golangci-lint run --timeout 5m       # 配置见 .golangci.yml

# 3. 单元测试（必须开 race）
go test -race -count=1 -coverprofile=cover.out -covermode=atomic ./...

# 4. 覆盖率门禁
go tool cover -func=cover.out | tail -1     # 检查 total >= 85%
go tool cover -html=cover.out -o cover.html

# 5. 模糊测试（转换/解析类必跑，至少 30s）
go test -fuzz=FuzzToInt -fuzztime=30s ./xconv
go test -fuzz=FuzzDedup -fuzztime=30s ./xslice
go test -fuzz=FuzzJSON -fuzztime=30s ./xcodec
go test -fuzz=FuzzParseDuration -fuzztime=30s ./xconv

# 6. 基准测试（回归对比）
go test -bench=. -benchmem -count=5 ./... > new.txt
benchstat old.txt new.txt            # 性能回退 > 10% 则门禁失败

# 7. 泄漏检测
go test -race -run TestGoroutineLeak ./...

# 8. 跨平台编译校验
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=linux   GOARCH=arm64 go build ./...
GOOS=darwin  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...

# 9. Go 版本矩阵
# CI 上跑 1.21 / 1.22 / 1.23
```

### 8.3 测试组织规范

| 类型 | 文件名 | 说明 |
|---|---|---|
| 单元测试 | `*_test.go` | 表驱动（table-driven），每个导出函数至少 1 个用例 |
| 示例测试 | `example_test.go` | `func ExampleDedup() { ... // Output: ... }`，既当文档又当测试 |
| 基准测试 | `*_bench_test.go` | 每个热路径函数至少 1 个 bench |
| 模糊测试 | `fuzz_test.go` | 转换/解析/编解码类必写 |
| 集成测试 | `integration_test.go` | 带 `//go:build integration` 标签，CI 用 testcontainers 起 Redis/etcd |
| 并发测试 | `*_race_test.go` | 容器/锁/管道必写并发压测，靠 `-race` 捕获数据竞争 |

### 8.4 黄金法则：边界用例必测

每个函数的测试用例必须覆盖：`nil` / 空 / 单元素 / 重复元素 / 超大输入 / 类型不匹配 / 并发调用 / 返回值和输入共享底层数组（别名安全）。

---

## 9. CI/CD 与版本发布

### 9.1 CI 流水线

```yaml
# .github/workflows/ci.yml（骨架）
name: CI
on: [push, pull_request]
jobs:
  lint:      { runs-on: ubuntu-latest, steps: [checkout, setup-go 1.21, golangci-lint] }
  test:
    strategy:
      matrix: { go: ['1.21', '1.22', '1.23'], os: [ubuntu-latest, macos-latest, windows-latest] }
    steps: [checkout, setup-go, "make test", "make cover-check", upload-coverage]
  fuzz:      { runs-on: ubuntu-latest, steps: [..., "make fuzz-short"] }
  bench:     { runs-on: ubuntu-latest, if: github.ref == 'refs/heads/main', steps: [benchstat-compare] }
  build-all: { steps: ["make cross-build"] }
  api-diff:  { runs-on: ubuntu-latest, steps: [gorelease] }
```

### 9.2 发布流程

1. 更新 `CHANGELOG.md`（Keep a Changelog 格式：Added / Changed / Deprecated / Removed / Fixed / Security）
2. `git tag v0.x.0` → 推 tag → CI 自动 `go list` 校验可安装性
3. 打 release notes（含破坏性变更醒目提示与迁移指引）
4. 子模块（`xdistlock/redis` 等）独立打 tag（`xdistlock/redis/v0.1.0`）

### 9.3 文档产出

- **README.md**：一句话定位 + 包索引表 + 每个包 3 行最小示例 + 安装方式
- **godoc**：每个导出符号必须有注释，注释以符号名开头
- **examples/**：每个包至少一个可运行示例
- **可选**：doc-site（VitePress / mkdocs）从 godoc 生成 API 参考

---

## 10. 里程碑规划

### P0 — MVP（约 2 周）：把「反复手写」的部分消灭掉

| 包 | 内容 | 验收标准 |
|---|---|---|
| `xconstraints` | 约束定义 | 全包复用无重复 |
| `xconv` | 标量转换、map↔slice、slice↔string、指针/空值 | 覆盖率 ≥ 90%，500+ 表驱动用例 |
| `xslice` | 去重、Map/Filter/Reduce、集合运算、分组、排序 | 覆盖率 ≥ 90%，含 fuzz |
| `xmap` | Keys/Values/Entries、Merge、ForEachSorted、GetPath | 覆盖率 ≥ 90% |
| `xtype` | Ptr/Addr/Deref/Add/Sub/Compare/Ternary/Clamp | 覆盖率 ≥ 95%（简单函数好测） |
| `xstr` / `xnum` / `xtime` | 命名风格、掩码、随机、单位、时间边界 | 覆盖率 ≥ 90% |
| `xcontainer/xstack` `xqueue` | 栈、队列（含环形、有界） | 含并发压测 + race |
| `xerr` / `xcontext` / `xsafe` | 错误码、ctx、panic 安全 | 覆盖率 ≥ 85% |
| `xtest` | 断言、faker、golden | 供全仓使用 |

### P1 — 核心框架（约 3–4 周）

`xpipe`（含背压/并发/取消/泄漏测试）、`xkeyed`（KeyedPipe/MapPipe）、`xlock`（Keyed/Sharded/Reentrant/ResourcePool + Stats）、`xevent`（主题匹配/顺序/背压/死信/重放）、`xcache`（Memory/Sharded/TwoLevel）、`xconcurrency`（Future/Pool/Semaphore/Limiter/Breaker/Retry/Singleflight）、`xlog`、`xconfig`、`xid`、`xset`/`xheap`/`xring`/`xlru`。

### P2 — 生产闭环（约 6–8 周）

`xdistlock`（memory/redis/etcd + watchdog + fencing + 集成测试）、`xcodec`、`xcrypto`、`xfile`（原子写/文件锁/遍历/归档）、`xnet`、`xvalidate`、`xreflect`（DeepCopy + 元数据缓存）、`xcontainer/*` 剩余、`xevent` 持久化与重放。

### P3 — 生态完善（持续）

`xmq` 适配器（mqtt/kafka/redis-stream/rabbitmq）、`contrib/xotel` / `contrib/xprom` / `contrib/xzap`、代码生成器（mock / deepcopy / validator）、WASM 兼容、性能极致优化（benchstat 门禁）、文档站点。

### 交付物定义（每个包完成的标准）

- [ ] 全部导出符号有 godoc 注释
- [ ] 覆盖率达标 + fuzz（如适用）
- [ ] 至少 1 个 `Example` 函数
- [ ] 至少 1 个 benchmark（热路径）
- [ ] 并发类型有 race 测试 + goroutine 泄漏测试
- [ ] `examples/` 下有可运行示例
- [ ] README 包索引已更新
- [ ] golangci-lint 零告警

---

## 11. 风险与取舍

| 风险 | 影响 | 应对 |
|---|---|---|
| **泛型不能在方法上用** | 无法写出 `p.Map(fn).Filter(pred)` 的链式 API | 采用包级函数风格（`xpipe.Map(p, fn)`），类型推导体验已足够好；fluent 风格只在**类型不变**的场景提供（如 `xmap.Pipe`） |
| **反射性能** | `StructToMap`/`MapToStruct` 在热路径可能成为瓶颈 | 元数据 `sync.Map` 缓存；热路径推荐代码生成替代反射 |
| **分布式锁正确性争议**（Redlock） | 极端情况下可能出现两个持有者 | 优先 etcd；Redis 场景强制配 fencing token + 被保护资源校验；文档写明局限 |
| **可重入锁在 Go 中是反模式** | 使用者误用导致难以排查的死锁 | 提供但**默认不推荐**，godoc 用 `// Deprecated:` 风格的警告块说明替代方案 |
| **goroutine 泄漏** | 长跑服务内存持续增长 | 所有并发构件提供 `Shutdown/Close` + `xtest.NoGoroutineLeak` 测试 + 文档中的生命周期示例 |
| **有界 vs 无界的默认值** | 无界默认会导致 OOM | **所有队列/通道默认必须有界**，无界需显式 `WithUnbounded()` 且文档警告 |
| **接口过多抽象层次** | 包变重、学习成本高 | 每个包只暴露 1 个主接口 + 1–2 个默认实现；`xcontainer` 统一接口保持极简 |
| **依赖膨胀** | `go get` 拉入巨大依赖树 | 根模块零 require；第三方只在子包/子模块 |
| **顺序与吞吐取舍** | 使用者预期与实现不符 | 顺序语义在 API 层显式（`WithKey`/`WithOrdered`/`WithConcurrency`），文档用表格说明各模式保证 |
| **时间/随机不可注入** | 测试不稳定、无法冻结时间 | 提供 `Clock` / `Rand` 接口注入点，`xtest.FreezeTime` 配合 |
| **维护成本** | 包多、测试多、长期投入大 | 严格按优先级推进；P0 先落地马上能用；未完成的包不发布（避免半成品 API 被依赖） |

---

## 附录 A — 命名速查

| 需求 | 包 | 函数/类型 |
|---|---|---|
| map → slice | `xconv` / `xmap` | `MapToSlice` / `MapValues` |
| slice → map | `xconv` | `SliceToMap` / `SliceToSet` |
| slice → string | `xconv` | `SliceToString` / `SliceToStringFunc` |
| slice 去重 | `xslice` | `Dedup` / `DedupBy` / `DedupLast` / `DedupInPlace` |
| 取地址 | `xtype` | `Ptr` / `Addr` / `Ptrs` |
| 解引用 | `xtype` | `Deref` / `DerefOr` |
| 相加 | `xtype` | `Add` / `Sub` / `Mul` / `Div` / `Sum` |
| 栈 | `xcontainer/xstack` | `xstack.New/Push/Pop/Peek` |
| 队列 | `xcontainer/xqueue` | `xqueue.NewQueue/NewRing/NewBounded` |
| 管道 | `xpipe` | `xpipe.FromSlice/Map/Filter/Batch/Drain` |
| 同 key 管道 | `xkeyed` | `xkeyed.New(K,V)/Submit/Shutdown` |
| map 链式操作 | `xkeyed` | `xkeyed.Of(m).Filter(...).ToMap()` |
| 唯一资源锁 | `xlock` | `xlock.NewKeyed` / `xlock.With` |
| 分片锁 | `xlock` | `xlock.NewSharded` |
| 资源并发上限 | `xlock` | `xlock.NewResourcePool(n)` |
| 分布式锁 | `xdistlock` | `xdistlock/redis.New` → `Lock/Unlock/Refresh` |
| 消息订阅 | `xevent` | `xevent.New` → `Subscribe/Publish` |
| 跨进程消息 | `xmq` | `xmq/kafka.New` → `Subscribe/Publish` |
| 事件总线（带 key） | `xevent` | `Publish(WithKey(k))` |
| 缓存 | `xcache` | `NewMemory` / `GetOrLoad` |
| 重试 | `xconcurrency` | `Retry(ctx, fn, WithJitter(0.2))` |
| 熔断 | `xconcurrency` | `NewBreaker(WithFailureThreshold(5))` |
| 限流 | `xconcurrency` | `NewTokenBucket(100, 200)` |
| 防击穿 | `xconcurrency` | `NewGroup[K,V]()` / `xcache.WithSingleflight` |
| 错误码 | `xerr` | `xerr.NewC(CodeInvalidParam, "msg")` |
| ID | `xid` | `UUIDv7()` / `Snowflake.Next()` |
| 时间 | `xtime` | `BeginOfDay` / `Humanize` |
| 校验 | `xvalidate` | `xvalidate.Struct(v)` |
| 测试 | `xtest` | `xtest.Equal(t, want, got)` |

---

## 附录 B — 依赖清单

**核心（0 第三方，仅标准库）**
```
context, sync, sync/atomic, errors, fmt, strconv, strings, bytes, unicode, unicode/utf8,
sort, slices, maps, cmp, math, math/big, math/rand, math/bits, time, reflect, io, io/fs,
os, path, path/filepath, bufio, encoding/json, encoding/base64, encoding/hex, encoding/csv,
encoding/binary, compress/gzip, compress/flate, regexp, net, net/http, net/url, crypto/*,
hash/*, log/slog, container/heap, container/list, runtime, runtime/debug, testing,
testing/fstest, io/ioutil(弃用), flag, os/signal, syscall, embed
```

**可选（仅子包/contrib 使用，不进根模块 require）**
| 包 | 用途 |
|---|---|
| `github.com/redis/go-redis/v9` | `xdistlock/redis`、`xmq/redisstream` |
| `go.etcd.io/etcd/client/v3` | `xdistlock/etcd`、`xconfig` 远程配置 |
| `github.com/segmentio/kafka-go` | `xmq/kafka` |
| `github.com/eclipse/paho.mqtt.golang` | `xmq/mqtt` |
| `github.com/rabbitmq/amqp091-go` | `xmq/rabbitmq` |
| `github.com/fsnotify/fsnotify` | `xfile` 文件监视（无则降级轮询） |
| `gopkg.in/yaml.v3` | `xconfig` YAML 支持（可自研极简解析器替代） |
| `github.com/klauspost/compress` | zstd / 更快的 gzip |
| `github.com/cespare/xxhash/v2` | 快速非加密哈希 |
| `github.com/json-iterator/go` | JSON 加速（`xcodec.RegisterJSONCodec`） |
| `github.com/vmihailenco/msgpack/v5` | msgpack |
| `github.com/google/uuid` | 可选的 UUID 实现（也可自研） |
| `golang.org/x/crypto` | argon2 / scrypt / hkdf（`xcrypto` 可选） |

---

## 附录 C — Go 陷阱清单（实现时必须避开）

| # | 陷阱 | 影响 | 规避 |
|---|---|---|---|
| 1 | **方法不能有类型参数** | 无法写链式泛型 API | 用包级函数（见 §5.8.1） |
| 2 | **typed nil** | `var p *T = nil; var i any = p; i != nil` → 判空失效 | 用 `reflect` 判 Kind 后 `IsNil`（`xreflect.IsNil`） |
| 3 | **map 并发写会 panic** | 服务随机崩溃 | 需要并发用 `xconcurrency.Map` 或加锁；`-race` 检测 |
| 4 | **`for _, v := range` 取地址** | 所有指针指向同一变量（Go 1.22 前） | `xstr`/`xtype.Ptrs` 内部显式拷贝 |
| 5 | **slice 共享底层数组** | `append` 后原 slice 被意外修改 | 返回新切片；`Clone` 提供显式拷贝 |
| 6 | **`time.After` 在循环中泄漏** | 高 QPS 下内存暴涨 | 用 `xconcurrency.TickCtx` 或复用 `Timer` |
| 7 | **`time.Tick` 永不释放** | 泄漏 | 用 `ticker.Stop()` 或 `TickCtx` |
| 8 | **裸 `go func()` 未 recover** | 单个 panic 崩掉整个进程 | 一律 `xsafe.Go` |
| 9 | **goroutine 泄漏** | 内存持续增长 | 所有组件提供 `Close`；`xtest.NoGoroutineLeak` |
| 10 | **channel 未关闭导致永久阻塞** | 死锁 | 明确 close 责任方；`select` + `ctx.Done()` |
| 11 | **`sync.Map` 类型不安全** | 存错类型 panic | 用 `xconcurrency.Map[K,V]` |
| 12 | **`math/rand` 未播种 / 用于安全场景** | 结果可预测 | 安全场景用 `crypto/rand`；`math/rand` 用 `rand.New(rand.NewSource(seed))` 显式播种（Go 1.20+ 全局自动播种） |
| 13 | **整数除法除零 panic** | 崩溃 | `xtype.Div` 判零；业务侧校验 |
| 14 | **浮点比较用 `==`** | 精度误差判断失败 | `xnum.InDelta(a, b, 1e-9)` |
| 15 | **`float64` 做金额** | 精度丢失 | 用 `xnum.Decimal` / `int64` 分 |
| 16 | **`sync.Mutex` 复制** | 锁失效、数据竞争 | 结构体含锁时用指针；`go vet` 可查 |
| 17 | **32 位平台 `atomic` 64 位对齐** | panic | 64 位原子变量放结构体首字段 |
| 18 | **`interface{}` 存大对象** | 逃逸到堆、GC 压力 | 泛型替代；或复用 `sync.Pool` |
| 19 | **`sync.Pool` 存长生命周期对象** | 被意外回收 | 只放短生命周期缓冲区 |
| 20 | **`defer` 在热路径循环内** | 性能下降 | 循环内避免 defer，或抽出函数 |
| 21 | **`context` 存可选参数** | 隐式依赖、难测试 | 用 Options 模式 |
| 22 | **`strings` 与 `[]byte` 反复转换** | 大量内存拷贝 | 用 `unsafe` 零拷贝（谨慎）或用 `bytes.Buffer` |
| 23 | **map 遍历顺序随机** | 输出/测试不稳定 | `xmap.ForEachSorted` / `KeysSorted` |
| 24 | **`errors.Is` 需要 `Unwrap` 链完整** | 判定失败 | 自定义错误实现 `Unwrap`；聚合错误实现 `Unwrap() []error` |
| 25 | **`recover` 只在直接 defer 中有效** | 间接调用捕获不到 | `xsafe.Recover` 用 `defer` + 命名返回值 |
| 26 | **`json.Marshal` 不导出字段静默忽略** | 数据丢失无提示 | 提供 `StructToMap` + 校验；单元测试断言 |
| 27 | **goroutine 里访问循环变量** | 捕获到最后一个值 | 传参进去；Go 1.22 起已修复但显式传参更清晰 |
| 28 | **`close(nil channel)` / `send on closed`** | panic | 明确所有权；用 `sync.Once` 保护 close |
| 29 | **`os.Rename` 跨设备失败** | 原子写失败 | `WriteAtomic` 需回退到 copy + fsync |
| 30 | **`filepath.Walk` 遇错即停** | 遍历中断 | 选择 `WalkDir`（Go 1.16+，性能更好）+ 错误回调策略 |

---

## 附录 D — 术语表

| 术语 | 含义 |
|---|---|
| **KeyedMutex** | 按 key 命名的互斥锁，同 key 互斥、不同 key 并行（本工具链 `xlock.KeyedLocker`） |
| **KeyedPipe** | 同 key 串行、跨 key 并行的任务管道（本工具链 `xkeyed`） |
| **Fencing Token** | 单调递增令牌，用于在被保护资源侧拒绝「过期持有者」的写入，防脑裂 |
| **Watchdog** | 分布式锁的看门狗，后台按 ttl/3 自动续约，防止业务未完成锁就过期 |
| **Redlock** | Redis 官方提出的多节点分布式锁算法，正确性存在争议 |
| **Singleflight** | 相同 key 的并发请求合并为一次执行，结果共享（防缓存击穿） |
| **背压（Backpressure）** | 消费速度跟不上生产速度时，通过有界队列阻塞/丢弃来传导压力 |
| **死信（Dead Letter）** | 重试耗尽仍失败的消息，转入独立通道以便人工/离线处理 |
| **消费组（Consumer Group）** | 同组内消息只被一个消费者处理，组间广播 |
| **Fencing 前的脑裂** | 持锁者因 GC 暂停错过续约，锁被他人获取，醒来后仍继续操作 |
| **Options 模式** | 用 `func WithXxx() Option` 传递可选配置，避免参数爆炸 |
| **Must 变体** | `MustXxx` 形式，失败即 panic，仅用于初始化/测试 |
| **Golden File** | 把期望输出存为文件，测试时比对，`-update` 时重新生成 |
| **Fuzz Test** | Go 原生模糊测试，自动生成输入寻找 panic/不一致，适合转换与解析类函数 |

---

## 附录 E — 立刻可开工的骨架命令

```bash
# 初始化（选择 Go 1.21）
mkdir -p ~/2T/gokit && cd ~/2T/gokit
export PATH=/opt/go_1.21/go/bin:$PATH
export GOTOOLCHAIN=local
go mod init github.com/zhangwenxing/gokit

# 生成 P0 包目录
for p in xconstraints xconv xslice xmap xstr xnum xtime xtype xerr xcontext xsafe xtest; do
  mkdir -p $p && printf 'package %s\n' $p > $p/doc.go
done

# 容器线
mkdir -p xcontainer/{xstack,xqueue,xdeque,xset,xheap,xring,xlru,xbitset,xbloom}
# 框架线
mkdir -p xpipe xkeyed
# 协调线
mkdir -p xlock xdistlock/{memory,redis,etcd,mysql} xconcurrency xcache
# 消息线
mkdir -p xevent xmq/{mqtt,kafka,redisstream,rabbitmq}
# 基础支撑
mkdir -p xlog xconfig xid xcodec xcrypto xfile xnet xvalidate xreflect
# 可选集成与示例
mkdir -p contrib/{xotel,xprom,xzap} examples

# 首次全量检查
go build ./... && go vet ./... && go test -race ./...
```

---

**文档结束。** 落地时建议以 P0 为第一迭代目标，先让「转换 + 集合 + 容器 + 错误」可用，随后按 P1/P2 推进。所有 API 签名以本文档为设计基线，实现中如遇冲突（尤其是泛型限制相关），以「可编译 + 类型安全 + 使用体验」三者平衡为最终判据，并回写本文档。
