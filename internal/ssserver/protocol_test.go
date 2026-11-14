package ssserver

import (
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

func testSnapshot(method, serverPSK string, clients ...ClientSnapshot) InboundSnapshot {
	return InboundSnapshot{
		ID:        1,
		Listen:    "127.0.0.1",
		Port:      8388,
		Method:    method,
		ServerPSK: serverPSK,
		Enabled:   true,
		Clients:   clients,
	}
}

// TestProtocolSignatureIgnoresNonProtocolFields verifies that the reload
// signature only depends on what buildProtocol consumes, so a quota or expiry
// change can reuse the running service.
func TestProtocolSignatureIgnoresNonProtocolFields(t *testing.T) {
	base := ClientSnapshot{ID: 1, PSK: "psk-a", Enabled: true}
	other := ClientSnapshot{ID: 2, PSK: "psk-b", Enabled: true}
	before := testSnapshot(model.Method2022AES128GCM, "server", base, other)

	// Changing quota, expiry, IP limit or note must keep the signature stable.
	changed := testSnapshot(model.Method2022AES128GCM, "server", base, other)
	changed.Remark = "renamed"
	changed.Clients[0].QuotaBytes = 1024
	changed.Clients[0].ExpiresAt = 1700000000
	changed.Clients[0].IPLimit = 3
	changed.Clients[0].Name = "renamed-client"
	if got, want := protocolSignature(changed), protocolSignature(before); got != want {
		t.Fatalf("non-protocol fields changed the signature")
	}

	// Any protocol input must change it.
	cases := map[string]func(*InboundSnapshot){
		"method":     func(s *InboundSnapshot) { s.Method = model.Method2022AES256GCM },
		"server psk": func(s *InboundSnapshot) { s.ServerPSK = "server-2" },
		"client psk": func(s *InboundSnapshot) { s.Clients[1].PSK = "psk-c" },
		"enabled":    func(s *InboundSnapshot) { s.Clients[0].Enabled = false },
		"add client": func(s *InboundSnapshot) {
			s.Clients = append(s.Clients, ClientSnapshot{ID: 3, PSK: "psk-d", Enabled: true})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			modified := testSnapshot(model.Method2022AES128GCM, "server", base, other)
			mutate(&modified)
			if got, want := protocolSignature(modified), protocolSignature(before); got == want {
				t.Fatalf("%s change did not alter the signature", name)
			}
		})
	}

	// The signature is order independent because clients are sorted by id.
	reordered := testSnapshot(model.Method2022AES128GCM, "server", other, base)
	if got, want := protocolSignature(reordered), protocolSignature(before); got != want {
		t.Fatalf("client order changed the signature")
	}
}

// TestEnabledClientsFiltersAndSorts verifies the protocol user table input.
func TestEnabledClientsFiltersAndSorts(t *testing.T) {
	clients := []ClientSnapshot{
		{ID: 3, PSK: "c", Enabled: false},
		{ID: 2, PSK: "b", Enabled: true},
		{ID: 1, PSK: "a", Enabled: true},
	}
	enabled := enabledClients(clients)
	if len(enabled) != 2 || enabled[0].ID != 1 || enabled[1].ID != 2 {
		t.Fatalf("unexpected enabled clients: %+v", enabled)
	}
	if n := enabledClientCount(clients); n != 2 {
		t.Fatalf("enabledClientCount = %d, want 2", n)
	}
}

// TestBuildProtocolExcludesDisabledClients verifies that a disabled client is
// not part of the protocol user table, and that a single user method only takes
// an enabled client as its default identity.
func TestBuildProtocolExcludesDisabledClients(t *testing.T) {
	multi := &handler{method: model.Method2022AES128GCM}
	service, defaultID, err := buildProtocol(testSnapshot(model.Method2022AES128GCM, newTestKey(t, 16),
		ClientSnapshot{ID: 1, PSK: newTestKey(t, 16), Enabled: false},
	), multi, 0)
	if err != nil {
		t.Fatalf("buildProtocol: %v", err)
	}
	if service == nil {
		t.Fatal("expected a service even without enabled users")
	}
	if defaultID != 0 {
		t.Fatalf("multi-user method must not set a default client, got %d", defaultID)
	}

	// Single user method: a disabled client must not become the default
	// identity, otherwise a disabled user would be served.
	single := &handler{method: model.Method2022ChaCha20}
	_, defaultID, err = buildProtocol(testSnapshot(model.Method2022ChaCha20, "",
		ClientSnapshot{ID: 2, PSK: newTestKey(t, 32), Enabled: false},
	), single, 0)
	if err != nil {
		t.Fatalf("buildProtocol single user: %v", err)
	}
	if defaultID != 0 {
		t.Fatalf("disabled client became the default identity: %d", defaultID)
	}

	// An enabled client is picked as the default identity.
	_, defaultID, err = buildProtocol(testSnapshot(model.Method2022ChaCha20, "",
		ClientSnapshot{ID: 3, PSK: newTestKey(t, 32), Enabled: true},
	), single, 0)
	if err != nil {
		t.Fatalf("buildProtocol single user: %v", err)
	}
	if defaultID != 3 {
		t.Fatalf("default identity = %d, want 3", defaultID)
	}
}
