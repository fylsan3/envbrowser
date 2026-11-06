package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testUp() *Upstream {
	up, _ := ParseUpstream("socks5h://u:p@203.0.113.1:1080")
	return up
}

func TestBuiltinTemplates(t *testing.T) {
	ts, err := newTestStore(t).Templates()
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 4 {
		t.Fatalf("内置模板应为 4 套，实际 %d", len(ts))
	}
	for _, x := range ts {
		if x.Chip != "Apple M2" || x.HardwareConcurrency != 8 {
			t.Errorf("模板 %s 应为 M2 / 8 核", x.Name)
		}
	}
}

// 新建 4 个环境应正好轮换到 4 套不同模板，种子各不相同。
func TestCreateRotatesTemplatesAndUniqueSeeds(t *testing.T) {
	s := newTestStore(t)
	tpl := map[string]bool{}
	seeds := map[int64]bool{}
	for i := 0; i < 4; i++ {
		e, err := s.Create(NewEnvInput{Primary: testUp()}, DefaultSettings())
		if err != nil {
			t.Fatal(err)
		}
		fp := e.Fingerprint
		if fp.PlatformVersion == "" || fp.HardwareConcurrency != 8 {
			t.Fatalf("未取模板值: %+v", fp)
		}
		if fp.Seed.Fingerprint < 1 || fp.Seed.Fingerprint > MaxSeed {
			t.Fatalf("种子越界: %d", fp.Seed.Fingerprint)
		}
		tpl[fp.Template] = true
		seeds[fp.Seed.Fingerprint] = true
	}
	if len(tpl) != 4 || len(seeds) != 4 {
		t.Fatalf("模板 %d 套、种子 %d 个，应各为 4", len(tpl), len(seeds))
	}
}

// 管理页面并发新建：编号、种子都不能重复，4 套模板各用 2 次。
func TestCreateConcurrent(t *testing.T) {
	s := newTestStore(t)
	const n = 8
	var wg sync.WaitGroup
	envs := make([]*Env, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			envs[i], errs[i] = s.Create(NewEnvInput{Primary: testUp()}, DefaultSettings())
		}(i)
	}
	wg.Wait()
	ids, seeds, tpl := map[string]bool{}, map[int64]bool{}, map[string]int{}
	for i, e := range envs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		ids[e.ID] = true
		seeds[e.Fingerprint.Seed.Fingerprint] = true
		tpl[e.Fingerprint.Template]++
	}
	if len(ids) != n || len(seeds) != n {
		t.Fatalf("编号 %d 个、种子 %d 个，应各为 %d", len(ids), len(seeds), n)
	}
	for k, c := range tpl {
		if c != 2 {
			t.Errorf("模板 %s 用了 %d 次，应为 2", k, c)
		}
	}
}

func TestNewSeedAvoidsUsed(t *testing.T) {
	used := map[int64]bool{}
	for i := 0; i < 1000; i++ {
		s := NewSeed(Window{}, used)
		if used[s.Fingerprint] {
			t.Fatal("种子重复")
		}
		used[s.Fingerprint] = true
	}
}

func TestPickTemplateSkipsDisabled(t *testing.T) {
	ts := []Template{{Name: "a", Enabled: false}, {Name: "b", Enabled: true}}
	for i := 0; i < 20; i++ {
		x, err := pickTemplate(ts, map[string]int{"b": 5})
		if err != nil || x.Name != "b" {
			t.Fatal("停用模板不应被选中")
		}
	}
	if _, err := pickTemplate([]Template{{Name: "a"}}, nil); err == nil {
		t.Fatal("无启用模板时应报错")
	}
}

// v1 环境读取时自动迁移到 v2，并保留备份。
func TestMigrateV1(t *testing.T) {
	s := newTestStore(t)
	v1 := map[string]any{
		"schema_version": 1, "id": "env-0001", "name": "老环境",
		"proxy": map[string]any{"primary": testUp(), "active": "primary", "exit_ip": "203.0.113.1"},
		"fingerprint": map[string]any{
			"seed": map[string]any{"canvas_noise": "aa", "audio_noise": "bb", "media_device_ids": "cc",
				"window": map[string]int{"width": 1280, "height": 800}},
			"timezone": "Asia/Tokyo", "languages": []string{"ja-JP", "ja"},
		},
		"modules": map[string]bool{"isolation": true, "network_guard": true, "timezone": true,
			"language": true, "window": false, "webrtc": true, "canvas": false},
	}
	dir := filepath.Join(s.Root, "env-0001")
	os.MkdirAll(dir, 0o700)
	data, _ := json.Marshal(v1)
	os.WriteFile(filepath.Join(dir, "env.json"), data, 0o600)

	e, err := s.Load("env-0001")
	if err != nil {
		t.Fatal(err)
	}
	if e.SchemaVersion != 2 || e.Fingerprint.Template == "" || e.Fingerprint.Seed.Fingerprint == 0 {
		t.Fatalf("迁移不完整: %+v", e.Fingerprint)
	}
	if e.Fingerprint.Seed.Window.Width != 1280 {
		t.Error("窗口尺寸应保留")
	}
	if e.Modules["window"] {
		t.Error("v1 中使用者关闭的 window 应保持关闭")
	}
	if !e.Modules["canvas"] || e.Modules["gpu"] {
		t.Error("指纹积木应换成 v2 默认值")
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "env.v1.json.bak")); err != nil {
		t.Error("应保留 v1 备份")
	}
	// 再次读取不应再变（已落盘为 v2）
	e2, _ := s.Load("env-0001")
	if e2.Fingerprint.Seed.Fingerprint != e.Fingerprint.Seed.Fingerprint {
		t.Error("迁移结果应已保存")
	}
}

func TestZonesForCountry(t *testing.T) {
	if zs := ZonesForCountry("jp"); len(zs) != 1 || zs[0].TZ != "Asia/Tokyo" {
		t.Errorf("JP 应唯一对应东京: %v", zs)
	}
	if zs := ZonesForCountry("US"); len(zs) != 4 {
		t.Errorf("US 应有 4 个候选时区: %d", len(zs))
	}
	if len(ZonesForCountry("DE")) != 0 {
		t.Error("不在列表中的国家应返回空")
	}
	for _, z := range Zones {
		if len(z.Country) != 2 {
			t.Errorf("%s 缺国家代码", z.TZ)
		}
	}
	if CountryOfZone("Asia/Taipei") != "TW" {
		t.Error("CountryOfZone 错误")
	}
}
