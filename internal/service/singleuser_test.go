package service

import (
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
)

// TestSingleUserRuleCoversEveryPath pins the invariant documented in
// docs/security.md: a method without an identity header serves at most one
// enabled client.
//
// The rule used to live only in the create and import paths. Toggling a second
// client on, or switching an inbound's method to chacha20-poly1305, could leave
// two enabled users behind. The runtime then built its single-key service from
// the lowest id, so the other user could not connect and both users' traffic,
// quota and expiry were accounted against one of them, while the panel showed
// both as enabled.
func TestSingleUserRuleCoversEveryPath(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022ChaCha20)

	// The environment creates no clients; add a disabled one directly so the
	// create rule (which would reject a second enabled user) is out of the way.
	disabled := model.Client{
		InboundID: in.ID, Name: "disabled", Enabled: false,
		PSK: testClientPSK(t, in), SubToken: "tok-disabled", ResetPolicy: model.ResetNone,
	}
	if err := st.CreateClient(&disabled); err != nil {
		t.Fatalf("create disabled client: %v", err)
	}

	first, err := svc.CreateClient(in.ID, ClientInput{Name: "first", Enabled: true})
	if err != nil {
		t.Fatalf("create first client: %v", err)
	}

	t.Run("toggle on is rejected", func(t *testing.T) {
		err := svc.ToggleClient(disabled.ID, true)
		if err == nil {
			t.Fatal("enabling a second client on a single-user inbound must fail")
		}
		if !IsValidation(err) {
			t.Fatalf("expected a validation error, got %v", err)
		}
		if !strings.Contains(err.Error(), "single enabled client") {
			t.Fatalf("error should explain the rule: %v", err)
		}
		stored, err := st.ClientByID(disabled.ID)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Enabled {
			t.Fatal("rejected toggle must not persist")
		}
	})

	t.Run("toggle on is allowed once the other is off", func(t *testing.T) {
		if err := svc.ToggleClient(first.ID, false); err != nil {
			t.Fatalf("disable first: %v", err)
		}
		if err := svc.ToggleClient(disabled.ID, true); err != nil {
			t.Fatalf("enable the only client: %v", err)
		}
		// Restore the starting state for the remaining subtests.
		if err := svc.ToggleClient(disabled.ID, false); err != nil {
			t.Fatalf("disable again: %v", err)
		}
		if err := svc.ToggleClient(first.ID, true); err != nil {
			t.Fatalf("re-enable first: %v", err)
		}
	})

	t.Run("method switch to a single-user method is rejected", func(t *testing.T) {
		// Two enabled users are valid under a multi-user method.
		// aes-256 keys are 32 bytes, the same size chacha20-poly1305 wants, so
		// the key-length check does not mask the enabled-count rule.
		multiSvc, multiStore, multiIn := newImportEnv(t, model.Method2022AES256GCM)
		a, err := multiSvc.CreateClient(multiIn.ID, ClientInput{Name: "a", Enabled: true})
		if err != nil {
			t.Fatalf("create a: %v", err)
		}
		b, err := multiSvc.CreateClient(multiIn.ID, ClientInput{Name: "b", Enabled: true})
		if err != nil {
			t.Fatalf("create b: %v", err)
		}
		if _, err := multiSvc.UpdateInbound(multiIn.ID, InboundInput{
			Remark: multiIn.Remark, Listen: multiIn.Listen, Port: multiIn.Port,
			Method: model.Method2022ChaCha20, UDPEnabled: false, Enabled: true,
		}); err == nil {
			t.Fatal("switching to a single-user method with two enabled clients must fail")
		} else if !strings.Contains(err.Error(), "single enabled client") {
			t.Fatalf("error should explain the rule: %v", err)
		}
		stored, err := multiStore.InboundByID(multiIn.ID)
		if err != nil {
			t.Fatalf("reload inbound: %v", err)
		}
		if stored.Method != model.Method2022AES256GCM {
			t.Fatalf("rejected update must not persist, method = %s", stored.Method)
		}
		if _, err := multiStore.ClientByID(a.ID); err != nil {
			t.Fatalf("client a vanished: %v", err)
		}
		if _, err := multiStore.ClientByID(b.ID); err != nil {
			t.Fatalf("client b vanished: %v", err)
		}

		// With one enabled client the switch is allowed and keeps working.
		if err := multiSvc.ToggleClient(b.ID, false); err != nil {
			t.Fatalf("disable b: %v", err)
		}
		if _, err := multiSvc.UpdateInbound(multiIn.ID, InboundInput{
			Remark: multiIn.Remark, Listen: multiIn.Listen, Port: multiIn.Port,
			Method: model.Method2022ChaCha20, UDPEnabled: false, Enabled: true,
		}); err != nil {
			t.Fatalf("switch with one enabled client: %v", err)
		}
	})
}

// testClientPSK returns a PSK valid for the inbound's method.
func testClientPSK(t *testing.T, in model.Inbound) string {
	t.Helper()
	psk, err := ssserver.GenerateClientPSK(in.Method)
	if err != nil {
		t.Fatalf("psk: %v", err)
	}
	return psk
}
