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
		Platform: "MacIntel", Cores: fp.HardwareConcurrency, Mem: 8.0, Lang: fp.Languages[0], Langs: strings.Join(fp.Languages, ","),
		IntlDate: fp.Languages[0], IntlNumber: fp.Languages[0], IntlCollator: fp.Languages[0],
		TZNameDefault: "日本標準時", TZNameLang: "日本標準時", DateString: "日本標準時",
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
		Ports: []PortResult{{22, "拒绝", 3}, {8800, "拒绝", 2}}},
		Headers: &Headers{UA: ps[0].UA, AcceptLang: acceptLang(e.Fingerprint.Languages),
			SecCHUA: `"Google Chrome";v="148", "Chromium";v="148", "Not/A)Brand";v="24"`, SecCHUAPF: `"macOS"`}}
}

func acceptLang(ls []string) string {
	out := ls[0]
	for i, l := range ls[1:] {
		out += "," + l + ";q=0." + itoa(9-i)
	}
	return out
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestEvaluateAllPass(t *testing.T) {
	_, e := testEnv(t)
	rep := Evaluate(e, goodRaw(e), net.ParseIP("198.51.100.9"), Chips{Host: "Apple M2", Template: "Apple M2"})
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
	rep := Evaluate(e, raw, nil, Chips{Host: "Apple M2", Template: "Apple M2"})
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
	// 跨环境音频相同：只提示，不算失败
	a2.Raw.Extra.Audio = a1.Raw.Extra.Audio
	b.Raw.Places[0].Canvas = "1111111111111111"
	b.Raw.Extra.Audio = a1.Raw.Extra.Audio
	warned := false
	for _, r := range Compare(map[string][]*Report{"env-0001": {a1, a2}, "env-0002": {b}}) {
		if !r.OK {
			t.Errorf("音频相同不应判失败: %+v", r)
		}
		if strings.Contains(r.Item, "音频") && r.Warn {
			warned = true
		}
	}
	if !warned {
		t.Fatal("音频相同应给出提示")
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

// 新增的组合矛盾检测：Intl 区域、时区显示名、显卡与模板芯片、核数与芯片、窗口与屏幕、内存、请求头。
func TestEvaluateCatchesCombination(t *testing.T) {
	_, e := testEnv(t)
	raw := goodRaw(e)
	for i := range raw.Places {
		raw.Places[i].IntlDate = "zh-CN"       // Intl 区域跟着系统走
		raw.Places[i].TZNameDefault = "日本标准时间" // 系统语言写出的时区名
		raw.Places[i].DateString = "日本标准时间"
		raw.Places[i].GLRenderer = "ANGLE (Apple, ANGLE Metal Renderer: Apple M4 Pro, Unspecified Version)"
		raw.Places[i].Mem = 12.0
	}
	raw.Extra.Window = "3000x2000"
	raw.Headers.AcceptLang = "zh-CN,zh;q=0.9"
	raw.Headers.SecCHUAPF = `"Windows"`
	// 本机就是 M4 Pro、显卡不伪装：「显卡（不伪装）」应通过，但与模板 M2 对不上
	rep := Evaluate(e, raw, nil, Chips{Host: "Apple M4 Pro", Template: "Apple M2"})
	bad := map[string]bool{}
	for _, r := range rep.Rows {
		if !r.OK {
			bad[r.Item] = true
		}
	}
	for _, want := range []string{"Intl 默认区域", "时区显示名与语言一致", "显卡与模板芯片一致", "核数与显卡芯片吻合",
		"窗口不大于屏幕", "内存 deviceMemory 取值", "请求头 Accept-Language", "请求头 sec-ch-ua-platform"} {
		if !bad[want] {
			t.Errorf("应判为不一致: %s（实际失败项 %v）", want, bad)
		}
	}
	if bad["显卡（不伪装，显示真机）"] {
		t.Error("显卡与本机一致，不应判失败")
	}
}

func TestHelpers(t *testing.T) {
	for _, c := range []struct {
		got, want string
		ok        bool
	}{{"ja-JP", "ja-JP", true}, {"ja", "ja-JP", true}, {"zh-CN", "ja-JP", false}, {"en-GB", "en-US", false}, {"", "en-US", false}} {
		if sameLocale(c.got, c.want) != c.ok {
			t.Errorf("sameLocale(%q,%q) 应为 %v", c.got, c.want, c.ok)
		}
	}
	if chipOf("ANGLE (Apple, ANGLE Metal Renderer: Apple M2 Pro, Unspecified Version)") != "Apple M2 Pro" {
		t.Error("chipOf 应取出 Apple M2 Pro")
	}
	if chipIn("ANGLE (Apple, ANGLE Metal Renderer: Apple M2 Pro)", "Apple M2") {
		t.Error("Apple M2 不应匹配 Apple M2 Pro")
	}
	if acceptLangTags("ja-JP,ja;q=0.9") != "ja-JP,ja" {
		t.Error("acceptLangTags")
	}
	if !validMem(8.0) || !validMem(16.0) || validMem(12.0) || validMem(nil) {
		t.Error("validMem")
	}
}
