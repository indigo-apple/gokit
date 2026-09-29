package redis_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangwenxing/gokit/xdistlock"
	"github.com/zhangwenxing/gokit/xdistlock/redis"
)

// ---------------------------------------------------------------------------
// 脚本化的假 Redis 服务端
//
// 目的：用**已知的原始字节**验证 RESP 解析器的每一个分支（含各种畸形响应），
// 这类用例用真实 Redis 是构造不出来的。
// 真实 Redis 的语义验证见文件末尾的集成测试。
// ---------------------------------------------------------------------------

type fakeServer struct {
	ln      net.Listener
	addr    string
	mu      sync.Mutex
	cmds    [][]string
	handler func(args []string) string // 返回原始 RESP 字节
	closed  bool
}

func newFakeServer(t *testing.T, handler func(args []string) string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	s := &fakeServer{ln: ln, addr: ln.Addr().String(), handler: handler}
	go s.serve()
	t.Cleanup(func() { s.Close() })
	return s
}

func (s *fakeServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *fakeServer) handleConn(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.cmds = append(s.cmds, args)
		h := s.handler
		s.mu.Unlock()

		reply := "+OK\r\n"
		if h != nil {
			reply = h(args)
		}
		if _, err := c.Write([]byte(reply)); err != nil {
			return
		}
		if strings.HasPrefix(reply, "+SLEEP:") {
			// 特殊约定：用于测试超时（回复后再挂住读）
			d, _ := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(reply, "+SLEEP:")))
			time.Sleep(d)
			return
		}
	}
}

func (s *fakeServer) commands() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.cmds))
	copy(out, s.cmds)
	return out
}

func (s *fakeServer) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
}

// readCommand 解析客户端发来的「数组 + bulk string」请求。
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := readCRLFLine(r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, fmt.Errorf("非数组请求: %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("非法数组长度: %q", line)
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, err := readCRLFLine(r)
		if err != nil {
			return nil, err
		}
		if len(l) == 0 || l[0] != '$' {
			return nil, fmt.Errorf("非 bulk 请求: %q", l)
		}
		ln, err := strconv.Atoi(l[1:])
		if err != nil || ln < 0 {
			return nil, fmt.Errorf("非法 bulk 长度: %q", l)
		}
		buf := make([]byte, ln+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:ln]))
	}
	return args, nil
}

func readCRLFLine(r *bufio.Reader) (string, error) {
	s, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(s, "\r\n"), nil
}

// respBulk 构造 bulk string 响应。
func respBulk(s string) string { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }

// respArray 构造数组响应。
func respArray(items ...string) string {
	out := "*" + strconv.Itoa(len(items)) + "\r\n"
	for _, it := range items {
		out += it
	}
	return out
}

// ---------------------------------------------------------------------------
// RESP 解析器 / 协议层
// ---------------------------------------------------------------------------

// 畸形 / 错误响应必须被识别成明确的错误（走 Acquire，让解析错误直接暴露）。
func TestRESPErrorBranches(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"服务端错误", "-ERR unknown command\r\n", "unknown command"},
		{"未知响应类型", "%2\r\n", "未知响应类型"},
		{"非法整数", ":abc\r\n", "非法整数响应"},
		{"非法 bulk 长度", "$abc\r\n", "非法 bulk 长度"},
		{"非法数组长度", "*abc\r\n", "非法数组长度"},
		{"空响应行", "\r\n", "空响应行"},
		{"CR 后非 LF", "+OK\rX", "CR 后不是 LF"},
		{"bulk 未以 CRLF 结尾", "$3\r\nabcXX", "bulk 未以 CRLF 结尾"},
		{"bulk 长度不足（连接被截断）", "$100\r\nabc", ""},
		{"响应行过长", "+" + strings.Repeat("x", 70000) + "\r\n", "响应行过长"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeServer(t, func([]string) string { return tc.reply })
			b := redis.New(srv.addr, redis.WithIOTimeout(2*time.Second))
			defer b.Close()

			_, _, err := b.Acquire(context.Background(), "k", "t", time.Second)
			if err == nil {
				t.Fatalf("期望错误包含 %q，实际 nil", tc.want)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v, 期望包含 %q", err, tc.want)
			}
		})
	}
}

// 成功响应的各种类型必须被正确解析（用真正用到它们的命令路径验证）。
func TestRESPValueParsing(t *testing.T) {
	t.Run("简单字符串:Ping", func(t *testing.T) {
		srv := newFakeServer(t, func([]string) string { return "+PONG\r\n" })
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()
		if err := b.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	t.Run("整数:Acquire", func(t *testing.T) {
		srv := newFakeServer(t, func([]string) string { return ":9223372036854775807\r\n" }) // int64 边界
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()
		f, ok, err := b.Acquire(context.Background(), "k", "t", time.Second)
		if err != nil || !ok {
			t.Fatalf("Acquire = %v,%v", ok, err)
		}
		if f != 9223372036854775807 {
			t.Fatalf("fencing = %d（int64 边界解析错误）", f)
		}
	})

	t.Run("零整数表示未获取到", func(t *testing.T) {
		srv := newFakeServer(t, func([]string) string { return ":0\r\n" })
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()
		if _, ok, err := b.Acquire(context.Background(), "k", "t", time.Second); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want false,nil", ok, err)
		}
		if ok, err := b.Refresh(context.Background(), "k", "t", time.Second); ok || err != nil {
			t.Fatalf("Refresh ok=%v err=%v", ok, err)
		}
		if ok, err := b.Release(context.Background(), "k", "t"); ok || err != nil {
			t.Fatalf("Release ok=%v err=%v", ok, err)
		}
	})

	t.Run("数组+bulk+nil bulk:Snapshot", func(t *testing.T) {
		// SCAN 返回 [cursor, [k1,k2]]，随后 GET/PTTL 逐 key
		var mu sync.Mutex
		step := 0
		srv := newFakeServer(t, func(args []string) string {
			mu.Lock()
			defer mu.Unlock()
			switch args[0] {
			case "SCAN":
				step++
				if step > 1 {
					return respArray(respBulk("0"), respArray()) // 第二次扫描结束
				}
				return respArray(respBulk("0"),
					respArray(respBulk("xdistlock:a"), respBulk("xdistlock:b")))
			case "GET":
				if strings.HasSuffix(args[1], ":a") {
					return respBulk("token-a")
				}
				return "$-1\r\n" // nil bulk：k2 在扫描与 GET 之间被释放
			case "PTTL":
				return ":4200\r\n"
			}
			return "-ERR unexpected\r\n"
		})
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()

		infos, err := b.Snapshot(context.Background(), "")
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(infos) != 1 {
			t.Fatalf("条数 = %d（nil bulk 的 key 应被跳过）: %+v", len(infos), infos)
		}
		if infos[0].Key != "a" || infos[0].Token != "token-a" {
			t.Fatalf("条目 = %+v（前缀应被剥离）", infos[0])
		}
		if infos[0].TTL != 4200*time.Millisecond {
			t.Fatalf("TTL = %v", infos[0].TTL)
		}
	})

	t.Run("空数组:Snapshot无锁", func(t *testing.T) {
		srv := newFakeServer(t, func([]string) string {
			return respArray(respBulk("0"), respArray())
		})
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()
		infos, err := b.Snapshot(context.Background(), "")
		if err != nil || len(infos) != 0 {
			t.Fatalf("Snapshot = %+v, %v", infos, err)
		}
	})

	t.Run("畸形 SCAN 响应", func(t *testing.T) {
		replies := []string{
			respBulk("not-an-array"),                // 不是数组
			respArray(respBulk("0")),                // 元素数不足
			respArray(respBulk("0"), respBulk("x")), // 第二个元素不是数组
			respArray(":1\r\n", respArray()),        // 游标不是 bulk
			"*-1\r\n",                               // nil 数组
		}
		for i, reply := range replies {
			t.Logf("case %d: %q", i, reply)
			srv := newFakeServer(t, func([]string) string { return reply })
			b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
			_, err := b.Snapshot(context.Background(), "")
			if err == nil {
				t.Fatalf("SCAN 响应 %q 应报错", reply)
			}
			_ = b.Close()
		}
	})

	t.Run("SCAN 多轮游标", func(t *testing.T) {
		var mu sync.Mutex
		step := 0
		srv := newFakeServer(t, func(args []string) string {
			mu.Lock()
			defer mu.Unlock()
			switch args[0] {
			case "SCAN":
				step++
				if step == 1 {
					return respArray(respBulk("17"), respArray(respBulk("xdistlock:k1")))
				}
				if args[1] != "17" {
					return "-ERR cursor not passed\r\n"
				}
				return respArray(respBulk("0"), respArray(respBulk("xdistlock:k2")))
			case "GET":
				return respBulk("tok")
			case "PTTL":
				return ":1000\r\n"
			}
			return "-ERR unexpected\r\n"
		})
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()

		infos, err := b.Snapshot(context.Background(), "")
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(infos) != 2 {
			t.Fatalf("条数 = %d（多轮游标未遍历）", len(infos))
		}
	})

	t.Run("bulk 整数响应也被接受", func(t *testing.T) {
		// 某些代理/兼容实现会把整数回成 bulk 字符串
		srv := newFakeServer(t, func([]string) string { return respBulk("5") })
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
		defer b.Close()
		f, ok, err := b.Acquire(context.Background(), "k", "t", time.Second)
		if err != nil || !ok || f != 5 {
			t.Fatalf("Acquire = %d,%v,%v", f, ok, err)
		}
	})
}

// 用真实命令路径逐个验证解析结果（上面只覆盖了 error 分支）。
func TestRESPParsedValues(t *testing.T) {
	// 通过 Acquire 走 EVAL 路径：脚本返回整数
	srv := newFakeServer(t, func(args []string) string {
		if args[0] != "EVAL" {
			return "-ERR unexpected\r\n"
		}
		return ":7\r\n"
	})
	b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
	defer b.Close()

	f, ok, err := b.Acquire(context.Background(), "k", "t", time.Second)
	if err != nil || !ok || f != 7 {
		t.Fatalf("Acquire = %d,%v,%v", f, ok, err)
	}

	// 命令序列必须包含 EVAL + 2 个 key + token + ttl(ms)
	cmds := srv.commands()
	if len(cmds) != 1 {
		t.Fatalf("命令数 = %d", len(cmds))
	}
	got := cmds[0]
	if got[0] != "EVAL" || got[2] != "2" {
		t.Fatalf("命令 = %v", got)
	}
	if got[4] != "xdistlock:fence" {
		t.Fatalf("fencing key = %q", got[4])
	}
	if got[5] != "t" {
		t.Fatalf("token = %q", got[5])
	}
	if got[6] != "1000" {
		t.Fatalf("ttl(ms) = %q, want 1000", got[6])
	}
	if !strings.Contains(got[1], "SET") || !strings.Contains(got[3], "xdistlock:k") {
		t.Fatalf("脚本或 key 不符: %v", got)
	}
	if !strings.Contains(got[1], "INCR") {
		t.Fatalf("脚本应包含 INCR: %v", got[1])
	}
}

// 集群重定向必须被明确识别（而不是被当成普通错误重试到超时）。
func TestClusterRedirectDetected(t *testing.T) {
	for _, reply := range []string{
		"-MOVED 3999 127.0.0.1:6381\r\n",
		"-ASK 3999 127.0.0.1:6381\r\n",
	} {
		srv := newFakeServer(t, func([]string) string { return reply })
		b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))

		_, _, err := b.Acquire(context.Background(), "k", "t", time.Second)
		if err == nil {
			t.Fatal("应报错")
		}
		if !errors.Is(err, redis.ErrClusterNotSupported) {
			t.Fatalf("错误 = %v, want ErrClusterNotSupported", err)
		}
		_ = b.Close()
	}
}

// 释放/续约脚本必须是 compare-and-* 语义（而不是裸 DEL / EXPIRE）。
func TestScriptsAreCompareAndAct(t *testing.T) {
	srv := newFakeServer(t, func(args []string) string {
		if args[0] == "EVAL" {
			return ":1\r\n"
		}
		return "-ERR unexpected\r\n"
	})
	b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
	defer b.Close()
	ctx := context.Background()

	if ok, err := b.Release(ctx, "k", "tok"); err != nil || !ok {
		t.Fatalf("Release = %v, %v", ok, err)
	}
	if ok, err := b.Refresh(ctx, "k", "tok", 2*time.Second); err != nil || !ok {
		t.Fatalf("Refresh = %v, %v", ok, err)
	}

	cmds := srv.commands()
	if len(cmds) != 2 {
		t.Fatalf("命令数 = %d", len(cmds))
	}
	// Release：脚本必须 GET 比对再 DEL，且 numkeys=1
	rel := cmds[0]
	if !strings.Contains(rel[1], "GET") || !strings.Contains(rel[1], "DEL") {
		t.Fatalf("释放脚本不是 compare-and-delete: %v", rel[1])
	}
	if rel[2] != "1" || rel[4] != "tok" {
		t.Fatalf("释放命令参数错误: %v", rel)
	}
	// Refresh：必须 GET 比对再 PEXPIRE
	ref := cmds[1]
	if !strings.Contains(ref[1], "GET") || !strings.Contains(ref[1], "PEXPIRE") {
		t.Fatalf("续约脚本不是 compare-and-expire: %v", ref[1])
	}
	if ref[5] != "2000" {
		t.Fatalf("续约 ttl(ms) = %q", ref[5])
	}
}

func TestAuthAndSelectHandshake(t *testing.T) {
	srv := newFakeServer(t, func(args []string) string {
		switch args[0] {
		case "AUTH":
			// 用旧的单参数形式（不带用户名）
			if len(args) != 2 || args[1] != "secret" {
				return "-ERR invalid password\r\n"
			}
			return "+OK\r\n"
		case "SELECT":
			if len(args) != 2 || args[1] != "3" {
				return "-ERR invalid db\r\n"
			}
			return "+OK\r\n"
		}
		return "+PONG\r\n"
	})
	b := redis.New(srv.addr,
		redis.WithPassword("secret"),
		redis.WithDB(3),
		redis.WithIOTimeout(time.Second),
	)
	defer b.Close()

	if err := b.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	cmds := srv.commands()
	if len(cmds) != 3 {
		t.Fatalf("握手命令数 = %d, want 3（AUTH/SELECT/PING）: %v", len(cmds), cmds)
	}
	if cmds[0][0] != "AUTH" || cmds[1][0] != "SELECT" || cmds[2][0] != "PING" {
		t.Fatalf("握手顺序错误: %v", cmds)
	}

	// 认证失败必须报错
	srv2 := newFakeServer(t, func([]string) string { return "-ERR invalid password\r\n" })
	b2 := redis.New(srv2.addr, redis.WithPassword("wrong"), redis.WithIOTimeout(time.Second))
	defer b2.Close()
	if err := b2.Ping(context.Background()); err == nil {
		t.Fatal("认证失败应报错")
	} else if !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("错误 = %v", err)
	}
}

func TestConnectionReuseAndDiscard(t *testing.T) {
	// 复用：连续 3 次成功命令应只用 1 条连接
	srv := newFakeServer(t, func([]string) string { return "+PONG\r\n" })
	b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
	defer b.Close()

	for i := 0; i < 3; i++ {
		if err := b.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	}
	st := b.PoolStats()
	if st.Dialed != 1 {
		t.Fatalf("建立连接数 = %d, want 1（未复用）", st.Dialed)
	}
	if st.Reused != 2 {
		t.Fatalf("复用次数 = %d, want 2", st.Reused)
	}
	if st.Idle != 1 {
		t.Fatalf("空闲连接 = %d", st.Idle)
	}

	// 协议错误必须丢弃连接（坏连接不能回池）
	srv2 := newFakeServer(t, func([]string) string { return "%unknown\r\n" })
	b2 := redis.New(srv2.addr, redis.WithIOTimeout(time.Second))
	defer b2.Close()
	if err := b2.Ping(context.Background()); err == nil {
		t.Fatal("应报错")
	}
	st2 := b2.PoolStats()
	if st2.Idle != 0 {
		t.Fatalf("协议错误后不该有空闲连接: %d", st2.Idle)
	}
	if st2.ClosedConn != 1 {
		t.Fatalf("应关闭 1 条连接: %d", st2.ClosedConn)
	}

	// 业务错误（-ERR）不影响连接可用性
	srv3 := newFakeServer(t, func([]string) string { return "-ERR unknown command 'FOO'\r\n" })
	b3 := redis.New(srv3.addr, redis.WithIOTimeout(time.Second))
	defer b3.Close()
	_ = b3.Ping(context.Background())
	if st3 := b3.PoolStats(); st3.Idle != 1 {
		t.Fatalf("业务错误后应复用连接: idle=%d", st3.Idle)
	}
}

func TestCtxDeadlineAndDialFailure(t *testing.T) {
	// ctx 超时必须生效（服务端挂住不回复）
	srv := newFakeServer(t, func([]string) string { return "+SLEEP:2s\r\n" })
	b := redis.New(srv.addr, redis.WithIOTimeout(5*time.Second))
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := b.Ping(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("应超时")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("超时未生效: %v", elapsed)
	}

	// 已取消的 ctx 必须快速失败，且不建连
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if err := b.Ping(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v, want Canceled", err)
	}

	// 连不上
	bad := redis.New("127.0.0.1:1", redis.WithDialTimeout(200*time.Millisecond))
	defer bad.Close()
	if err := bad.Ping(context.Background()); err == nil {
		t.Fatal("应报连接失败")
	}

	// Ping 响应异常
	srv2 := newFakeServer(t, func([]string) string { return "+NOTPONG\r\n" })
	b2 := redis.New(srv2.addr, redis.WithIOTimeout(time.Second))
	defer b2.Close()
	if err := b2.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "PING 响应异常") {
		t.Fatalf("错误 = %v", err)
	}
}

func TestOptionValidationAndNilSafety(t *testing.T) {
	// 参数校验
	srv := newFakeServer(t, func([]string) string { return ":1\r\n" })
	b := redis.New(srv.addr, redis.WithIOTimeout(time.Second))
	defer b.Close()
	ctx := context.Background()

	if _, _, err := b.Acquire(ctx, "", "t", time.Second); err == nil {
		t.Fatal("空 key 应报错")
	}
	if _, _, err := b.Acquire(ctx, "k", "", time.Second); err == nil {
		t.Fatal("空 token 应报错")
	}
	if _, _, err := b.Acquire(ctx, "k", "t", 0); err == nil {
		t.Fatal("ttl=0 应报错")
	}
	if _, err := b.Refresh(ctx, "", "t", time.Second); err == nil {
		t.Fatal("Refresh 空 key 应报错")
	}
	if _, err := b.Refresh(ctx, "k", "", time.Second); err == nil {
		t.Fatal("Refresh 空 token 应报错")
	}
	if _, err := b.Refresh(ctx, "k", "t", -1); err == nil {
		t.Fatal("Refresh 负 ttl 应报错")
	}
	if _, err := b.Release(ctx, "", "t"); err == nil {
		t.Fatal("Release 空 key 应报错")
	}
	if _, err := b.Release(ctx, "k", ""); err == nil {
		t.Fatal("Release 空 token 应报错")
	}
	// 以上校验都不该产生命令
	if n := len(srv.commands()); n != 0 {
		t.Fatalf("参数校验失败时不应发命令: %d", n)
	}

	// key 前缀
	b2 := redis.New(srv.addr, redis.WithKeyPrefix("mes"), redis.WithIOTimeout(time.Second))
	defer b2.Close()
	if got := b2.KeyFor("order:1"); got != "mes:order:1" {
		t.Fatalf("KeyFor = %q（前缀应自动补冒号）", got)
	}
	b3 := redis.New(srv.addr, redis.WithKeyPrefix("mes:"), redis.WithIOTimeout(time.Second))
	defer b3.Close()
	if got := b3.KeyFor("order:1"); got != "mes:order:1" {
		t.Fatalf("KeyFor = %q（不该出现双冒号）", got)
	}
	if b.Addr() != srv.addr {
		t.Fatalf("Addr = %q", b.Addr())
	}
	if b.Name() != "redis" {
		t.Fatalf("Name = %q", b.Name())
	}

	// nil 接收者
	var nb *redis.Backend
	if nb.Name() != "redis" || nb.Addr() != "" || nb.KeyFor("k") != "" {
		t.Fatal("nil 零值方法错误")
	}
	if err := nb.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	if err := nb.Ping(ctx); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Ping = %v", err)
	}
	if _, _, err := nb.Acquire(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Acquire = %v", err)
	}
	if _, err := nb.Refresh(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Refresh = %v", err)
	}
	if _, err := nb.Release(ctx, "k", "t"); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Release = %v", err)
	}
	if _, err := nb.Snapshot(ctx, ""); !errors.Is(err, xdistlock.ErrNilBackend) {
		t.Fatalf("nil Snapshot = %v", err)
	}
	if st := nb.PoolStats(); st != (redis.PoolStats{}) {
		t.Fatalf("nil PoolStats = %+v", st)
	}

	// Close 幂等
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
	if _, _, err := b.Acquire(ctx, "k", "t", time.Second); !errors.Is(err, xdistlock.ErrClosed) {
		t.Fatalf("关闭后 = %v, want ErrClosed", err)
	}
}

// ---------------------------------------------------------------------------
// 真实 Redis 集成测试
// ---------------------------------------------------------------------------

// redisAddr 返回可用的真实 Redis 地址；都不可达时跳过集成测试。
func redisAddr(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if v := strings.TrimSpace(os.Getenv("GOKIT_REDIS_ADDR")); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, "127.0.0.1:6382", "127.0.0.1:6379")

	for _, addr := range candidates {
		b := redis.New(addr, redis.WithDialTimeout(300*time.Millisecond), redis.WithIOTimeout(500*time.Millisecond))
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		err := b.Ping(ctx)
		cancel()
		_ = b.Close()
		if err == nil {
			return addr
		}
	}
	t.Skip("真实 Redis 不可达，跳过集成测试（可用 GOKIT_REDIS_ADDR=host:port 指定）")
	return ""
}

// testPrefix 生成互不干扰的 key 前缀。
//
// ⚠️ 本机 6382 上跑着 MES 业务的 Redis，因此集成测试**必须**使用独立前缀并清理，
// 绝不能碰业务 key。
func testPrefix(t *testing.T) (prefix, fenceKey string) {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		b = [6]byte{1, 2, 3, 4, 5, 6}
	}
	h := hex.EncodeToString(b[:])
	return "gokit:test:" + h + ":", "gokit:test:" + h + ":fence"
}

func newRealBackend(t *testing.T, addr, prefix, fenceKey string) *redis.Backend {
	t.Helper()
	b := redis.New(addr,
		redis.WithKeyPrefix(prefix),
		redis.WithFencingKey(fenceKey),
		redis.WithIOTimeout(2*time.Second),
	)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestIntegrationRealRedisContract(t *testing.T) {
	addr := redisAddr(t)
	prefix, fenceKey := testPrefix(t)
	be := newRealBackend(t, addr, prefix, fenceKey)
	ctx := context.Background()

	// 清掉可能残留的 key
	cleanup := func() {
		c := xdistlock.New(be)
		infos, err := c.Snapshot(context.Background(), "")
		if err == nil {
			for _, in := range infos {
				_, _ = be.Release(context.Background(), in.Key, in.Token)
			}
		}
		c.Close()
	}
	t.Cleanup(cleanup)

	// 1) 基本获取 / 冲突 / 安全释放
	f1, ok, err := be.Acquire(ctx, "order:1", "A", 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("Acquire = %v,%v", ok, err)
	}
	if f1 == 0 {
		t.Fatal("fencing 不应为 0")
	}
	// 同 key 冲突
	if _, ok, err := be.Acquire(ctx, "order:1", "B", 5*time.Second); ok || err != nil {
		t.Fatalf("重复获取应失败: ok=%v err=%v", ok, err)
	}
	// 不同 key 并行
	if f2, ok, err := be.Acquire(ctx, "order:2", "B", 5*time.Second); !ok || err != nil {
		t.Fatalf("不同 key 应可获取: %v", err)
	} else if f2 <= f1 {
		t.Fatalf("fencing 未全局单调: %d → %d", f1, f2)
	}
	// ★ 安全释放：非持有者不得删除
	if ok, err := be.Release(ctx, "order:1", "B"); err != nil || ok {
		t.Fatalf("非持有者释放应返回 false: ok=%v err=%v", ok, err)
	}
	// 锁必须还在
	if _, ok, _ := be.Acquire(ctx, "order:1", "C", 5*time.Second); ok {
		t.Fatal("锁被误删了！")
	}
	// 持有者可以释放
	if ok, err := be.Release(ctx, "order:1", "A"); err != nil || !ok {
		t.Fatalf("持有者释放失败: %v", err)
	}
	if ok, _ := be.Release(ctx, "order:2", "B"); !ok {
		t.Fatal("释放 order:2 失败")
	}

	// 2) 续约：只有持有者能续
	if _, ok, err := be.Acquire(ctx, "r", "T1", time.Second); !ok || err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if ok, err := be.Refresh(ctx, "r", "OTHER", time.Second); err != nil || ok {
		t.Fatalf("非持有者续约应失败: ok=%v err=%v", ok, err)
	}
	if ok, err := be.Refresh(ctx, "r", "T1", 3*time.Second); err != nil || !ok {
		t.Fatalf("持有者续约失败: %v", err)
	}
	// 续约后 TTL 应接近 3s
	if infos, err := be.Snapshot(ctx, "r"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	} else if len(infos) != 1 {
		t.Fatalf("Snapshot 条数 = %d", len(infos))
	} else if infos[0].TTL < 2*time.Second || infos[0].TTL > 3500*time.Millisecond {
		t.Fatalf("续约后 TTL = %v", infos[0].TTL)
	}

	// 3) ★ 过期语义：过期后老持有者既不能续约也不能释放
	time.Sleep(3100 * time.Millisecond) // 等租约过期
	if ok, err := be.Refresh(ctx, "r", "T1", time.Second); err != nil || ok {
		t.Fatalf("过期后不应允许续约: ok=%v err=%v", ok, err)
	}
	f3, ok, err := be.Acquire(ctx, "r", "T2", 3*time.Second)
	if err != nil || !ok {
		t.Fatalf("过期后应可被他人获取: %v", err)
	}
	if f3 <= f1 {
		t.Fatalf("fencing 未单调: %d → %d", f1, f3)
	}
	// 老持有者绝不能删掉新持有者的锁
	if ok, err := be.Release(ctx, "r", "T1"); err != nil || ok {
		t.Fatalf("过期持有者不应释放成功: ok=%v err=%v", ok, err)
	}
	infos, err := be.Snapshot(ctx, "r")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(infos) != 1 || infos[0].Token != "T2" {
		t.Fatalf("新持有者的锁被误删: %+v", infos)
	}
	if ok, _ := be.Release(ctx, "r", "T2"); !ok {
		t.Fatal("新持有者释放失败")
	}

	// 4) 全部清理后 Snapshot 应为空
	infos, err = be.Snapshot(ctx, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("残留 %d 把锁: %+v", len(infos), infos)
	}
}

// 真实 Redis 上的端到端：并发互斥 + 看门狗 + 无泄漏。
func TestIntegrationRealRedisConcurrent(t *testing.T) {
	addr := redisAddr(t)
	prefix, fenceKey := testPrefix(t)
	be := newRealBackend(t, addr, prefix, fenceKey)

	c := xdistlock.New(be,
		xdistlock.WithDefaultTTL(2*time.Second),
		xdistlock.WithDefaultAutoRenew(true),
		xdistlock.WithDefaultRetry(20*time.Millisecond, 0),
		xdistlock.WithDefaultAttemptTimeout(2*time.Second),
	)
	defer c.Close()
	ctx := context.Background()

	const goroutines, perG = 12, 8
	var inCritical, violations int64
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				lk, err := c.Lock(ctx, "hot", xdistlock.WithWaitTimeout(30*time.Second))
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				if n := atomicAddInt64(&inCritical, 1); n != 1 {
					atomicAddInt64(&violations, 1)
				}
				time.Sleep(20 * time.Millisecond)
				atomicAddInt64(&inCritical, -1)
				if err := lk.Unlock(ctx); err != nil {
					t.Errorf("Unlock: %v", err)
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("并发测试超时")
	}

	if violations != 0 {
		t.Fatalf("互斥被破坏 %d 次", violations)
	}
	m := c.Metrics()
	if m.AcquireOK != goroutines*perG {
		t.Fatalf("成功获取 = %d, want %d", m.AcquireOK, goroutines*perG)
	}
	if m.ReleaseOK != m.AcquireOK {
		t.Fatalf("获取 %d 次但释放 %d 次（泄漏）", m.AcquireOK, m.ReleaseOK)
	}
	if m.LocksLost != 0 {
		t.Fatalf("发生 %d 次丢锁（看门狗未能保住租约）", m.LocksLost)
	}

	// 结束后 Redis 里不应残留锁
	infos, err := be.Snapshot(ctx, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("残留 %d 把锁: %+v", len(infos), infos)
	}
}

// 真实 Redis 上的「锁被别人抢走后 Ulock 不误删」—— 这是本包最关键的安全性质。
func TestIntegrationRealRedisLostLockDoesNotDelete(t *testing.T) {
	addr := redisAddr(t)
	prefix, fenceKey := testPrefix(t)
	be := newRealBackend(t, addr, prefix, fenceKey)
	c := xdistlock.New(be)
	defer c.Close()
	ctx := context.Background()

	// A 拿一把短租约的锁，且**不开看门狗**，让租约自然过期
	lkA, err := c.Lock(ctx, "s", xdistlock.WithTTL(600*time.Millisecond))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := lkA.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	lkA2, err := c.Lock(ctx, "s", xdistlock.WithTTL(600*time.Millisecond))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// 等租约过期
	time.Sleep(900 * time.Millisecond)

	// B 拿到锁
	lkB, ok, err := c.TryLock(ctx, "s", xdistlock.WithTTL(10*time.Second))
	if err != nil || !ok {
		t.Fatalf("B 应能拿到过期锁: ok=%v err=%v", ok, err)
	}

	// ★ A 释放：必须是 ErrLockLost 且**不能**删掉 B 的锁
	if err := lkA2.Unlock(ctx); !errors.Is(err, xdistlock.ErrLockLost) {
		t.Fatalf("A.Unlock = %v, want ErrLockLost", err)
	}
	// B 的锁必须还在
	if _, ok, _ := c.TryLock(ctx, "s"); ok {
		t.Fatal("B 的锁被 A 误删了！")
	}
	if err := lkB.Unlock(ctx); err != nil {
		t.Fatalf("B.Unlock: %v", err)
	}
	if infos, _ := be.Snapshot(ctx, ""); len(infos) != 0 {
		t.Fatalf("残留: %+v", infos)
	}
}

// 小工具（避免 import 冲突）
// 小工具：避免把 sync/atomic 的调用点写得到处都是
func atomicAddInt64(p *int64, d int64) int64 { return atomic.AddInt64(p, d) }
