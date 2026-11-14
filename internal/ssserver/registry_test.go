package ssserver

import (
	"testing"
)

// TestOnlineIPsByInboundMatchesPerClient verifies the per-inbound online IP
// count is exactly what unioning the per-client IP sets gives, so the panel
// keeps reporting the same number after moving off the client rows.
func TestOnlineIPsByInboundMatchesPerClient(t *testing.T) {
	r := newRegistry()
	type sessionSpec struct {
		clientID  int64
		inboundID int64
		ip        string
	}
	// client 1 and 2 share an IP inside inbound 10; client 3 is in inbound 20;
	// client 1 also has a second session from the same IP (must not double count).
	specs := []sessionSpec{
		{1, 10, "203.0.113.1"},
		{1, 10, "203.0.113.1"},
		{2, 10, "203.0.113.1"},
		{2, 10, "203.0.113.2"},
		{3, 20, "203.0.113.9"},
		{3, 20, "203.0.113.9"},
	}
	for _, spec := range specs {
		if _, err := r.acquire(spec.clientID, spec.inboundID, spec.ip, 0, 0); err != nil {
			t.Fatalf("acquire: %v", err)
		}
	}

	byInbound := r.onlineIPsByInbound()

	// Expected: inbound 10 has two distinct IPs, inbound 20 has one.
	if got := byInbound[10]; got != 2 {
		t.Fatalf("inbound 10 online IPs = %d, want 2", got)
	}
	if got := byInbound[20]; got != 1 {
		t.Fatalf("inbound 20 online IPs = %d, want 1", got)
	}

	// Cross-check against the per-client view: the union of each inbound's
	// clients' IP sets must agree, which is the semantic the old code used.
	perClient := r.onlineIPs()
	want := make(map[int64]map[string]struct{})
	for _, spec := range specs {
		set := want[spec.inboundID]
		if set == nil {
			set = make(map[string]struct{})
			want[spec.inboundID] = set
		}
		for _, ip := range perClient[spec.clientID] {
			set[ip] = struct{}{}
		}
	}
	for inboundID, set := range want {
		if byInbound[inboundID] != len(set) {
			t.Fatalf("inbound %d: byInbound=%d, union of client sets=%d",
				inboundID, byInbound[inboundID], len(set))
		}
	}
	if len(byInbound) != len(want) {
		t.Fatalf("byInbound has %d inbounds, want %d", len(byInbound), len(want))
	}
}

// TestOnlineIPsByInboundEmpty verifies the empty registry case.
func TestOnlineIPsByInboundEmpty(t *testing.T) {
	if got := newRegistry().onlineIPsByInbound(); len(got) != 0 {
		t.Fatalf("expected no inbounds, got %+v", got)
	}
}
