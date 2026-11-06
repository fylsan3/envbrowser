package e2e

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/launcher"
)

// 阶段 5：证书校验。正式启动参数（不带任何跳过证书的参数）打开一个自签名证书的 https 网站，
// 浏览器必须拦截：页面内容不能出现，网站的处理函数一次都不能被调用（TLS 握手阶段就被拒绝）。
// 对照：同一网站的 http 版本能正常打开，说明链路本身是通的。
func TestCertificateRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 模式跳过真浏览器测试")
	}
	chromium := chromiumPath(t)
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("<html><body>SECRET-PAGE-" + r.URL.Scheme + "</body></html>"))
	})
	s := newSite(t, h)
	m := newMock(t, "socks5h", s.routes(nil))
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, store, m.Upstream(), "Asia/Tokyo")
	r := startRelay(t, m.Upstream())

	dump := func(u string) string {
		args := launcher.Args(e, r.Port())
		args = args[:len(args)-1]
		// 只追加无界面所需参数；不加任何证书相关参数
		args = append(args, "--headless=new", "--disable-gpu", "--no-sandbox", "--dump-dom", u)
		if err := launcher.CheckExtra([]string{"--headless=new", "--no-sandbox"}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, launcher.ExecPath(chromium), args...)
		cmd.Env = launcher.Env(e)
		out, _ := cmd.Output()
		return string(out)
	}

	if got := dump("http://site.test/"); !strings.Contains(got, "SECRET-PAGE") {
		t.Fatalf("对照组 http 应能打开，实际: %.300s", got)
	}
	before := hits.Load()
	got := dump("https://site.test/")
	if strings.Contains(got, "SECRET-PAGE") {
		t.Fatal("自签名证书的网站被打开了：证书校验没有生效")
	}
	if n := hits.Load() - before; n != 0 {
		t.Fatalf("自签名证书的网站收到了 %d 次请求：证书校验没有在握手阶段拦截", n)
	}
	if !strings.Contains(strings.Join(m.Targets(), " "), "site.test:443") {
		t.Fatal("浏览器没有尝试连接 https 网站，测试无效")
	}
}
