package service

import (
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
)

func TestValidateListenAddressIPv6(t *testing.T) {
	accepted := []string{
		"127.0.0.1",
		"0.0.0.0",
		"::",
		"::1",
		"2001:db8::1",
		"localhost",
	}
	for _, value := range accepted {
		if err := validateListenAddress(value); err != nil {
			t.Fatalf("expected %q to be accepted: %v", value, err)
		}
	}
	rejected := map[string]string{
		"":              "must not be empty",
		"example.com":   "must be an IP address",
		"127.0.0.1:80":  "must be an IP address",
		"[::1]":         "without brackets",
		"fe80::1%eth0":  "zone",
		" 127.0.0.1":    "must be an IP address",
	}
	for value, hint := range rejected {
		err := validateListenAddress(value)
		if err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Fatalf("error for %q = %v, want it to mention %q", value, err, hint)
		}
	}
}

func TestInboundAddrAndSnapshot(t *testing.T) {
	in := model.Inbound{Listen: "::1", Port: 8388}
	if got := in.Addr(); got != "[::1]:8388" {
		t.Fatalf("Inbound.Addr() = %q", got)
	}
	snap := ssserver.InboundSnapshot{Listen: "::", Port: 8388}
	if got := snap.Addr(); got != "[::]:8388" {
		t.Fatalf("InboundSnapshot.Addr() = %q", got)
	}
}

func TestResolveHostPortFallsBackToLoopback(t *testing.T) {
	svc := &Service{}
	svc.loc = nil
	cases := []struct {
		listen string
		want   string
	}{
		{"0.0.0.0", "127.0.0.1"},
		{"::", "::1"},
		{"[::]", "::1"},
		{"127.0.0.1", "127.0.0.1"},
		{"2001:db8::1", "2001:db8::1"},
	}
	for _, tc := range cases {
		in := &model.Inbound{Listen: tc.listen, Port: 8388}
		host, port := svc.resolveHostPort(in, "")
		if host != tc.want || port != 8388 {
			t.Fatalf("resolveHostPort(%q) = %q:%d, want %q:8388", tc.listen, host, port, tc.want)
		}
	}
}
