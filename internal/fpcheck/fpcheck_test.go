package fpcheck

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"envbrowser/internal/config"
)

func testEnv(t *testing.T) (*config.Store, *config.Env) {
	t.Helper()
	st, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	up := &config.Upstream{Type: "socks5h", Host: "192.0.2.10", Port: 1080, Username: "proxy-user", Password: "proxy-pass"}
	e, err := st.Create(config.NewEnvInput{Primary: up, Timezone: "Asia/Tokyo"}, config.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	return st, e
}

func goodPlace(e *config.Env, where string) Place {
	fp := e.Fingerprint
	return Place{Where: where, UA: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/148.0.7778.215 Safari/537.36",
		Platform: "MacIntel", Cores: fp.HardwareConcurrency, Mem: 16, Lang: fp.Languages[0], Langs: strings.Join(fp.Languages, ","),
		TZ: fp.Timezone, Brands: "Google Chrome/148,Chromium/148", UAPlatform: "macOS", PlatformVersion: fp.PlatformVersion,
		Arch: "arm", Canvas: "0123456789abcdef", GLRenderer: "ANGLE (Apple, ANGLE Metal Renderer: Apple M2)", GLImage: "fedcba9876543210"}
}

func goodRaw(e *config.Env) Raw {
	var ps []Place
	for _, w := range []string{"主页面", "iframe", "Worker", "Service Worker"} {
		ps = append(ps, goodPlace(e, w))
	}
	w := e.Fingerprint.Seed.Window
	return Raw{Env: e.ID, Places: ps, Extra: Extra{Screen: "1470x956x30x2", Window: itoa(w.Width) + "x" + itoa(w.Height),
		Audio: "aaaaaaaaaaaaaaaa", Fonts: "Arial,Menlo", Rects: "1|2", RTCIPs: "abcd-1234.local",
		Ports: []PortResult{{22, "拒绝", 3}, {8800, "拒绝", 2}}}}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestEvaluateAllPass(t *testing.T) {
	_, e := testEnv(t)
	rep := Evaluate(e, goodRaw(e), net.ParseIP("198.51.100.9"), "Apple M2")
	for _, r := range rep.Rows {
		if !r.OK {
			t.Errorf("不应失败: %+v", r)
		}
	}
	if rep.Failed != 0 {
		t.Fatal(rep.Summary)
	}
}

func TestEvaluateCatchesMismatch(t *testing.T) {
	_, e := testEnv(t)
	raw := goodRaw(e)
	raw.Places[2].TZ = "Asia/Shanghai"         // Worker 时区不一致
	raw.Places[0].Cores = 4                    // 核数不符
	raw.Extra.RTCIPs = "192.168.1.5,abc.local" // 内网 IP 泄露
	raw.Extra.Ports[1].Result = "可连"           // 端口表现不一致
	rep := Evaluate(e, raw, nil, "Apple M2")
	bad := map[string]bool{}
	for _, r := range rep.Rows {
		if !r.OK {
			bad[r.Item] = true
		}
	}
	for _, want := range []string{"多位置一致：时区", "CPU 核数", "多位置一致：CPU 核数", "WebRTC 候选地址", "本机端口扫描"} {
		if !bad[want] {
			t.Errorf("应判为不一致: %s（实际失败项 %v）", want, bad)
		}
	}
}

func TestRTCLeaks(t *testing.T) {
	local := net.ParseIP("198.51.100.9")
	if l := rtcLeaks("x.local,203.0.113.5", local); len(l) != 0 {
		t.Fatal(l)
	}
	if l := rtcLeaks("198.51.100.9", local); len(l) != 1 {
		t.Fatal("真实公网 IP 应算泄露")
	}
	if l := rtcLeaks("10.0.0.2", nil); len(l) != 1 {
		t.Fatal("内网 IP 应算泄露")
	}
}

func TestCompare(t *testing.T) {
	_, e := testEnv(t)
	a1 := &Report{Raw: goodRaw(e)}
	a2 := &Report{Raw: goodRaw(e)}
	b := &Report{Raw: goodRaw(e)}
	b.Raw.Places[0].Canvas = "1111111111111111"
	b.Raw.Extra.Audio = "bbbbbbbbbbbbbbbb"
	for _, r := range Compare(map[string][]*Report{"env-0001": {a1, a2}, "env-0002": {b}}) {
		if !r.OK {
			t.Errorf("不应失败: %+v", r)
		}
	}
	a2.Raw.Extra.Audio = "cccccccccccccccc" // 重启后变了
	b.Raw.Places[0].Canvas = a1.Raw.Places[0].Canvas
	fails := 0
	for _, r := range Compare(map[string][]*Report{"env-0001": {a1, a2}, "env-0002": {b}}) {
		if !r.OK {
			fails++
		}
	}
	if fails != 2 {
		t.Fatalf("应有 2 项失败（重启不一致、Canvas 相同），实际 %d", fails)
	}
}

func TestServerGuardAndReport(t *testing.T) {
	st, e := testEnv(t)
	s := New(st, "127.0.0.1:9000", nil)
	h := s.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "evil.test:9000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("错误 Host 应 403，实际 %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "127.0.0.1:9000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "指纹检测") {
		t.Fatalf("首页: %d", rec.Code)
	}

	body, _ := json.Marshal(goodRaw(e))
	post := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/report", bytes.NewReader(body))
		req.Host = "127.0.0.1:9000"
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("http://evil.test"); rec.Code != http.StatusForbidden {
		t.Fatalf("错误 Origin 应 403，实际 %d", rec.Code)
	}
	got := make(chan *Report, 1)
	go func() { r, _ := s.Wait(e.ID, 5*time.Second); got <- r }()
	time.Sleep(50 * time.Millisecond)
	if rec := post("http://127.0.0.1:9000"); rec.Code != 200 {
		t.Fatalf("提交报告: %d %s", rec.Code, rec.Body.String())
	}
	if r := <-got; r == nil || r.File == "" {
		t.Fatal("Wait 应收到已保存的报告")
	}
	rs, err := LoadReports(e)
	if err != nil || len(rs) != 1 {
		t.Fatalf("reports/ 下应有 1 份报告: %v %d", err, len(rs))
	}
}
