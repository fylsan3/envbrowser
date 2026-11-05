package e2e

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strconv"
)

// handleSocks 只接受用户名密码认证（0x02），按 RFC 1929 校验。
func (m *mockUpstream) handleSocks(c net.Conn) {
	br := bufio.NewReader(c)
	h := make([]byte, 2)
	if _, err := io.ReadFull(br, h); err != nil || h[0] != 5 {
		return
	}
	methods := make([]byte, h[1])
	io.ReadFull(br, methods)
	ok := false
	for _, x := range methods {
		ok = ok || x == 0x02
	}
	if !ok {
		c.Write([]byte{5, 0xFF})
		return
	}
	c.Write([]byte{5, 0x02})
	v := make([]byte, 2)
	if _, err := io.ReadFull(br, v); err != nil {
		return
	}
	u := make([]byte, v[1])
	io.ReadFull(br, u)
	pl, _ := br.ReadByte()
	p := make([]byte, pl)
	io.ReadFull(br, p)
	if string(u) != m.user || string(p) != m.pass {
		m.record("", false)
		c.Write([]byte{1, 1})
		return
	}
	c.Write([]byte{1, 0})

	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(br, b)
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		io.ReadFull(br, b)
		host = net.IP(b).String()
	case 3:
		l, _ := br.ReadByte()
		b := make([]byte, l)
		io.ReadFull(br, b)
		host = string(b)
	default:
		return
	}
	pb := make([]byte, 2)
	io.ReadFull(br, pb)
	target := net.JoinHostPort(host, strconv.Itoa(int(pb[0])<<8|int(pb[1])))
	m.record(target, true)
	dst, found := m.routes[target]
	if !found {
		c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0}) // host unreachable
		return
	}
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	m.pipe(c, br, dst)
}

// handleHTTP 只支持 CONNECT（中转对 http 和 https 网站都用 CONNECT），校验 Basic 认证。
func (m *mockUpstream) handleHTTP(c net.Conn) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(m.user+":"+m.pass))
	if req.Header.Get("Proxy-Authorization") != want {
		m.record("", false)
		io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
		return
	}
	if req.Method != http.MethodConnect {
		io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}
	m.record(req.Host, true)
	dst, found := m.routes[req.Host]
	if !found {
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	m.pipe(c, br, dst)
}
