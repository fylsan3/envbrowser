package admin

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/runner"
)

// fakeOpener 替换 openEnv：等 release 关闭后返回（模拟出口校验耗时），err 非空时返回失败。
type fakeOpener struct {
	release chan struct{}
	err     error
	closed  chan string
}

func (f *fakeOpener) open(e *config.Env, _ config.Settings, _ net.IP, _ func(*config.Env) error) (*runner.Session, error) {
	<-f.release
	if f.err != nil {
		return nil, f.err
	}
	return runner.Fake(e, "running"), nil
}

func useFake(t *testing.T, f *fakeOpener) {
	old := openEnv
	openEnv = f.open
	t.Cleanup(func() { openEnv = old })
}

func status(t *testing.T, s *Server, hd http.Handler, id string) envRow {
	t.Helper()
	w := do(t, s, hd, "GET", "/api/envs/"+id, "")
	var out struct {
		Row envRow `json:"row"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Row.ID == "" {
		// 兼容列表接口
		var rows []envRow
		json.Unmarshal(do(t, s, hd, "GET", "/api/envs", "").Body.Bytes(), &rows)
		for _, r := range rows {
			if r.ID == id {
				return r
			}
		}
	}
	return out.Row
}

func waitStatus(t *testing.T, s *Server, hd http.Handler, id, want string) envRow {
	t.Helper()
	var r envRow
	for i := 0; i < 100; i++ {
		if r = status(t, s, hd, id); r.Status == want {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s 状态应为 %s，实际 %s", id, want, r.Status)
	return r
}

// 点“打开”立即返回并显示启动中；启动中不能重复打开、编辑代理、删除；完成后变运行中。
func TestOpenShowsStarting(t *testing.T) {
	s, hd := newTestServer(t)
	f := &fakeOpener{release: make(chan struct{})}
	useFake(t, f)
	createEnv(t, s, hd, "")

	if w := do(t, s, hd, "POST", "/api/envs/env-0001/open", ""); w.Code != 202 {
		t.Fatalf("打开应立即返回 202，得到 %d: %s", w.Code, w.Body)
	}
	waitStatus(t, s, hd, "env-0001", "starting")
	if w := do(t, s, hd, "POST", "/api/envs/env-0001/open", ""); w.Code != 409 {
		t.Errorf("启动中重复打开应返回 409，得到 %d", w.Code)
	}
	for _, p := range []string{"/regen-seed", "/trash"} {
		if w := do(t, s, hd, "POST", "/api/envs/env-0001"+p, ""); w.Code != 409 {
			t.Errorf("启动中 %s 应返回 409，得到 %d", p, w.Code)
		}
	}
	close(f.release)
	waitStatus(t, s, hd, "env-0001", "running")
}

// 打开失败：回到未打开，并显示失败原因；再次打开时清除原因。
func TestOpenFailureShowsReason(t *testing.T) {
	s, hd := newTestServer(t)
	f := &fakeOpener{release: make(chan struct{}), err: errors.New("出口校验未通过，不启动浏览器: 测试")}
	close(f.release)
	useFake(t, f)
	createEnv(t, s, hd, "")

	do(t, s, hd, "POST", "/api/envs/env-0001/open", "")
	var r envRow
	for i := 0; i < 100; i++ {
		if r = status(t, s, hd, "env-0001"); r.LastErr != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.Status != "closed" || !strings.Contains(r.LastErr, "上次打开失败") || !strings.Contains(r.LastErr, "出口校验未通过") {
		t.Fatalf("失败后应为未打开并显示原因，得到 %+v", r)
	}

	f2 := &fakeOpener{release: make(chan struct{})}
	useFake(t, f2)
	do(t, s, hd, "POST", "/api/envs/env-0001/open", "")
	if r := waitStatus(t, s, hd, "env-0001", "starting"); r.LastErr != "" {
		t.Errorf("重新打开时应清除上次的失败原因，得到 %q", r.LastErr)
	}
	close(f2.release)
	waitStatus(t, s, hd, "env-0001", "running")
}

// 启动中点“取消”：启动完成后立即关闭，不留在运行列表里。
func TestCancelWhileStarting(t *testing.T) {
	s, hd := newTestServer(t)
	f := &fakeOpener{release: make(chan struct{})}
	useFake(t, f)
	createEnv(t, s, hd, "")

	do(t, s, hd, "POST", "/api/envs/env-0001/open", "")
	waitStatus(t, s, hd, "env-0001", "starting")
	if w := do(t, s, hd, "POST", "/api/envs/env-0001/close", ""); w.Code != 204 {
		t.Fatalf("取消应返回 204，得到 %d", w.Code)
	}
	close(f.release)
	time.Sleep(100 * time.Millisecond)
	if r := status(t, s, hd, "env-0001"); r.Status != "closed" {
		t.Fatalf("取消后应为未打开，得到 %s", r.Status)
	}
	s.mu.Lock()
	n := len(s.sess)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("取消后不应留下会话")
	}
}

// meta 带出内存提示阈值（页面据此提示）。
func TestMetaMemoryWarn(t *testing.T) {
	s, hd := newTestServer(t)
	var m map[string]any
	json.Unmarshal(do(t, s, hd, "GET", "/api/meta", "").Body.Bytes(), &m)
	if m["memory_warn_n"] != float64(config.DefaultSettings().MemoryWarnN) {
		t.Fatalf("meta 应包含 memory_warn_n，得到 %v", m["memory_warn_n"])
	}
}
