// Package admin 管理页面（需求规格第 11 节）：只监听本机，带访问令牌、Host 与 Origin 校验。
// 页面是一个内嵌的 HTML 文件（只用浏览器自带的 JS，不引入外部库），数据全部走 /api。
package admin

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"envbrowser/internal/config"
	"envbrowser/internal/runner"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/app.js
var appJS []byte

//go:embed web/form.js
var formJS []byte

// script 页面脚本：app.js（公共与列表、模板、设置）+ form.js（新建编辑、详情）。
var script = append(append(append([]byte{}, appJS...), '\n'), formJS...)

type Server struct {
	Store *config.Store
	Local net.IP

	token    string
	mu       sync.Mutex
	settings config.Settings
	sess     map[string]*runner.Session
}

func New(store *config.Store, st config.Settings, local net.IP) *Server {
	b := make([]byte, 24)
	rand.Read(b)
	return &Server{Store: store, settings: st, Local: local, token: hex.EncodeToString(b), sess: map[string]*runner.Session{}}
}

// Settings 返回当前设置的副本。
func (s *Server) Settings() config.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// URL 带令牌的首次访问地址。
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?token=%s", s.Settings().AdminAddr, s.token)
}

const cookieName = "envbrowser_token"

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /app.js", s.serveScript)
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/envs", s.listEnvs)
	mux.HandleFunc("POST /api/envs", s.createEnv)
	mux.HandleFunc("GET /api/envs/{id}", s.getEnv)
	mux.HandleFunc("POST /api/envs/{id}", s.updateEnv)
	mux.HandleFunc("GET /api/envs/{id}/log", s.envLog)
	mux.HandleFunc("POST /api/envs/{id}/open", s.open)
	mux.HandleFunc("POST /api/envs/{id}/close", s.close)
	mux.HandleFunc("POST /api/envs/{id}/check", s.checkEnv)
	mux.HandleFunc("POST /api/envs/{id}/regen-seed", s.regenSeed)
	mux.HandleFunc("POST /api/envs/{id}/trash", s.trash)
	mux.HandleFunc("POST /api/proxy/check", s.checkProxy)
	mux.HandleFunc("POST /api/proxy/autozone", s.autoZone)
	mux.HandleFunc("GET /api/templates", s.listTemplates)
	mux.HandleFunc("POST /api/templates", s.addTemplate)
	mux.HandleFunc("POST /api/templates/enable", s.enableTemplate)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("POST /api/settings", s.saveSettings)
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.Settings().AdminAddr)
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
		// 脚本只允许本站的 app.js，页面里不写内联脚本和 onclick
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Write(script)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// session 返回运行中的会话；已关闭的会顺带清掉。
func (s *Server) session(id string) *runner.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sess[id]
	if ss != nil && ss.Status0() == "closed" {
		delete(s.sess, id)
		return nil
	}
	return ss
}

func (s *Server) running(id string) bool { return s.session(id) != nil }

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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("请求格式错误: %w", err)
	}
	return nil
}
