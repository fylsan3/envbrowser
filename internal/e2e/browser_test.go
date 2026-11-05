package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/launcher"
)

// chromiumPath 优先用环境变量 ENVBROWSER_CHROMIUM，否则找仓库内 .toolchain 安装位置。
func chromiumPath(t *testing.T) string {
	p := os.Getenv("ENVBROWSER_CHROMIUM")
	if p == "" {
		p, _ = filepath.Abs("../../.toolchain/fpchromium-148/Chromium.app")
	}
	if _, err := os.Stat(launcher.ExecPath(p)); err != nil {
		t.Skipf("未找到 fingerprint-chromium（%s），跳过真浏览器测试；先运行 scripts/install-browser.sh", p)
	}
	return p
}

// cookieSite /set 写入带环境标记的 Cookie；/show 显示请求带来的 Cookie，
// 页面脚本再补上浏览器看到的时区和核数。
func cookieSite() http.Handler {
	mux := http.NewServeMux()
	page := `<html><body><pre id="r">cookie=%s</pre><script>
document.getElementById("r").textContent += "|tz=" + Intl.DateTimeFormat().resolvedOptions().timeZone +
  "|hc=" + navigator.hardwareConcurrency + "|end";
</script></body></html>`
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: r.URL.Query().Get("v"), Path: "/", MaxAge: 3600})
		fmt.Fprintf(w, page, r.Header.Get("Cookie"))
	})
	mux.HandleFunc("/show", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, page, r.Header.Get("Cookie"))
	})
	return mux
}

// newEnv 在临时根目录下建一个环境，代理指向模拟上游。
func newEnv(t *testing.T, store *config.Store, up config.Upstream, tz string) *config.Env {
	e, err := store.Create(config.NewEnvInput{Primary: &up, ExitIP: "198.51.100.7", Timezone: tz}, config.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// visit 用本程序生成的启动参数（外加无界面相关参数）打开一个网址，返回页面结果行。
// 页面输出后先 SIGTERM 让浏览器正常退出（把 Cookie 写盘），5 秒不退再强制结束。
func visit(t *testing.T, chromium string, e *config.Env, relayPort int, u string) string {
	args := launcher.Args(e, relayPort)
	args = args[:len(args)-1] // 去掉末尾的 about:blank
	args = append(args, "--headless=new", "--disable-gpu", "--no-sandbox", "--dump-dom", u)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, launcher.ExecPath(chromium), args...)
	cmd.Env = launcher.Env(e)
	out, _ := cmd.StdoutPipe()
	logf, _ := os.Create(filepath.Join(t.TempDir(), "chromium.log"))
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var result string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "cookie="); i >= 0 {
			result = line[i:]
		}
		if strings.Contains(line, "</html>") {
			break
		}
	}
	go io.Copy(io.Discard, out)
	cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-done
	}
	if result == "" {
		b, _ := os.ReadFile(logf.Name())
		t.Fatalf("页面没有输出结果：%s\n浏览器日志末尾:\n%s", u, tail(string(b), 1500))
	}
	if i := strings.Index(result, "|end"); i >= 0 {
		result = result[:i]
	}
	return strings.TrimSuffix(result, "</pre>")
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// 真浏览器：两个环境各走各的模拟上游。
//   - 页面能经中转打开（域名只由上游解析，本地 DNS 已被启动参数禁止）；
//   - A 写入的 Cookie，A 重开后还在，B 看不到；
//   - 时区和 CPU 核数按环境配置生效；
//   - 网站收到的连接数等于两个上游转发数之和（没有绕过代理的连接）。
func TestBrowserIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 模式跳过真浏览器测试")
	}
	chromium := chromiumPath(t)
	s := newSite(t, cookieSite())
	ma := newMock(t, "socks5h", s.routes(nil))
	mb := newMock(t, "http", s.routes(nil))
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ea := newEnv(t, store, ma.Upstream(), "Asia/Tokyo")
	eb := newEnv(t, store, mb.Upstream(), "America/New_York")
	ra := startRelay(t, ma.Upstream())
	rb := startRelay(t, mb.Upstream())

	got := visit(t, chromium, ea, ra.Port(), "http://site.test/set?v="+ea.ID)
	t.Logf("A 首次: %s", got)
	want := fmt.Sprintf("|tz=Asia/Tokyo|hc=%d", ea.Fingerprint.HardwareConcurrency)
	if !strings.HasPrefix(got, "cookie=|") || !strings.HasSuffix(got, want) {
		t.Fatalf("A 首次访问应无 Cookie 且 %s，实际 %q", want, got)
	}

	got = visit(t, chromium, ea, ra.Port(), "http://site.test/show")
	t.Logf("A 重开: %s", got)
	if !strings.HasPrefix(got, "cookie=sid="+ea.ID+"|") {
		t.Fatalf("A 重开后应保留自己的 Cookie，实际 %q", got)
	}

	got = visit(t, chromium, eb, rb.Port(), "http://site.test/show")
	t.Logf("B: %s", got)
	if !strings.HasPrefix(got, "cookie=|") {
		t.Fatalf("B 不应看到 A 的 Cookie，实际 %q", got)
	}
	if !strings.HasSuffix(got, "|tz=America/New_York|hc="+fmt.Sprint(eb.Fingerprint.HardwareConcurrency)) {
		t.Fatalf("B 的时区/核数未生效: %q", got)
	}

	ta, tb := ma.Targets(), mb.Targets()
	for _, x := range append(ta, tb...) {
		if x != "site.test:80" && x != "site.test:443" {
			t.Errorf("上游收到了意外的目标 %s（浏览器后台请求未被禁止？）", x)
		}
	}
	if n := s.conns.Load(); n > int64(len(ta)+len(tb)) {
		t.Fatalf("网站收到 %d 个连接，两个上游只转发 %d 个：存在绕过代理的连接", n, len(ta)+len(tb))
	}
}
