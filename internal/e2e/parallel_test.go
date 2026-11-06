package e2e

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/fpcheck"
	"envbrowser/internal/launcher"
	"envbrowser/internal/runner"
)

// 阶段 3：5 个环境同时打开（走真实启动流程 runner.OpenWith：出口校验 → 启动浏览器 → 运行中复查）。
//   - 5 个环境同时启动，各自出口校验通过，记录的出口 IP 各不相同且与录入值一致；
//   - 5 个浏览器同时打开检测页，各项与各自配置一致，跨环境比对通过；
//   - 停掉第 3 个环境的上游：只有第 3 个变为“已断网”，经它的中转打不开网站；
//     其余 4 个仍是“运行中”，经各自中转仍能打开网站。
//
// 模拟上游、出口 IP 全部是本机模拟和文档保留地址（198.51.100.0/24），不涉及真实代理。
// 需要约 2～4GB 内存；-short 模式跳过。
func TestFiveEnvsParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 模式跳过 5 环境并行测试")
	}
	const n = 5
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
	(&httpServer{ln: ln, h: srv.Handler()}).start(t)
	site := newSite(t, hello())

	st := config.DefaultSettings()
	st.ChromiumPath = chromium
	st.PortMin, st.PortMax = portMin, portMax
	st.EchoServices = []string{"http://echo.test/cdn-cgi/trace"}
	st.RecheckMinSec, st.RecheckMaxSec, st.RecheckFailMax = 1, 2, 2
	st.TestMode, st.TestTarget = true, srv.Addr

	zones := []string{"Asia/Tokyo", "America/New_York", "Europe/London", "Asia/Singapore", "America/Los_Angeles"}
	mocks := make([]*mockUpstream, n)
	envs := make([]*config.Env, n)
	for i := 0; i < n; i++ {
		exit := fmt.Sprintf("198.51.100.%d", 11+i)
		_, addr := newTrace(t, exit, "JP")
		kind := "socks5h"
		if i%2 == 1 {
			kind = "http"
		}
		mocks[i] = newMock(t, kind, site.routes(map[string]string{"echo.test:80": addr}))
		up := mocks[i].Upstream()
		e, err := store.Create(config.NewEnvInput{Primary: &up, ExitIP: exit, Timezone: zones[i]}, st)
		if err != nil {
			t.Fatal(err)
		}
		envs[i] = e
	}

	// 同时打开 5 个环境
	sess := make([]*runner.Session, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := range envs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess[i], errs[i] = runner.OpenWith(envs[i], st, nil, store.Save, launcher.Options{
				URLs:  []string{srv.URL(envs[i].ID)},
				Extra: []string{"--headless=new", "--no-sandbox"},
			})
		}(i)
	}
	wg.Wait()
	t.Cleanup(func() {
		var cw sync.WaitGroup
		for _, s := range sess {
			if s != nil {
				cw.Add(1)
				go func(s *runner.Session) { defer cw.Done(); s.Close() }(s)
			}
		}
		cw.Wait()
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("环境 %d（%s）启动失败: %v", i+1, envs[i].ID, err)
		}
	}
	t.Logf("5 个环境全部启动，用时 %s", time.Since(t0).Round(time.Millisecond))

	ports := map[int]bool{}
	for i, s := range sess {
		status, ip, _ := s.Status()
		if status != "running" || ip != envs[i].Proxy.ExitIP {
			t.Errorf("环境 %d 应为 running 且出口 %s，实际 %s / %s", i+1, envs[i].Proxy.ExitIP, status, ip)
		}
		if ports[s.Relay.Port()] {
			t.Errorf("环境 %d 中转端口 %d 与其他环境重复", i+1, s.Relay.Port())
		}
		ports[s.Relay.Port()] = true
	}

	// 5 个检测页同时回报
	headlessOnly := func(item string) bool { return headlessOrBase(item) }
	byEnv := map[string][]*fpcheck.Report{}
	var mu sync.Mutex
	for i := range envs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rep, err := srv.Wait(envs[i].ID, 90*time.Second)
			if err != nil {
				t.Errorf("环境 %d 检测页没有回报: %v", i+1, err)
				return
			}
			mu.Lock()
			byEnv[envs[i].ID] = append(byEnv[envs[i].ID], rep)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for i, e := range envs {
		for _, r := range byEnv[e.ID] {
			t.Logf("== 环境 %d %s：%s", i+1, e.ID, r.Summary)
			for _, row := range r.Rows {
				if !row.OK && !headlessOnly(row.Item) {
					t.Errorf("环境 %d 检测不一致: %s 目标=%s 实际=%s", i+1, row.Item, row.Want, row.Got)
				}
			}
		}
	}
	for _, row := range fpcheck.Compare(byEnv) {
		t.Logf("  比对 %s：%s", row.Item, row.Got)
		if !row.OK {
			t.Errorf("跨环境比对失败: %s %s", row.Item, row.Got)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// 每个环境经自己的中转都能打开网站
	for i, s := range sess {
		if _, err := get(browserLike(s.Relay.Port()), "http://site.test/"); err != nil {
			t.Fatalf("断上游前环境 %d 应能打开网站: %v", i+1, err)
		}
	}

	// 停掉第 3 个上游
	const victim = 2
	mocks[victim].Close()
	deadline := time.Now().Add(30 * time.Second)
	for sess[victim].Status0() != "blocked" {
		if time.Now().After(deadline) {
			t.Fatalf("第 3 个上游停掉 30 秒后仍未断网，状态 %s", sess[victim].Status0())
		}
		time.Sleep(200 * time.Millisecond)
	}
	_, _, reason := sess[victim].Status()
	t.Logf("环境 3 已断网：%s", reason)
	// 再观察几轮复查，确认其余环境不受影响
	time.Sleep(5 * time.Second)

	for i, s := range sess {
		_, err := get(browserLike(s.Relay.Port()), "http://site.test/")
		status := s.Status0()
		if i == victim {
			if err == nil || !s.Relay.Blocked() {
				t.Errorf("环境 3 断网后仍能打开网站（err=%v）", err)
			}
			continue
		}
		if status != "running" || err != nil {
			t.Errorf("环境 %d 应不受影响，实际状态 %s，访问错误 %v", i+1, status, err)
		}
	}
}
