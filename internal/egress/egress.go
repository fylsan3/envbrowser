// Package egress 负责出口 IP 校验：经上游代理请求公共回显服务，
// 比对实际出口与环境录入的 exit_ip，并确认不等于本机 IP。
package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/relay"
)

const requestTimeout = 15 * time.Second

// ErrMismatch 出口 IP 与录入值不符或等于本机 IP，属于必须立即断网的错误。
var ErrMismatch = errors.New("出口 IP 不符")

// clientVia 构造一个只能经上游代理出网的 HTTP 客户端，不存在直连回退。
func clientVia(up *config.Upstream) *http.Client {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return relay.DialVia(ctx, up, addr)
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: requestTimeout,
	}
	return &http.Client{Transport: tr, Timeout: requestTimeout}
}

// directClient 只用于获取本机 IP（不经代理）。
func directClient() *http.Client {
	return &http.Client{Timeout: requestTimeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
}

// parseTrace 解析 Cloudflare /cdn-cgi/trace 中的 loc= 国家代码；没有时返回空。
func parseTrace(body string) (country string) {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "loc="); ok {
			v = strings.ToUpper(strings.TrimSpace(v))
			if len(v) == 2 && v != "XX" && v != "T1" { // XX 未知，T1 Tor
				return v
			}
		}
	}
	return ""
}

// parseIP 兼容 Cloudflare trace（ip=x.x.x.x 行）和纯文本回显。
func parseIP(body string) (net.IP, error) {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			line = v
		}
		if ip := net.ParseIP(line); ip != nil {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("回显内容中没有 IP: %.60q", body)
}

// fetchIP 返回出口 IP；回显服务是 Cloudflare trace 时一并返回国家代码，其余服务国家为空。
func fetchIP(ctx context.Context, c *http.Client, svc string) (net.IP, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("%s 返回 %s", svc, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, "", err
	}
	ip, err := parseIP(string(b))
	return ip, parseTrace(string(b)), err
}

// firstIP 按给定顺序轮换服务，某个失败就换下一个；全部失败返回最后一个错误。
func firstIP(ctx context.Context, c *http.Client, services []string, start int) (Result, error) {
	if len(services) == 0 {
		return Result{}, errors.New("未配置回显服务")
	}
	var lastErr error
	for i := 0; i < len(services); i++ {
		svc := services[(start+i)%len(services)]
		ip, cc, err := fetchIP(ctx, c, svc)
		if err == nil {
			return Result{ExitIP: ip, Country: cc, Service: svc}, nil
		}
		lastErr = fmt.Errorf("%s: %w", hostOf(svc), err)
	}
	return Result{}, fmt.Errorf("所有回显服务均失败，最后一个错误: %w", lastErr)
}

// TraceURL Cloudflare 回显地址，唯一能同时给出国家代码的服务。
const TraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// Country 经上游请求 Cloudflare trace，取出口国家代码。
func Country(ctx context.Context, up *config.Upstream) (string, error) {
	_, cc, err := fetchIP(ctx, clientVia(up), TraceURL)
	if err != nil {
		return "", err
	}
	if cc == "" {
		return "", errors.New("Cloudflare 未返回国家代码")
	}
	return cc, nil
}

// CheckCountry 出口国家与记录值不同即为不符（与出口 IP 变化同等处理）。任一方为空时不判断。
func CheckCountry(expected, got string) error {
	if expected == "" || got == "" || strings.EqualFold(expected, got) {
		return nil
	}
	return fmt.Errorf("%w: 出口国家由 %s 变为 %s", ErrMismatch, expected, got)
}

func hostOf(s string) string {
	if u, err := url.Parse(s); err == nil {
		return u.Host
	}
	return s
}

// LocalIP 本机公网 IP，直连获取。程序启动时取一次并缓存。
var (
	localOnce sync.Once
	localIP   net.IP
	localErr  error
)

func LocalIP(ctx context.Context, services []string) (net.IP, error) {
	localOnce.Do(func() {
		var r Result
		r, localErr = firstIP(ctx, directClient(), services, 0)
		localIP = r.ExitIP
	})
	return localIP, localErr
}

// Result 一次校验的结果。
type Result struct {
	ExitIP  net.IP
	Country string // 仅当本次用的是 Cloudflare trace 时有值
	Service string
}

// Check 经上游取出口 IP：必须等于 expected，且不等于本机 IP。
// 返回的错误若 errors.Is(err, ErrMismatch) 则表示出口确实不对（不是网络抖动）。
func Check(ctx context.Context, up *config.Upstream, expected string, local net.IP, services []string, start int) (Result, error) {
	res, err := firstIP(ctx, clientVia(up), services, start)
	if err != nil {
		return Result{}, err
	}
	ip := res.ExitIP
	if local != nil && ip.Equal(local) {
		return res, fmt.Errorf("%w: 出口 %s 等于本机 IP", ErrMismatch, ip)
	}
	if want := net.ParseIP(expected); want == nil || !ip.Equal(want) {
		return res, fmt.Errorf("%w: 实际出口 %s，录入值 %s", ErrMismatch, ip, expected)
	}
	return res, nil
}

// HeaderCheck 经上游访问明文 http 回显页，返回对方看到的请求头。
// 用于确认代理没有添加 Via / X-Forwarded-For 等暴露头，同时验证 80 端口可用。
func HeaderCheck(ctx context.Context, up *config.Upstream, echoURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, echoURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := clientVia(up).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16384))
	return string(b), err
}

// LeakHeaders 返回回显内容中出现的暴露类请求头名称。
func LeakHeaders(body string) []string {
	var found []string
	low := strings.ToLower(body)
	for _, h := range []string{"via", "x-forwarded-for", "forwarded", "x-real-ip", "proxy-connection", "x-proxy-id"} {
		if strings.Contains(low, `"`+h+`"`) {
			found = append(found, h)
		}
	}
	return found
}

// Monitor 运行中按 [min,max] 秒随机间隔复查出口 IP。
//   - 出口不符：立即调用 onFail。
//   - 出口国家与 Country 不同（仅 Cloudflare 那次能取到国家）：立即调用 onFail。
//   - 所有回显服务都失败：计一次；连续 failMax 次才调用 onFail。
type Monitor struct {
	Upstream *config.Upstream
	Expected string
	// Country 记录的出口国家代码。为空时以第一次取到的值为准，并调用 OnCountry。
	Country   string
	OnCountry func(string)
	Local     net.IP
	Services  []string
	MinSec    int
	MaxSec    int
	FailMax   int
	OnFail    func(reason string)
	OnOK      func(Result)
	Logf      func(string, ...any)
}

func (m *Monitor) Run(ctx context.Context) {
	fails, start := 0, rand.IntN(max(len(m.Services), 1))
	for {
		wait := time.Duration(m.MinSec) * time.Second
		if m.MaxSec > m.MinSec {
			wait += time.Duration(rand.Int64N(int64(m.MaxSec-m.MinSec)*int64(time.Second) + 1))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		cctx, cancel := context.WithTimeout(ctx, 2*requestTimeout)
		res, err := Check(cctx, m.Upstream, m.Expected, m.Local, m.Services, start)
		cancel()
		if err == nil {
			err = CheckCountry(m.Country, res.Country)
			if err == nil && m.Country == "" && res.Country != "" {
				m.Country = res.Country
				if m.OnCountry != nil {
					m.OnCountry(res.Country)
				}
			}
		}
		start++ // 下一次从下一个服务开始，分散请求
		if ctx.Err() != nil {
			return
		}
		switch {
		case err == nil:
			fails = 0
			if m.OnOK != nil {
				m.OnOK(res)
			}
		case errors.Is(err, ErrMismatch):
			m.OnFail(err.Error())
			return
		default:
			fails++
			if m.Logf != nil {
				m.Logf("出口复查失败 (%d/%d): %v", fails, m.FailMax, err)
			}
			if fails >= m.FailMax {
				m.OnFail(fmt.Sprintf("连续 %d 次无法确认出口 IP: %v", fails, err))
				return
			}
		}
	}
}
