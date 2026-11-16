package panel

import (
	"net/http"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// TestReauthIsThrottledAndAudited covers the password confirmation gate: without
// it a stolen session could guess the password as fast as bcrypt allows, then
// rotate it or remove the second factor.
func TestReauthIsThrottledAndAudited(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	// The limiter allows five failures per window; the sixth attempt must be
	// refused instead of hashing another guess.
	for i := 0; i < 5; i++ {
		rec := tp.do(http.MethodPut, "/api/v1/auth/password", map[string]string{
			"current_password": "wrong-password", "new_password": "another-password-456",
		}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: expected 400, got %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := tp.do(http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "test-password-123", "new_password": "another-password-456",
	}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after the failure budget, got %d %s", rec.Code, rec.Body.String())
	}
	// The same budget covers the two-step verification endpoints, so a guessed
	// password cannot be spent there instead.
	rec = tp.do(http.MethodPost, "/api/v1/auth/totp/setup", map[string]string{
		"password": "test-password-123",
	}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("totp setup: expected 429, got %d %s", rec.Code, rec.Body.String())
	}

	// Failures are recorded so an operator sees someone guessing.
	audit := decode[struct {
		Items []model.AuditLog `json:"items"`
	}](t, tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditAdminReauthFailed+"&limit=50", nil, nil))
	if len(audit.Items) != 5 {
		t.Fatalf("audited %d failed confirmations, want 5", len(audit.Items))
	}

	// A correct confirmation clears the budget: the operator is not locked out
	// of their own account by their own typo once the window passes.
	tp.server.limiter.Reset(reauthKeyForTest(t, tp))
	if rec := tp.do(http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "test-password-123", "new_password": "another-password-456",
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("confirmation after reset: %d %s", rec.Code, rec.Body.String())
	}
}

// reauthKeyForTest resolves the limiter key of the logged-in administrator.
func reauthKeyForTest(t *testing.T, tp *testPanel) string {
	t.Helper()
	admin, err := tp.store.AdminByUsername("admin")
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	return reauthKey(admin)
}
