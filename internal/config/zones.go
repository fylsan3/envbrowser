package config

import "strings"

// Zone 时区下拉选项。列表来自需求规格第 8.5 节。
type Zone struct {
	Label    string `json:"label"`
	TZ       string `json:"tz"`
	Language string `json:"language"`
	Country  string `json:"country"` // ISO 3166-1 两位国家代码，与 Cloudflare trace 的 loc= 对应
}

var Zones = []Zone{
	{"日本 · 东京（默认）", "Asia/Tokyo", "ja-JP", "JP"},
	{"美国 · 东部（纽约）", "America/New_York", "en-US", "US"},
	{"美国 · 中部（芝加哥）", "America/Chicago", "en-US", "US"},
	{"美国 · 山地（丹佛）", "America/Denver", "en-US", "US"},
	{"美国 · 西部（洛杉矶）", "America/Los_Angeles", "en-US", "US"},
	{"中国台湾 · 台北", "Asia/Taipei", "zh-TW", "TW"},
	{"新加坡", "Asia/Singapore", "en-SG", "SG"},
	{"马来西亚 · 吉隆坡", "Asia/Kuala_Lumpur", "ms-MY", "MY"},
	{"泰国 · 曼谷", "Asia/Bangkok", "th-TH", "TH"},
	{"越南 · 胡志明市", "Asia/Ho_Chi_Minh", "vi-VN", "VN"},
	{"印度尼西亚 · 雅加达", "Asia/Jakarta", "id-ID", "ID"},
	{"菲律宾 · 马尼拉", "Asia/Manila", "en-PH", "PH"},
	{"英国 · 伦敦", "Europe/London", "en-GB", "GB"},
	{"法国 · 巴黎", "Europe/Paris", "fr-FR", "FR"},
}

func DefaultLanguage(tz string) string {
	for _, z := range Zones {
		if z.TZ == tz {
			return z.Language
		}
	}
	return "en-US"
}

// ZonesForCountry 返回某国家代码对应的候选时区（可能多个，如 US）。不在列表中的国家返回空。
func ZonesForCountry(cc string) []Zone {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	var out []Zone
	for _, z := range Zones {
		if z.Country == cc {
			out = append(out, z)
		}
	}
	return out
}

// CountryOfZone 返回时区所属国家代码；不在列表中返回空。
func CountryOfZone(tz string) string {
	for _, z := range Zones {
		if z.TZ == tz {
			return z.Country
		}
	}
	return ""
}
