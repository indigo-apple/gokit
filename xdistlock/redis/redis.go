// Package redis 提供基于 Redis 的 xdistlock 后端。
//
// # 实现要点（每一条都对应一个真实事故）
//
//  1. **加锁**：一个 Lua 脚本里完成「INCR 全局计数器」+「SET key token NX PX ttl」，
//     保证「拿到锁」与「分配到 fencing token」是原子的，且只花一个 RTT。
//     INCR 得到的值天然全局单调，可直接作为 fencing token。
//  2. **释放**：Lua compare-and-delete（GET == token 才 DEL）。
//     裸 `DEL key` 会删掉别人已重新获取的锁 —— 分布式锁头号事故。
//  3. **续约**：Lua compare-and-expire（GET == token 才 PEXPIRE）。
//     这同时解决了「租约已过期但本地还以为持有」的僵尸续约问题：
//     过期后 GET 拿不到自己的 token，续约自然失败，客户端据此判定丢锁。
//  4. **不使用 WATCH/MULTI 事务**：Lua 脚本在 Redis 内单线程执行，
//     比 WATCH 重试简单且没有 ABA 问题。
//
// # 边界
//
//   - 内置的是**极简 RESP2 客户端**（见 resp.go），只支持**单节点** Redis
//     （含主从 + 哨兵前置代理）。检测到 MOVED/ASK 会返回 ErrClusterNotSupported。
//     需要 Redis Cluster / 完整功能请自行接入 go-redis 并参考本文件写 3 个原语。
//   - **不支持公平锁**（WithFair(true) 返回 ErrNotSupported）：
//     公平语义需要 sorted set 或 Redlock 变种，设计文档亦明确不做。
//   - **不内置 Redlock**：Redlock 的正确性依赖各节点时钟，业界争议大；
//     要防脑裂请使用 fencing token 并在资源侧校验（见包 xdistlock 的说明）。
//
// 并发安全。
package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhangwenxing/gokit/xdistlock"
)

// 编译期断言。
var (
	_ xdistlock.Backend     = (*Backend)(nil)
	_ xdistlock.Snapshotter = (*Backend)(nil)
)

const (
	// defaultPoolSize 是空闲连接数上限。
	defaultPoolSize = 8

	// defaultKeyPrefix 默认 key 前缀，避免与业务 key 冲突。
	defaultKeyPrefix = "xdistlock:"

	// defaultFencingKey 默认 fencing 计数器 key（全局唯一，所有锁共用）。
	defaultFencingKey = "xdistlock:fence"

	// maxSnapshotKeys 限制 Snapshot 扫描的 key 数量，避免在大库上把内存打爆。
	maxSnapshotKeys = 10000

	// maxScanRounds 限制 SCAN 迭代轮数（防「游标不推进」导致的死循环）。
	maxScanRounds = 10000
)

// Lua 脚本：加锁 + 分配 fencing token（原子）。
//
//	KEYS[1] = 锁 key，KEYS[2] = fencing 计数器 key
//	ARGV[1] = token，ARGV[2] = ttl（毫秒）
//	返回：>0 = 成功且即为 fencing token；0 = 已被占用
const luaAcquire = `
local fence = redis.call('INCR', KEYS[2])
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then
    return fence
end
return 0
`

// Lua 脚本：续约（compare-and-expire）。
//
//	KEYS[1] = 锁 key；ARGV[1] = token；ARGV[2] = ttl（毫秒）
//	返回：1 = 续约成功；0 = 锁已不属于调用方（含已过期）
const luaRefresh = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

// Lua 脚本：释放（compare-and-delete）。
//
//	KEYS[1] = 锁 key；ARGV[1] = token
//	返回：1 = 成功释放；0 = 锁已不属于调用方（绝不误删）
const luaRelease = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`

// config 是后端配置。
type config struct {
	password   string
	db         int
	keyPrefix  string
	fenceKey   string
	poolSize   int
	dialWait   time.Duration
	ioTimeout  time.Duration
	lockPrefix string // 由 keyPrefix 派生：用于拼实际 key
}

// Option 是后端选项。
type Option func(*config)

// WithPassword 设置认证密码。
func WithPassword(pw string) Option {
	return func(c *config) { c.password = pw }
}

// WithDB 设置数据库编号（默认 0）。
func WithDB(n int) Option {
	return func(c *config) { c.db = n }
}

// WithKeyPrefix 设置锁 key 前缀（默认 `xdistlock:`）。
//
// 多个业务线共用同一个 Redis 时，用前缀隔离；同时保留清空/排查的边界。
func WithKeyPrefix(p string) Option {
	return func(c *config) { c.keyPrefix = p }
}

// WithFencingKey 设置 fencing 计数器 key（默认 `xdistlock:fence`）。
//
// ⚠️ 同一套「受保护资源」必须共用一个计数器 key，否则 token 不再全局单调，
// fencing 校验会失效。
func WithFencingKey(k string) Option {
	return func(c *config) { c.fenceKey = k }
}

// WithDialTimeout 设置建连超时（默认 3s）。
func WithDialTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.dialWait = d
		}
	}
}

// WithIOTimeout 设置单次读写的兜底超时（默认 3s）；ctx 带 Deadline 时以 ctx 为准。
func WithIOTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.ioTimeout = d
		}
	}
}

func newConfig(opts []Option) config {
	c := config{
		keyPrefix: defaultKeyPrefix,
		fenceKey:  defaultFencingKey,
		poolSize:  defaultPoolSize,
		dialWait:  3 * time.Second,
		ioTimeout: 3 * time.Second,
	}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if !strings.HasSuffix(c.keyPrefix, ":") && c.keyPrefix != "" {
		c.keyPrefix += ":"
	}
	return c
}

// Backend 是 Redis 版的分布式锁后端。
type Backend struct {
	cli  *client
	cfg  config
	addr string
}

// New 创建 Redis 后端。
//
// addr 支持 `host:port` 与 unix socket 路径（以 `/` 开头），如：
//
//	xdistlock.New("127.0.0.1:6379")
//	xdistlock.New("127.0.0.1:6379", WithPassword("pw"), WithDB(1), WithKeyPrefix("mes:"))
//	xdistlock.New("/var/run/redis/redis.sock")
//
// 注意：本函数**不建立连接**（懒连接），因此不会在启动时失败；
// 需要启动即校验请调用 Ping。
func New(addr string, opts ...Option) *Backend {
	cfg := newConfig(opts)
	cli := newClient(connConfig{
		addr:        addr,
		password:    cfg.password,
		db:          cfg.db,
		dialTimeout: cfg.dialWait,
		ioTimeout:   cfg.ioTimeout,
	})
	return &Backend{cli: cli, cfg: cfg, addr: addr}
}

// Addr 返回目标地址（诊断用）。
func (b *Backend) Addr() string {
	if b == nil {
		return ""
	}
	return b.addr
}

// Name 实现 xdistlock.Backend。
func (b *Backend) Name() string { return "redis" }

// Ping 检查连通性（启动自检 / 集成测试用）。
func (b *Backend) Ping(ctx context.Context) error {
	if b == nil || b.cli == nil {
		return xdistlock.ErrNilBackend
	}
	v, err := b.cli.do(b.normCtx(ctx), "PING")
	if err != nil {
		return err
	}
	if s, ok := v.(string); !ok || s != "PONG" {
		return fmt.Errorf("xdistlock/redis: PING 响应异常: %v", v)
	}
	return nil
}

// Close 实现 xdistlock.Backend。
func (b *Backend) Close() error {
	if b == nil || b.cli == nil {
		return nil
	}
	return b.cli.close()
}

// normCtx 只为兜底 nil ctx。
//
// 不在这里包 WithTimeout：conn.do 已经在「ctx 无 Deadline」时用配置的 ioTimeout
// 设置 socket deadline，包一层只会多一个 timer 与一个必须被调用的 cancel。
func (b *Backend) normCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// lockKey 拼出实际的 Redis key。
func (b *Backend) lockKey(key string) string {
	return b.cfg.keyPrefix + key
}

// toInt 把 RESP 整数响应转成 int64（Lua 返回整数）。
func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case []byte:
		return strconv.ParseInt(string(x), 10, 64)
	case string:
		return strconv.ParseInt(x, 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("xdistlock/redis: 期望整数响应，实际 %T", v)
	}
}

// ---------------------------------------------------------------------------
// Backend 实现
// ---------------------------------------------------------------------------

// Acquire 实现 xdistlock.Backend：Lua 内原子完成「判定空闲 + 占位 + 分配 fencing」。
func (b *Backend) Acquire(ctx context.Context, key, token string, ttl time.Duration) (uint64, bool, error) {
	if b == nil || b.cli == nil {
		return 0, false, xdistlock.ErrNilBackend
	}
	if key == "" || token == "" {
		return 0, false, fmt.Errorf("xdistlock/redis: key 与 token 不能为空")
	}
	if ttl <= 0 {
		return 0, false, fmt.Errorf("xdistlock/redis: ttl 必须为正")
	}

	v, err := b.cli.do(b.normCtx(ctx), "EVAL", luaAcquire, "2",
		b.lockKey(key), b.cfg.fenceKey, token, strconv.FormatInt(ttl.Milliseconds(), 10))
	if err != nil {
		return 0, false, err
	}
	n, err := toInt(v)
	if err != nil {
		return 0, false, err
	}
	if n <= 0 {
		return 0, false, nil // 已被占用
	}
	return uint64(n), true, nil
}

// Refresh 实现 xdistlock.Backend。
func (b *Backend) Refresh(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	if b == nil || b.cli == nil {
		return false, xdistlock.ErrNilBackend
	}
	if key == "" || token == "" {
		return false, fmt.Errorf("xdistlock/redis: key 与 token 不能为空")
	}
	if ttl <= 0 {
		return false, fmt.Errorf("xdistlock/redis: ttl 必须为正")
	}

	v, err := b.cli.do(b.normCtx(ctx), "EVAL", luaRefresh, "1",
		b.lockKey(key), token, strconv.FormatInt(ttl.Milliseconds(), 10))
	if err != nil {
		return false, err
	}
	n, err := toInt(v)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Release 实现 xdistlock.Backend 的 compare-and-delete 语义。
func (b *Backend) Release(ctx context.Context, key, token string) (bool, error) {
	if b == nil || b.cli == nil {
		return false, xdistlock.ErrNilBackend
	}
	if key == "" || token == "" {
		return false, fmt.Errorf("xdistlock/redis: key 与 token 不能为空")
	}

	v, err := b.cli.do(b.normCtx(ctx), "EVAL", luaRelease, "1", b.lockKey(key), token)
	if err != nil {
		return false, err
	}
	n, err := toInt(v)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Snapshot 实现 xdistlock.Snapshotter：SCAN 扫描 + 逐个读取 token / PTTL。
//
// ⚠️ 这是排障工具，复杂度是「O(命中 key 数) 次 RTT」，
// 不要放进高频路径；单次调用最多扫描 maxSnapshotKeys 个 key。
func (b *Backend) Snapshot(ctx context.Context, prefix string) ([]xdistlock.LockInfo, error) {
	if b == nil || b.cli == nil {
		return nil, xdistlock.ErrNilBackend
	}
	ctx = b.normCtx(ctx)

	match := b.cfg.keyPrefix + prefix + "*"
	var keys []string
	cursor := "0"
	prev := ""

	// 双重终止保护：任何「游标不推进」或「迭代过多」都必须报错退出。
	// 真实 Redis 保证游标单调推进，因此触发即说明服务端/代理行为异常。
	for round := 0; ; round++ {
		if round >= maxScanRounds {
			return nil, fmt.Errorf("xdistlock/redis: SCAN 迭代超过 %d 轮，服务端游标疑似异常", maxScanRounds)
		}
		v, err := b.cli.do(ctx, "SCAN", cursor, "MATCH", match, "COUNT", "200")
		if err != nil {
			return nil, err
		}
		arr, ok := v.([]any)
		if !ok || len(arr) != 2 {
			return nil, fmt.Errorf("xdistlock/redis: SCAN 响应格式异常: %v", v)
		}
		cur, err := bulkString(arr[0])
		if err != nil {
			return nil, err
		}
		items, ok := arr[1].([]any)
		if !ok {
			return nil, fmt.Errorf("xdistlock/redis: SCAN keys 响应格式异常: %T", arr[1])
		}
		for _, it := range items {
			s, err := bulkString(it)
			if err != nil {
				return nil, err
			}
			// 注意：fencing 计数器 key 与锁 key 同前缀，但它不是锁（值是纯整数、无 TTL）。
			// 必须显式排除，否则会被当成一把「永不释放的锁」列出来。
			if s == b.cfg.fenceKey {
				continue
			}
			keys = append(keys, s)
			if len(keys) >= maxSnapshotKeys {
				goto done
			}
		}
		cursor = cur
		if cursor == "0" {
			break
		}
		if cursor == prev {
			return nil, fmt.Errorf("xdistlock/redis: SCAN 游标未推进（%q），已中止以避免死循环", cursor)
		}
		prev = cursor
	}

done:
	out := make([]xdistlock.LockInfo, 0, len(keys))
	for _, full := range keys {
		tokenV, err := b.cli.do(ctx, "GET", full)
		if err != nil {
			return nil, err
		}
		tok, err := bulkString(tokenV)
		if err != nil {
			return nil, err
		}
		if tok == "" {
			continue // 在 SCAN 与 GET 之间被释放了
		}
		pttlV, err := b.cli.do(ctx, "PTTL", full)
		if err != nil {
			return nil, err
		}
		ttlMs, err := toInt(pttlV)
		if err != nil {
			return nil, err
		}
		out = append(out, xdistlock.LockInfo{
			Key:   strings.TrimPrefix(full, b.cfg.keyPrefix),
			Token: tok,
			TTL:   time.Duration(ttlMs) * time.Millisecond,
		})
	}
	return out, nil
}

// bulkString 把 RESP bulk 响应转成字符串（nil 转空串）。
//
// 刻意**只接受** bulk / nil：GET 与 SCAN 游标在协议上不可能是整数。
// 早期版本为了「宽容」也接受了整数，结果一个返回整型游标的畸形成功响应
// 会让 Snapshot 的游标永远不推进 —— 直接变成死循环。
func bulkString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case []byte:
		return string(x), nil
	case string:
		return x, nil
	default:
		return "", fmt.Errorf("xdistlock/redis: 期望 bulk 字符串响应，实际 %T", v)
	}
}

// PoolStats 返回连接池统计（诊断用）。
type PoolStats struct {
	Dialed     int64 // 累计建立连接数
	Reused     int64 // 累计复用次数
	ClosedConn int64 // 累计关闭连接数
	Idle       int   // 当前空闲连接数
}

// PoolStats 返回连接池统计；连接复用率异常低通常意味着网络不稳定。
func (b *Backend) PoolStats() PoolStats {
	if b == nil || b.cli == nil {
		return PoolStats{}
	}
	dialed, reused, closedConn, idle := b.cli.stats()
	return PoolStats{Dialed: dialed, Reused: reused, ClosedConn: closedConn, Idle: idle}
}

// KeyFor 返回某个业务 key 在 Redis 中的实际 key（排障用，例如去 redis-cli 里 GET）。
func (b *Backend) KeyFor(key string) string {
	if b == nil {
		return ""
	}
	return b.lockKey(key)
}
