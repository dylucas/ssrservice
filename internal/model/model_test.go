package model

import "testing"

func TestInboundAddr(t *testing.T) {
	cases := []struct {
		name   string
		listen string
		port   int
		want   string
	}{
		{"ipv4 any", "0.0.0.0", 8388, "0.0.0.0:8388"},
		{"ipv4 loopback", "127.0.0.1", 2096, "127.0.0.1:2096"},
		{"ipv6 any", "::", 8388, "[::]:8388"},
		{"ipv6 loopback", "::1", 8388, "[::1]:8388"},
		{"ipv6 global", "2001:db8::1", 443, "[2001:db8::1]:443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Inbound{Listen: tc.listen, Port: tc.port}
			if got := in.Addr(); got != tc.want {
				t.Fatalf("Addr() = %q, want %q", got, tc.want)
			}
		})
	}
}
