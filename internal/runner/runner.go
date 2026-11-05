// Package runner 把一个环境的完整生命周期串起来：
// 加锁 → 启动中转 → 首次出口校验（不通过不启动浏览器）→ 启动 Chromium → 运行中复查 → 关闭清理。
package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"envbrowser/internal/config"
	"envbrowser/internal/egress"
	"envbrowser/internal/launcher"
	"envbrowser/internal/relay"
)

type Session struct {
	Env   *config.Env
	Relay *relay.Relay

	logger *log.Logger
	lock   *launcher.Lock
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	status  string
	lastIP  string
	lastErr string
}

func (s *Session) Logf(format string, args ...any) { s.logger.Printf(format, args...) }

func (s *Session) setStatus(st, errMsg string) {
	s.mu.Lock()
	s.status, s.lastErr = st, errMsg
	s.mu.Unlock()
}

// Status 返回 状态、最近出口 IP、最近错误。
func (s *Session) Status() (string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.lastIP, s.lastErr
}

func (s *Session) Done() <-chan struct{} { return s.done }

func newLogger(e *config.Env) (*log.Logger, *os.File) {
	os.MkdirAll(e.LogDir(), 0o700)
	f, err := os.OpenFile(filepath.Join(e.LogDir(), "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return log.New(os.Stderr, "["+e.ID+"] ", log.LstdFlags), nil
	}
	return log.New(multiWriter{f, os.Stderr}, "["+e.ID+"] ", log.LstdFlags), f
}

type multiWriter []interface{ Write([]byte) (int, error) }

func (m multiWriter) Write(p []byte) (int, error) {
	for _, w := range m {
		w.Write(p)
	}
	return len(p), nil
}

// Open 打开一个环境。成功返回时浏览器已启动；浏览器退出后 Done() 关闭。
func Open(e *config.Env, st config.Settings, local net.IP) (*Session, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	up, _ := e.ActiveUpstream()
	logger, logFile := newLogger(e)
	s := &Session{Env: e, logger: logger, done: make(chan struct{}), status: "starting"}

	lock, err := launcher.AcquireLock(e)
	if err != nil {
		return nil, err
	}
	s.lock = lock
	fail := func(err error) (*Session, error) {
		if s.Relay != nil {
			s.Relay.Close()
		}
		lock.Release()
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}

	// 先结束所有孤儿浏览器（主程序异常退出后残留的），再分配中转端口，
	// 避免孤儿浏览器连上新分配给别的环境的端口而串号。
	if killed, err := launcher.KillOrphans(filepath.Dir(e.Dir())); err != nil {
		return fail(fmt.Errorf("清理残留浏览器失败，为防串号不启动: %w", err))
	} else {
		for _, o := range killed {
			s.Logf("已结束残留浏览器 %s PID %d", o.EnvID, o.PID)
		}
	}

	r, err := relay.Start(e.ID, *up, st.PortMin, st.PortMax, s.Logf)
	if err != nil {
		return fail(err)
	}
	s.Relay = r
	if st.TestMode && st.TestTarget != "" {
		r.SetTestTarget(st.TestTarget)
		s.Logf("测试模式开启，放行本机检测页 %s", st.TestTarget)
	}
	s.Logf("中转 127.0.0.1:%d → %s", r.Port(), up.Redacted())

	// 首次出口校验：不通过不启动浏览器。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	res, err := egress.Check(ctx, up, e.Proxy.ExitIP, local, st.EchoServices, 0)
	cancel()
	if err != nil {
		return fail(fmt.Errorf("出口校验未通过，不启动浏览器: %w", err))
	}
	s.lastIP = res.ExitIP.String()
	s.Logf("出口校验通过: %s（%s）", res.ExitIP, res.Service)

	cmd, err := launcher.Start(st.ChromiumPath, e, r.Port())
	if err != nil {
		return fail(err)
	}
	if err := lock.SetBrowserPID(cmd.Process.Pid); err != nil {
		s.Logf("警告: 写入浏览器 PID 失败: %v", err)
	}
	s.setStatus("running", "")
	s.Logf("Chromium 已启动 PID %d", cmd.Process.Pid)

	mctx, mcancel := context.WithCancel(context.Background())
	s.cancel = mcancel
	mon := &egress.Monitor{
		Upstream: up, Expected: e.Proxy.ExitIP, Local: local, Services: st.EchoServices,
		MinSec: st.RecheckMinSec, MaxSec: st.RecheckMaxSec, FailMax: st.RecheckFailMax,
		Logf: s.Logf,
		OnOK: func(res egress.Result) {
			s.mu.Lock()
			s.lastIP = res.ExitIP.String()
			s.mu.Unlock()
		},
		OnFail: func(reason string) {
			r.Block(reason)
			s.setStatus("blocked", reason)
		},
	}
	go mon.Run(mctx)

	go func() {
		err := cmd.Wait()
		mcancel()
		r.Close()
		lock.Release()
		if err != nil && !isSignal(err) {
			s.Logf("Chromium 退出: %v", err)
		} else {
			s.Logf("Chromium 已关闭")
		}
		s.setStatus("closed", "")
		if logFile != nil {
			logFile.Close()
		}
		close(s.done)
	}()
	go func() {
		<-mctx.Done()
		// 被要求关闭时结束浏览器进程
		if s.Status0() != "closed" && cmd.ProcessState == nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()
	return s, nil
}

func (s *Session) Status0() string { st, _, _ := s.Status(); return st }

// Close 请求关闭浏览器，并等待清理完成。
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
}

func isSignal(err error) bool {
	var ee interface{ ExitCode() int }
	return errors.As(err, &ee) && ee.ExitCode() == -1
}
