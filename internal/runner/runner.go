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
// save 用于把首次取得的出口国家写回 env.json；可为 nil。
func Open(e *config.Env, st config.Settings, local net.IP, save func(*config.Env) error) (*Session, error) {
	return OpenWith(e, st, local, save, launcher.Options{})
}

// OpenWith 同 Open，可指定启动后打开的网址（检测页用）和附加参数（只给测试用）。
func OpenWith(e *config.Env, st config.Settings, local net.IP, save func(*config.Env) error, opt launcher.Options) (*Session, error) {
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

	// 出口国家：经 Cloudflare 取 loc=。已记录过且不同 → 不启动；首次 → 记录。
	// 取不到（Cloudflare 不可用）时不阻止启动，由运行中复查补记。
	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
	cc, cerr := egress.Country(cctx, up)
	ccancel()
	switch {
	case cerr != nil:
		s.Logf("未取得出口国家代码（不影响启动，复查时再取）: %v", cerr)
	case egress.CheckCountry(e.Proxy.ExitCountry, cc) != nil:
		return fail(fmt.Errorf("出口校验未通过，不启动浏览器: %w", egress.CheckCountry(e.Proxy.ExitCountry, cc)))
	default:
		s.Logf("出口国家 %s", cc)
		recordCountry(e, cc, save, s.Logf)
	}
	if want := config.CountryOfZone(e.Fingerprint.Timezone); want != "" && e.Proxy.ExitCountry != "" && want != e.Proxy.ExitCountry {
		s.Logf("提示: 时区 %s 属于 %s，与出口国家 %s 不符（不拦截）", e.Fingerprint.Timezone, want, e.Proxy.ExitCountry)
	}

	cmd, err := launcher.StartWith(st.ChromiumPath, e, r.Port(), opt)
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
		Country:   e.Proxy.ExitCountry,
		OnCountry: func(cc string) { recordCountry(e, cc, save, s.Logf) },
		MinSec:    st.RecheckMinSec, MaxSec: st.RecheckMaxSec, FailMax: st.RecheckFailMax,
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
		// 被要求关闭时结束浏览器进程：先 SIGTERM，5 秒内没退出再 SIGKILL
		// （断网后浏览器可能卡在网络请求上，迟迟不退出）。
		// 用 s.done 判断浏览器是否已退出；不读 cmd.ProcessState（它由 Wait 写入，并发读会数据竞争）。
		select {
		case <-s.done:
			return
		default:
		}
		{
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-s.done:
			case <-time.After(KillAfter):
				s.Logf("浏览器 %s 内未退出，强制结束", KillAfter)
				cmd.Process.Kill()
			}
		}
	}()
	return s, nil
}

// KillAfter 关闭环境时等浏览器自行退出的时间，超时强制结束。
var KillAfter = 5 * time.Second

func (s *Session) Status0() string { st, _, _ := s.Status(); return st }

// Fake 只给其他包的测试用：造一个指定状态的会话，不启动中转和浏览器。
func Fake(e *config.Env, status string) *Session {
	return &Session{Env: e, status: status, done: make(chan struct{})}
}

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

// recordCountry 首次记录出口国家并保存。已有值时不覆盖（变化由校验负责断网）。
func recordCountry(e *config.Env, cc string, save func(*config.Env) error, logf func(string, ...any)) {
	if e.Proxy.ExitCountry != "" || cc == "" {
		return
	}
	e.Proxy.ExitCountry = cc
	if save != nil {
		if err := save(e); err != nil {
			logf("警告: 保存出口国家失败: %v", err)
		}
	}
}

func isSignal(err error) bool {
	var ee interface{ ExitCode() int }
	return errors.As(err, &ee) && ee.ExitCode() == -1
}
