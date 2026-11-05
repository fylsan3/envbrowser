// envbrowser 主程序。
//
//	envbrowser serve                       启动管理页面（127.0.0.1:8800）
//	envbrowser create -proxy URL [...]     新建环境
//	envbrowser list                        列出环境
//	envbrowser open  env-0001              前台打开一个环境（Ctrl+C 关闭）
//	envbrowser check -proxy URL [-exit IP] 测试代理：认证、出口 IP、请求头
//	envbrowser settings -chromium PATH     修改设置
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"envbrowser/internal/admin"
	"envbrowser/internal/config"
	"envbrowser/internal/egress"
	"envbrowser/internal/runner"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	root := os.Getenv("ENVBROWSER_ROOT")
	if root == "" {
		root = config.DefaultRoot()
	}
	store, err := config.NewStore(root)
	must(err)
	st, err := store.LoadSettings()
	must(err)

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "serve":
		serve(store, st)
	case "create":
		create(store, st, args)
	case "list":
		list(store)
	case "open":
		open(store, st, args)
	case "check":
		check(st, args)
	case "settings":
		settings(store, st, args)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法:
  envbrowser serve
  envbrowser create -name 名称 -proxy socks5h://用户:密码@主机:端口 [-exit 出口IP] [-tz Asia/Tokyo] [-lang ja-JP] [-user 账号] [-pass 密码] [-note 备注]
  envbrowser list
  envbrowser open env-0001
  envbrowser check -proxy URL [-exit 出口IP]
  envbrowser settings [-chromium /path/Chromium.app] [-chromium-version 159.0.8145.0] [-test-mode=true|false]

数据目录默认 ~/BrowserEnvs，可用环境变量 ENVBROWSER_ROOT 修改。`)
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func localIP(st config.Settings) net.IP {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ip, err := egress.LocalIP(ctx, st.EchoServices)
	if err != nil {
		fmt.Fprintln(os.Stderr, "警告: 获取本机公网 IP 失败，将无法比对“出口不等于本机”:", err)
		return nil
	}
	return ip
}

func create(store *config.Store, st config.Settings, args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	name := fs.String("name", "", "环境名称")
	proxy := fs.String("proxy", "", "主代理 URL（必填）")
	proxy2 := fs.String("proxy2", "", "备用代理 URL（可选，只能在环境关闭时手动切换）")
	exit := fs.String("exit", "", "指定出口 IP（默认取主代理主机）")
	tz := fs.String("tz", "", "时区（默认 "+st.DefaultTZ+"）")
	lang := fs.String("lang", "", "语言（默认按时区）")
	user := fs.String("user", "", "账号")
	pass := fs.String("pass", "", "账号登录密码")
	note := fs.String("note", "", "备注")
	fs.Parse(args)
	if *proxy == "" {
		must(fmt.Errorf("必须提供 -proxy"))
	}
	p1, err := config.ParseUpstream(*proxy)
	must(err)
	in := config.NewEnvInput{Name: *name, Primary: p1, ExitIP: *exit, Timezone: *tz, Language: *lang,
		Account: config.Account{Username: *user, Password: *pass, Note: *note}}
	if *proxy2 != "" {
		p2, err := config.ParseUpstream(*proxy2)
		must(err)
		in.Secondary = p2
	}
	e, err := store.Create(in, st)
	must(err)
	fmt.Printf("已创建 %s（%s）出口 %s 时区 %s\n", e.ID, e.Name, e.Proxy.ExitIP, e.Fingerprint.Timezone)
}

func list(store *config.Store) {
	envs, err := store.List()
	must(err)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\t名称\t账号\t代理\t出口\t时区")
	for _, e := range envs {
		up, _ := e.ActiveUpstream()
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Name, e.Account.Username, up.Redacted(), e.Proxy.ExitIP, e.Fingerprint.Timezone)
	}
	w.Flush()
}

func check(st config.Settings, args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	proxy := fs.String("proxy", "", "代理 URL")
	exit := fs.String("exit", "", "期望出口 IP（默认取代理主机）")
	fs.Parse(args)
	up, err := config.ParseUpstream(*proxy)
	must(err)
	if *exit == "" {
		*exit = up.Host
	}
	local := localIP(st)
	fmt.Println("本机公网 IP:", local)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := egress.Check(ctx, up, *exit, local, st.EchoServices, 0)
	if res.ExitIP != nil {
		fmt.Printf("实际出口: %s（%s）\n", res.ExitIP, res.Service)
	}
	if err != nil {
		fmt.Println("出口校验: 未通过 —", err)
	} else {
		fmt.Println("出口校验: 通过")
	}
	body, err := egress.HeaderCheck(ctx, up, st.HeaderEchoURL)
	if err != nil {
		fmt.Println("明文 http(80) 请求头检查: 失败 —", err)
		return
	}
	if leaks := egress.LeakHeaders(body); len(leaks) > 0 {
		fmt.Println("明文 http(80) 请求头检查: 发现暴露头", leaks)
	} else {
		fmt.Println("明文 http(80) 请求头检查: 通过，无 Via / X-Forwarded-For 等")
	}
}

func open(store *config.Store, st config.Settings, args []string) {
	if len(args) != 1 {
		usage()
	}
	e, err := store.Load(args[0])
	must(err)
	ss, err := runner.Open(e, st, localIP(st))
	must(err)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		ss.Close()
	case <-ss.Done():
	}
}

func serve(store *config.Store, st config.Settings) {
	srv := admin.New(store, st, localIP(st))
	ln, err := net.Listen("tcp", st.AdminAddr)
	must(err)
	fmt.Println("管理页面:", srv.URL())
	exec.Command("open", srv.URL()).Start()
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go hs.Serve(ln)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("正在关闭所有环境…")
	srv.CloseAll()
	hs.Close()
}

func settings(store *config.Store, st config.Settings, args []string) {
	fs := flag.NewFlagSet("settings", flag.ExitOnError)
	chromium := fs.String("chromium", st.ChromiumPath, "Chromium.app 路径")
	ver := fs.String("chromium-version", st.ChromiumVer, "Chromium 版本号")
	test := fs.Bool("test-mode", st.TestMode, "测试模式（放行本机检测页）")
	fs.Parse(args)
	st.ChromiumPath, st.ChromiumVer, st.TestMode = *chromium, *ver, *test
	must(store.SaveSettings(st))
	fmt.Printf("已保存: chromium=%s version=%s test_mode=%v\n", st.ChromiumPath, st.ChromiumVer, st.TestMode)
}
