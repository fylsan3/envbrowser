package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"envbrowser/internal/config"
)

func testEnv(t *testing.T) *config.Env {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	up, _ := config.ParseUpstream("socks5h://u:p@203.0.113.1:1080")
	e, err := store.Create(config.NewEnvInput{Name: "t", Primary: up}, config.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestArgs(t *testing.T) {
	e := testEnv(t)
	fp := e.Fingerprint
	a := strings.Join(Args(e, 20001), " ")
	for _, want := range []string{
		"--proxy-server=socks5://127.0.0.1:20001",
		"--proxy-bypass-list=<-loopback>",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-quic",
		"--timezone=Asia/Tokyo",
		"--lang=ja-JP",
		"--window-size=1440,900",
		"--user-data-dir=" + e.BrowserDir(),
		"--fingerprint=" + strconv.FormatInt(fp.Seed.Fingerprint, 10),
		"--fingerprint-platform=macos",
		"--fingerprint-brand=Chrome",
		"--fingerprint-platform-version=" + fp.PlatformVersion,
		"--fingerprint-hardware-concurrency=8",
		"--disable-spoofing=gpu",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("缺少参数 %s", want)
		}
	}
	for _, kv := range Env(e) {
		if strings.HasPrefix(kv, "TZ=") || strings.HasPrefix(strings.ToLower(kv), "all_proxy=") {
			t.Errorf("环境变量不应包含 %s", kv)
		}
	}

	// 关闭 canvas、打开 gpu
	e.Modules["canvas"], e.Modules["gpu"] = false, true
	a = strings.Join(Args(e, 20001), " ")
	if !strings.Contains(a, "--disable-spoofing=canvas ") {
		t.Errorf("disable-spoofing 应只含 canvas: %s", a)
	}
	// 指纹总开关关闭：不传任何 --fingerprint 系列参数
	e.Modules["fingerprint"] = false
	a = strings.Join(Args(e, 20001), " ")
	if strings.Contains(a, "--fingerprint") || strings.Contains(a, "--disable-spoofing") {
		t.Errorf("fingerprint 关闭时不应有指纹参数: %s", a)
	}
}

func TestEnvTZCleared(t *testing.T) {
	t.Setenv("TZ", "UTC")
	e := testEnv(t)
	for _, kv := range Env(e) {
		if strings.HasPrefix(kv, "TZ=") {
			t.Fatal("TZ 应被清除")
		}
	}
}

func TestLock(t *testing.T) {
	e := testEnv(t)
	l, err := AcquireLock(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(e); err == nil {
		t.Fatal("同一环境不应能加锁两次")
	}
	l.Release()
	// 残留锁：写一个不存在的 PID
	os.WriteFile(filepath.Join(e.Dir(), "env.lock"), []byte("999999\n"), 0o600)
	l2, err := AcquireLock(e)
	if err != nil {
		t.Fatalf("残留锁应自动清除: %v", err)
	}
	l2.Release()
}

// fakeBrowser 启动一个命令行里带本环境 --user-data-dir 的长驻进程，冒充浏览器。
// 立即在后台 Wait，被结束后马上回收，不留僵尸进程（真实孤儿由系统 launchd 回收）。
type fake struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func fakeBrowser(t *testing.T, e *config.Env) *fake {
	t.Helper()
	// "; :" 防止 sh 直接 exec 成 sleep，导致命令行参数丢失
	cmd := exec.Command("/bin/sh", "-c", "sleep 60; :", "sh", userDataArg(e.BrowserDir()))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &fake{cmd: cmd, exited: make(chan struct{})}
	go func() { cmd.Wait(); close(f.exited) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-f.exited })
	for i := 0; i < 20 && !isEnvBrowser(cmd.Process.Pid, e); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	return f
}

func (f *fake) pid() int { return f.cmd.Process.Pid }

func (f *fake) waitExit() bool {
	select {
	case <-f.exited:
		return true
	case <-time.After(8 * time.Second):
		return false
	}
}

// 主程序崩溃后锁里留着浏览器 PID：再次加锁时应结束残留浏览器。
func TestLockKillsLeftoverBrowser(t *testing.T) {
	e := testEnv(t)
	b := fakeBrowser(t, e)
	if !isEnvBrowser(b.pid(), e) {
		t.Skip("当前系统 ps 看不到进程参数")
	}
	lock := fmt.Sprintf("999999\n%d\n", b.pid()) // 主程序已死
	os.WriteFile(filepath.Join(e.Dir(), "env.lock"), []byte(lock), 0o600)
	l, err := AcquireLock(e)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if !b.waitExit() {
		t.Fatal("残留浏览器应被结束")
	}
}

// 锁里的浏览器 PID 已被别的进程复用：不能误杀。
func TestLockNoKillForeignPID(t *testing.T) {
	e := testEnv(t)
	other := exec.Command("sleep", "60")
	other.Start()
	defer func() { other.Process.Kill(); other.Wait() }()
	os.WriteFile(filepath.Join(e.Dir(), "env.lock"), []byte(fmt.Sprintf("999999\n%d\n", other.Process.Pid)), 0o600)
	l, err := AcquireLock(e)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if !processAlive(other.Process.Pid) {
		t.Fatal("不属于本环境的进程被误杀")
	}
}

func TestSetBrowserPID(t *testing.T) {
	e := testEnv(t)
	l, err := AcquireLock(e)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	l.SetBrowserPID(4242)
	owner, browser := readLock(filepath.Join(e.Dir(), "env.lock"))
	if owner != os.Getpid() || browser != 4242 {
		t.Fatalf("锁内容错误: %d %d", owner, browser)
	}
}

func TestFindOrphans(t *testing.T) {
	e := testEnv(t)
	b := fakeBrowser(t, e)
	if !isEnvBrowser(b.pid(), e) {
		t.Skip("当前系统 ps 看不到进程参数")
	}
	root := filepath.Dir(e.Dir())
	find := func() bool {
		os, err := FindOrphans(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range os {
			if o.PID == b.pid() && o.EnvID == e.ID {
				return true
			}
		}
		return false
	}
	if !find() {
		t.Fatal("没有锁的浏览器应判为孤儿")
	}
	// 本进程持锁时不算孤儿
	l, _ := AcquireLock(e)
	if find() {
		t.Fatal("有存活主程序持锁时不应判为孤儿")
	}
	l.Release()
	killed, err := KillOrphans(root)
	if err != nil || len(killed) != 1 {
		t.Fatalf("KillOrphans: %v %v", killed, err)
	}
	if !b.waitExit() {
		t.Fatal("孤儿未被结束")
	}
}
