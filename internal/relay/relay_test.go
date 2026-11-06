package relay

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"envbrowser/internal/config"
)

// echoServer 原样回写收到的数据。
func echoServer(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// fakeSocks5 带用户名密码认证的假上游。域名 "echo.test" 映射到 echoAddr；
// 记录收到的目标，用于确认域名原样交给上游（中转没做本地解析）。
func fakeSocks5(t *testing.T, user, pass, echoAddr string, seen chan<- string) (string, int) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				h := make([]byte, 2)
				io.ReadFull(br, h)
				io.ReadFull(br, make([]byte, h[1]))
				c.Write([]byte{5, 2})
				v := make([]byte, 2)
				io.ReadFull(br, v)
				u := make([]byte, v[1])
				io.ReadFull(br, u)
				pl, _ := br.ReadByte()
				p := make([]byte, pl)
				io.ReadFull(br, p)
				if string(u) != user || string(p) != pass {
					c.Write([]byte{1, 1})
					return
				}
				c.Write([]byte{1, 0})
				req := make([]byte, 4)
				io.ReadFull(br, req)
				host, port, err := readAddr(br, req[3])
				if err != nil {
					return
				}
				target := net.JoinHostPort(host, strconv.Itoa(port))
				if seen != nil {
					seen <- target
				}
				if host != "echo.test" {
					c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				e, err := net.Dial("tcp", echoAddr)
				if err != nil {
					return
				}
				defer e.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(e, br)
				io.Copy(c, e)
			}(c)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

// fakeHTTP 带 Basic 认证的假 HTTP CONNECT 上游。
func fakeHTTP(t *testing.T, user, pass, echoAddr string) (string, int) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if req.Header.Get("Proxy-Authorization") != want {
					io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
					return
				}
				if !strings.HasPrefix(req.Host, "echo.test:") {
					io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				e, err := net.Dial("tcp", echoAddr)
				if err != nil {
					return
				}
				defer e.Close()
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go io.Copy(e, br)
				io.Copy(c, e)
			}(c)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

// socksConnect 扮演浏览器：无认证 SOCKS5 连中转，请求域名目标。返回 REP。
func socksConnect(t *testing.T, port int, host string, dport int) (net.Conn, byte) {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 0})
	r := make([]byte, 2)
	if _, err := io.ReadFull(c, r); err != nil {
		return c, 0xFF // 中转直接关闭连接（例如已断网）也算拒绝
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 1)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(dport>>8), byte(dport))
	c.Write(req)
	resp := make([]byte, 10)
	if _, err := io.ReadFull(c, resp); err != nil {
		return c, 0xFF
	}
	return c, resp[1]
}

func roundTrip(t *testing.T, c net.Conn) {
	c.Write([]byte("hello"))
	b := make([]byte, 5)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "hello" {
		t.Fatalf("数据未正确往返: %q %v", b, err)
	}
}

func startRelay(t *testing.T, up config.Upstream) *Relay {
	r, err := Start("env-test", up, 30001, 30999, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestSocks5hAuthOK(t *testing.T) {
	echo := echoServer(t)
	seen := make(chan string, 1)
	h, p := fakeSocks5(t, "u", "p", echo, seen)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: h, Port: p, Username: "u", Password: "p"})
	c, rep := socksConnect(t, r.Port(), "echo.test", 443)
	defer c.Close()
	if rep != 0 {
		t.Fatalf("rep=%d", rep)
	}
	if got := <-seen; got != "echo.test:443" {
		t.Fatalf("上游收到的目标应为域名，实际 %s", got)
	}
	roundTrip(t, c)
}

func TestSocks5hAuthFail(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeSocks5(t, "u", "p", echo, nil)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: h, Port: p, Username: "u", Password: "wrong"})
	c, rep := socksConnect(t, r.Port(), "echo.test", 443)
	defer c.Close()
	if rep == 0 {
		t.Fatal("认证失败时不应连通")
	}
}

func TestHTTPConnectAuth(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeHTTP(t, "u", "p", echo)
	ok := startRelay(t, config.Upstream{Type: "http", Host: h, Port: p, Username: "u", Password: "p"})
	c, rep := socksConnect(t, ok.Port(), "echo.test", 80)
	if rep != 0 {
		t.Fatalf("rep=%d", rep)
	}
	roundTrip(t, c)
	c.Close()

	bad := startRelay(t, config.Upstream{Type: "http", Host: h, Port: p, Username: "u", Password: "x"})
	c2, rep2 := socksConnect(t, bad.Port(), "echo.test", 80)
	defer c2.Close()
	if rep2 == 0 {
		t.Fatal("407 时不应连通")
	}
}

func TestUpstreamDownNoDirect(t *testing.T) {
	// 上游端口无人监听：必须失败，不能直连。
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: "127.0.0.1", Port: port, Username: "u", Password: "p"})
	c, rep := socksConnect(t, r.Port(), "example.com", 80)
	defer c.Close()
	if rep == 0 {
		t.Fatal("上游不可用时不应连通")
	}
}

func TestLocalTargetsRejected(t *testing.T) {
	echo := echoServer(t)
	seen := make(chan string, 16)
	h, p := fakeSocks5(t, "u", "p", echo, seen)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: h, Port: p, Username: "u", Password: "p"})
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "0.0.0.0", "localhost", "a.localhost"} {
		c, rep := socksConnect(t, r.Port(), host, 8800)
		c.Close()
		if rep != repNotAllowed {
			t.Errorf("%s 应被拒绝(rep=2)，实际 rep=%d", host, rep)
		}
	}
	select {
	case s := <-seen:
		t.Fatalf("本机地址不应到达上游: %s", s)
	default:
	}
}

func TestTestModeOnlyOneTarget(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeSocks5(t, "u", "p", echo, nil)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: h, Port: p, Username: "u", Password: "p"})
	r.SetTestTarget(echo)
	eh, ep, _ := net.SplitHostPort(echo)
	port, _ := strconv.Atoi(ep)
	c, rep := socksConnect(t, r.Port(), eh, port)
	if rep != 0 {
		t.Fatalf("测试目标应放行，rep=%d", rep)
	}
	roundTrip(t, c)
	c.Close()
	c2, rep2 := socksConnect(t, r.Port(), eh, port+1)
	c2.Close()
	if rep2 != repNotAllowed {
		t.Fatalf("其他本机端口仍应拒绝，rep=%d", rep2)
	}
}

func TestBlockCutsConnections(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeSocks5(t, "u", "p", echo, nil)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: h, Port: p, Username: "u", Password: "p"})
	c, rep := socksConnect(t, r.Port(), "echo.test", 443)
	if rep != 0 {
		t.Fatalf("rep=%d", rep)
	}
	roundTrip(t, c)
	r.Block("test")
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("断网后现有连接应被关闭")
	}
	c2, rep2 := socksConnect(t, r.Port(), "echo.test", 443)
	c2.Close()
	if rep2 == 0 {
		t.Fatal("断网后新连接不应连通")
	}
}

func TestUDPRejected(t *testing.T) {
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: "127.0.0.1", Port: 1, Username: "u", Password: "p"})
	c, _ := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(r.Port()))
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 0})
	io.ReadFull(c, make([]byte, 2))
	c.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}) // UDP ASSOCIATE
	resp := make([]byte, 10)
	io.ReadFull(c, resp)
	if resp[1] != repCmdNotSupp {
		t.Fatalf("UDP 应被拒绝，rep=%d", resp[1])
	}
}

// 端口释放后在冷却期内不得被分配给下一个环境。
func TestPortCooldown(t *testing.T) {
	up := config.Upstream{Type: "socks5h", Host: "203.0.113.1", Port: 1080, Username: "u", Password: "p"}
	r1, err := Start("env-a", up, 31001, 31002, nil)
	if err != nil {
		t.Fatal(err)
	}
	p1 := r1.Port()
	r1.Close()
	r2, err := Start("env-b", up, 31001, 31002, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if r2.Port() == p1 {
		t.Fatalf("端口 %d 刚释放就被重新分配", p1)
	}
	// 范围内只剩冷却中的端口时应拒绝启动，而不是复用
	if r3, err := Start("env-c", up, p1, p1, nil); err == nil {
		r3.Close()
		t.Fatal("冷却中的端口不应被分配")
	}
}

// flakyFront 在 backend 前面套一层：前 drop 个连接一进来就断开，之后原样转发。返回端口和连接计数。
func flakyFront(t *testing.T, backend string, drop int32) (int, *atomic.Int32) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if n.Add(1) <= drop {
				c.Close()
				continue
			}
			go func(c net.Conn) {
				defer c.Close()
				b, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer b.Close()
				go io.Copy(b, c)
				io.Copy(c, b)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, &n
}

func TestRetryOnceSameUpstream(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeSocks5(t, "u", "p", echo, nil)
	port, n := flakyFront(t, net.JoinHostPort(h, strconv.Itoa(p)), 1)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: "127.0.0.1", Port: port, Username: "u", Password: "p"})
	c, rep := socksConnect(t, r.Port(), "echo.test", 443)
	defer c.Close()
	if rep != 0 {
		t.Fatalf("第一次被断开后应经同一上游重试成功，rep=%d", rep)
	}
	roundTrip(t, c)
	if got := n.Load(); got != 2 {
		t.Fatalf("上游应收到 2 次连接，实际 %d", got)
	}
}

func TestRetryAtMostOnceNoDirect(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeSocks5(t, "u", "p", echo, nil)
	port, n := flakyFront(t, net.JoinHostPort(h, strconv.Itoa(p)), 100)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: "127.0.0.1", Port: port, Username: "u", Password: "p"})
	c, rep := socksConnect(t, r.Port(), "echo.test", 443)
	defer c.Close()
	if rep == 0 {
		t.Fatal("两次都失败时不应连通（不能直连）")
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("最多重试 1 次，上游应收到 2 次连接，实际 %d", got)
	}
}

func TestNoRetryOnAuthFail(t *testing.T) {
	echo := echoServer(t)
	h, p := fakeSocks5(t, "u", "p", echo, nil)
	port, n := flakyFront(t, net.JoinHostPort(h, strconv.Itoa(p)), 0)
	r := startRelay(t, config.Upstream{Type: "socks5h", Host: "127.0.0.1", Port: port, Username: "u", Password: "wrong"})
	c, rep := socksConnect(t, r.Port(), "echo.test", 443)
	defer c.Close()
	if rep == 0 {
		t.Fatal("认证失败时不应连通")
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("认证失败不应重试，上游应只收到 1 次连接，实际 %d", got)
	}
}
