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
		"":             "must not be empty",
		"example.com":  "must be an IP address",
		"127.0.0.1:80": "must be an IP address",
		"[::1]":        "without brackets",
		"fe80::1%eth0": "zone",
		" 127.0.0.1":   "must be an IP address",
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

// TestAggregateClients covers the one-pass aggregation used by the inbound
// list: per-inbound counts, totals and distinct online IPs.
func TestAggregateClients(t *testing.T) {
	clients := []model.Client{
		{ID: 1, InboundID: 10, Enabled: true, UpBytes: 100, DownBytes: 200},
		{ID: 2, InboundID: 10, Enabled: false, UpBytes: 1, DownBytes: 2},
		{ID: 3, InboundID: 20, Enabled: true, UpBytes: 3, DownBytes: 4},
		{ID: 4, InboundID: 20, Enabled: true, UpBytes: 5, DownBytes: 6},
	}
	online := map[int64][]string{
		1: {"203.0.113.1", "203.0.113.2"},
		2: {"203.0.113.1"}, // shares an IP with client 1
		3: {"203.0.113.9"},
	}
	aggregates := aggregateClients(clients, online)

	total := InboundView{}
	aggregates[10].apply(&total)
	if total.ClientCount != 2 || total.EnabledClientCount != 1 {
		t.Fatalf("unexpected counts for inbound 10: %+v", total)
	}
	if total.UpBytes != 101 || total.DownBytes != 202 {
		t.Fatalf("unexpected totals for inbound 10: %+v", total)
	}
	// Clients 1 and 2 share 203.0.113.1, so only two distinct IPs remain.
	if total.OnlineIPs != 2 {
		t.Fatalf("online split count = %d, want 2", total.OnlineIPs)
	}

	empty := InboundView{}
	aggregates[99].apply(&empty)
	if empty.ClientCount != 0 || empty.OnlineIPs != 0 {
		t.Fatalf("missing aggregate must leave zero values: %+v", empty)
	}
}

// TestSubscriptionPath checks the subscription path helper honors the toggle.
func TestSubscriptionPath(t *testing.T) {
	if got := subscriptionPath("tok", true); got != "/sub/tok" {
		t.Fatalf("enabled path = %q", got)
	}
	if got := subscriptionPath("tok", false); got != "" {
		t.Fatalf("disabled path = %q", got)
	}
	if got := subscriptionPath("", true); got != "" {
		t.Fatalf("empty token path = %q", got)
	}
}
