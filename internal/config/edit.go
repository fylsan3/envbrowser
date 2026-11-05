package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvEdit 管理页面“编辑环境”提交的字段。指针为 nil 表示不修改。
// 代理与指纹（时区、语言、积木）只能在环境关闭时修改，由调用方检查运行状态。
type EnvEdit struct {
	Name      *string
	Account   *Account
	Primary   *Upstream
	Secondary *Upstream // 传入 Type 为空的值表示删除备用代理
	Active    *string
	ExitIP    *string
	Timezone  *string
	Language  *string
	Modules   map[string]bool
}

// TouchesLocked 是否修改了运行中禁止修改的字段（代理、指纹）。
func (in EnvEdit) TouchesLocked() bool {
	return in.Primary != nil || in.Secondary != nil || in.Active != nil || in.ExitIP != nil ||
		in.Timezone != nil || in.Language != nil || in.Modules != nil
}

// Update 应用修改并保存。校验失败时不写盘。
func (s *Store) Update(id string, in EnvEdit) (*Env, error) {
	e, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		e.Name = strings.TrimSpace(*in.Name)
		if e.Name == "" {
			e.Name = e.ID
		}
	}
	if in.Account != nil {
		e.Account = *in.Account
	}
	if in.Primary != nil {
		p := *in.Primary
		e.Proxy.Primary = &p
	}
	if in.Secondary != nil {
		if in.Secondary.Type == "" {
			e.Proxy.Secondary = nil
		} else {
			p := *in.Secondary
			e.Proxy.Secondary = &p
		}
	}
	if in.Active != nil {
		e.Proxy.Active = *in.Active
	}
	if in.ExitIP != nil && *in.ExitIP != e.Proxy.ExitIP {
		e.Proxy.ExitIP = strings.TrimSpace(*in.ExitIP)
		e.Proxy.ExitCountry = "" // 出口换了，国家重新记录
	}
	if in.Timezone != nil {
		e.Fingerprint.Timezone = *in.Timezone
	}
	if in.Language != nil {
		lang := strings.TrimSpace(*in.Language)
		if lang == "" {
			lang = DefaultLanguage(e.Fingerprint.Timezone)
		}
		e.Fingerprint.Languages = SplitLang(lang)
	}
	if in.Modules != nil {
		e.Modules = mergeModules(in.Modules)
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, s.Save(e)
}

// RegenSeed 重新生成指纹种子（窗口大小保留），仍保证各环境不重复。
func (s *Store) RegenSeed(id string) (*Env, error) {
	e, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	used := s.usage().seeds
	e.Fingerprint.Seed = NewSeed(e.Fingerprint.Seed.Window, used)
	return e, s.Save(e)
}

// Trash 把整个环境目录移到 ~/.Trash（不直接删除），返回移动后的路径。
func (s *Store) Trash(id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("环境 ID 无效: %q", id)
	}
	src := filepath.Join(s.Root, id)
	if _, err := os.Stat(src); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	trash := filepath.Join(home, ".Trash")
	if d := os.Getenv("ENVBROWSER_TRASH"); d != "" { // 测试用
		trash = d
	}
	dst := filepath.Join(trash, fmt.Sprintf("%s-%s", id, time.Now().Format("20060102-150405")))
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("移到废纸篓失败（未删除任何文件）: %w", err)
	}
	return dst, nil
}

// SetTemplateEnabled 启用 / 停用模板。改动写到 <根目录>/templates/presets.json
// （首次修改时由内置模板生成这个文件）。停用不影响已绑定的环境。
func (s *Store) SetTemplateEnabled(name string, on bool) error {
	ts, err := s.Templates()
	if err != nil {
		return err
	}
	found := false
	for i := range ts {
		if ts[i].Name == name {
			ts[i].Enabled, found = on, true
		}
	}
	if !found {
		return fmt.Errorf("没有模板 %q", name)
	}
	if !on {
		n := 0
		for _, t := range ts {
			if t.Enabled {
				n++
			}
		}
		if n == 0 {
			return errors.New("至少要保留一个启用的模板")
		}
	}
	return s.saveTemplates(ts)
}

// AddTemplate 手动新增模板（需求规格第 11 节：填系统版本与核数）。
func (s *Store) AddTemplate(t Template) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || t.PlatformVersion == "" || t.HardwareConcurrency <= 0 {
		return errors.New("模板必须填写名称、系统版本和 CPU 核数")
	}
	ts, err := s.Templates()
	if err != nil {
		return err
	}
	for _, x := range ts {
		if x.Name == t.Name {
			return fmt.Errorf("模板 %q 已存在", t.Name)
		}
	}
	t.Enabled = true
	return s.saveTemplates(append(ts, t))
}

func (s *Store) saveTemplates(ts []Template) error {
	dir := filepath.Join(s.Root, "templates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(presetFile{Templates: ts}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "presets.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "presets.json"))
}
