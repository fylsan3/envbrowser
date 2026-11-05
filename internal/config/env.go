// Package config 定义环境配置（env.json）与全局设置（settings.json），并负责读写。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

// Env 一个环境 = 一个账号 = 一套设备指纹 = 一条代理。全部信息存在同一个 env.json。
type Env struct {
	SchemaVersion   int             `json:"schema_version"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	CreatedAt       time.Time       `json:"created_at"`
	Account         Account         `json:"account"`
	Proxy           ProxyConfig     `json:"proxy"`
	Fingerprint     Fingerprint     `json:"fingerprint"`
	Modules         map[string]bool `json:"modules"`
	ChromiumVersion string          `json:"chromium_version,omitempty"`

	dir string // 环境目录，不写入 JSON
}

type Account struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note"`
}

type ProxyConfig struct {
	Primary   *Upstream `json:"primary"`
	Secondary *Upstream `json:"secondary,omitempty"`
	Active    string    `json:"active"`  // "primary" | "secondary"，只能在环境关闭时修改
	ExitIP    string    `json:"exit_ip"` // 指定出口 IP，实际出口必须与之相同
}

// Upstream 上游代理。socks5h 与 http 都必须带用户名密码。
type Upstream struct {
	Type     string `json:"type"` // "socks5h" | "http"
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Fingerprint struct {
	Template  string   `json:"template,omitempty"`
	Seed      Seed     `json:"seed"`
	Timezone  string   `json:"timezone"`
	Languages []string `json:"languages"`
}

// Seed 由程序生成，与环境永久绑定。阶段一只有窗口尺寸生效，其余项待内核积木。
type Seed struct {
	CanvasNoise    string `json:"canvas_noise"`
	AudioNoise     string `json:"audio_noise"`
	MediaDeviceIDs string `json:"media_device_ids"`
	Window         Window `json:"window"`
}

type Window struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (e *Env) Dir() string        { return e.dir }
func (e *Env) BrowserDir() string { return filepath.Join(e.dir, "browser") }
func (e *Env) LogDir() string     { return filepath.Join(e.dir, "logs") }

// ActiveUpstream 返回当前生效的上游。不会自动切换。
func (e *Env) ActiveUpstream() (*Upstream, error) {
	switch e.Proxy.Active {
	case "", "primary":
		if e.Proxy.Primary == nil {
			return nil, errors.New("未配置主代理")
		}
		return e.Proxy.Primary, nil
	case "secondary":
		if e.Proxy.Secondary == nil {
			return nil, errors.New("active=secondary 但未配置备用代理")
		}
		return e.Proxy.Secondary, nil
	}
	return nil, fmt.Errorf("proxy.active 取值无效: %q", e.Proxy.Active)
}

func (u *Upstream) Addr() string { return net.JoinHostPort(u.Host, strconv.Itoa(u.Port)) }

// Redacted 用于日志，隐藏密码。
func (u *Upstream) Redacted() string {
	return fmt.Sprintf("%s://%s:***@%s", u.Type, u.Username, u.Addr())
}

func (u *Upstream) Validate() error {
	if u.Type != "socks5h" && u.Type != "http" {
		return fmt.Errorf("代理类型只支持 socks5h / http，收到 %q", u.Type)
	}
	if u.Host == "" || u.Port <= 0 || u.Port > 65535 {
		return errors.New("代理地址或端口无效")
	}
	if u.Username == "" || u.Password == "" {
		return errors.New("代理必须填写用户名和密码")
	}
	if u.Type == "socks5h" && (len(u.Username) > 255 || len(u.Password) > 255) {
		return errors.New("socks5h 用户名和密码长度不能超过 255 字节")
	}
	return nil
}

// ParseUpstream 解析 socks5h://user:pass@host:port 或 http://user:pass@host:port。
func ParseUpstream(s string) (*Upstream, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("代理地址格式错误: %w", err)
	}
	t := u.Scheme
	if t == "socks5" {
		t = "socks5h" // 统一按远端解析处理
	}
	port, _ := strconv.Atoi(u.Port())
	up := &Upstream{Type: t, Host: u.Hostname(), Port: port}
	if u.User != nil {
		up.Username = u.User.Username()
		up.Password, _ = u.User.Password()
	}
	return up, up.Validate()
}

// Validate 检查环境配置本身是否完整。积木级的检查在 brick 包里。
func (e *Env) Validate() error {
	if e.Proxy.Primary == nil {
		return errors.New("必须配置主代理")
	}
	if err := e.Proxy.Primary.Validate(); err != nil {
		return fmt.Errorf("主代理: %w", err)
	}
	if e.Proxy.Secondary != nil {
		if err := e.Proxy.Secondary.Validate(); err != nil {
			return fmt.Errorf("备用代理: %w", err)
		}
	}
	if _, err := e.ActiveUpstream(); err != nil {
		return err
	}
	if net.ParseIP(e.Proxy.ExitIP) == nil {
		return fmt.Errorf("指定出口 IP 无效: %q", e.Proxy.ExitIP)
	}
	if _, err := time.LoadLocation(e.Fingerprint.Timezone); err != nil {
		return fmt.Errorf("时区无效: %q", e.Fingerprint.Timezone)
	}
	return nil
}

// NewSeed 生成随机种子。
func NewSeed(w Window) Seed {
	return Seed{CanvasNoise: randHex(16), AudioNoise: randHex(16), MediaDeviceIDs: randHex(16), Window: w}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// writeJSONAtomic 先写临时文件再改名，避免写一半损坏。
func writeJSONAtomic(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SplitLang 把 "ja-JP" 变成 ["ja-JP","ja"]。
func SplitLang(lang string) []string {
	out := []string{lang}
	if i := strings.IndexByte(lang, '-'); i > 0 {
		out = append(out, lang[:i])
	}
	return out
}
