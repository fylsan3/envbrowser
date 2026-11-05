package config

// Zone 时区下拉选项。列表来自需求规格第 8.5 节。
type Zone struct {
	Label    string `json:"label"`
	TZ       string `json:"tz"`
	Language string `json:"language"`
}

var Zones = []Zone{
	{"日本 · 东京（默认）", "Asia/Tokyo", "ja-JP"},
	{"美国 · 东部（纽约）", "America/New_York", "en-US"},
	{"美国 · 中部（芝加哥）", "America/Chicago", "en-US"},
	{"美国 · 山地（丹佛）", "America/Denver", "en-US"},
	{"美国 · 西部（洛杉矶）", "America/Los_Angeles", "en-US"},
	{"中国台湾 · 台北", "Asia/Taipei", "zh-TW"},
	{"新加坡", "Asia/Singapore", "en-SG"},
	{"马来西亚 · 吉隆坡", "Asia/Kuala_Lumpur", "ms-MY"},
	{"泰国 · 曼谷", "Asia/Bangkok", "th-TH"},
	{"越南 · 胡志明市", "Asia/Ho_Chi_Minh", "vi-VN"},
	{"印度尼西亚 · 雅加达", "Asia/Jakarta", "id-ID"},
	{"菲律宾 · 马尼拉", "Asia/Manila", "en-PH"},
	{"英国 · 伦敦", "Europe/London", "en-GB"},
	{"法国 · 巴黎", "Europe/Paris", "fr-FR"},
}

func DefaultLanguage(tz string) string {
	for _, z := range Zones {
		if z.TZ == tz {
			return z.Language
		}
	}
	return "en-US"
}
