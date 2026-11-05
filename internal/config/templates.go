package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"envbrowser/templates"
)

// Template 一款真实 Mac 机型的公开规格。网页读不到机型，生效的只有系统版本与 CPU 核数。
type Template struct {
	Name                string  `json:"name"`
	Model               string  `json:"model"`
	Source              string  `json:"source"`
	Chip                string  `json:"chip"`
	HardwareConcurrency int     `json:"hardware_concurrency"`
	BuiltinDisplay      *string `json:"builtin_display"`
	PlatformVersion     string  `json:"platform_version"`
	Enabled             bool    `json:"enabled"`
}

type presetFile struct {
	Templates []Template `json:"templates"`
}

// Templates 读取模板：<根目录>/templates/presets.json 存在时优先使用，否则用程序内置的。
func (s *Store) Templates() ([]Template, error) {
	data, err := os.ReadFile(filepath.Join(s.Root, "templates", "presets.json"))
	if errors.Is(err, os.ErrNotExist) {
		data, err = templates.Presets, nil
	}
	if err != nil {
		return nil, err
	}
	var pf presetFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("presets.json 格式错误: %w", err)
	}
	for _, t := range pf.Templates {
		if t.Name == "" || t.PlatformVersion == "" || t.HardwareConcurrency <= 0 {
			return nil, fmt.Errorf("模板 %q 缺少系统版本或 CPU 核数", t.Name)
		}
	}
	return pf.Templates, nil
}

// pickTemplate 从已启用模板中选被使用次数最少的，次数相同时随机。
func pickTemplate(ts []Template, used map[string]int) (Template, error) {
	var best []Template
	min := -1
	for _, t := range ts {
		if !t.Enabled {
			continue
		}
		n := used[t.Name]
		switch {
		case min < 0 || n < min:
			min, best = n, []Template{t}
		case n == min:
			best = append(best, t)
		}
	}
	if len(best) == 0 {
		return Template{}, errors.New("没有已启用的模板")
	}
	return best[rand.IntN(len(best))], nil
}

// HostChip 返回本机芯片名，如 "Apple M2"。取不到时返回空字符串。
func HostChip() string {
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
