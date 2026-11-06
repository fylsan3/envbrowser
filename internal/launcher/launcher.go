// Package launcher 负责环境锁、孤儿浏览器清理与 fingerprint-chromium 启动参数。
package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"envbrowser/internal/config"
)

// ---------- 环境锁 ----------

// Lock 同一环境同时只能打开一次。
// 锁文件两行：第一行主程序 PID，第二行浏览器 PID（浏览器启动后写入）。
//
// 主程序崩溃时浏览器可能还活着，而它连的中转端口已关闭；如果这个端口随后被
// 另一个环境的中转占用，孤儿浏览器就会走到别人的代理上（串号）。所以：
//   - 主程序 PID 还活着 → 环境确实在运行，拒绝加锁；
//   - 主程序已死、浏览器 PID 还活着且确属本环境 → 先结束孤儿浏览器，再加锁。
type Lock struct{ path string }

func lockPath(e *config.Env) string { return filepath.Join(e.Dir(), "env.lock") }

func AcquireLock(e *config.Env) (*Lock, error) {
	p := lockPath(e)
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
		owner, browser := readLock(p)
		if ownerAlive(owner) {
			return nil, fmt.Errorf("环境 %s 已在运行（PID %d）", e.ID, owner)
		}
		if browser > 0 && isEnvBrowser(browser, e) {
			if err := killAndWait(browser); err != nil {
				return nil, fmt.Errorf("环境 %s 有残留浏览器（PID %d）无法结束: %w", e.ID, browser, err)
			}
		}
		os.Remove(p) // 残留锁
	}
	return nil, fmt.Errorf("无法获取环境 %s 的锁", e.ID)
}

// SetBrowserPID 浏览器启动后立即记录其 PID，供崩溃后清理。
func (l *Lock) SetBrowserPID(pid int) error {
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d\n%d\n", os.Getpid(), pid)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func (l *Lock) Release() { os.Remove(l.path) }

func readLock(p string) (owner, browser int) {
	b, _ := os.ReadFile(p)
	lines := strings.Fields(string(b))
	if len(lines) > 0 {
		owner, _ = strconv.Atoi(lines[0])
	}
	if len(lines) > 1 {
		browser, _ = strconv.Atoi(lines[1])
	}
	return
}

// ownerAlive 锁的主程序是否还活着。除了 PID 存在，还要求进程名是 envbrowser，
// 防止主程序崩溃后 PID 被系统分给别的进程，导致环境永远打不开。
func ownerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	if !processAlive(pid) {
		return false
	}
	args, ok := procArg(pid)
	if !ok {
		return true // 查不到时按存活处理，宁可拒绝打开也不误判
	}
	exe, _, _ := strings.Cut(args, " ")
	return filepath.Base(exe) == "envbrowser"
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// procArgs 返回所有进程的 PID → 完整命令行。用 pgrep -lf（在受限环境下 ps 可能被禁用）。
func procArgs() (map[int]string, error) {
	out, err := exec.Command("pgrep", "-lf", ".").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return map[int]string{}, nil // 没有匹配
		}
		return nil, fmt.Errorf("读取进程列表失败: %w", err)
	}
	m := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		pidStr, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if pid, err := strconv.Atoi(pidStr); ok && err == nil && pid > 0 {
			m[pid] = args
		}
	}
	return m, nil
}

func procArg(pid int) (string, bool) {
	m, err := procArgs()
	if err != nil {
		return "", false
	}
	a, ok := m[pid]
	return a, ok
}

// isEnvBrowser 确认 PID 当前确实是本环境的浏览器主进程（防止 PID 被系统复用后误杀别的进程）。
func isEnvBrowser(pid int, e *config.Env) bool {
	if !processAlive(pid) {
		return false
	}
	args, ok := procArg(pid)
	return ok && strings.Contains(args+" ", userDataArg(e.BrowserDir())+" ")
}

func userDataArg(dir string) string { return "--user-data-dir=" + dir }

// killAndWait 先 SIGTERM，最多等 5 秒，仍未退出再 SIGKILL。
func killAndWait(pid int) error {
	syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	for i := 0; i < 20; i++ {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("进程未退出")
}

// Orphan 一个不归任何存活主程序管理的浏览器主进程。
type Orphan struct {
	PID   int
	EnvID string
}

var envIDRe = regexp.MustCompile(`^env-\d{4,}$`)

// FindOrphans 找出 <root>/env-*/browser 下、所属环境没有存活主程序的浏览器主进程。
// 只看主进程（带 --user-data-dir 且不带 --type=），子进程随主进程退出。
func FindOrphans(root string) ([]Orphan, error) {
	procs, err := procArgs()
	if err != nil {
		return nil, err
	}
	prefix := userDataArg(filepath.Join(root, "env-"))
	var res []Orphan
	for pid, args := range procs {
		if strings.Contains(args, " --type=") {
			continue
		}
		i := strings.Index(args, prefix)
		if i < 0 {
			continue
		}
		rest := args[i+len(prefix)-len("env-"):]
		envID, after, _ := strings.Cut(rest, "/")
		if !envIDRe.MatchString(envID) || !(after == "browser" || strings.HasPrefix(after, "browser ")) {
			continue
		}
		owner, _ := readLock(filepath.Join(root, envID, "env.lock"))
		if ownerAlive(owner) {
			continue // 另一个存活的主程序在管理它
		}
		res = append(res, Orphan{PID: pid, EnvID: envID})
	}
	return res, nil
}

// KillOrphans 主程序启动时调用：结束所有孤儿浏览器，返回被结束的列表。
func KillOrphans(root string) ([]Orphan, error) {
	list, err := FindOrphans(root)
	if err != nil {
		return nil, err
	}
	var done []Orphan
	var errs []error
	for _, o := range list {
		if err := killAndWait(o.PID); err != nil {
			errs = append(errs, fmt.Errorf("%s PID %d: %w", o.EnvID, o.PID, err))
			continue
		}
		done = append(done, o)
	}
	return done, errors.Join(errs...)
}

// ---------- Chromium ----------

// Args 生成启动参数。只使用启动参数，不注入脚本。
func Args(e *config.Env, relayPort int) []string {
	proxy := fmt.Sprintf("socks5://127.0.0.1:%d", relayPort)
	args := []string{
		userDataArg(e.BrowserDir()),
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
	fp := e.Fingerprint
	if m["fingerprint"] && fp.Seed.Fingerprint > 0 {
		args = append(args, "--fingerprint="+strconv.FormatInt(fp.Seed.Fingerprint, 10))
		if m["ua_client_hints"] {
			args = append(args, "--fingerprint-platform=macos", "--fingerprint-brand=Chrome")
			if fp.PlatformVersion != "" {
				args = append(args, "--fingerprint-platform-version="+fp.PlatformVersion)
			}
		}
		if m["hardware"] && fp.HardwareConcurrency > 0 {
			args = append(args, "--fingerprint-hardware-concurrency="+strconv.Itoa(fp.HardwareConcurrency))
		}
		if off := DisabledSpoofing(m); len(off) > 0 {
			args = append(args, "--disable-spoofing="+strings.Join(off, ","))
		}
	}
	if m["timezone"] && fp.Timezone != "" {
		args = append(args, "--timezone="+fp.Timezone)
	}
	if m["language"] && len(fp.Languages) > 0 {
		args = append(args,
			"--lang="+fp.Languages[0],
			"--accept-lang="+strings.Join(fp.Languages, ","))
	}
	if m["window"] {
		w := fp.Seed.Window
		if w.Width > 0 && w.Height > 0 {
			args = append(args, fmt.Sprintf("--window-size=%d,%d", w.Width, w.Height))
		}
	}
	return append(args, "about:blank")
}

// spoofModules 积木名 → --disable-spoofing 的取值。积木关闭即加入列表。
var spoofModules = []struct{ module, flag string }{
	{"fonts", "font"}, {"audio", "audio"}, {"canvas", "canvas"},
	{"clientrects", "clientrects"}, {"gpu", "gpu"},
}

// DisabledSpoofing 返回需要关闭的伪装项（顺序固定）。gpu 默认关闭，显示真机显卡。
func DisabledSpoofing(m map[string]bool) []string {
	var out []string
	for _, x := range spoofModules {
		if !m[x.module] {
			out = append(out, x.flag)
		}
	}
	return out
}

// Env 生成进程环境变量。时区改由 --timezone 设置；TZ 和代理变量一律清掉。
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
	return out
}

// ExecPath 由 Chromium.app 路径得到可执行文件路径。
func ExecPath(appOrBin string) string {
	if strings.HasSuffix(appOrBin, ".app") {
		return filepath.Join(appOrBin, "Contents", "MacOS", "Chromium")
	}
	return appOrBin
}

// Options 启动时的可选项。
//   - URLs：启动后打开的网址，空时打开 about:blank。
//   - Extra：附加参数，只给自动化测试用（例如无界面模式），正式使用不传。
type Options struct {
	URLs  []string
	Extra []string
}

// Start 启动 Chromium。调用方负责 Wait。
func Start(chromium string, e *config.Env, relayPort int) (*exec.Cmd, error) {
	return StartWith(chromium, e, relayPort, Options{})
}

// StartWith 同 Start，按 opt 替换起始网址、追加参数。
func StartWith(chromium string, e *config.Env, relayPort int, opt Options) (*exec.Cmd, error) {
	bin := ExecPath(chromium)
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("找不到 Chromium: %s", bin)
	}
	if err := os.MkdirAll(e.BrowserDir(), 0o700); err != nil {
		return nil, err
	}
	args := Args(e, relayPort)
	if len(opt.URLs) > 0 || len(opt.Extra) > 0 {
		args = args[:len(args)-1] // 去掉 about:blank
		args = append(args, opt.Extra...)
		if len(opt.URLs) > 0 {
			args = append(args, opt.URLs...)
		} else {
			args = append(args, "about:blank")
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = Env(e)
	if err := os.MkdirAll(e.LogDir(), 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(e.LogDir(), "chromium.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
		}
	}
	return cmd, cmd.Start()
}
