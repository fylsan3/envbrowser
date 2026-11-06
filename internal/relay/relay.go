// Package relay 是本地中转：浏览器以无认证 SOCKS5 连 127.0.0.1:端口，
// 中转带着用户名密码连上游（socks5h 或 http CONNECT）。
//
// 硬规则：
//   - 不做任何本地 DNS 解析，域名原样交给上游。
//   - 上游网络层偶发失败（超时 / 断开）经同一上游重试 1 次，仍失败即断开；代码中不存在直连分支。
//   - 目标为本机/内网字面地址一律拒绝（测试模式下只放行一个地址）。
//   - 运行中上游不可更换。
package relay

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"envbrowser/internal/config"
)

const dialTimeout = 15 * time.Second

// Relay 一个环境一个实例，独占一个本地端口。
type Relay struct {
	envID    string
	upstream config.Upstream // 拷贝，运行中只读
	ln       net.Listener
	log      func(format string, args ...any)

	// 测试模式下唯一放行的本机地址，如 "127.0.0.1:9000"；空字符串表示关闭。
	testTarget atomic.Value

	blocked atomic.Bool // 出口异常时置位，拒绝所有新连接并断开现有连接

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

// Start 在 127.0.0.1 的 [portMin, portMax] 中找一个空闲端口监听。
func Start(envID string, up config.Upstream, portMin, portMax int, logf func(string, ...any)) (*Relay, error) {
	if err := up.Validate(); err != nil {
		return nil, err
	}
	var ln net.Listener
	var err error
	for p := portMin; p <= portMax; p++ {
		if coolingDown(p) {
			continue
		}
		ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err == nil {
			break
		}
	}
	if ln == nil {
		return nil, fmt.Errorf("端口范围 %d-%d 内没有空闲端口: %v", portMin, portMax, err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r := &Relay{envID: envID, upstream: up, ln: ln, log: logf, conns: map[net.Conn]struct{}{}}
	r.testTarget.Store("")
	r.wg.Add(1)
	go r.serve()
	return r, nil
}

func (r *Relay) Port() int { return r.ln.Addr().(*net.TCPAddr).Port }

func (r *Relay) SetTestTarget(addr string) { r.testTarget.Store(addr) }

// Block 断网：拒绝新连接，并关闭全部现有连接。
func (r *Relay) Block(reason string) {
	if r.blocked.CompareAndSwap(false, true) {
		r.log("[%s] 断网: %s", r.envID, reason)
		r.closeAll()
	}
}

func (r *Relay) Blocked() bool { return r.blocked.Load() }

// Conns 当前经中转的连接数（浏览器侧与上游侧各算一条）。
func (r *Relay) Conns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

// PortCooldown 中转端口释放后，这段时间内不再分配给其他环境。
// 防止仍连着旧端口的浏览器（例如主程序异常退出后残留的）接到别的环境的代理上。
var PortCooldown = 60 * time.Second

var released sync.Map // port -> time.Time

func coolingDown(port int) bool {
	v, ok := released.Load(port)
	if !ok {
		return false
	}
	if time.Since(v.(time.Time)) >= PortCooldown {
		released.Delete(port)
		return false
	}
	return true
}

func (r *Relay) Close() error {
	defer released.Store(r.Port(), time.Now())
	err := r.ln.Close()
	r.closeAll()
	r.wg.Wait()
	return err
}

func (r *Relay) closeAll() {
	r.mu.Lock()
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
}

func (r *Relay) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocked.Load() {
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *Relay) untrack(c net.Conn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

func (r *Relay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c)
		}()
	}
}

// ---------- 浏览器侧：无认证 SOCKS5 ----------

const (
	repSucceeded   = 0x00
	repGeneralFail = 0x01
	repNotAllowed  = 0x02
	repHostUnreach = 0x04
	repCmdNotSupp  = 0x07
	repAddrNotSupp = 0x08
)

func (r *Relay) handle(c net.Conn) {
	if !r.track(c) {
		c.Close()
		return
	}
	defer r.untrack(c)
	defer c.Close()

	c.SetDeadline(time.Now().Add(dialTimeout))
	br := bufio.NewReader(c)

	// 问候：VER NMETHODS METHODS...
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if !contains(methods, 0x00) {
		c.Write([]byte{5, 0xFF})
		return
	}
	c.Write([]byte{5, 0x00})

	// 请求：VER CMD RSV ATYP DST.ADDR DST.PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[0] != 5 {
		return
	}
	if req[1] != 0x01 { // 只支持 CONNECT；UDP ASSOCIATE / BIND 一律拒绝
		reply(c, repCmdNotSupp)
		return
	}
	host, port, err := readAddr(br, req[3])
	if err != nil {
		reply(c, repAddrNotSupp)
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))

	if r.blocked.Load() {
		reply(c, repNotAllowed)
		return
	}
	if !r.allowTarget(host, target) {
		reply(c, repNotAllowed)
		return
	}

	var up net.Conn
	if tt, _ := r.testTarget.Load().(string); tt != "" && target == tt {
		// 测试模式：唯一的本机放行地址，直接连本机检测页（不出网）。
		up, err = net.DialTimeout("tcp", tt, dialTimeout)
	} else {
		up, err = r.dialUpstream(target)
		// 网络层的偶发失败（超时、连接被断开）经同一条上游再试 1 次；
		// 认证失败、上游明确拒绝不重试。绝不改走直连或其他代理。
		if err != nil && retryable(err) && !r.blocked.Load() {
			r.log("[%s] 连接 %s 失败，经同一上游重试 1 次: %v", r.envID, target, err)
			c.SetDeadline(time.Now().Add(dialTimeout + 5*time.Second))
			up, err = r.dialUpstream(target)
			if err == nil && r.blocked.Load() {
				up.Close()
				reply(c, repNotAllowed)
				return
			}
		}
	}
	if err != nil {
		r.log("[%s] 连接 %s 失败: %v", r.envID, target, err)
		reply(c, repHostUnreach)
		return
	}
	if !r.track(up) {
		up.Close()
		reply(c, repNotAllowed)
		return
	}
	defer r.untrack(up)
	defer up.Close()

	reply(c, repSucceeded)
	c.SetDeadline(time.Time{})
	up.SetDeadline(time.Time{})

	// br 里可能已缓冲了浏览器发来的首包，先转发。
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, br); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

// allowTarget 拒绝本机和内网字面地址。域名一律放行给上游解析（中转不做 DNS）。
// localhost 这类名字也在此拦截。
func (r *Relay) allowTarget(host, target string) bool {
	if tt, _ := r.testTarget.Load().(string); tt != "" && target == tt {
		return true
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return true
	}
	return !IsLocalOrPrivate(ip)
}

// IsLocalOrPrivate 本机、内网、链路本地、未指定、组播地址。
func IsLocalOrPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast()
}

func readAddr(br *bufio.Reader, atyp byte) (string, int, error) {
	var host string
	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case 0x03:
		l, err := br.ReadByte()
		if err != nil {
			return "", 0, err
		}
		b := make([]byte, l)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		return "", 0, errors.New("unknown atyp")
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return "", 0, err
	}
	return host, int(pb[0])<<8 | int(pb[1]), nil
}

func reply(c net.Conn, rep byte) {
	c.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
}

func contains(b []byte, v byte) bool {
	for _, x := range b {
		if x == v {
			return true
		}
	}
	return false
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		tc.CloseWrite()
	} else {
		c.Close()
	}
}

// ---------- 上游侧 ----------

// dialUpstream 经上游代理连接 target（host:port，host 可以是域名）。
// 失败直接返回错误，不存在任何回退。
func (r *Relay) dialUpstream(target string) (net.Conn, error) {
	return DialVia(context.Background(), &r.upstream, target)
}

// retryable 判断是否属于值得重试一次的网络层偶发失败。
func retryable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

// DialVia 供中转和出口校验共用。
func DialVia(ctx context.Context, up *config.Upstream, target string) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", up.Addr())
	if err != nil {
		return nil, fmt.Errorf("连接上游代理失败: %w", err)
	}
	conn.SetDeadline(time.Now().Add(dialTimeout))
	switch up.Type {
	case "socks5h":
		err = socks5Handshake(conn, up, target)
	case "http":
		err = httpConnect(conn, up, target)
	default:
		err = fmt.Errorf("不支持的上游类型 %q", up.Type)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

// socks5Handshake 用户名密码认证（RFC 1929），目标以域名形式发出（远端解析）。
func socks5Handshake(conn net.Conn, up *config.Upstream, target string) error {
	if _, err := conn.Write([]byte{5, 1, 0x02}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("上游 socks5 握手失败: %w", err)
	}
	if resp[0] != 5 || resp[1] != 0x02 {
		return fmt.Errorf("上游 socks5 不接受用户名密码认证 (method=0x%02x)", resp[1])
	}
	auth := []byte{1, byte(len(up.Username))}
	auth = append(auth, up.Username...)
	auth = append(auth, byte(len(up.Password)))
	auth = append(auth, up.Password...)
	if _, err := conn.Write(auth); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("上游 socks5 认证无响应: %w", err)
	}
	if resp[1] != 0 {
		return errors.New("上游 socks5 用户名或密码错误")
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(portStr)
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 1)
			req = append(req, v4...)
		} else {
			req = append(req, 4)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("域名过长")
		}
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("上游 socks5 CONNECT 无响应: %w", err)
	}
	if head[1] != 0 {
		return fmt.Errorf("上游 socks5 拒绝连接 %s (rep=0x%02x)", target, head[1])
	}
	var skip int
	switch head[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return err
		}
		skip = int(l[0])
	default:
		return errors.New("上游 socks5 返回未知地址类型")
	}
	_, err = io.ReadFull(conn, make([]byte, skip+2))
	return err
}

// httpConnect 发送 CONNECT 并带 Basic 认证。
func httpConnect(conn net.Conn, up *config.Upstream, target string) error {
	cred := base64.StdEncoding.EncodeToString([]byte(up.Username + ":" + up.Password))
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target +
		"\r\nProxy-Authorization: Basic " + cred + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return fmt.Errorf("上游 http 代理无响应: %w", err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == 407:
		return errors.New("上游 http 代理用户名或密码错误 (407)")
	case resp.StatusCode != 200:
		return fmt.Errorf("上游 http 代理拒绝 CONNECT %s (%s)", target, resp.Status)
	}
	if br.Buffered() > 0 {
		return errors.New("上游 http 代理在 CONNECT 响应后多发了数据")
	}
	return nil
}
