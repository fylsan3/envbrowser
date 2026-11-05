package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdate(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(NewEnvInput{Primary: testUp(), ExitCountry: "JP"}, DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	name, tz, lang, exit := "新名称", "America/New_York", "", "203.0.113.2"
	got, err := s.Update(e.ID, EnvEdit{Name: &name, Timezone: &tz, Language: &lang, ExitIP: &exit,
		Modules: map[string]bool{"canvas": false, "unknown": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.Fingerprint.Timezone != tz || got.Fingerprint.Languages[0] != "en-US" {
		t.Fatalf("字段未更新: %+v", got.Fingerprint)
	}
	if got.Proxy.ExitCountry != "" {
		t.Fatal("出口 IP 改了，出口国家应清空重新记录")
	}
	if got.Modules["canvas"] || !got.Modules["audio"] {
		t.Fatal("积木应以默认值为底覆盖传入值")
	}
	if _, ok := got.Modules["unknown"]; ok {
		t.Fatal("未知积木名不应写入")
	}
	if got.Fingerprint.Seed != e.Fingerprint.Seed || got.Fingerprint.Template != e.Fingerprint.Template {
		t.Fatal("编辑不应改动种子和模板")
	}

	bad := "Not/AZone"
	if _, err := s.Update(e.ID, EnvEdit{Timezone: &bad}); err == nil {
		t.Fatal("无效时区应报错")
	}
	again, _ := s.Load(e.ID)
	if again.Fingerprint.Timezone != tz {
		t.Fatal("校验失败时不应写盘")
	}
}

func TestEditTouchesLocked(t *testing.T) {
	n := "x"
	if (EnvEdit{Name: &n, Account: &Account{}}).TouchesLocked() {
		t.Fatal("只改名称和账号不应算锁定字段")
	}
	if !(EnvEdit{Timezone: &n}).TouchesLocked() || !(EnvEdit{Modules: map[string]bool{}}).TouchesLocked() {
		t.Fatal("时区、积木属于运行中禁止修改的字段")
	}
}

func TestRegenSeed(t *testing.T) {
	s := newTestStore(t)
	e, _ := s.Create(NewEnvInput{Primary: testUp()}, DefaultSettings())
	got, err := s.RegenSeed(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint.Seed.Fingerprint == e.Fingerprint.Seed.Fingerprint {
		t.Fatal("种子应改变")
	}
	if got.Fingerprint.Seed.Window != e.Fingerprint.Seed.Window {
		t.Fatal("窗口大小应保留")
	}
}

func TestCreateWithModules(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(NewEnvInput{Primary: testUp(), Modules: map[string]bool{"fonts": false}}, DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if e.Modules["fonts"] || !e.Modules["canvas"] {
		t.Fatalf("积木: %v", e.Modules)
	}
}

func TestTrash(t *testing.T) {
	s := newTestStore(t)
	trash := t.TempDir()
	t.Setenv("ENVBROWSER_TRASH", trash)
	e, _ := s.Create(NewEnvInput{Primary: testUp()}, DefaultSettings())
	dst, err := s.Trash(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "env.json")); err != nil {
		t.Fatal("环境目录应完整移到废纸篓")
	}
	if _, err := os.Stat(e.Dir()); !os.IsNotExist(err) {
		t.Fatal("原目录应已不存在")
	}
	if _, err := s.Trash("../etc"); err == nil {
		t.Fatal("非法 ID 应拒绝")
	}
}

func TestTemplateEnableAndAdd(t *testing.T) {
	s := newTestStore(t)
	ts, _ := s.Templates()
	for _, x := range ts[1:] {
		if err := s.SetTemplateEnabled(x.Name, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetTemplateEnabled(ts[0].Name, false); err == nil {
		t.Fatal("不能把模板全部停用")
	}
	e, _ := s.Create(NewEnvInput{Primary: testUp()}, DefaultSettings())
	if e.Fingerprint.Template != ts[0].Name {
		t.Fatalf("只剩一个启用模板时应选它，实际 %s", e.Fingerprint.Template)
	}
	if err := s.AddTemplate(Template{Name: "custom", PlatformVersion: "26.7.2", HardwareConcurrency: 8}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTemplate(Template{Name: "custom", PlatformVersion: "26.7.2", HardwareConcurrency: 8}); err == nil {
		t.Fatal("重名应拒绝")
	}
	if err := s.AddTemplate(Template{Name: "bad"}); err == nil {
		t.Fatal("缺系统版本和核数应拒绝")
	}
	ts2, _ := s.Templates()
	if len(ts2) != 5 || !ts2[4].Enabled {
		t.Fatalf("新增后应有 5 个模板且新模板启用: %d", len(ts2))
	}
}
