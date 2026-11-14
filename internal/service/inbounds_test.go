package service

import (
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
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

// TestInboundViewOf covers the view assembly: a missing aggregate (an inbound
// without clients) leaves zero counters but still carries the online IP count.
func TestInboundViewOf(t *testing.T) {
	in := model.Inbound{ID: 10, Remark: "香港"}
	agg := store.InboundAggregate{
		InboundID: 10, ClientCount: 3, EnabledClientCount: 2,
		UpBytes: 100, DownBytes: 200,
	}
	view := inboundViewOf(in, &agg, 4)
	if view.ClientCount != 3 || view.EnabledClientCount != 2 {
		t.Fatalf("unexpected counts: %+v", view)
	}
	if view.UpBytes != 100 || view.DownBytes != 200 || view.OnlineIPs != 4 {
		t.Fatalf("unexpected totals: %+v", view)
	}
	if view.ID != 10 || view.Remark != "香港" {
		t.Fatalf("inbound fields not copied: %+v", view)
	}

	empty := inboundViewOf(in, nil, 2)
	if empty.ClientCount != 0 || empty.UpBytes != 0 || empty.DownBytes != 0 {
		t.Fatalf("missing aggregate must leave zero counters: %+v", empty)
	}
	if empty.OnlineIPs != 2 {
		t.Fatalf("online IPs must still be reported: %+v", empty)
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
