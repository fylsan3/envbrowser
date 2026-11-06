// Package fpcheck 是本机检测页（需求规格第 9 节）。
//
// 检测页只监听 127.0.0.1（默认 9000）。环境浏览器访问它也要经过中转，
// 中转只在测试模式下放行这个地址，所以平时环境浏览器打不开它。
// 页面在主页面、iframe、Worker、Service Worker 中分别采集指纹，提交到 /report；
// 服务端与该环境的配置逐项比对，报告保存到 <环境目录>/reports/。
package fpcheck

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"envbrowser/internal/config"
)

//go:embed web/*
var webFS embed.FS

// Place 一个读取位置（主页面 / iframe / Worker / Service Worker）采集到的值。
type Place struct {
	Where           string `json:"where"`
	UA              string `json:"ua"`
	Platform        string `json:"platform"`
	Cores           int    `json:"cores"`
	Mem             any    `json:"mem"`
	Lang            string `json:"lang"`
	Langs           string `json:"langs"`
	TZ              string `json:"tz"`
	TZOffset        int    `json:"tzOffset"`
	IntlDate        string `json:"intlDate"`      // Intl.DateTimeFormat 默认区域
	IntlNumber      string `json:"intlNumber"`    // Intl.NumberFormat 默认区域
	IntlCollator    string `json:"intlCollator"`  // Intl.Collator 默认区域
	TZNameDefault   string `json:"tzNameDefault"` // 默认区域写出的时区名
	TZNameLang      string `json:"tzNameLang"`    // 按 navigator.language 写出的时区名
	DateString      string `json:"dateString"`    // Date.toString() 括号里的时区名
	IntlErr         string `json:"intlErr,omitempty"`
	Webdriver       bool   `json:"webdriver"`
	Brands          string `json:"brands"`
	UAPlatform      string `json:"uaPlatform"`
	PlatformVersion string `json:"platformVersion"`
	Arch            string `json:"arch"`
	FullVersions    string `json:"fullVersions"`
	UAErr           string `json:"uaErr,omitempty"`
	Canvas          string `json:"canvas"`
	GLVendor        string `json:"glVendor"`
	GLRenderer      string `json:"glRenderer"`
	GLImage         string `json:"glImage"`
	GLErr           string `json:"glErr,omitempty"`
}

type PortResult struct {
	Port   int    `json:"port"`
	Result string `json:"result"`
	MS     int    `json:"ms"`
}

// Extra 只在主页面测的项。
type Extra struct {
	Screen   string       `json:"screen"`
	Window   string       `json:"window"`
	Audio    string       `json:"audio"`
	Fonts    string       `json:"fonts"`
	Rects    string       `json:"rects"`
	RTCIPs   string       `json:"rtcIPs"`
	RTCTypes string       `json:"rtcTypes"`
	Ports    []PortResult `json:"ports"`
	Cookie   string       `json:"cookie"`
}

// Headers 浏览器提交报告时实际发出的请求头（由服务端记录，不采信页面自报）。
type Headers struct {
	UA         string `json:"ua"`
	AcceptLang string `json:"acceptLang"`
	SecCHUA    string `json:"secChUa"`
	SecCHUAPF  string `json:"secChUaPlatform"`
}

// Raw 页面提交的原始数据。
type Raw struct {
	Env     string   `json:"env"`
	Places  []Place  `json:"places"`
	Extra   Extra    `json:"extra"`
	Errors  []string `json:"errors"`
	Headers *Headers `json:"headers,omitempty"`
}

// Row 报告的一行：检测项 / 配置目标 / 实际读取 / 读取位置 / 结果。
type Row struct {
	Item  string `json:"item"`
	Want  string `json:"want"`
	Got   string `json:"got"`
	Where string `json:"where"`
	OK    bool   `json:"ok"`
	Warn  bool   `json:"warn,omitempty"` // 不算失败，但需要人看一眼
}

// Report 保存到 reports/ 的完整报告。
type Report struct {
	Env     string    `json:"env"`
	Time    time.Time `json:"time"`
	Seed    int64     `json:"seed"`
	Summary string    `json:"summary"`
	Failed  int       `json:"failed"`
	Rows    []Row     `json:"rows"`
	Raw     Raw       `json:"raw"`
	File    string    `json:"file,omitempty"`
}

// Server 检测页服务。
type Server struct {
	Store   *config.Store
	Addr    string // 如 127.0.0.1:9000
	LocalIP net.IP // 本机公网 IP，用于 WebRTC 泄露判断；可为 nil

	mu      sync.Mutex
	waiters map[string][]chan *Report
}

func New(store *config.Store, addr string, local net.IP) *Server {
	return &Server{Store: store, Addr: addr, LocalIP: local, waiters: map[string][]chan *Report{}}
}

var envIDRe = regexp.MustCompile(`^env-\d{4,}$`)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, f := range []string{"index.html", "frame.html", "collect.js", "main.js", "frame.js", "worker.js", "sw.js"} {
		name := f
		path := "/" + f
		if f == "index.html" {
			path = "/{$}"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { s.static(w, name) })
	}
	mux.HandleFunc("POST /report", s.report)
	return s.guard(mux)
}

// guard 只接受 Host 为本服务地址的请求（防 DNS 重绑定）；POST 还要求 Origin 是本页面。
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.Addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Origin") != "http://"+s.Addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; worker-src 'self'; frame-src 'self'; connect-src 'self' http://127.0.0.1:*; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static(w http.ResponseWriter, name string) {
	b, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	ct := "text/html; charset=utf-8"
	if strings.HasSuffix(name, ".js") {
		ct = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Write(b)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	var raw Raw
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&raw); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !envIDRe.MatchString(raw.Env) {
		http.Error(w, "缺少或错误的 env 参数", http.StatusBadRequest)
		return
	}
	e, err := s.Store.Load(raw.Env)
	if err != nil {
		http.Error(w, "环境不存在", http.StatusNotFound)
		return
	}
	raw.Headers = &Headers{UA: r.UserAgent(), AcceptLang: r.Header.Get("Accept-Language"),
		SecCHUA: r.Header.Get("Sec-Ch-Ua"), SecCHUAPF: r.Header.Get("Sec-Ch-Ua-Platform")}
	tplChip := ""
	if t, ok := s.Store.TemplateByName(e.Fingerprint.Template); ok {
		tplChip = t.Chip
	}
	rep := Evaluate(e, raw, s.LocalIP, Chips{Host: config.HostChip(), Template: tplChip})
	if f, err := Save(e, rep); err == nil {
		rep.File = f
	}
	s.notify(raw.Env, rep)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rep)
}

// Wait 等待某环境的下一份报告。
func (s *Server) Wait(envID string, timeout time.Duration) (*Report, error) {
	ch := make(chan *Report, 1)
	s.mu.Lock()
	s.waiters[envID] = append(s.waiters[envID], ch)
	s.mu.Unlock()
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s 等待检测报告超时（%s）", envID, timeout)
	}
}

func (s *Server) notify(envID string, r *Report) {
	s.mu.Lock()
	ws := s.waiters[envID]
	delete(s.waiters, envID)
	s.mu.Unlock()
	for _, ch := range ws {
		ch <- r
	}
}

// URL 环境浏览器打开的检测页地址。
func (s *Server) URL(envID string) string { return "http://" + s.Addr + "/?env=" + envID }

// Save 把报告写到 <环境目录>/reports/fp-时间.json，返回文件名。
func Save(e *config.Env, r *Report) (string, error) {
	dir := filepath.Join(e.Dir(), "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "fp-" + r.Time.Format("20060102-150405.000") + ".json"
	b, _ := json.MarshalIndent(r, "", "  ")
	return name, os.WriteFile(filepath.Join(dir, name), b, 0o600)
}

// LoadReports 读取某环境全部检测报告（按时间先后）。
func LoadReports(e *config.Env) ([]*Report, error) {
	files, err := filepath.Glob(filepath.Join(e.Dir(), "reports", "fp-*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Report
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var r Report
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		r.File = filepath.Base(f)
		out = append(out, &r)
	}
	return out, nil
}

var ErrNoReport = errors.New("没有检测报告")
