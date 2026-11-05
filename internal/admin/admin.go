// Package admin 是最小管理接口（阶段 1）：只监听本机，带访问令牌、Host 与 Origin 校验。
// 完整的管理页面在阶段 4 实现。
package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sync"

	"envbrowser/internal/config"
	"envbrowser/internal/runner"
)

type Server struct {
	Store    *config.Store
	Settings config.Settings
	Local    net.IP

	token string
	mu    sync.Mutex
	sess  map[string]*runner.Session
}

func New(store *config.Store, st config.Settings, local net.IP) *Server {
	b := make([]byte, 24)
	rand.Read(b)
	return &Server{Store: store, Settings: st, Local: local, token: hex.EncodeToString(b), sess: map[string]*runner.Session{}}
}

// URL 带令牌的首次访问地址。
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?token=%s", s.Settings.AdminAddr, s.token)
}

const cookieName = "envbrowser_token"

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/envs", s.listEnvs)
	mux.HandleFunc("POST /api/envs/{id}/open", s.open)
	mux.HandleFunc("POST /api/envs/{id}/close", s.close)
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.Settings.AdminAddr)
	allowedHost := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 防 DNS 重绑定
		if !allowedHost[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		tok := r.URL.Query().Get("token")
		if tok == "" {
			if c, err := r.Cookie(cookieName); err == nil {
				tok = c.Value
			}
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			o, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || !allowedHost[o.Host] || o.Scheme != "http" {
				http.Error(w, "bad origin", http.StatusForbidden)
				return
			}
		}
		if r.URL.Query().Get("token") != "" {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther) // 地址栏里不留令牌
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

type envView struct {
	ID, Name, Account, Proxy, ExitIP, Timezone string
	Status, LastIP, LastErr                    string
	Port                                       int
}

func (s *Server) views() ([]envView, error) {
	envs, err := s.Store.List()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []envView
	for _, e := range envs {
		up, _ := e.ActiveUpstream()
		v := envView{ID: e.ID, Name: e.Name, Account: e.Account.Username, ExitIP: e.Proxy.ExitIP, Timezone: e.Fingerprint.Timezone, Status: "closed"}
		if up != nil {
			v.Proxy = up.Redacted()
		}
		if ss := s.sess[e.ID]; ss != nil {
			v.Status, v.LastIP, v.LastErr = ss.Status()
			if v.Status == "closed" {
				delete(s.sess, e.ID)
			} else {
				v.Port = ss.Relay.Port()
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) listEnvs(w http.ResponseWriter, r *http.Request) {
	v, err := s.views()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := s.Store.Load(id)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.mu.Lock()
	if ss := s.sess[id]; ss != nil && ss.Status0() != "closed" {
		s.mu.Unlock()
		http.Error(w, "环境已在运行", 409)
		return
	}
	s.mu.Unlock()
	ss, err := runner.Open(e, s.Settings, s.Local)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	s.mu.Lock()
	s.sess[id] = ss
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *Server) close(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss := s.sess[r.PathValue("id")]
	s.mu.Unlock()
	if ss != nil {
		ss.Close()
	}
	w.WriteHeader(204)
}

// CloseAll 主程序退出时关闭所有环境。
func (s *Server) CloseAll() {
	s.mu.Lock()
	list := make([]*runner.Session, 0, len(s.sess))
	for _, ss := range s.sess {
		list = append(list, ss)
	}
	s.mu.Unlock()
	for _, ss := range list {
		ss.Close()
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	v, err := s.views()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTpl.Execute(w, v)
}

var indexTpl = template.Must(template.New("i").Parse(`<!doctype html><meta charset="utf-8"><title>envbrowser</title>
<style>body{font:14px -apple-system,sans-serif;margin:24px;color:#222}table{border-collapse:collapse}td,th{border:1px solid #ddd;padding:6px 10px;text-align:left}
.running{color:#0a7}.blocked{color:#c22}.err{color:#c22;font-size:12px}button{padding:3px 10px}</style>
<h2>环境列表 <small style="font-weight:normal;color:#888">阶段 1 最小管理页 · 每 3 秒刷新</small></h2>
<table><tr><th>ID</th><th>名称</th><th>账号</th><th>代理</th><th>录入出口</th><th>时区</th><th>状态</th><th>最近出口</th><th>中转端口</th><th></th></tr>
{{range .}}<tr><td>{{.ID}}</td><td>{{.Name}}</td><td>{{.Account}}</td><td>{{.Proxy}}</td><td>{{.ExitIP}}</td><td>{{.Timezone}}</td>
<td class="{{.Status}}">{{.Status}}{{if .LastErr}}<div class="err">{{.LastErr}}</div>{{end}}</td><td>{{.LastIP}}</td><td>{{if .Port}}{{.Port}}{{end}}</td>
<td>{{if eq .Status "closed"}}<button onclick="act('{{.ID}}','open')">打开</button>{{else}}<button onclick="act('{{.ID}}','close')">关闭</button>{{end}}</td></tr>
{{else}}<tr><td colspan="10">还没有环境，用命令行 envbrowser create 新建</td></tr>{{end}}</table>
<script>
async function act(id,a){const r=await fetch('/api/envs/'+id+'/'+a,{method:'POST'});if(!r.ok)alert(await r.text());location.reload()}
setInterval(()=>location.reload(),3000)
</script>`))
