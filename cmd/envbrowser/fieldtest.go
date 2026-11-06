package main

// fieldtest：真实代理实测（需求规格 12 节阶段 1.5b + 12.1 节）。由 scripts/field-test.sh 调用。
// 代理 URL 从标准输入读一行，不出现在命令行和进程列表里；输出中代理地址、用户名、密码、
// 本机公网 IP、出口 IP 全部替换成占位符，可以直接把结果发给别人看。
// 全部在临时数据目录里进行，不碰 ~/BrowserEnvs，结束后删除。

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/egress"
	"envbrowser/internal/fpcheck"
	"envbrowser/internal/launcher"
	"envbrowser/internal/runner"
)

// 浏览器在实测中会打开的外部网站（DNS 监控里不应出现它们）
var fieldSites = []string{"https://example.com/", "http://neverssl.com/"}

type masker struct {
	pairs []string
	out   io.Writer
	mu    sync.Mutex
}

func (m *masker) add(secret, label string) {
	// 本机地址不打码（否则会把说明文字里的 127.0.0.1 也替换掉；真实代理不会是本机）
	if secret != "" && secret != "127.0.0.1" && secret != "<nil>" {
		m.pairs = append(m.pairs, secret, label)
	}
}

func (m *masker) printf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	s = strings.NewReplacer(m.pairs...).Replace(s)
	m.mu.Lock()
	io.WriteString(m.out, s)
	m.mu.Unlock()
}

type result struct {
	name string
	ok   bool
	note string
}

func fieldtest(st config.Settings, args []string) {
	fs := flag.NewFlagSet("fieldtest", flag.ExitOnError)
	exit := fs.String("exit", "", "期望出口 IP（默认取代理主机）")
	report := fs.String("o", "field-report.txt", "报告文件（已打码）")
	watch := fs.Int("watch", 40, "每个环境打开后观察多少秒")
	headless := fs.Bool("headless", false, "无界面运行（只用于开发时自测，实测不要加）")
	fs.Parse(args)

	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	up, err := config.ParseUpstream(strings.TrimSpace(line))
	must(err)
	if *exit == "" {
		*exit = up.Host
	}
	f, err := os.OpenFile(*report, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	must(err)
	defer f.Close()
	m := &masker{out: io.MultiWriter(os.Stdout, f)}
	m.add(up.Password, "<代理密码>")
	m.add(up.Username, "<代理用户名>")
	m.add(up.Host, "<代理IP>")
	m.add(*exit, "<出口IP>")
	maskStderr(m)

	var results []result
	pass := func(name string, ok bool, note string, a ...any) {
		n := fmt.Sprintf(note, a...)
		results = append(results, result{name, ok, n})
		mark := "通过"
		if !ok {
			mark = "未通过"
		}
		m.printf("  → [%s] %s %s\n", mark, name, n)
	}

	m.printf("游龙 真实代理实测 %s  %s\n浏览器：%s  本机芯片：%s\n代理类型：%s\n\n",
		version, time.Now().Format("2006-01-02 15:04"), browserInfo, config.HostChip(), up.Type)

	local := localIP(st)
	if local != nil {
		m.add(local.String(), "<本机公网IP>")
	}
	m.printf("本机公网 IP：%s\n", local)

	// ① 出口 IP ② 明文 http 请求头
	m.printf("\n【①】出口 IP\n")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	res, err := egress.Check(ctx, up, *exit, local, st.EchoServices, 0)
	if res.ExitIP != nil {
		m.printf("  实际出口：%s（%s）\n", res.ExitIP, res.Service)
	}
	pass("① 出口 IP 与录入一致、不等于本机", err == nil, "%v", errText(err))
	cc, cerr := egress.Country(ctx, up)
	pass("① 出口国家", cerr == nil, "%s%s", cc, errText(cerr))
	m.printf("\n【②】明文 http 请求头\n")
	body, herr := egress.HeaderCheck(ctx, up, st.HeaderEchoURL)
	leaks := egress.LeakHeaders(body)
	pass("② 明文 http(80) 可访问且无暴露头", herr == nil && len(leaks) == 0, "%s%v", errText(herr), leaks)
	cancel()
	if err != nil {
		m.printf("\n出口校验未通过，浏览器测试无法进行，结束。\n")
		summary(m, results)
		os.Exit(1)
	}

	// 临时数据目录，两个环境共用这条代理
	root, err := os.MkdirTemp("", "envbrowser-field-")
	must(err)
	defer os.RemoveAll(root)
	store, err := config.NewStore(root)
	must(err)
	tz := "Asia/Tokyo"
	if zs := config.ZonesForCountry(cc); len(zs) > 0 {
		tz = zs[0].TZ
	}
	var envs []*config.Env
	for _, name := range []string{"实测A", "实测B"} {
		u := *up
		e, err := store.Create(config.NewEnvInput{Name: name, Primary: &u, ExitIP: *exit, Timezone: tz, ExitCountry: cc}, st)
		must(err)
		envs = append(envs, e)
	}

	srv, stop := startFPServer(store, st, local)
	defer stop()
	st.TestMode, st.TestTarget = true, srv.Addr

	// ③ 零直连：观察期间每秒查一次浏览器进程和主程序的连接
	mon := newConnMonitor(root, up.Addr())
	go mon.run()

	var extra []string
	if *headless {
		// 开发自测所在的受限环境里沙箱不可用；正式实测不加 -headless，也就不会带 --no-sandbox
		extra = []string{"--headless=new", "--no-sandbox"}
	}
	reports := map[string][]*fpcheck.Report{}
	for _, e := range envs {
		m.printf("\n【③④】打开 %s（%s，有界面，约 %d 秒，请不要操作窗口）\n", e.Name, e.ID, *watch)
		urls := append([]string{srv.URL(e.ID)}, fieldSites...)
		if *headless {
			urls = urls[:1] // 无界面模式只支持一个起始网址
		}
		ss, err := runner.OpenWith(e, st, local, store.Save, launcher.Options{URLs: urls, Extra: extra})
		if err != nil {
			pass("打开 "+e.Name, false, "%v", err)
			continue
		}
		rep, werr := waitReport(srv, ss, e.ID)
		if werr != nil {
			pass(e.Name+" 检测页", false, "%v", werr)
		} else {
			reports[e.ID] = append(reports[e.ID], rep)
			for _, row := range rep.Rows {
				if !row.OK {
					m.printf("  [不一致] %s | 目标 %s | 实际 %s\n", row.Item, row.Want, row.Got)
				}
			}
			pass(e.Name+" 检测页（含 WebRTC、端口扫描、Cookie 隔离）", rep.Failed == 0, "%s", rep.Summary)
		}
		time.Sleep(time.Duration(*watch) * time.Second)

		// ⑤ 断网：模拟出口异常，中转立即切断全部连接；之后浏览器仍不得出现直连
		if ss.Relay != nil {
			before := ss.Relay.Conns()
			ss.Relay.Block("实测：模拟出口异常")
			time.Sleep(8 * time.Second)
			n := ss.Relay.Conns()
			pass(e.Name+" 断网后中转连接全部切断", n == 0, "断网前 %d 条，断网后 %d 条", before, n)
		}
		ss.Close()
	}
	mon.stop()

	direct, mainOther := mon.findings()
	pass("③ 浏览器进程零直连（只连 127.0.0.1）", len(direct) == 0 && mon.samples() > 0,
		"采样 %d 次%s", mon.samples(), listText(direct))
	pass("③ 主程序对外只连上游代理", len(mainOther) == 0, "%s", listText(mainOther))

	m.printf("\n【跨环境比对】\n")
	for _, r := range fpcheck.Compare(reports) {
		note := r.Got
		if r.Warn {
			note += "（注意：只提示，不算失败）"
		}
		pass(r.Item, r.OK, "%s", note)
	}

	// 错误密码：应在浏览器启动前失败
	m.printf("\n【⑤】错误密码\n")
	bad := *up
	bad.Password += "-wrong"
	be, _ := store.Create(config.NewEnvInput{Name: "错误密码", Primary: &bad, ExitIP: *exit, Timezone: tz}, st)
	if be != nil {
		ss, err := runner.Open(be, st, local, nil)
		if ss != nil {
			ss.Close()
		}
		pass("⑤ 密码错误时不启动浏览器", err != nil, "%s", errText(err))
	}

	// ⑥ 清理：没有残留浏览器（最多等 10 秒，子进程退出有延迟）
	var left []string
	for i := 0; i < 10; i++ {
		if left = browserPIDs(root); len(left) == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	var what []string
	for _, pid := range left {
		out, _ := exec.Command("ps", "-o", "command=", "-p", pid).Output()
		cmd := string(out)
		if i := strings.Index(cmd, "--type="); i >= 0 {
			cmd = strings.Fields(cmd[i:])[0]
		} else {
			cmd = "主进程"
		}
		what = append(what, pid+" "+cmd)
	}
	pass("⑥ 结束后无残留浏览器进程", len(left) == 0, "剩余 %d 个%s", len(left), listText(what))

	summary(m, results)
	m.printf("\n报告已保存：%s（已打码）\n", *report)
}

func summary(m *masker, rs []result) {
	bad := 0
	for _, r := range rs {
		if !r.ok {
			bad++
		}
	}
	m.printf("\n==== 汇总：%d 项，未通过 %d 项 ====\n", len(rs), bad)
	for _, r := range rs {
		if !r.ok {
			m.printf("  未通过：%s %s\n", r.name, r.note)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return "— " + err.Error()
}

func listText(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	if len(xs) > 15 {
		xs = append(xs[:15], "…")
	}
	return "：" + strings.Join(xs, "；")
}

// ---------- 连接监控（需求规格 12.1 节第 2、3 步） ----------

type connMonitor struct {
	root     string
	upstream string
	quit     chan struct{}
	done     chan struct{}

	mu        sync.Mutex
	n         int
	direct    map[string]bool // 浏览器进程的非本机连接
	mainOther map[string]bool // 主程序连了上游以外的地址
}

func newConnMonitor(root, upstream string) *connMonitor {
	return &connMonitor{root: root, upstream: upstream, quit: make(chan struct{}), done: make(chan struct{}),
		direct: map[string]bool{}, mainOther: map[string]bool{}}
}

func (c *connMonitor) run() {
	defer close(c.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		c.sample()
		select {
		case <-c.quit:
			return
		case <-t.C:
		}
	}
}

func (c *connMonitor) stop() { close(c.quit); <-c.done }

func (c *connMonitor) sample() {
	pids := browserPIDs(c.root)
	var bconns []string
	if len(pids) > 0 {
		bconns = remotes(strings.Join(pids, ","))
	}
	mconns := remotes(strconv.Itoa(os.Getpid()))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	for _, r := range bconns {
		if !isLoopback(r) {
			c.direct[r] = true
		}
	}
	for _, r := range mconns {
		if !isLoopback(r) && !strings.HasSuffix(r, "->"+c.upstream) {
			c.mainOther[r] = true
		}
	}
}

func (c *connMonitor) samples() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func (c *connMonitor) findings() (direct, mainOther []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.direct {
		direct = append(direct, k)
	}
	for k := range c.mainOther {
		mainOther = append(mainOther, k)
	}
	sort.Strings(direct)
	sort.Strings(mainOther)
	return
}

// browserPIDs 实测数据目录下所有浏览器进程（含子进程）。
func browserPIDs(root string) []string {
	out, _ := exec.Command("pgrep", "-f", "user-data-dir="+filepath.Join(root, "env-")).Output()
	return strings.Fields(string(out))
}

// remotes 返回这些进程的对外连接，形如 "TCP 127.0.0.1:5000->203.0.113.1:443"；监听端口不算。
func remotes(pids string) []string {
	out, _ := exec.Command("lsof", "-nP", "-iTCP", "-iUDP", "-a", "-p", pids, "-F", "Pn").Output()
	var res []string
	proto := ""
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" {
			continue
		}
		switch l[0] {
		case 'P':
			proto = l[1:]
		case 'n':
			name := l[1:]
			if strings.Contains(name, "->") || proto == "UDP" {
				res = append(res, proto+" "+name)
			}
		}
	}
	return res
}

// isLoopback 连接（或 UDP 绑定）的对端是否本机。UDP 未连接时只有本地地址，按本地地址判断。
func isLoopback(conn string) bool {
	_, addr, _ := strings.Cut(conn, " ")
	if _, dst, ok := strings.Cut(addr, "->"); ok {
		addr = dst
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// maskStderr 运行日志（中转、出口校验等）原本直接写到终端，里面有代理用户名和地址。
// 改为经过打码后再输出，并一并写进报告。
func maskStderr(m *masker) {
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	os.Stderr = w
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			m.printf("    日志 %s\n", sc.Text())
		}
	}()
}

// waitReport 等检测报告；浏览器提前退出时立即返回，不干等。
func waitReport(srv *fpcheck.Server, ss *runner.Session, id string) (*fpcheck.Report, error) {
	type res struct {
		r   *fpcheck.Report
		err error
	}
	ch := make(chan res, 1)
	go func() { r, err := srv.Wait(id, 90*time.Second); ch <- res{r, err} }()
	select {
	case x := <-ch:
		return x.r, x.err
	case <-ss.Done():
		return nil, fmt.Errorf("浏览器提前退出，详见日志")
	}
}
