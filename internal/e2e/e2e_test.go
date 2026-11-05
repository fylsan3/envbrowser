package e2e

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/egress"
	"envbrowser/internal/relay"
)

// 本包的中转端口段，与 relay 包测试（30001-31002）错开，避免并行测试抢端口。
const portMin, portMax = 32001, 32999

// site 模拟目标网站，统计收到的 TCP 连接数（用于确认每个连接都来自上游代理）。
type site struct {
	plain, tls *httptest.Server
	conns      atomic.Int64
}

func newSite(t *testing.T, h http.Handler) *site {
	s := &site{}
	count := func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			s.conns.Add(1)
		}
	}
	s.plain = httptest.NewUnstartedServer(h)
	s.plain.Config.ConnState = count
	s.plain.Start()
	s.tls = httptest.NewUnstartedServer(h)
	s.tls.Config.ConnState = count
	s.tls.StartTLS()
	t.Cleanup(func() { s.plain.Close(); s.tls.Close() })
	return s
}

func hostOf(u string) string { x, _ := url.Parse(u); return x.Host }

// routes 让模拟上游把 site.test:80 / :443 指到本机网站。
func (s *site) routes(extra map[string]string) map[string]string {
	r := map[string]string{"site.test:80": hostOf(s.plain.URL), "site.test:443": hostOf(s.tls.URL)}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func startRelay(t *testing.T, up config.Upstream) *relay.Relay {
	r, err := relay.Start("env-e2e", up, portMin, portMax, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// browserLike 扮演浏览器：只认中转的 socks5 地址，域名交给中转（Go 的 socks5 客户端不在本地解析）。
func browserLike(port int) *http.Client {
	pu, _ := url.Parse("socks5://127.0.0.1:" + strconv.Itoa(port))
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		Proxy:             http.ProxyURL(pu),
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // 测试证书
	}}
}

func get(c *http.Client, u string) (string, error) {
	resp, err := c.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return string(b), fmt.Errorf("状态 %s", resp.Status)
	}
	return string(b), err
}

func hello() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "site-ok") })
}

// 两种上游都要：带认证连得上；http(80) 和 https(443) 网站都能打开；
// 上游收到的目标是域名（中转没有在本地解析）；网站收到的每个连接都经过上游。
func TestUpstreamKinds(t *testing.T) {
	for _, kind := range []string{"socks5h", "http"} {
		t.Run(kind, func(t *testing.T) {
			s := newSite(t, hello())
			m := newMock(t, kind, s.routes(nil))
			r := startRelay(t, m.Upstream())
			c := browserLike(r.Port())
			for _, u := range []string{"http://site.test/", "https://site.test/"} {
				body, err := get(c, u)
				if err != nil || body != "site-ok" {
					t.Fatalf("%s 打不开: %q %v", u, body, err)
				}
			}
			got := m.Targets()
			if len(got) != 2 || got[0] != "site.test:80" || got[1] != "site.test:443" {
				t.Fatalf("上游应收到域名目标 site.test:80 / :443，实际 %v", got)
			}
			if n := s.conns.Load(); n != int64(len(got)) {
				t.Fatalf("网站收到 %d 个连接，上游只转发了 %d 个：存在绕过代理的连接", n, len(got))
			}
		})
	}
}

// 密码错误：打不开，且网站一个连接都没收到（没有直连回退）。
func TestWrongPassword(t *testing.T) {
	for _, kind := range []string{"socks5h", "http"} {
		t.Run(kind, func(t *testing.T) {
			s := newSite(t, hello())
			m := newMock(t, kind, s.routes(nil))
			up := m.Upstream()
			up.Password += "-wrong"
			r := startRelay(t, up)
			if _, err := get(browserLike(r.Port()), "http://site.test/"); err == nil {
				t.Fatal("密码错误时不应打开网站")
			}
			if m.AuthFails() == 0 {
				t.Fatal("上游应记录到认证失败")
			}
			if s.conns.Load() != 0 {
				t.Fatal("网站收到了连接：密码错误时存在直连")
			}
		})
	}
}

// 上游掉线：已建立的连接被切断，新请求打不开，网站不再收到任何连接。
func TestUpstreamDownCutsOff(t *testing.T) {
	for _, kind := range []string{"socks5h", "http"} {
		t.Run(kind, func(t *testing.T) {
			s := newSite(t, hello())
			m := newMock(t, kind, s.routes(nil))
			r := startRelay(t, m.Upstream())
			c := browserLike(r.Port())
			if _, err := get(c, "http://site.test/"); err != nil {
				t.Fatal(err)
			}
			before := s.conns.Load()
			m.Close()
			for _, u := range []string{"http://site.test/", "https://site.test/"} {
				if _, err := get(c, u); err == nil {
					t.Fatalf("上游掉线后 %s 仍能打开", u)
				}
			}
			if s.conns.Load() != before {
				t.Fatal("上游掉线后网站仍收到连接：存在直连")
			}
		})
	}
}

// 本机和内网地址一律被中转拒绝，模拟上游也收不到这些请求。
func TestLocalTargetsNeverLeaveRelay(t *testing.T) {
	s := newSite(t, hello())
	m := newMock(t, "socks5h", s.routes(nil))
	r := startRelay(t, m.Upstream())
	c := browserLike(r.Port())
	for _, u := range []string{"http://127.0.0.1:" + strconv.Itoa(r.Port()) + "/", "http://localhost/", "http://192.168.1.1/", "http://10.0.0.1/"} {
		if _, err := get(c, u); err == nil {
			t.Fatalf("%s 不应能打开", u)
		}
	}
	if got := m.Targets(); len(got) != 0 {
		t.Fatalf("本机/内网地址不应交给上游: %v", got)
	}
}

// trace 模拟 Cloudflare trace，出口 IP 和国家可以在测试中途改变。
type trace struct {
	ip, loc atomic.Value
}

func newTrace(t *testing.T, ip, loc string) (*trace, string) {
	tr := &trace{}
	tr.ip.Store(ip)
	tr.loc.Store(loc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "fl=1\nip=%s\nloc=%s\n", tr.ip.Load(), tr.loc.Load())
	}))
	t.Cleanup(srv.Close)
	return tr, hostOf(srv.URL)
}

// 出口校验经模拟上游取 IP 和国家：一致通过；IP 不符、等于本机 IP 都判为 ErrMismatch。
func TestEgressCheckViaMock(t *testing.T) {
	const exit = "198.51.100.7" // 文档保留段，非真实地址
	_, addr := newTrace(t, exit, "JP")
	m := newMock(t, "http", map[string]string{"echo.test:80": addr})
	up := m.Upstream()
	svc := []string{"http://echo.test/cdn-cgi/trace"}
	ctx := context.Background()

	res, err := egress.Check(ctx, &up, exit, nil, svc, 0)
	if err != nil || res.ExitIP.String() != exit || res.Country != "JP" {
		t.Fatalf("应通过并取到 JP: %+v %v", res, err)
	}
	if _, err := egress.Check(ctx, &up, "198.51.100.8", nil, svc, 0); !errors.Is(err, egress.ErrMismatch) {
		t.Fatalf("录入值不同应为 ErrMismatch: %v", err)
	}
	if _, err := egress.Check(ctx, &up, exit, net.ParseIP(exit), svc, 0); !errors.Is(err, egress.ErrMismatch) {
		t.Fatalf("出口等于本机 IP 应为 ErrMismatch: %v", err)
	}
}

// 运行中出口 IP 或国家变化：复查发现后立即断网，浏览器侧的现有连接和新请求都断开。
func TestMonitorBlocksOnChange(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*trace)
		wantSub string
	}{
		{"IP变化", func(tr *trace) { tr.ip.Store("198.51.100.99") }, "198.51.100.99"},
		{"国家变化", func(tr *trace) { tr.loc.Store("US") }, "US"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const exit = "198.51.100.7"
			tr, addr := newTrace(t, exit, "JP")
			s := newSite(t, hello())
			m := newMock(t, "socks5h", s.routes(map[string]string{"echo.test:80": addr}))
			up := m.Upstream()
			r := startRelay(t, up)
			c := browserLike(r.Port())

			blocked := make(chan string, 1)
			mon := &egress.Monitor{
				Upstream: &up, Expected: exit, Country: "JP",
				Services: []string{"http://echo.test/cdn-cgi/trace"},
				MinSec:   0, MaxSec: 0, FailMax: 3,
				Logf:   t.Logf,
				OnFail: func(reason string) { r.Block(reason); blocked <- reason },
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go mon.Run(ctx)

			if _, err := get(c, "http://site.test/"); err != nil {
				t.Fatalf("变化前应能打开: %v", err)
			}
			tc.change(tr)
			select {
			case reason := <-blocked:
				if !strings.Contains(reason, tc.wantSub) {
					t.Fatalf("断网原因应提到 %s: %s", tc.wantSub, reason)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("出口变化后 10 秒内未断网")
			}
			if !r.Blocked() {
				t.Fatal("中转应处于断网状态")
			}
			if _, err := get(c, "http://site.test/"); err == nil {
				t.Fatal("断网后仍能打开网站")
			}
		})
	}
}
