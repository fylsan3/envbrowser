// Package launcher 负责环境锁与 Chromium 启动参数。
package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"envbrowser/internal/config"
)

// ---------- 环境锁 ----------

// Lock 同一环境同时只能打开一次。锁文件记录 PID，进程已不存在时视为残留锁自动清除。
type Lock struct{ path string }

func AcquireLock(e *config.Env) (*Lock, error) {
	p := filepath.Join(e.Dir(), "env.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return &Lock{path: p}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		b, _ := os.ReadFile(p)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && processAlive(pid) {
			return nil, fmt.Errorf("环境 %s 已在运行（PID %d）", e.ID, pid)
		}
		os.Remove(p) // 残留锁
	}
	return nil, fmt.Errorf("无法获取环境 %s 的锁", e.ID)
}

func (l *Lock) Release() { os.Remove(l.path) }

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ---------- Chromium ----------

// Args 生成启动参数。只使用启动参数，不注入脚本。
func Args(e *config.Env, relayPort int) []string {
	proxy := fmt.Sprintf("socks5://127.0.0.1:%d", relayPort)
	args := []string{
		"--user-data-dir=" + e.BrowserDir(),
		"--proxy-server=" + proxy,
		// 默认 Chromium 会让 localhost 绕过代理；<-loopback> 取消这一例外，
		// 本机地址也交给中转，由中转统一拒绝（本机隔离）。
		"--proxy-bypass-list=<-loopback>",
		// 禁止本地 DNS 解析：所有域名只能由代理远端解析。中转地址本身除外。
		"--host-resolver-rules=MAP * ~NOTFOUND , EXCLUDE 127.0.0.1",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-quic",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-domain-reliability",
		"--no-first-run",
		"--no-default-browser-check",
		"--use-mock-keychain",
		"--password-store=basic",
		"--metrics-recording-only",
	}
	m := e.Modules
	if m["language"] && len(e.Fingerprint.Languages) > 0 {
		args = append(args,
			"--lang="+e.Fingerprint.Languages[0],
			"--accept-lang="+strings.Join(e.Fingerprint.Languages, ","))
	}
	if m["window"] {
		w := e.Fingerprint.Seed.Window
		if w.Width > 0 && w.Height > 0 {
			args = append(args, fmt.Sprintf("--window-size=%d,%d", w.Width, w.Height))
		}
	}
	return append(args, "about:blank")
}

// Env 生成进程环境变量。时区通过 TZ 设置（需实测 Mac 版是否生效）。
func Env(e *config.Env) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		// 清掉可能让浏览器走别的代理的系统变量
		case "TZ", "http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "no_proxy", "NO_PROXY":
			continue
		}
		out = append(out, kv)
	}
	if e.Modules["timezone"] && e.Fingerprint.Timezone != "" {
		out = append(out, "TZ="+e.Fingerprint.Timezone)
	}
	return out
}

// ExecPath 由 Chromium.app 路径得到可执行文件路径。
func ExecPath(appOrBin string) string {
	if strings.HasSuffix(appOrBin, ".app") {
		return filepath.Join(appOrBin, "Contents", "MacOS", "Chromium")
	}
	return appOrBin
}

// Start 启动 Chromium。调用方负责 Wait。
func Start(chromium string, e *config.Env, relayPort int) (*exec.Cmd, error) {
	bin := ExecPath(chromium)
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("找不到 Chromium: %s", bin)
	}
	if err := os.MkdirAll(e.BrowserDir(), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, Args(e, relayPort)...)
	cmd.Env = Env(e)
	if err := os.MkdirAll(e.LogDir(), 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(e.LogDir(), "chromium.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
		}
	}
	return cmd, cmd.Start()
}
