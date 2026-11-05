package egress

import (
	"errors"
	"net"
	"testing"
)

func TestParseIP(t *testing.T) {
	cases := map[string]string{
		"fl=1\nh=www.cloudflare.com\nip=203.0.113.7\nts=1\n": "203.0.113.7",
		"198.51.100.2\n": "198.51.100.2",
		"2001:db8::1":    "2001:db8::1",
	}
	for in, want := range cases {
		ip, err := parseIP(in)
		if err != nil || ip.String() != want {
			t.Errorf("parseIP(%q)=%v,%v want %s", in, ip, err, want)
		}
	}
	if _, err := parseIP("<html>blocked</html>"); err == nil {
		t.Error("非 IP 内容应报错")
	}
}

func TestLeakHeaders(t *testing.T) {
	ok := `{"headers":{"Accept":"*/*","Host":"httpbin.org","User-Agent":"Go"}}`
	if l := LeakHeaders(ok); len(l) != 0 {
		t.Errorf("误报: %v", l)
	}
	bad := `{"headers":{"Via":"1.1 squid","X-Forwarded-For":"203.0.113.9"}}`
	if l := LeakHeaders(bad); len(l) != 2 {
		t.Errorf("漏报: %v", l)
	}
}

func TestMismatchIsDistinct(t *testing.T) {
	// 出口不符应立即断网，与“网络失败”区分开
	err := errors.Join(ErrMismatch, net.UnknownNetworkError("x"))
	if !errors.Is(err, ErrMismatch) {
		t.Fatal()
	}
}

func TestParseTraceCountry(t *testing.T) {
	cases := map[string]string{
		"fl=1\nip=203.0.113.7\nloc=JP\ntls=TLSv1.3\n": "JP",
		"ip=203.0.113.7\nloc=us\n":                    "US",
		"ip=203.0.113.7\nloc=XX\n":                    "",
		"203.0.113.7\n":                               "",
	}
	for in, want := range cases {
		if got := parseTrace(in); got != want {
			t.Errorf("parseTrace(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCheckCountry(t *testing.T) {
	if CheckCountry("JP", "JP") != nil || CheckCountry("", "US") != nil || CheckCountry("JP", "") != nil {
		t.Error("相同或未知时不应报错")
	}
	if err := CheckCountry("JP", "US"); !errors.Is(err, ErrMismatch) {
		t.Error("国家变化应视为出口不符（立即断网）")
	}
}
