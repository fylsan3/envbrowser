package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"envbrowser/internal/config"
	"envbrowser/internal/runner"
)

// 测试里的代理一律用文档示例地址和占位用户名，不出现真实代理（铁律）。
const testProxy = `{"type":"socks5h","host":"192.0.2.10","port":1080,"username":"proxy-user","password":"proxy-pass"}`

func newTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	st, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, config.DefaultSettings(), nil)
	return s, s.Handler()
}

// do 发一个已登录、来源正确的请求。
func do(t *testing.T, s *Server, hd http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8800"+path, rd)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: s.token})
	if method != http.MethodGet {
		r.Header.Set("Origin", "http://127.0.0.1:8800")
	}
	w := httptest.NewRecorder()
	hd.ServeHTTP(w, r)
	return w
}

func TestGuard(t *testing.T) {
	s, hd := newTestServer(t)
	cases := []struct {
		name   string
		host   string
		cookie string
		origin string
		method string
		want   int
	}{
		{"无令牌", "127.0.0.1:8800", "", "", "GET", 403},
		{"令牌错", "127.0.0.1:8800", "x", "", "GET", 403},
		{"Host 不对（DNS 重绑定）", "evil.example:8800", s.token, "", "GET", 403},
		{"正常 GET", "127.0.0.1:8800", s.token, "", "GET", 200},
		{"localhost 也可", "localhost:8800", s.token, "", "GET", 200},
		{"POST 无 Origin", "127.0.0.1:8800", s.token, "", "POST", 403},
		{"POST 外站 Origin", "127.0.0.1:8800", s.token, "http://evil.example", "POST", 403},
		{"POST https Origin", "127.0.0.1:8800", s.token, "https://127.0.0.1:8800", "POST", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := "/api/envs"
			r := httptest.NewRequest(c.method, "http://"+c.host+path, strings.NewReader("{}"))
			r.Host = c.host
			if c.cookie != "" {
				r.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
			}
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			hd.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("状态码 %d，期望 %d：%s", w.Code, c.want, w.Body)
			}
		})
	}
}

func TestTokenInURLSetsCookieAndRedirects(t *testing.T) {
	s, hd := newTestServer(t)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8800/?token="+s.token, nil)
	w := httptest.NewRecorder()
	hd.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("应跳转到 /，得到 %d %q", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("Cookie 属性不对: %+v", c)
	}
}

func TestPageAndScript(t *testing.T) {
	s, hd := newTestServer(t)
	w := do(t, s, hd, "GET", "/", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<script src="/app.js">`) {
		t.Fatalf("首页不对: %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("缺少 CSP: %q", csp)
	}
	// 页面不能有内联脚本或内联事件（CSP 禁止，写了也不会执行）
	if strings.Contains(w.Body.String(), "onclick") || strings.Count(w.Body.String(), "<script") != 1 {
		t.Fatal("首页含内联脚本")
	}
	js := do(t, s, hd, "GET", "/app.js", "").Body.String()
	for _, fn := range []string{"routes.list", "routes.detail", "function envForm", "function pwBox"} {
		if !strings.Contains(js, fn) {
			t.Fatalf("脚本缺少 %s", fn)
		}
	}
	// 页面文字一律 textContent，不用 innerHTML（防注入）
	if strings.Contains(js, "innerHTML") {
		t.Fatal("脚本里不应使用 innerHTML")
	}
}

func createEnv(t *testing.T, s *Server, hd http.Handler, extra string) envRow {
	t.Helper()
	body := `{"name":"店铺A","account":{"username":"a@example.com","password":"login-pass","note":"备注"},` +
		`"primary":` + testProxy + `,"exit_ip":"192.0.2.10","timezone":"America/New_York","language":"",` +
		`"modules":{"canvas":false}` + extra + `}`
	w := do(t, s, hd, "POST", "/api/envs", body)
	if w.Code != 200 {
		t.Fatalf("新建失败 %d: %s", w.Code, w.Body)
	}
	var r envRow
	json.Unmarshal(w.Body.Bytes(), &r)
	return r
}

func TestCreateListGet(t *testing.T) {
	s, hd := newTestServer(t)
	r := createEnv(t, s, hd, "")
	if r.ID != "env-0001" || r.Status != "closed" || r.ProxyAddr != "192.0.2.10:1080" {
		t.Fatalf("新建结果不对: %+v", r)
	}
	list := do(t, s, hd, "GET", "/api/envs", "").Body.String()
	// 列表接口不能带代理用户名和密码
	if strings.Contains(list, "proxy-pass") || strings.Contains(list, "proxy-user") || strings.Contains(list, "login-pass") {
		t.Fatalf("列表泄露了密码: %s", list)
	}
	var got struct{ Env config.Env }
	json.Unmarshal(do(t, s, hd, "GET", "/api/envs/env-0001", "").Body.Bytes(), &got)
	e := got.Env
	if e.Fingerprint.Languages[0] != "en-US" || e.Modules["canvas"] || !e.Modules["audio"] {
		t.Fatalf("语言或积木不对: %+v %+v", e.Fingerprint.Languages, e.Modules)
	}
	if e.Proxy.Primary.Password != "proxy-pass" {
		t.Fatal("编辑页需要拿到代理密码")
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	s, hd := newTestServer(t)
	for name, body := range map[string]string{
		"缺主代理":  `{"name":"x"}`,
		"代理缺密码": `{"primary":{"type":"socks5h","host":"192.0.2.10","port":1080,"username":"u"}}`,
		"未知字段":  `{"primary":` + testProxy + `,"evil":1}`,
		"时区无效":  `{"primary":` + testProxy + `,"timezone":"Mars/Base"}`,
	} {
		if w := do(t, s, hd, "POST", "/api/envs", body); w.Code != 400 {
			t.Errorf("%s: 期望 400，得到 %d", name, w.Code)
		}
	}
}

func TestCopyRegeneratesSeed(t *testing.T) {
	s, hd := newTestServer(t)
	createEnv(t, s, hd, "")
	createEnv(t, s, hd, `,"copy_from":"env-0001"`)
	a, _ := s.Store.Load("env-0001")
	b, _ := s.Store.Load("env-0002")
	if a.Fingerprint.Seed.Fingerprint == b.Fingerprint.Seed.Fingerprint {
		t.Fatal("复制后种子必须不同")
	}
	if a.Fingerprint.Seed.Window != b.Fingerprint.Seed.Window {
		t.Fatal("复制应沿用窗口大小")
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "browser")); err == nil {
		t.Fatal("浏览器数据不应复制")
	}
}

// fakeRunning 把环境标成运行中（不真的启动浏览器）。
func fakeRunning(s *Server, id string) {
	e, _ := s.Store.Load(id)
	s.mu.Lock()
	s.sess[id] = runner.Fake(e, "running")
	s.mu.Unlock()
}

func TestRunningLocksProxyAndFingerprint(t *testing.T) {
	s, hd := newTestServer(t)
	createEnv(t, s, hd, "")
	fakeRunning(s, "env-0001")
	base := `{"name":"新名字","account":{"username":"b@example.com","password":"p2","note":""},"primary":` + testProxy +
		`,"active":"primary","exit_ip":"192.0.2.10","timezone":"America/New_York","language":"en-US","modules":{"canvas":false}}`

	// 只改名称和账号：允许
	if w := do(t, s, hd, "POST", "/api/envs/env-0001", base); w.Code != 200 {
		t.Fatalf("运行中改名称应允许，得到 %d: %s", w.Code, w.Body)
	}
	e, _ := s.Store.Load("env-0001")
	if e.Name != "新名字" || e.Account.Username != "b@example.com" {
		t.Fatal("名称或账号没保存")
	}
	// 改时区、积木、代理：拒绝，且不写盘
	for name, body := range map[string]string{
		"时区": strings.Replace(base, "America/New_York", "Asia/Tokyo", 1),
		"积木": strings.Replace(base, `"canvas":false`, `"canvas":true`, 1),
		"代理": strings.Replace(base, "proxy-pass", "other-pass", 1),
		"出口": strings.Replace(base, `"exit_ip":"192.0.2.10"`, `"exit_ip":"192.0.2.11"`, 1),
	} {
		if w := do(t, s, hd, "POST", "/api/envs/env-0001", body); w.Code != 409 {
			t.Errorf("运行中改%s应返回 409，得到 %d", name, w.Code)
		}
	}
	e2, _ := s.Store.Load("env-0001")
	if e2.Fingerprint.Timezone != "America/New_York" || e2.Proxy.Primary.Password != "proxy-pass" {
		t.Fatal("被拒绝的修改不应写盘")
	}
	for _, p := range []string{"/regen-seed", "/trash"} {
		if w := do(t, s, hd, "POST", "/api/envs/env-0001"+p, ""); w.Code != 409 {
			t.Errorf("运行中 %s 应返回 409，得到 %d", p, w.Code)
		}
	}
	if w := do(t, s, hd, "POST", "/api/envs/env-0001/open", ""); w.Code != 409 {
		t.Errorf("重复打开应返回 409，得到 %d", w.Code)
	}
}

func TestEditWhenClosed(t *testing.T) {
	s, hd := newTestServer(t)
	createEnv(t, s, hd, "")
	body := `{"name":"A","account":{"username":"","password":"","note":""},"primary":` + testProxy +
		`,"secondary":{"type":"http","host":"192.0.2.20","port":8080,"username":"u2","password":"p2"},` +
		`"active":"secondary","exit_ip":"192.0.2.20","timezone":"Asia/Tokyo","language":"","modules":{"canvas":true,"gpu":true}}`
	if w := do(t, s, hd, "POST", "/api/envs/env-0001", body); w.Code != 200 {
		t.Fatalf("关闭时编辑应允许: %d %s", w.Code, w.Body)
	}
	e, _ := s.Store.Load("env-0001")
	if e.Proxy.Active != "secondary" || e.Proxy.Secondary.Host != "192.0.2.20" || e.Fingerprint.Timezone != "Asia/Tokyo" ||
		!e.Modules["canvas"] || !e.Modules["gpu"] {
		t.Fatalf("编辑未生效: %+v %+v", e.Proxy, e.Modules)
	}
	// 去掉备用代理（同时切回主代理）
	body2 := strings.Replace(body, `{"type":"http","host":"192.0.2.20","port":8080,"username":"u2","password":"p2"}`, `null`, 1)
	body2 = strings.Replace(body2, `"active":"secondary"`, `"active":"primary"`, 1)
	if w := do(t, s, hd, "POST", "/api/envs/env-0001", body2); w.Code != 200 {
		t.Fatalf("删除备用代理失败: %d %s", w.Code, w.Body)
	}
	if e, _ := s.Store.Load("env-0001"); e.Proxy.Secondary != nil {
		t.Fatal("备用代理没删掉")
	}
}

func TestRegenSeedAndTrash(t *testing.T) {
	s, hd := newTestServer(t)
	createEnv(t, s, hd, "")
	before, _ := s.Store.Load("env-0001")
	if w := do(t, s, hd, "POST", "/api/envs/env-0001/regen-seed", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	after, _ := s.Store.Load("env-0001")
	if before.Fingerprint.Seed.Fingerprint == after.Fingerprint.Seed.Fingerprint {
		t.Fatal("种子没变")
	}
	trash := t.TempDir()
	t.Setenv("ENVBROWSER_TRASH", trash)
	if w := do(t, s, hd, "POST", "/api/envs/env-0001/trash", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if _, err := s.Store.Load("env-0001"); err == nil {
		t.Fatal("环境应已移走")
	}
	if ents, _ := os.ReadDir(trash); len(ents) != 1 {
		t.Fatal("废纸篓里应有 1 个目录")
	}
	if w := do(t, s, hd, "POST", "/api/envs/..%2Fx/trash", ""); w.Code == 200 {
		t.Fatal("非法 ID 不应成功")
	}
}

func TestTemplatesAndSettings(t *testing.T) {
	s, hd := newTestServer(t)
	var ts []templateRow
	json.Unmarshal(do(t, s, hd, "GET", "/api/templates", "").Body.Bytes(), &ts)
	if len(ts) != 4 {
		t.Fatalf("应有 4 套内置模板，得到 %d", len(ts))
	}
	if w := do(t, s, hd, "POST", "/api/templates/enable", `{"name":"`+ts[0].Name+`","enabled":false}`); w.Code != 204 {
		t.Fatal(w.Body)
	}
	if w := do(t, s, hd, "POST", "/api/templates", `{"name":"自定义","platform_version":"15.1.0","hardware_concurrency":8}`); w.Code != 204 {
		t.Fatal(w.Body)
	}
	if w := do(t, s, hd, "POST", "/api/templates", `{"name":"缺核数","platform_version":"15.1.0"}`); w.Code != 400 {
		t.Fatal("缺核数应拒绝")
	}

	set := func(body string) int { return do(t, s, hd, "POST", "/api/settings", body).Code }
	ok := `{"chromium_path":"/x/Chromium.app","chromium_version":"148","port_min":20001,"port_max":20999,"memory_warn_n":10,"test_mode":true,"test_target":"127.0.0.1:9000"}`
	if c := set(ok); c != 204 {
		t.Fatalf("保存设置失败 %d", c)
	}
	if st, _ := s.Store.LoadSettings(); !st.TestMode || st.ChromiumPath != "/x/Chromium.app" || len(st.EchoServices) == 0 {
		t.Fatalf("设置未保存或其他字段被清空: %+v", st)
	}
	if !strings.Contains(do(t, s, hd, "GET", "/api/meta", "").Body.String(), `"test_mode":true`) {
		t.Fatal("页面顶部提醒需要 test_mode")
	}
	for name, body := range map[string]string{
		"检测页指向管理端口": strings.Replace(ok, "127.0.0.1:9000", "127.0.0.1:8800", 1),
		"检测页不是本机":   strings.Replace(ok, "127.0.0.1:9000", "192.0.2.1:9000", 1),
		"端口范围反了":    strings.Replace(ok, `"port_min":20001`, `"port_min":30001`, 1),
	} {
		if c := set(body); c != 400 {
			t.Errorf("%s 应拒绝，得到 %d", name, c)
		}
	}
}

func TestCheckProxyValidates(t *testing.T) {
	s, hd := newTestServer(t)
	if w := do(t, s, hd, "POST", "/api/proxy/check", `{"proxy":{"type":"socks5h","host":"192.0.2.10","port":1080}}`); w.Code != 400 {
		t.Fatalf("缺用户名密码应 400，得到 %d", w.Code)
	}
}
