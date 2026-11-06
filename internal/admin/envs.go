package admin

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"envbrowser/internal/config"
	"envbrowser/internal/runner"
)

// envRow 列表页的一行。代理只给类型和地址，不含用户名密码。
type envRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Account     string `json:"account"`
	Note        string `json:"note"`
	ProxyType   string `json:"proxy_type"`
	ProxyAddr   string `json:"proxy_addr"`
	ExitIP      string `json:"exit_ip"`
	ExitCountry string `json:"exit_country"`
	Timezone    string `json:"timezone"`
	Template    string `json:"template"`
	Status      string `json:"status"` // closed | starting | running | blocked
	LastIP      string `json:"last_ip"`
	LastErr     string `json:"last_err"`
	Port        int    `json:"port,omitempty"`
}

func (s *Server) row(e *config.Env) envRow {
	v := envRow{ID: e.ID, Name: e.Name, Account: e.Account.Username, Note: e.Account.Note,
		ExitIP: e.Proxy.ExitIP, ExitCountry: e.Proxy.ExitCountry, Timezone: e.Fingerprint.Timezone,
		Template: e.Fingerprint.Template, Status: "closed"}
	if up, err := e.ActiveUpstream(); err == nil {
		v.ProxyType, v.ProxyAddr = up.Type, up.Addr()
	}
	if ss := s.session(e.ID); ss != nil {
		v.Status, v.LastIP, v.LastErr = ss.Status()
		if ss.Relay != nil {
			v.Port = ss.Relay.Port()
		}
		return v
	}
	s.mu.Lock()
	if _, ok := s.starting[e.ID]; ok {
		v.Status = "starting"
	} else if msg := s.openErr[e.ID]; msg != "" {
		v.LastErr = "上次打开失败：" + msg
	}
	s.mu.Unlock()
	return v
}

func (s *Server) listEnvs(w http.ResponseWriter, r *http.Request) {
	envs, err := s.Store.List()
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := make([]envRow, 0, len(envs))
	for _, e := range envs {
		out = append(out, s.row(e))
	}
	writeJSON(w, out)
}

// getEnv 编辑页与详情页用：返回完整配置（含密码，页面上默认隐藏）。
func (s *Server) getEnv(w http.ResponseWriter, r *http.Request) {
	e, err := s.Store.Load(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	writeJSON(w, map[string]any{"env": e, "row": s.row(e)})
}

// envInput 新建与编辑共用的提交格式。
type envInput struct {
	Name        string           `json:"name"`
	Account     config.Account   `json:"account"`
	Primary     *config.Upstream `json:"primary"`
	Secondary   *config.Upstream `json:"secondary"` // nil 或 Type 为空 = 无备用代理
	Active      string           `json:"active"`
	ExitIP      string           `json:"exit_ip"`
	ExitCountry string           `json:"exit_country"`
	Timezone    string           `json:"timezone"`
	Language    string           `json:"language"`
	Modules     map[string]bool  `json:"modules"`
	CopyFrom    string           `json:"copy_from"` // 复制环境：沿用原环境的窗口大小
}

func (s *Server) createEnv(w http.ResponseWriter, r *http.Request) {
	var in envInput
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Primary == nil {
		fail(w, 400, errors.New("必须填写主代理"))
		return
	}
	ni := config.NewEnvInput{Name: in.Name, Account: in.Account, Primary: in.Primary,
		ExitIP: strings.TrimSpace(in.ExitIP), ExitCountry: in.ExitCountry,
		Timezone: in.Timezone, Language: strings.TrimSpace(in.Language), Modules: in.Modules}
	if in.Secondary != nil && in.Secondary.Type != "" {
		ni.Secondary = in.Secondary
	}
	if in.CopyFrom != "" {
		src, err := s.Store.Load(in.CopyFrom)
		if err != nil {
			fail(w, 400, err)
			return
		}
		ni.Window = src.Fingerprint.Seed.Window // 种子仍由 Create 重新生成
	}
	e, err := s.Store.Create(ni, s.Settings())
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, s.row(e))
}

// updateEnv 编辑。运行中只允许改名称和账号；代理与指纹字段返回 409。
func (s *Server) updateEnv(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cur, err := s.Store.Load(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	var in envInput
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	ed := config.EnvEdit{Name: &in.Name, Account: &in.Account}
	// 只把真正变化的锁定字段放进去，这样运行中仅改名称/账号也能保存。
	if in.Primary != nil && !sameUp(in.Primary, cur.Proxy.Primary) {
		ed.Primary = in.Primary
	}
	sec := in.Secondary
	if sec == nil {
		sec = &config.Upstream{}
	}
	if !sameUp(sec, cur.Proxy.Secondary) {
		ed.Secondary = sec
	}
	if in.Active != "" && in.Active != activeOf(cur) {
		ed.Active = &in.Active
	}
	if x := strings.TrimSpace(in.ExitIP); x != "" && x != cur.Proxy.ExitIP {
		ed.ExitIP = &x
	}
	if in.Timezone != "" && in.Timezone != cur.Fingerprint.Timezone {
		ed.Timezone = &in.Timezone
	}
	if l := strings.TrimSpace(in.Language); l != "" && l != firstLang(cur) {
		ed.Language = &l
	}
	if in.Modules != nil && !sameModules(in.Modules, cur.Modules) {
		ed.Modules = in.Modules
	}
	if ed.TouchesLocked() && s.running(id) {
		fail(w, 409, errors.New("环境运行中，代理和指纹不能修改；请先关闭环境"))
		return
	}
	e, err := s.Store.Update(id, ed)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, s.row(e))
}

func sameUp(a, b *config.Upstream) bool {
	empty := func(u *config.Upstream) bool { return u == nil || u.Type == "" }
	if empty(a) || empty(b) {
		return empty(a) && empty(b)
	}
	return *a == *b
}

func activeOf(e *config.Env) string {
	if e.Proxy.Active == "" {
		return "primary"
	}
	return e.Proxy.Active
}

func firstLang(e *config.Env) string {
	if len(e.Fingerprint.Languages) == 0 {
		return ""
	}
	return e.Fingerprint.Languages[0]
}

func sameModules(in, cur map[string]bool) bool {
	for k, v := range config.DefaultModules() {
		a, ok := in[k]
		if !ok {
			a = v
		}
		b, ok := cur[k]
		if !ok {
			b = v
		}
		if a != b {
			return false
		}
	}
	return true
}

func (s *Server) regenSeed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.running(id) {
		fail(w, 409, errors.New("环境运行中，不能重新生成种子；请先关闭环境"))
		return
	}
	e, err := s.Store.RegenSeed(id)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"seed": e.Fingerprint.Seed.Fingerprint})
}

func (s *Server) trash(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.running(id) {
		fail(w, 409, errors.New("环境运行中，不能删除；请先关闭环境"))
		return
	}
	dst, err := s.Store.Trash(id)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, map[string]string{"moved_to": dst})
}

// open 先登记为“启动中”并立即返回 202，出口校验和启动浏览器在后台进行（最长约 60 秒）。
// 页面每 3 秒刷新列表即可看到结果；失败原因显示在状态下方，环境回到“未打开”。
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := s.Store.Load(id)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if s.session(id) != nil {
		fail(w, 409, errors.New("环境已在运行"))
		return
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		fail(w, 503, errors.New("主程序正在退出"))
		return
	}
	if _, ok := s.starting[id]; ok {
		s.mu.Unlock()
		fail(w, 409, errors.New("环境正在启动"))
		return
	}
	s.starting[id] = false
	delete(s.openErr, id)
	st := s.settings
	s.mu.Unlock()

	go func() {
		ss, err := openEnv(e, st, s.Local, s.Store.Save)
		s.mu.Lock()
		cancelled := s.starting[id] || s.closing
		delete(s.starting, id)
		if err != nil {
			s.openErr[id] = err.Error()
		} else if !cancelled {
			s.sess[id] = ss
		}
		s.mu.Unlock()
		if err == nil && cancelled {
			ss.Close()
		}
	}()
	w.WriteHeader(202)
}

// openEnv 实际打开环境；测试里替换成假的。
var openEnv = runner.Open

func (s *Server) close(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if ss := s.session(id); ss != nil {
		ss.Close()
	}
	s.mu.Lock()
	if _, ok := s.starting[id]; ok {
		s.starting[id] = true // 启动完成后立即关闭
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}

// envLog 返回 run.log 末尾 64KB。
func (s *Server) envLog(w http.ResponseWriter, r *http.Request) {
	e, err := s.Store.Load(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	f, err := os.Open(filepath.Join(e.LogDir(), "run.log"))
	if err != nil {
		return // 还没有日志
	}
	defer f.Close()
	const max = 64 << 10
	if st, err := f.Stat(); err == nil && st.Size() > max {
		f.Seek(st.Size()-max, io.SeekStart)
	}
	io.Copy(w, f)
}
