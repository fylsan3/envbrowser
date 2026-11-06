package fpcheck

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"envbrowser/internal/config"
)

// Evaluate 把页面采集结果与环境配置逐项比对。
// hostChip 为本机芯片名（如 "Apple M2"），显卡不伪装时 WebGL 型号应包含它。
func Evaluate(e *config.Env, raw Raw, local net.IP, hostChip string) *Report {
	rep := &Report{Env: e.ID, Time: time.Now(), Seed: e.Fingerprint.Seed.Fingerprint, Raw: raw}
	add := func(r Row) { rep.Rows = append(rep.Rows, r) }
	fp, m := e.Fingerprint, e.Modules

	for _, msg := range raw.Errors {
		add(Row{Item: "采集出错", Got: msg, Where: "页面"})
	}
	wantPlaces := []string{"主页面", "iframe", "Worker", "Service Worker"}
	var got []string
	for _, p := range raw.Places {
		got = append(got, p.Where)
	}
	add(Row{Item: "读取位置", Want: strings.Join(wantPlaces, "、"), Got: strings.Join(got, "、"), Where: "—",
		OK: len(raw.Places) == len(wantPlaces)})

	if len(raw.Places) > 0 {
		p := raw.Places[0]
		where := p.Where
		check := func(item, want, got string, ok bool) {
			add(Row{Item: item, Want: want, Got: got, Where: where, OK: ok})
		}

		if m["timezone"] {
			check("时区", fp.Timezone, p.TZ, p.TZ == fp.Timezone)
		}
		if m["language"] && len(fp.Languages) > 0 {
			check("语言 navigator.language", fp.Languages[0], p.Lang, p.Lang == fp.Languages[0])
			want := strings.Join(fp.Languages, ",")
			check("语言 navigator.languages", want, p.Langs, p.Langs == want)
		}
		if m["fingerprint"] && m["hardware"] && fp.HardwareConcurrency > 0 {
			check("CPU 核数", fmt.Sprint(fp.HardwareConcurrency), fmt.Sprint(p.Cores), p.Cores == fp.HardwareConcurrency)
		}
		if m["fingerprint"] && m["ua_client_hints"] {
			check("UA 系统", "Macintosh", p.UA, strings.Contains(p.UA, "Macintosh"))
			check("UA 版本", "Chrome/148", p.UA, strings.Contains(p.UA, "Chrome/148."))
			check("navigator.platform", "MacIntel", p.Platform, p.Platform == "MacIntel")
			check("Client Hints 品牌", "含 Google Chrome", p.Brands, strings.Contains(p.Brands, "Google Chrome/148"))
			check("Client Hints 系统", "macOS", p.UAPlatform, p.UAPlatform == "macOS")
			if fp.PlatformVersion != "" {
				check("Client Hints 系统版本", fp.PlatformVersion, p.PlatformVersion, p.PlatformVersion == fp.PlatformVersion)
			}
			check("Client Hints 架构", "arm", p.Arch, p.Arch == "arm")
		}
		check("自动化痕迹 webdriver", "false", fmt.Sprint(p.Webdriver), !p.Webdriver)
		if !m["gpu"] {
			ok := hostChip == "" || strings.Contains(p.GLRenderer, hostChip)
			check("显卡（不伪装，显示真机）", nonEmpty(hostChip, "本机芯片"), p.GLRenderer, ok)
		} else {
			add(Row{Item: "显卡（伪装）", Want: "随种子", Got: p.GLRenderer, Where: where, OK: true, Warn: true})
		}
		add(Row{Item: "内存 deviceMemory", Want: "随种子", Got: fmt.Sprint(p.Mem), Where: where, OK: true})
		add(Row{Item: "Canvas 哈希", Want: "随种子", Got: p.Canvas, Where: where, OK: isHash(p.Canvas)})
		add(Row{Item: "WebGL 图像哈希", Want: "随种子", Got: p.GLImage, Where: where, OK: isHash(p.GLImage)})
	}

	// 多位置一致：每一项在所有位置读到的值必须相同（主页面 / iframe / Worker / Service Worker）
	if len(raw.Places) > 1 {
		fields := []struct {
			name string
			get  func(Place) string
		}{
			{"UA", func(p Place) string { return p.UA }},
			{"CPU 核数", func(p Place) string { return fmt.Sprint(p.Cores) }},
			{"内存", func(p Place) string { return fmt.Sprint(p.Mem) }},
			{"语言", func(p Place) string { return p.Langs }},
			{"时区", func(p Place) string { return p.TZ }},
			{"Client Hints 品牌", func(p Place) string { return p.Brands }},
			{"Client Hints 系统版本", func(p Place) string { return p.PlatformVersion }},
			{"Canvas 哈希", func(p Place) string { return p.Canvas }},
			{"WebGL 型号", func(p Place) string { return p.GLRenderer }},
			{"WebGL 图像哈希", func(p Place) string { return p.GLImage }},
		}
		for _, f := range fields {
			vals := map[string][]string{}
			for _, p := range raw.Places {
				v := f.get(p)
				vals[v] = append(vals[v], p.Where)
			}
			add(Row{Item: "多位置一致：" + f.name, Want: "各位置相同", Got: describe(vals), Where: "全部", OK: len(vals) == 1})
		}
	}

	x := raw.Extra
	if m["window"] && fp.Seed.Window.Width > 0 {
		want := fmt.Sprintf("%dx%d", fp.Seed.Window.Width, fp.Seed.Window.Height)
		add(Row{Item: "窗口尺寸", Want: want, Got: x.Window, Where: "主页面", OK: true, Warn: x.Window != want})
	}
	add(Row{Item: "屏幕（不支持伪装）", Want: "真机值", Got: x.Screen, Where: "主页面", OK: true})
	add(Row{Item: "音频哈希", Want: "随种子", Got: x.Audio, Where: "主页面", OK: isHash(x.Audio)})
	add(Row{Item: "字体列表", Want: "真机字体", Got: x.Fonts, Where: "主页面", OK: x.Fonts != ""})
	add(Row{Item: "字体测宽（ClientRects）", Want: "随种子", Got: shortHash(x.Rects), Where: "主页面", OK: x.Rects != ""})

	// Cookie 隔离：只允许看到本环境自己写的标记
	var foreign []string
	for _, kv := range strings.Split(x.Cookie, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		if k == "fp_env" && v != e.ID {
			foreign = append(foreign, v)
		}
	}
	add(Row{Item: "Cookie 隔离", Want: "看不到其他环境的 Cookie", Got: nonEmpty(x.Cookie, "（无）"), Where: "主页面", OK: len(foreign) == 0})

	leak := rtcLeaks(x.RTCIPs, local)
	add(Row{Item: "WebRTC 候选地址", Want: "无本机、内网、真实公网 IP", Got: nonEmpty(x.RTCIPs, "（无）"),
		Where: "主页面", OK: len(leak) == 0})

	if len(x.Ports) > 0 {
		res := map[string][]string{}
		open := false
		for _, p := range x.Ports {
			res[p.Result] = append(res[p.Result], fmt.Sprint(p.Port))
			open = open || p.Result == "可连"
		}
		add(Row{Item: "本机端口扫描", Want: "所有端口表现一致且都连不上", Got: describe(res), Where: "主页面",
			OK: len(res) == 1 && !open})
	} else {
		add(Row{Item: "本机端口扫描", Want: "所有端口表现一致且都连不上", Got: "未执行", Where: "主页面"})
	}

	for _, r := range rep.Rows {
		if !r.OK {
			rep.Failed++
		}
	}
	if rep.Failed == 0 {
		rep.Summary = fmt.Sprintf("%s：%d 项全部通过", e.ID, len(rep.Rows))
	} else {
		rep.Summary = fmt.Sprintf("%s：%d 项中 %d 项不一致", e.ID, len(rep.Rows), rep.Failed)
	}
	return rep
}

// rtcLeaks 返回 WebRTC 候选中的泄露地址：本机 / 内网 IP、本机真实公网 IP。
// .local 结尾的 mDNS 名字不算泄露（浏览器用它隐藏内网 IP）。
func rtcLeaks(list string, local net.IP) []string {
	var out []string
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasSuffix(s, ".local") {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || (local != nil && ip.Equal(local)) {
			out = append(out, s)
		}
	}
	return out
}

func describe(vals map[string][]string) string {
	if len(vals) == 1 {
		for v := range vals {
			return nonEmpty(v, "（空）")
		}
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []string
	for _, k := range keys {
		b = append(b, nonEmpty(k, "（空）")+" ← "+strings.Join(vals[k], "、"))
	}
	return strings.Join(b, "；")
}

func isHash(s string) bool { return len(s) == 16 && !strings.Contains(s, " ") }

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// shortHash 把长串缩成短指纹，便于在报告里比较。
func shortHash(s string) string {
	if s == "" {
		return ""
	}
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

// Compare 跨环境与重启比对（需求规格 9.2）：
//   - byEnv：每个环境的多次报告（同一环境重启多次）。同一环境所有报告的随种子项必须完全相同。
//   - 不同环境之间 Canvas 哈希不能相同；音频重复只提示（只有 7 档）。
func Compare(byEnv map[string][]*Report) []Row {
	var rows []Row
	ids := make([]string, 0, len(byEnv))
	for id := range byEnv {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	keyOf := func(r *Report) map[string]string {
		k := map[string]string{"音频": r.Raw.Extra.Audio, "字体测宽": shortHash(r.Raw.Extra.Rects)}
		if len(r.Raw.Places) > 0 {
			p := r.Raw.Places[0]
			k["Canvas"], k["WebGL 图像"], k["内存"], k["UA"] = p.Canvas, p.GLImage, fmt.Sprint(p.Mem), p.UA
		}
		return k
	}
	first := map[string]map[string]string{}
	for _, id := range ids {
		rs := byEnv[id]
		if len(rs) == 0 {
			continue
		}
		base := keyOf(rs[0])
		first[id] = base
		if len(rs) < 2 {
			continue
		}
		var diff []string
		for _, r := range rs[1:] {
			for k, v := range keyOf(r) {
				if base[k] != v {
					diff = append(diff, fmt.Sprintf("%s：%s → %s", k, base[k], v))
				}
			}
		}
		sort.Strings(diff)
		rows = append(rows, Row{Item: fmt.Sprintf("重启 %d 次结果一致", len(rs)), Want: "完全相同",
			Got: nonEmpty(strings.Join(diff, "；"), "相同"), Where: id, OK: len(diff) == 0})
	}
	for _, item := range []string{"Canvas", "音频"} {
		seen := map[string][]string{}
		for _, id := range ids {
			if k, ok := first[id]; ok {
				seen[k[item]] = append(seen[k[item]], id)
			}
		}
		var dup []string
		for v, who := range seen {
			if len(who) > 1 {
				dup = append(dup, v+" ← "+strings.Join(who, "、"))
			}
		}
		sort.Strings(dup)
		row := Row{Item: "跨环境 " + item + " 哈希互不相同", Want: "两两不同",
			Got: nonEmpty(strings.Join(dup, "；"), fmt.Sprintf("%d 个环境各不相同", len(first))), Where: "跨环境", OK: len(dup) == 0}
		if item == "音频" {
			// 音频扰动只有 7 档，重复属正常（需求规格第 13 节，使用者 2026-11-06 选择只提示）
			row.Want, row.OK, row.Warn = "允许重复（只提示）", true, len(dup) > 0
		}
		rows = append(rows, row)
	}
	return rows
}
