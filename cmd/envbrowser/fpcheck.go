package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/fpcheck"
	"envbrowser/internal/launcher"
	"envbrowser/internal/runner"
)

// startFPServer 在 st.TestTarget（默认 127.0.0.1:9000）启动检测页。
func startFPServer(store *config.Store, st config.Settings, local net.IP) (*fpcheck.Server, func()) {
	addr := st.TestTarget
	if addr == "" {
		addr = "127.0.0.1:9000"
	}
	ln, err := net.Listen("tcp", addr)
	must(err)
	srv := fpcheck.New(store, addr, local)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go hs.Serve(ln)
	return srv, func() { hs.Close() }
}

// fpcheckCmd 依次打开各环境，自动访问检测页，收齐报告后做跨环境和重启比对。
// 本命令只对它自己打开的环境放行检测页（相当于临时测试模式），不改 settings.json。
func fpcheckCmd(store *config.Store, st config.Settings, args []string) {
	fs := flag.NewFlagSet("fpcheck", flag.ExitOnError)
	n := fs.Int("n", 3, "每个环境打开几次（检查重启后结果是否一致）")
	var ids []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			ids = append(ids, args[0])
			args = args[1:]
		}
	}
	if len(ids) == 0 {
		usage()
	}
	local := localIP(st)
	srv, stop := startFPServer(store, st, local)
	defer stop()
	st.TestMode, st.TestTarget = true, srv.Addr

	byEnv := map[string][]*fpcheck.Report{}
	failed := 0
	for round := 1; round <= *n; round++ {
		for _, id := range ids {
			fmt.Printf("== %s 第 %d/%d 次\n", id, round, *n)
			rep, err := fpOnce(store, st, local, srv, id)
			if err != nil {
				fmt.Println("  失败:", err)
				failed++
				continue
			}
			printReport(rep, round == 1)
			failed += rep.Failed
			byEnv[id] = append(byEnv[id], rep)
		}
	}
	fmt.Println("== 跨环境 / 重启比对")
	for _, r := range fpcheck.Compare(byEnv) {
		printRow(r)
		if !r.OK {
			failed++
		}
	}
	if failed > 0 {
		fmt.Printf("结论: %d 项不一致，报告在各环境 reports/ 目录\n", failed)
		os.Exit(1)
	}
	fmt.Println("结论: 全部通过")
}

func fpOnce(store *config.Store, st config.Settings, local net.IP, srv *fpcheck.Server, id string) (*fpcheck.Report, error) {
	e, err := store.Load(id)
	if err != nil {
		return nil, err
	}
	ss, err := runner.OpenWith(e, st, local, store.Save, launcher.Options{URLs: []string{srv.URL(id)}})
	if err != nil {
		return nil, err
	}
	defer ss.Close()
	return srv.Wait(id, 90*time.Second)
}

func printReport(r *fpcheck.Report, full bool) {
	fmt.Printf("  %s（%s）\n", r.Summary, r.File)
	for _, row := range r.Rows {
		if full || !row.OK {
			printRow(row)
		}
	}
}

func printRow(r fpcheck.Row) {
	mark := "通过"
	switch {
	case !r.OK:
		mark = "不一致"
	case r.Warn:
		mark = "注意"
	}
	got := r.Got
	if len(got) > 120 {
		got = got[:117] + "…"
	}
	fmt.Printf("  [%s] %s | 目标 %s | 实际 %s | %s\n", mark, r.Item, r.Want, strings.ReplaceAll(got, "\n", " "), r.Where)
}
