package redis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/indigo-apple/gokit/xdistlock"
)

// 本文件是一个**极简 RESP2 客户端**。
//
// 为什么不依赖 go-redis？
//
//	xdistlock/redis 只需要 3 个能力：EVAL、SET NX PX、INCR（以及排障用的 SCAN/PTTL）。
//	为了这几个命令引入一个上万行的第三方依赖并污染整个项目的 go.mod，
//	与「核心包零第三方依赖」的设计目标冲突。因此这里只实现用得到的协议子集：
//
//	✓ 完整支持 RESP2 的 5 种响应类型（simple string / error / integer / bulk / array）
//	✓ 单命令请求-响应，无 pipeline、无 pub/sub、无 RESP3、无集群拓扑感知
//	✗ 不支持 cluster 重定向（MOVED/ASK）—— 需要集群请换用 go-redis 等成熟客户端
//
// 因此本实现**只覆盖单节点 Redis（含主从 + 哨兵前置代理）**，这一点必须在文档写清楚。

// RESP2 响应类型前缀。
const (
	replySimple = '+'
	replyError  = '-'
	replyInt    = ':'
	replyBulk   = '$'
	replyArray  = '*'
)

// maxLineLen 限制单行响应的长度（防止异常/恶意服务端用超长行耗尽内存）。
const maxLineLen = 64 * 1024

// maxBulkLen 限制单个 bulk string 的长度。
const maxBulkLen = 64 << 20

// redisError 是服务端返回的 `-ERR ...` 错误。
type redisError string

func (e redisError) Error() string { return "redis: " + string(e) }

// ErrClusterNotSupported 表示服务端返回了集群重定向，说明连到集群节点上了。
var ErrClusterNotSupported = errors.New(
	"xdistlock/redis: 检测到集群重定向（MOVED/ASK）；本内置客户端只支持单节点，" +
		"请改用 go-redis 等成熟客户端访问集群")

// connConfig 是连接参数。
type connConfig struct {
	addr        string
	password    string
	db          int
	dialTimeout time.Duration
	ioTimeout   time.Duration
}

// conn 是一条 Redis 连接（非并发安全，由连接池保证独占使用）。
type conn struct {
	c net.Conn
	r *bufio.Reader
	w *bufio.Writer
}

func dial(cfg connConfig) (*conn, error) {
	network := "tcp"
	if strings.HasPrefix(cfg.addr, "/") {
		network = "unix" // 支持 unix socket：addr 以 / 开头
	}
	nc, err := net.DialTimeout(network, cfg.addr, cfg.dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("xdistlock/redis: 连接 %s 失败: %w", cfg.addr, err)
	}
	cn := &conn{
		c: nc,
		r: bufio.NewReaderSize(nc, 4096),
		w: bufio.NewWriterSize(nc, 4096),
	}
	// 握手阶段也要有超时，否则连上一个不响应的服务端会永久卡住
	_ = nc.SetDeadline(time.Now().Add(cfg.ioTimeout))

	if cfg.password != "" {
		if _, err := cn.do(context.Background(), cfg, "AUTH", cfg.password); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("xdistlock/redis: AUTH 失败: %w", err)
		}
	}
	if cfg.db != 0 {
		if _, err := cn.do(context.Background(), cfg, "SELECT", strconv.Itoa(cfg.db)); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("xdistlock/redis: SELECT %d 失败: %w", cfg.db, err)
		}
	}
	return cn, nil
}

func (cn *conn) close() {
	if cn == nil || cn.c == nil {
		return
	}
	_ = cn.c.Close()
}

// do 执行一条命令并解析响应。
//
// ctx 的 Deadline 会映射成 socket 读写超时；没有 Deadline 时使用配置的 ioTimeout。
func (cn *conn) do(ctx context.Context, cfg connConfig, args ...string) (any, error) {
	deadline := time.Now().Add(cfg.ioTimeout)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	if err := cn.c.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cn.writeCommand(args); err != nil {
		return nil, err
	}
	if err := cn.w.Flush(); err != nil {
		return nil, err
	}
	return cn.readReply()
}

func (cn *conn) writeCommand(args []string) error {
	if _, err := cn.w.WriteString("*"); err != nil {
		return err
	}
	if _, err := cn.w.WriteString(strconv.Itoa(len(args))); err != nil {
		return err
	}
	if _, err := cn.w.WriteString("\r\n"); err != nil {
		return err
	}
	for _, a := range args {
		if _, err := cn.w.WriteString("$"); err != nil {
			return err
		}
		if _, err := cn.w.WriteString(strconv.Itoa(len(a))); err != nil {
			return err
		}
		if _, err := cn.w.WriteString("\r\n"); err != nil {
			return err
		}
		if _, err := cn.w.WriteString(a); err != nil {
			return err
		}
		if _, err := cn.w.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// readReply 递归解析一个 RESP2 响应。
//
// 返回类型：string（simple）/ int64（integer）/ []byte 或 nil（bulk）/ []any 或 nil（array）。
// error 回复会被转成 redisError。
func (cn *conn) readReply() (any, error) {
	line, err := readLine(cn.r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, errors.New("xdistlock/redis: 空响应行")
	}
	typ, payload := line[0], string(line[1:])

	switch typ {
	case replySimple:
		return payload, nil

	case replyError:
		if isClusterRedirect(payload) {
			return nil, fmt.Errorf("%w（服务端返回: %s）", ErrClusterNotSupported, payload)
		}
		return nil, redisError(payload)

	case replyInt:
		n, err := strconv.ParseInt(payload, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("xdistlock/redis: 非法整数响应 %q", payload)
		}
		return n, nil

	case replyBulk:
		n, err := strconv.Atoi(payload)
		if err != nil {
			return nil, fmt.Errorf("xdistlock/redis: 非法 bulk 长度 %q", payload)
		}
		if n < 0 {
			return nil, nil // nil bulk
		}
		if n > maxBulkLen {
			return nil, fmt.Errorf("xdistlock/redis: bulk 长度 %d 超过上限", n)
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(cn.r, buf); err != nil {
			return nil, err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return nil, errors.New("xdistlock/redis: bulk 未以 CRLF 结尾")
		}
		return buf[:n], nil

	case replyArray:
		n, err := strconv.Atoi(payload)
		if err != nil {
			return nil, fmt.Errorf("xdistlock/redis: 非法数组长度 %q", payload)
		}
		if n < 0 {
			return nil, nil // nil array
		}
		out := make([]any, n)
		for i := 0; i < n; i++ {
			v, err := cn.readReply()
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil

	default:
		return nil, fmt.Errorf("xdistlock/redis: 未知响应类型 %q", string(typ))
	}
}

// isClusterRedirect 判断错误是否为集群重定向。
func isClusterRedirect(s string) bool {
	return strings.HasPrefix(s, "MOVED") || strings.HasPrefix(s, "ASK")
}

// readLine 读取一行（不含 CRLF 结尾）。
func readLine(r *bufio.Reader) ([]byte, error) {
	buf := make([]byte, 0, 32)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '\r' {
			nb, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			if nb != '\n' {
				return nil, errors.New("xdistlock/redis: 协议错误（CR 后不是 LF）")
			}
			return buf, nil
		}
		buf = append(buf, b)
		if len(buf) > maxLineLen {
			return nil, errors.New("xdistlock/redis: 响应行过长")
		}
	}
}

// ---------------------------------------------------------------------------
// 连接池
// ---------------------------------------------------------------------------

// client 是极简连接池：取不到空闲连接就新建，归还失败则关闭（不做背压排队，
// 因为分布式锁的调用方本来就有 ctx 超时与重试）。
type client struct {
	cfg connConfig

	mu        sync.Mutex
	idle      []*conn
	closed    bool
	closeOnce sync.Once

	dialed  int64 // 累计建立连接数（用于测试观察复用是否生效）
	reused  int64 // 累计复用次数
	closedN int64 // 累计关闭连接数
}

func newClient(cfg connConfig) *client {
	return &client{cfg: cfg}
}

func (p *client) get(ctx context.Context) (*conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, xdistlock.ErrClosed
	}
	if n := len(p.idle); n > 0 {
		cn := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.reused++
		p.mu.Unlock()
		return cn, nil
	}
	p.dialed++
	p.mu.Unlock()

	return dial(p.cfg)
}

func (p *client) put(cn *conn) {
	if cn == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.closedN++
		p.mu.Unlock()
		cn.close()
		return
	}
	// 池上限固定：超出则直接关闭（保持池大小有界，不阻塞归还路径）
	if len(p.idle) >= defaultPoolSize {
		p.closedN++
		p.mu.Unlock()
		cn.close()
		return
	}
	p.idle = append(p.idle, cn)
	p.mu.Unlock()
}

// discard 在发生协议/网络错误后丢弃连接（不归还池，避免复用坏连接）。
func (p *client) discard(cn *conn) {
	if cn == nil {
		return
	}
	p.mu.Lock()
	p.closedN++
	p.mu.Unlock()
	cn.close()
}

func (p *client) close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		idle := p.idle
		p.idle = nil
		p.closedN += int64(len(idle))
		p.mu.Unlock()
		for _, cn := range idle {
			cn.close()
		}
	})
	return nil
}

// do 取连接执行命令，并按结果决定「归还」还是「丢弃」。
func (p *client) do(ctx context.Context, args ...string) (any, error) {
	cn, err := p.get(ctx)
	if err != nil {
		return nil, err
	}
	v, err := cn.do(ctx, p.cfg, args...)
	if err != nil {
		// 服务端返回的业务错误（如脚本报错）不影响连接可用性，可以复用；
		// 网络/协议错误必须丢弃。
		var re redisError
		if errors.As(err, &re) {
			p.put(cn)
		} else {
			p.discard(cn)
		}
		return nil, err
	}
	p.put(cn)
	return v, nil
}

// stats 返回池状态（测试用）。
func (p *client) stats() (dialed, reused, closedConn int64, idle int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dialed, p.reused, p.closedN, len(p.idle)
}
