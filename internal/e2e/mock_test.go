// Package e2e 用本机模拟的上游代理做端到端测试，替代真实代理验证：
// 认证、http/https 访问、断上游即断网、域名不在本地解析、环境之间 Cookie 隔离。
// 模拟代理的用户名密码是测试时随机生成的，不涉及任何真实代理信息。
package e2e

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"sync"
	"testing"

	"envbrowser/internal/config"
)

// mockUpstream 模拟上游代理。只认 routes 里的目标（host:port → 本机地址），
// 其余一律拒绝；所有收到的目标都记录下来。
type mockUpstream struct {
	kind   string // "socks5h" | "http"
	user   string
	pass   string
	routes map[string]string
	ln     net.Listener

	mu       sync.Mutex
	targets  []string
	authFail int
	conns    map[net.Conn]struct{}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newMock(t *testing.T, kind string, routes map[string]string) *mockUpstream {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mockUpstream{kind: kind, user: "e2e-" + randHex(4), pass: randHex(12),
		routes: routes, ln: ln, conns: map[net.Conn]struct{}{}}
	t.Cleanup(m.Close)
	go m.serve()
	return m
}

// Upstream 返回指向模拟代理的上游配置。
func (m *mockUpstream) Upstream() config.Upstream {
	a := m.ln.Addr().(*net.TCPAddr)
	return config.Upstream{Type: m.kind, Host: "127.0.0.1", Port: a.Port, Username: m.user, Password: m.pass}
}

// Close 模拟上游掉线：停止监听并断开所有现有连接。
func (m *mockUpstream) Close() {
	m.ln.Close()
	m.mu.Lock()
	for c := range m.conns {
		c.Close()
	}
	m.mu.Unlock()
}

func (m *mockUpstream) Targets() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.targets...)
}

func (m *mockUpstream) AuthFails() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authFail
}

func (m *mockUpstream) serve() {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			return
		}
		m.mu.Lock()
		m.conns[c] = struct{}{}
		m.mu.Unlock()
		go func() {
			defer func() {
				m.mu.Lock()
				delete(m.conns, c)
				m.mu.Unlock()
				c.Close()
			}()
			if m.kind == "http" {
				m.handleHTTP(c)
			} else {
				m.handleSocks(c)
			}
		}()
	}
}

func (m *mockUpstream) record(target string, authOK bool) {
	m.mu.Lock()
	if authOK {
		m.targets = append(m.targets, target)
	} else {
		m.authFail++
	}
	m.mu.Unlock()
}

// pipe 把客户端接到 routes 里的本机地址。
func (m *mockUpstream) pipe(c net.Conn, br *bufio.Reader, dst string) {
	d, err := net.Dial("tcp", dst)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.conns[d] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.conns, d)
		m.mu.Unlock()
		d.Close()
	}()
	go func() { io.Copy(d, br); d.Close() }()
	io.Copy(c, d)
}
