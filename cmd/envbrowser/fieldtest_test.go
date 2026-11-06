package main

import (
	"reflect"
	"testing"
)

func TestParseLsof(t *testing.T) {
	out := "p101\nPTCP\nn127.0.0.1:5000->127.0.0.1:20001\nPTCP\nn*:9222\nPUDP\nn*:56104\np102\nPTCP\nn192.0.2.5:5100->203.0.113.9:443\n"
	got := parseLsof(out)
	want := []sock{
		{"101", "TCP", "127.0.0.1:5000->127.0.0.1:20001"},
		{"101", "UDP", "*:56104"},
		{"102", "TCP", "192.0.2.5:5100->203.0.113.9:443"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if !isLoopback("TCP "+want[0].name) || isLoopback("UDP "+want[1].name) || isLoopback("TCP "+want[2].name) {
		t.Fatal("isLoopback 判断错")
	}
}

func TestParseNettop(t *testing.T) {
	out := ",bytes_out,\nChromium Helper.101,136,\nudp4 *:56104<->*:*,0,\nudp6 *.61832<->*.*,96,\nudp4 192.0.2.5:5000<->203.0.113.9:3478,40,\nudp4 127.0.0.1:6000<->127.0.0.1:20001,8,\n"
	sent, peers := parseNettop(out)
	want := map[string]int64{"56104": 0, "61832": 96, "5000": 40, "6000": 8}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent %v", sent)
	}
	if !reflect.DeepEqual(peers, []string{"UDP 192.0.2.5:5000->203.0.113.9:3478"}) {
		t.Fatalf("peers %v", peers)
	}
}

func TestProcKind(t *testing.T) {
	cases := map[string]string{
		"101 /x/Chromium --user-data-dir=/t/env-0001": "主进程",
		"102 /x/Chromium Helper --type=utility --utility-sub-type=network.mojom.NetworkService --user-data-dir=/t/env-0001": "网络服务进程",
		"103 /x/Chromium Helper (Renderer) --type=renderer --user-data-dir=/t/env-0001":                                     "renderer",
	}
	for in, want := range cases {
		if got := procKind(in); got != want {
			t.Errorf("%q → %q，期望 %q", in, got, want)
		}
	}
	if port("*:56104") != "56104" || port("*.61832") != "61832" || port("[::1]:9") != "9" {
		t.Fatal("port 解析错")
	}
}
