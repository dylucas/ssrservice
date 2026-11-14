package service

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

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

// TestPrepareInboundDomain covers the domain rules applied on save.
func TestPrepareInboundDomain(t *testing.T) {
	svc := &Service{}
	base := func() *model.Inbound {
		return &model.Inbound{Remark: "x", Listen: "0.0.0.0", Port: 8388,
			Method: model.Method2022AES128GCM}
	}

	// Canonicalisation: trimmed, lowercased, trailing dot removed.
	in := base()
	in.Domain = "  HK.Example.COM.  "
	if err := svc.prepareInbound(in); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if in.Domain != "hk.example.com" {
		t.Fatalf("domain = %q, want hk.example.com", in.Domain)
	}

	// Unset is fine.
	in = base()
	if err := svc.prepareInbound(in); err != nil {
		t.Fatalf("prepare without domain: %v", err)
	}
	if in.Domain != "" || in.DomainPort != 0 {
		t.Fatalf("unset domain should stay empty: %+v", in)
	}

	// Valid domain with a port.
	in = base()
	in.Domain = "hk.example.com"
	in.DomainPort = 8443
	if err := svc.prepareInbound(in); err != nil {
		t.Fatalf("prepare with port: %v", err)
	}

	failures := map[string]func(*model.Inbound){
		"invalid domain":      func(in *model.Inbound) { in.Domain = "http://x" },
		"port without domain": func(in *model.Inbound) { in.DomainPort = 8443 },
		"negative port":       func(in *model.Inbound) { in.Domain = "a.com"; in.DomainPort = -1 },
		"port above range":    func(in *model.Inbound) { in.Domain = "a.com"; in.DomainPort = 70000 },
		"bracketed IPv6":      func(in *model.Inbound) { in.Domain = "[::1]" },
		"scheme prefix":       func(in *model.Inbound) { in.Domain = "https://a.com" },
	}
	for name, mutate := range failures {
		in := base()
		mutate(in)
		err := svc.prepareInbound(in)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if !IsValidation(err) {
			t.Fatalf("%s: expected a validation error, got %v", name, err)
		}
	}
}

// TestResolveHostPortPrecedence pins the host/port chain used by share links.
func TestResolveHostPortPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		publicHost string
		publicPort int
		in         model.Inbound
		fallback   string
		wantHost   string
		wantPort   int
	}{
		{
			name:       "inbound domain and port win over global settings",
			publicHost: "global.example.com", publicPort: 9000,
			in:       model.Inbound{Domain: "hk.example.com", DomainPort: 8443, Port: 8388},
			wantHost: "hk.example.com", wantPort: 8443,
		},
		{
			name:       "domain without port falls back to the global port",
			publicHost: "global.example.com", publicPort: 9000,
			in:       model.Inbound{Domain: "hk.example.com", Port: 8388},
			wantHost: "hk.example.com", wantPort: 9000,
		},
		{
			name:     "domain without any global port falls back to the inbound port",
			in:       model.Inbound{Domain: "hk.example.com", Port: 8388},
			wantHost: "hk.example.com", wantPort: 8388,
		},
		{
			name:       "no domain keeps the previous behaviour",
			publicHost: "global.example.com", publicPort: 9000,
			in:       model.Inbound{Listen: "127.0.0.1", Port: 8388},
			wantHost: "global.example.com", wantPort: 9000,
		},
		{
			name:     "no domain, no global host: request hint wins over the listen address",
			in:       model.Inbound{Listen: "203.0.113.5", Port: 8388},
			fallback: "hint.example.com",
			wantHost: "hint.example.com", wantPort: 8388,
		},
		{
			name:     "domain ignores the request hint",
			in:       model.Inbound{Domain: "hk.example.com", Port: 8388},
			fallback: "hint.example.com",
			wantHost: "hk.example.com", wantPort: 8388,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{}
			svc.publicHost = tc.publicHost
			svc.publicPort = tc.publicPort
			host, port := svc.resolveHostPort(&tc.in, tc.fallback)
			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf("resolveHostPort = %s:%d, want %s:%d", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

// TestToggleInboundRollsBackWhenTheListenerFails keeps the database and the
// runtime agreeing when enabling an inbound cannot bind its port.
//
// ToggleInbound used to persist enabled=true first and only then reload. A
// failed reload returned an error while leaving the row enabled, so the panel
// showed a running inbound that was not listening, and nothing retried it.
func TestToggleInboundRollsBackWhenTheListenerFails(t *testing.T) {
	svc, st, _ := newImportEnv(t, model.Method2022AES128GCM)

	port := freePortForTest(t)
	in := model.Inbound{
		Remark: "toggle", Listen: "127.0.0.1", Port: port,
		Method: model.Method2022AES128GCM, ServerPSK: mustServerPSK(t, model.Method2022AES128GCM),
		Enabled: false,
	}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	// Hold the port so the listener cannot start.
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("could not occupy the port: %v", err)
	}
	defer blocker.Close()

	if err := svc.ToggleInbound(in.ID, true); err == nil {
		t.Fatal("enabling a blocked inbound must fail")
	}
	stored, err := st.InboundByID(in.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Enabled {
		t.Fatal("a failed enable must not persist")
	}
}

// TestRestartInboundKeepsServingAfterFailure checks that a restart which cannot
// rebuild the listener does not take the working one down with it.
//
// The restart stops the listener before rebinding, so the window exists by
// construction; what must not happen is ending up with no listener at all while
// the row is unchanged.
func TestRestartInboundKeepsServingAfterFailure(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)

	// A healthy restart keeps the inbound reachable.
	if err := svc.RestartInbound(in.ID); err != nil {
		t.Fatalf("healthy restart: %v", err)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(in.Port)), time.Second)
	if err != nil {
		t.Fatalf("inbound not listening after restart: %v", err)
	}
	conn.Close()

	// Now make the rebuild fail: another process grabs the port between the
	// stop and the bind. The previous listener cannot be restored either
	// because the address is gone, so the error must at least be reported and
	// the row left untouched.
	svc.mgr.RemoveInbound(in.ID)
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(in.Port)))
	if err != nil {
		t.Skipf("could not occupy the port: %v", err)
	}
	defer blocker.Close()

	if err := svc.RestartInbound(in.ID); err == nil {
		t.Fatal("restart with the port taken must fail")
	}
	stored, err := st.InboundByID(in.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Port != in.Port || !stored.Enabled {
		t.Fatalf("a failed restart must not modify the row: %+v", stored)
	}
}

// mustServerPSK generates a server key valid for the method.
func mustServerPSK(t *testing.T, method string) string {
	t.Helper()
	psk, err := ssserver.GenerateServerPSK(method)
	if err != nil {
		t.Fatalf("server psk: %v", err)
	}
	return psk
}
