package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Settings 全局设置，存于 <根目录>/settings.json。
type Settings struct {
	ChromiumPath   string   `json:"chromium_path"`
	ChromiumVer    string   `json:"chromium_version"`
	AdminAddr      string   `json:"admin_addr"` // 管理页面，默认 127.0.0.1:8800
	PortMin        int      `json:"port_min"`   // 中转端口范围
	PortMax        int      `json:"port_max"`
	RecheckMinSec  int      `json:"recheck_min_sec"` // 出口复查随机间隔
	RecheckMaxSec  int      `json:"recheck_max_sec"`
	RecheckFailMax int      `json:"recheck_fail_max"` // 连续几次全部失败才断网
	EchoServices   []string `json:"echo_services"`    // 公共出口 IP 回显服务
	HeaderEchoURL  string   `json:"header_echo_url"`  // 明文 http 请求头检查
	TestMode       bool     `json:"test_mode"`        // 测试模式，默认关
	TestTarget     string   `json:"test_target"`      // 测试模式下唯一放行的本机地址
	MemoryWarnN    int      `json:"memory_warn_n"`
	PFEnabled      bool     `json:"pf_enabled"` // pf 防火墙开关，默认关；暂未排入实施阶段
	DefaultTZ      string   `json:"default_timezone"`
}

func DefaultSettings() Settings {
	return Settings{
		AdminAddr:      "127.0.0.1:8800",
		PortMin:        20001,
		PortMax:        20999,
		RecheckMinSec:  1,
		RecheckMaxSec:  10,
		RecheckFailMax: 3,
		EchoServices: []string{
			"https://www.cloudflare.com/cdn-cgi/trace",
			"https://api.ipify.org",
			"https://icanhazip.com",
			"https://ifconfig.me/ip",
		},
		HeaderEchoURL: "http://httpbin.org/headers",
		TestTarget:    "127.0.0.1:9000",
		MemoryWarnN:   10,
		DefaultTZ:     "Asia/Tokyo",
		ChromiumVer:   "148.0.7778.215",
	}
}

// Store 管理数据根目录（默认 ~/BrowserEnvs）。
type Store struct {
	Root string
}

func DefaultRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "BrowserEnvs")
}

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	for _, d := range []string{"templates", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{Root: root}, nil
}

func (s *Store) settingsPath() string { return filepath.Join(s.Root, "settings.json") }

func (s *Store) LoadSettings() (Settings, error) {
	st := DefaultSettings()
	data, err := os.ReadFile(s.settingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return st, writeJSONAtomic(s.settingsPath(), st, 0o600)
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("settings.json 格式错误: %w", err)
	}
	return st, nil
}

func (s *Store) SaveSettings(st Settings) error {
	return writeJSONAtomic(s.settingsPath(), st, 0o600)
}

var idRe = regexp.MustCompile(`^env-\d{4,}$`)

func (s *Store) envPath(id string) string { return filepath.Join(s.Root, id, "env.json") }

func (s *Store) Load(id string) (*Env, error) {
	if !idRe.MatchString(id) {
		return nil, fmt.Errorf("环境 ID 无效: %q", id)
	}
	data, err := os.ReadFile(s.envPath(id))
	if err != nil {
		return nil, err
	}
	var e Env
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("%s/env.json 格式错误: %w", id, err)
	}
	e.dir = filepath.Join(s.Root, id)
	switch e.SchemaVersion {
	case SchemaVersion:
	case 1:
		if err := s.migrateV1(&e, data); err != nil {
			return nil, fmt.Errorf("%s: 从版本 1 迁移失败: %w", id, err)
		}
	default:
		return nil, fmt.Errorf("%s: 不支持的配置版本 %d", id, e.SchemaVersion)
	}
	return &e, nil
}

// migrateV1 把 v1 环境升级到 v2：备份原文件，选模板、重新生成种子、换新积木表。
// v1 的 canvas_noise 等字段在 fingerprint-chromium 下无对应参数，直接丢弃。
func (s *Store) migrateV1(e *Env, raw []byte) error {
	bak := filepath.Join(e.dir, "env.v1.json.bak")
	if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(bak, raw, 0o600); err != nil {
			return err
		}
	}
	ts, err := s.Templates()
	if err != nil {
		return err
	}
	used := s.usage()
	t, err := pickTemplate(ts, used.templates)
	if err != nil {
		return err
	}
	e.SchemaVersion = SchemaVersion
	e.Fingerprint.Template = t.Name
	e.Fingerprint.PlatformVersion = t.PlatformVersion
	e.Fingerprint.HardwareConcurrency = t.HardwareConcurrency
	e.Fingerprint.Seed = NewSeed(e.Fingerprint.Seed.Window, used.seeds)
	// v1 阶段一已生效的开关保留使用者的值，其余用 v2 默认值。
	m := DefaultModules()
	for _, k := range []string{"isolation", "network_guard", "timezone", "language", "window", "webrtc"} {
		if v, ok := e.Modules[k]; ok {
			m[k] = v
		}
	}
	e.Modules = m
	return s.Save(e)
}

type usage struct {
	templates map[string]int
	seeds     map[int64]bool
}

// usage 直接读各环境 env.json 的种子和模板，不经过 Load（避免迁移时递归）。
func (s *Store) usage() usage {
	u := usage{templates: map[string]int{}, seeds: map[int64]bool{}}
	entries, _ := os.ReadDir(s.Root)
	for _, en := range entries {
		if !en.IsDir() || !idRe.MatchString(en.Name()) {
			continue
		}
		data, err := os.ReadFile(s.envPath(en.Name()))
		if err != nil {
			continue
		}
		var e Env
		if json.Unmarshal(data, &e) != nil {
			continue
		}
		if e.Fingerprint.Template != "" {
			u.templates[e.Fingerprint.Template]++
		}
		if e.Fingerprint.Seed.Fingerprint > 0 {
			u.seeds[e.Fingerprint.Seed.Fingerprint] = true
		}
	}
	return u
}

// TemplateUsage 每个模板被几个环境使用。
func (s *Store) TemplateUsage() map[string]int { return s.usage().templates }

func (s *Store) Save(e *Env) error {
	if e.dir == "" {
		e.dir = filepath.Join(s.Root, e.ID)
	}
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(s.envPath(e.ID), e, 0o600)
}

func (s *Store) List() ([]*Env, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	var out []*Env
	for _, en := range entries {
		if !en.IsDir() || !idRe.MatchString(en.Name()) {
			continue
		}
		e, err := s.Load(en.Name())
		if err != nil {
			continue // 损坏的环境不阻塞列表，由 Load 单独报错
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// NextID 生成下一个环境 ID。
func (s *Store) NextID() (string, error) {
	envs, err := os.ReadDir(s.Root)
	if err != nil {
		return "", err
	}
	max := 0
	for _, en := range envs {
		var n int
		if _, err := fmt.Sscanf(en.Name(), "env-%d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("env-%04d", max+1), nil
}

// NewEnvInput 新建环境所需的字段。
type NewEnvInput struct {
	Name      string
	Account   Account
	Primary   *Upstream
	Secondary *Upstream
	ExitIP    string // 为空时取主代理地址
	// ExitCountry 已知的出口国家代码（自动匹配时区时取得），为空则首次打开时记录
	ExitCountry string
	Timezone    string
	Language    string // 为空时按时区默认
	Window      Window
	// Modules 为空时用默认积木；复制环境时传入原环境的积木
	Modules map[string]bool
}

func (s *Store) Create(in NewEnvInput, st Settings) (*Env, error) {
	id, err := s.NextID()
	if err != nil {
		return nil, err
	}
	tz := in.Timezone
	if tz == "" {
		tz = st.DefaultTZ
	}
	lang := in.Language
	if lang == "" {
		lang = DefaultLanguage(tz)
	}
	exit := in.ExitIP
	if exit == "" && in.Primary != nil {
		exit = in.Primary.Host
	}
	w := in.Window
	if w.Width == 0 {
		w = Window{Width: 1440, Height: 900}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = id
	}
	ts, err := s.Templates()
	if err != nil {
		return nil, err
	}
	used := s.usage()
	t, err := pickTemplate(ts, used.templates)
	if err != nil {
		return nil, err
	}
	e := &Env{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Name:          name,
		CreatedAt:     time.Now().Truncate(time.Second),
		Account:       in.Account,
		Proxy:         ProxyConfig{Primary: in.Primary, Secondary: in.Secondary, Active: "primary", ExitIP: exit, ExitCountry: in.ExitCountry},
		Fingerprint: Fingerprint{
			Template:            t.Name,
			Seed:                NewSeed(w, used.seeds),
			PlatformVersion:     t.PlatformVersion,
			HardwareConcurrency: t.HardwareConcurrency,
			Timezone:            tz,
			Languages:           SplitLang(lang),
		},
		Modules:         mergeModules(in.Modules),
		ChromiumVersion: st.ChromiumVer,
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, s.Save(e)
}

// mergeModules 以默认积木为底，覆盖传入的值（只认已知积木名）。
func mergeModules(in map[string]bool) map[string]bool {
	m := DefaultModules()
	for k := range m {
		if v, ok := in[k]; ok {
			m[k] = v
		}
	}
	return m
}

// DefaultModules v2 积木默认值。值为 false 的指纹积木会进入 --disable-spoofing。
// gpu 默认 false：显卡显示真机型号（D24）。
func DefaultModules() map[string]bool {
	return map[string]bool{
		"isolation": true, "network_guard": true, "fingerprint": true,
		"timezone": true, "language": true, "window": true, "webrtc": true,
		"ua_client_hints": true, "hardware": true,
		"canvas": true, "audio": true, "fonts": true, "clientrects": true,
		"gpu": false,
	}
}
