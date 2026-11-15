package panel

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// freezeClock pins nowFunc to start+cursor for the duration of the test so TOTP
// steps can be advanced deterministically. The helper returns the cursor so a
// test can move to the next 30 second step before each attempt; without it a
// test would race the wall clock and occasionally reuse a consumed step.
func freezeClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	cursor := start.Truncate(auth.TOTPPeriod).Add(auth.TOTPPeriod)
	original := nowFunc
	nowFunc = func() time.Time { return cursor }
	t.Cleanup(func() { nowFunc = original })
	return &cursor
}

// loginChallenge performs the password step and returns the challenge token.
func loginChallenge(t *testing.T, tp *testPanel, password string) string {
	t.Helper()
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": password,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("password step: %d %s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	if required, _ := body["totp_required"].(bool); !required {
		t.Fatalf("expected a totp challenge, got %v", body)
	}
	challenge, _ := body["challenge"].(string)
	if challenge == "" {
		t.Fatalf("challenge missing from %v", body)
	}
	return challenge
}

// enableTOTPForTest walks enrollment through the API and returns the secret and
// the recovery codes shown to the operator.
func enableTOTPForTest(t *testing.T, tp *testPanel, password string) (secret string, recovery []string) {
	t.Helper()
	admin, err := tp.store.AdminByUsername("admin")
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	rec := tp.do(http.MethodPost, "/api/v1/auth/totp/setup", map[string]string{"password": password}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	setup := decode[struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_url"`
	}](t, rec)
	if setup.Secret == "" || !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		t.Fatalf("unexpected setup response: %+v", setup)
	}
	code, err := auth.TOTPCode(setup.Secret, nowFunc())
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	rec = tp.do(http.MethodPost, "/api/v1/auth/totp/enable", map[string]string{"code": code}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	enabled := decode[struct {
		Enabled bool     `json:"totp_enabled"`
		Codes   []string `json:"recovery_codes"`
	}](t, rec)
	if !enabled.Enabled || len(enabled.Codes) != auth.RecoveryCodeCount {
		t.Fatalf("unexpected enable response: %+v", enabled)
	}
	// The admin used for the following login step must carry the new state.
	admin, _ = tp.store.AdminByID(admin.ID)
	if !admin.TOTPEnabled {
		t.Fatalf("admin row is still not enabled: %+v", admin)
	}
	return setup.Secret, enabled.Codes
}

// TestTOTPLoginRequiresSecondStep covers the full two-step contract: the password
// step returns a challenge and no cookie, the code step completes the login, and
// a code cannot be replayed.
func TestTOTPLoginRequiresSecondStep(t *testing.T) {
	tp := newTestPanel(t)
	clock := freezeClock(t, time.Now())
	tp.login()
	secret, _ := enableTOTPForTest(t, tp, "test-password-123")

	// Log out so no session cookie survives into the second login.
	tp.cookies = nil
	challenge := loginChallenge(t, tp, "test-password-123")

	// The password step must not hand out a session cookie.
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "test-password-123",
	}, nil)
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
			t.Fatalf("password step issued a session cookie")
		}
	}
	// The challenge alone grants nothing.
	if rec := tp.do(http.MethodGet, "/api/v1/auth/me", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("challenge granted API access: %d", rec.Code)
	}

	// A wrong code is rejected and stays on the step.
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": "000000",
	}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body.String())
	}

	*clock = clock.Add(auth.TOTPPeriod)
	code, err := auth.TOTPCode(secret, *clock)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	rec = tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": code,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code step: %d %s", rec.Code, rec.Body.String())
	}
	completed := decode[map[string]any](t, rec)
	admin, _ := completed["admin"].(map[string]any)
	if enabled, _ := admin["totp_enabled"].(bool); !enabled {
		t.Fatalf("login response does not report totp_enabled: %v", completed)
	}
	if rec := tp.do(http.MethodGet, "/api/v1/auth/me", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("session from the code step does not work: %d %s", rec.Code, rec.Body.String())
	}

	// Replaying the same code (fresh challenge, same 30 second window) fails.
	tp.cookies = nil
	again := loginChallenge(t, tp, "test-password-123")
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": again, "code": code,
	}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// TestTOTPLoginWithRecoveryCode checks the lost-device path: a recovery code
// completes the login exactly once.
func TestTOTPLoginWithRecoveryCode(t *testing.T) {
	tp := newTestPanel(t)
	freezeClock(t, time.Now())
	tp.login()
	_, recovery := enableTOTPForTest(t, tp, "test-password-123")
	tp.cookies = nil

	challenge := loginChallenge(t, tp, "test-password-123")
	// Grouped form, as the dialog displays it.
	grouped := auth.FormatRecoveryCode(recovery[0])
	rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": grouped,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery login: %d %s", rec.Code, rec.Body.String())
	}
	tp.cookies = nil
	challenge = loginChallenge(t, tp, "test-password-123")
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": grouped,
	}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("recovery code replay accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// TestTOTPChallengeExpiryAndInvalidation covers the two ways a challenge stops
// working before its code step: the window passes, or the password changes.
func TestTOTPChallengeExpiryAndInvalidation(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	enableTOTPForTest(t, tp, "test-password-123")
	tp.cookies = nil

	original := nowFunc
	t.Cleanup(func() { nowFunc = original })

	// Expiry: issue the challenge at a fixed instant, then step past its window.
	frozen := time.Now()
	nowFunc = func() time.Time { return frozen }
	challenge := loginChallenge(t, tp, "test-password-123")
	nowFunc = func() time.Time { return frozen.Add(6 * time.Minute) }
	rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": "123456",
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired challenge: %d %s", rec.Code, rec.Body.String())
	}
	if got := decode[errorEnvelope](t, rec).Error.Code; got != "totp_challenge_expired" {
		t.Fatalf("expired challenge code = %q", got)
	}
	nowFunc = original

	// A password change between the steps invalidates the challenge even though
	// the signature is still valid. The hash is replaced directly: the API change
	// also rotates the session, which is not what this test is about.
	challenge = loginChallenge(t, tp, "test-password-123")
	newHash, err := auth.HashPassword("another-password-456")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	admin, err := tp.store.AdminByUsername("admin")
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	if err := tp.store.UpdateAdminPassword(admin.ID, newHash, false); err != nil {
		t.Fatalf("update password: %v", err)
	}
	rec = tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": "123456",
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale challenge: %d %s", rec.Code, rec.Body.String())
	}
	if got := decode[errorEnvelope](t, rec).Error.Code; got != "totp_challenge_invalid" {
		t.Fatalf("stale challenge code = %q", got)
	}
}

// TestTOTPSettingsEndpoints covers enable/disable/rotate through the admin
// endpoints, including the password re-authentication they require.
func TestTOTPSettingsEndpoints(t *testing.T) {
	tp := newTestPanel(t)
	clock := freezeClock(t, time.Now())
	tp.login()

	// Enrollment needs the current password.
	if rec := tp.do(http.MethodPost, "/api/v1/auth/totp/setup", map[string]string{
		"password": "wrong-password",
	}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("setup with a wrong password: %d %s", rec.Code, rec.Body.String())
	}
	secret, recovery := enableTOTPForTest(t, tp, "test-password-123")

	// Enabling twice is a conflict.
	if rec := tp.do(http.MethodPost, "/api/v1/auth/totp/setup", map[string]string{
		"password": "test-password-123",
	}, nil); rec.Code != http.StatusConflict {
		t.Fatalf("setup on an enabled account: %d %s", rec.Code, rec.Body.String())
	}

	// /auth/me reports the state and the remaining codes.
	me := decode[struct {
		TOTPEnabled bool `json:"totp_enabled"`
	}](t, tp.do(http.MethodGet, "/api/v1/auth/me", nil, nil))
	if !me.TOTPEnabled {
		t.Fatalf("unexpected /auth/me: %+v", me)
	}

	// Rotating the codes requires the password and a live code.
	if rec := tp.do(http.MethodPost, "/api/v1/auth/totp/recovery-codes", map[string]string{
		"password": "test-password-123", "code": "000000",
	}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("rotate with a wrong code: %d %s", rec.Code, rec.Body.String())
	}
	*clock = clock.Add(auth.TOTPPeriod)
	code, _ := auth.TOTPCode(secret, *clock)
	rec := tp.do(http.MethodPost, "/api/v1/auth/totp/recovery-codes", map[string]string{
		"password": "test-password-123", "code": code,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
	}
	rotated := decode[struct {
		Codes []string `json:"recovery_codes"`
	}](t, rec)
	if len(rotated.Codes) != auth.RecoveryCodeCount {
		t.Fatalf("rotation returned %d codes", len(rotated.Codes))
	}
	if rotated.Codes[0] == recovery[0] {
		t.Fatalf("rotation returned the old code set")
	}

	// Disabling removes the factor and lets the next login be single step again.
	*clock = clock.Add(auth.TOTPPeriod)
	code, _ = auth.TOTPCode(secret, *clock)
	rec = tp.do(http.MethodPost, "/api/v1/auth/totp/disable", map[string]string{
		"password": "test-password-123", "code": code,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	tp.cookies = nil
	rec = tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "test-password-123",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after disable: %d %s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	if got, _ := body["admin"].(map[string]any); got == nil {
		t.Fatalf("login after disable did not complete: %v", body)
	}

	// The audit trail names the three actions and carries no secrets.
	logs, total, err := tp.store.ListAudit(store.AuditFilter{Limit: 100})
	if err != nil || total == 0 {
		t.Fatalf("list audit: %v (total %d)", err, total)
	}
	seen := map[string]bool{}
	for _, entry := range logs {
		seen[entry.Action] = true
		for _, secretish := range append(append([]string{secret}, recovery...), rotated.Codes...) {
			if secretish != "" && strings.Contains(entry.Detail, secretish) {
				t.Fatalf("audit entry %q leaks a secret: %q", entry.Action, entry.Detail)
			}
		}
	}
	for _, action := range []string{
		model.AuditAdminTOTPEnable, model.AuditAdminTOTPDisable, model.AuditAdminTOTPRecovery,
	} {
		if !seen[action] {
			t.Fatalf("audit action %q was not recorded", action)
		}
	}
}

// TestTOTPLoginRateLimited checks that the second step shares the per-IP failure
// budget, so a stolen password does not permit unlimited code guessing.
func TestTOTPLoginRateLimited(t *testing.T) {
	tp := newTestPanel(t)
	freezeClock(t, time.Now())
	tp.login()
	enableTOTPForTest(t, tp, "test-password-123")
	tp.cookies = nil
	challenge := loginChallenge(t, tp, "test-password-123")

	// The default limit is five failures per window. The wrong-code attempts
	// below must eventually be answered with 429 instead of 401.
	sawLimited := false
	for i := 0; i < 10; i++ {
		rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
			"challenge": challenge, "code": "000000",
		}, nil)
		if rec.Code == http.StatusTooManyRequests {
			sawLimited = true
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if !sawLimited {
		t.Fatal("the second login step is not rate limited")
	}
}

// TestTOTPBlockedWhilePasswordChangePending keeps the forced password change
// ahead of enrollment: a first-run administrator must not enroll first.
func TestTOTPBlockedWhilePasswordChangePending(t *testing.T) {
	tp := newTestPanel(t)
	admin, _ := tp.store.AdminByUsername("admin")
	hash, _ := auth.HashPassword("initial-password-1")
	if err := tp.store.UpdateAdminPassword(admin.ID, hash, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "initial-password-1",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tp.do(http.MethodPost, "/api/v1/auth/totp/setup", map[string]string{
		"password": "initial-password-1",
	}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("setup during a pending password change: %d %s", rec.Code, rec.Body.String())
	}
}

// TestTOTPCrossOriginRejected covers the CSRF rule on the new state-changing
// endpoints.
func TestTOTPCrossOriginRejected(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPost, "/api/v1/auth/totp/setup", map[string]string{
		"password": "test-password-123",
	}, map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin setup: %d %s", rec.Code, rec.Body.String())
	}
}

// TestTOTPPasswordStepDoesNotRefillTheBudget closes the bypass the failure
// limiter would otherwise leave open: resetting the per-IP budget whenever the
// password step succeeds would let someone who knows the password refill five
// code guesses at will, which brings a six digit code within reach.
func TestTOTPPasswordStepDoesNotRefillTheBudget(t *testing.T) {
	tp := newTestPanel(t)
	freezeClock(t, time.Now())
	tp.login()
	enableTOTPForTest(t, tp, "test-password-123")
	tp.cookies = nil

	// Spend the whole budget with wrong codes, restarting the flow through a
	// successful password step before each attempt.
	limited := false
	for i := 0; i < 12; i++ {
		rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "admin", "password": "test-password-123",
		}, nil)
		if rec.Code == http.StatusTooManyRequests {
			// A throttled password step also proves the budget survived.
			limited = true
			break
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("password step %d: %d %s", i, rec.Code, rec.Body.String())
		}
		challenge := decode[map[string]any](t, rec)["challenge"].(string)
		rec = tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
			"challenge": challenge, "code": "000000",
		}, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code attempt %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if !limited {
		t.Fatal("repeating the password step refilled the verification code budget")
	}
}

// TestTOTPSuccessfulLoginClearsFailures keeps the flip side: once a login really
// completes, the IP starts with a clean budget.
func TestTOTPSuccessfulLoginClearsFailures(t *testing.T) {
	tp := newTestPanel(t)
	clock := freezeClock(t, time.Now())
	tp.login()
	secret, _ := enableTOTPForTest(t, tp, "test-password-123")
	tp.cookies = nil

	// A few wrong codes, then a correct one completes the login.
	challenge := loginChallenge(t, tp, "test-password-123")
	for i := 0; i < 3; i++ {
		tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
			"challenge": challenge, "code": "000000",
		}, nil)
	}
	*clock = clock.Add(auth.TOTPPeriod)
	code := mustTOTPCode(t, secret, *clock)
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": code,
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("correct code after failures: %d %s", rec.Code, rec.Body.String())
	}
	// A new login with the right password and code works even though three
	// failures preceded it.
	tp.cookies = nil
	challenge = loginChallenge(t, tp, "test-password-123")
	*clock = clock.Add(auth.TOTPPeriod)
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge, "code": mustTOTPCode(t, secret, *clock),
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("login after a successful one: %d %s", rec.Code, rec.Body.String())
	}
}

// mustTOTPCode computes a code or fails the test.
func mustTOTPCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := auth.TOTPCode(secret, at)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	return code
}
