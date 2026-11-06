package e2e

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/fpcheck"
	"envbrowser/internal/launcher"
)

// 真浏览器 + 检测页（阶段 2）：
//   - 中转开测试模式，只放行检测页地址；浏览器经中转打开检测页；
//   - 检测页在主页面 / iframe / Worker / Service Worker 采集，服务端逐项比对；
//   - 环境 A 重启 3 次结果一致；A、B 的 Canvas 哈希不同（音频允许重复，只提示）。
//
// 无界面模式下显卡、窗口与有界面不同，这两项只记录不判定；其余项必须全部通过。
func TestFingerprintPage(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 模式跳过真浏览器测试")
	}
	chromium := chromiumPath(t)
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := fpcheck.New(store, ln.Addr().String(), nil)
	hs := &httpServer{ln: ln, h: srv.Handler()}
	hs.start(t)

	ma := newMock(t, "socks5h", nil)
	mb := newMock(t, "http", nil)
	ea := newEnv(t, store, ma.Upstream(), "Asia/Tokyo")
	eb := newEnv(t, store, mb.Upstream(), "America/New_York")
	ra := startRelay(t, ma.Upstream())
	rb := startRelay(t, mb.Upstream())
	ra.SetTestTarget(srv.Addr)
	rb.SetTestTarget(srv.Addr)

	run := func(e *config.Env, port int) *fpcheck.Report {
		args := launcher.Args(e, port)
		args = append(args[:len(args)-1], "--headless=new", "--no-sandbox", srv.URL(e.ID))
		cmd := exec.Command(launcher.ExecPath(chromium), args...)
		cmd.Env = launcher.Env(e)
		logf, _ := os.Create(t.TempDir() + "/chromium.log")
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		rep, err := srv.Wait(e.ID, 60*time.Second)
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		if err != nil {
			b, _ := os.ReadFile(logf.Name())
			t.Fatalf("%v\n浏览器日志末尾:\n%s", err, tail(string(b), 1500))
		}
		return rep
	}

	// 无界面模式下不判定的项
	headlessOnly := func(item string) bool {
		return strings.HasPrefix(item, "显卡") || item == "窗口尺寸"
	}
	byEnv := map[string][]*fpcheck.Report{}
	for i := 0; i < 3; i++ {
		byEnv[ea.ID] = append(byEnv[ea.ID], run(ea, ra.Port()))
	}
	byEnv[eb.ID] = append(byEnv[eb.ID], run(eb, rb.Port()))

	for _, rs := range byEnv {
		for _, r := range rs {
			t.Logf("== %s 种子 %d：%s（%s）", r.Env, r.Seed, r.Summary, r.File)
			for _, row := range r.Rows {
				mark := "✓"
				if !row.OK {
					mark = "✗"
				}
				t.Logf("  %s %-24s 目标=%s 实际=%s [%s]", mark, row.Item, row.Want, row.Got, row.Where)
				if !row.OK && !headlessOnly(row.Item) {
					t.Errorf("%s 检测不一致: %s 目标=%s 实际=%s", r.Env, row.Item, row.Want, row.Got)
				}
			}
		}
	}
	for _, row := range fpcheck.Compare(byEnv) {
		t.Logf("  比对 %s：%s [%s]", row.Item, row.Got, row.Where)
		if !row.OK {
			t.Errorf("比对失败: %s %s", row.Item, row.Got)
		}
	}
	// 检测页之外，浏览器不应经上游连任何目标（后台请求已关闭；检测页走本机放行地址）
	for _, x := range append(ma.Targets(), mb.Targets()...) {
		t.Errorf("上游收到了意外的目标 %s", x)
	}
}

type httpServer struct {
	ln net.Listener
	h  http.Handler
}

func (s *httpServer) start(t *testing.T) {
	hs := &http.Server{Handler: s.h, ReadHeaderTimeout: 10 * time.Second}
	go hs.Serve(s.ln)
	t.Cleanup(func() { hs.Close() })
}
