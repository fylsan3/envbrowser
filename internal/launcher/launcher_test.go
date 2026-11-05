package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	a := strings.Join(Args(e, 20001), " ")
	for _, want := range []string{
		"--proxy-server=socks5://127.0.0.1:20001",
		"--proxy-bypass-list=<-loopback>",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-quic",
		"--lang=ja-JP",
		"--window-size=1440,900",
		"--user-data-dir=" + e.BrowserDir(),
	} {
		if !strings.Contains(a, want) {
			t.Errorf("缺少参数 %s", want)
		}
	}
	envs := strings.Join(Env(e), "\n")
	if !strings.Contains(envs, "TZ=Asia/Tokyo") {
		t.Error("缺少 TZ")
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
