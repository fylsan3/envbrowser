package admin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/egress"
)

// checkResult “测试代理”/“检测”按钮的结果，每一项单独给出，失败不影响后面的项。
type checkResult struct {
	ExitIP      string        `json:"exit_ip"`
	ExitOK      bool          `json:"exit_ok"`
	ExitErr     string        `json:"exit_err,omitempty"`
	Country     string        `json:"country"`
	CountryErr  string        `json:"country_err,omitempty"`
	Zones       []config.Zone `json:"zones"`
	HeaderOK    bool          `json:"header_ok"`
	HeaderLeaks []string      `json:"header_leaks,omitempty"`
	HeaderErr   string        `json:"header_err,omitempty"`
}

func (s *Server) runCheck(ctx context.Context, up *config.Upstream, exit string) checkResult {
	st := s.Settings()
	var cr checkResult
	if exit == "" {
		exit = up.Host
	}
	res, err := egress.Check(ctx, up, exit, s.Local, st.EchoServices, 0)
	if res.ExitIP != nil {
		cr.ExitIP = res.ExitIP.String()
	}
	if err != nil {
		cr.ExitErr = err.Error()
	} else {
		cr.ExitOK = true
	}
	if cc, err := egress.Country(ctx, up); err != nil {
		cr.CountryErr = err.Error()
	} else {
		cr.Country, cr.Zones = cc, config.ZonesForCountry(cc)
	}
	body, err := egress.HeaderCheck(ctx, up, st.HeaderEchoURL)
	switch {
	case err != nil:
		cr.HeaderErr = err.Error()
	default:
		cr.HeaderLeaks = egress.LeakHeaders(body)
		cr.HeaderOK = len(cr.HeaderLeaks) == 0
	}
	return cr
}

type proxyReq struct {
	Proxy  *config.Upstream `json:"proxy"`
	ExitIP string           `json:"exit_ip"`
}

// checkProxy 新建/编辑页的“测试代理”：用表单里还没保存的代理。
func (s *Server) checkProxy(w http.ResponseWriter, r *http.Request) {
	var in proxyReq
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Proxy == nil {
		fail(w, 400, errors.New("没有填写代理"))
		return
	}
	if err := in.Proxy.Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	writeJSON(w, s.runCheck(ctx, in.Proxy, strings.TrimSpace(in.ExitIP)))
}

// checkEnv 列表页的“检测”：用已保存的当前生效代理。不影响运行中的环境。
func (s *Server) checkEnv(w http.ResponseWriter, r *http.Request) {
	e, err := s.Store.Load(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	up, err := e.ActiveUpstream()
	if err != nil {
		fail(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	writeJSON(w, s.runCheck(ctx, up, e.Proxy.ExitIP))
}

// autoZone “按出口国家自动匹配”：只取国家和候选时区，由页面决定填哪个。
func (s *Server) autoZone(w http.ResponseWriter, r *http.Request) {
	var in proxyReq
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Proxy == nil {
		fail(w, 400, errors.New("没有填写代理"))
		return
	}
	if err := in.Proxy.Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cc, err := egress.Country(ctx, in.Proxy)
	if err != nil {
		fail(w, 502, err)
		return
	}
	writeJSON(w, map[string]any{"country": cc, "zones": config.ZonesForCountry(cc)})
}

// meta 页面需要的固定数据：时区列表、积木默认值、测试模式状态等。
func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	st := s.Settings()
	writeJSON(w, map[string]any{
		"zones":       config.Zones,
		"modules":     config.DefaultModules(),
		"test_mode":   st.TestMode,
		"test_target": st.TestTarget,
		"host_chip":   config.HostChip(),
	})
}

type templateRow struct {
	config.Template
	Used int `json:"used"`
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.Templates()
	if err != nil {
		fail(w, 500, err)
		return
	}
	used := s.Store.TemplateUsage()
	out := make([]templateRow, 0, len(ts))
	for _, t := range ts {
		out = append(out, templateRow{t, used[t.Name]})
	}
	writeJSON(w, out)
}

func (s *Server) addTemplate(w http.ResponseWriter, r *http.Request) {
	var t config.Template
	if err := readJSON(r, &t); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.Store.AddTemplate(t); err != nil {
		fail(w, 400, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) enableTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.Store.SetTemplateEnabled(in.Name, in.Enabled); err != nil {
		fail(w, 400, err)
		return
	}
	w.WriteHeader(204)
}

// settingsView 设置页可见可改的字段。管理页面地址改了要重启才生效，所以只读。
type settingsView struct {
	Root         string `json:"root"`
	AdminAddr    string `json:"admin_addr"`
	ChromiumPath string `json:"chromium_path"`
	ChromiumVer  string `json:"chromium_version"`
	PortMin      int    `json:"port_min"`
	PortMax      int    `json:"port_max"`
	MemoryWarnN  int    `json:"memory_warn_n"`
	TestMode     bool   `json:"test_mode"`
	TestTarget   string `json:"test_target"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st := s.Settings()
	writeJSON(w, settingsView{Root: s.Store.Root, AdminAddr: st.AdminAddr, ChromiumPath: st.ChromiumPath,
		ChromiumVer: st.ChromiumVer, PortMin: st.PortMin, PortMax: st.PortMax, MemoryWarnN: st.MemoryWarnN,
		TestMode: st.TestMode, TestTarget: st.TestTarget})
}

// saveSettings 只改设置页上的字段，其余（出口回显服务等）原样保留。新设置对之后打开的环境生效。
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsView
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.PortMin < 1024 || in.PortMax > 65535 || in.PortMin > in.PortMax {
		fail(w, 400, errors.New("端口范围无效（1024–65535，起始不大于结束）"))
		return
	}
	if in.MemoryWarnN < 1 {
		fail(w, 400, errors.New("内存提示阈值至少为 1"))
		return
	}
	in.TestTarget = strings.TrimSpace(in.TestTarget)
	if in.TestTarget != "" {
		h, p, err := net.SplitHostPort(in.TestTarget)
		_, ap, _ := net.SplitHostPort(s.Settings().AdminAddr)
		if err != nil || h != "127.0.0.1" || p == ap {
			fail(w, 400, errors.New("测试检测页只能是 127.0.0.1:端口，且不能是管理页面端口"))
			return
		}
	}
	s.mu.Lock()
	st := s.settings
	st.ChromiumPath, st.ChromiumVer = strings.TrimSpace(in.ChromiumPath), strings.TrimSpace(in.ChromiumVer)
	st.PortMin, st.PortMax, st.MemoryWarnN = in.PortMin, in.PortMax, in.MemoryWarnN
	st.TestMode, st.TestTarget = in.TestMode, strings.TrimSpace(in.TestTarget)
	err := s.Store.SaveSettings(st)
	if err == nil {
		s.settings = st
	}
	s.mu.Unlock()
	if err != nil {
		fail(w, 500, err)
		return
	}
	w.WriteHeader(204)
}
