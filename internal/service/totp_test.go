package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// newTOTPEnv builds a Service over a temp store holding one administrator with a
// known password.
func newTOTPEnv(t *testing.T) (*Service, *store.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "totp.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	mgr := ssserver.New(ssserver.Options{
		Sink: st, AccessSink: st, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: 10 * time.Second,
		Policy: ssserver.PolicyConfig{AllowPrivateTargets: true},
	})
	svc := New(st, mgr, cfg, nil)
	t.Cleanup(func() { svc.Stop() })

	hash, err := auth.HashPassword("test-password-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	admin, err := st.CreateAdmin("admin", hash, false)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return svc, st, admin.ID
}

// enableTOTP walks the enrollment and returns the secret plus the new recovery
// codes, which is exactly what the settings dialog would receive.
func enableTOTP(t *testing.T, svc *Service, adminID int64, now time.Time) (secret string, recovery []string) {
	t.Helper()
	secret, uri, err := svc.TOTPSetup(adminID, "test-password-123", now)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if uri == "" || secret == "" {
		t.Fatalf("setup returned secret=%q uri=%q", secret, uri)
	}
	code, err := auth.TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	recovery, err = svc.TOTPEnable(adminID, code, now)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	return secret, recovery
}

func TestTOTPSetupRequiresPassword(t *testing.T) {
	svc, _, adminID := newTOTPEnv(t)
	now := time.Now()
	if _, _, err := svc.TOTPSetup(adminID, "wrong-password", now); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("wrong password setup = %v, want ErrInvalidPassword", err)
	}
	// The failed attempt must not leave a pending secret behind.
	admin, err := svc.AdminByID(adminID)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	if admin.TOTPPendingSecret != "" {
		t.Fatalf("a rejected setup stored a pending secret")
	}
}

func TestTOTPSetupRejectsEnabledAccount(t *testing.T) {
	svc, _, adminID := newTOTPEnv(t)
	now := time.Now()
	enableTOTP(t, svc, adminID, now)
	if _, _, err := svc.TOTPSetup(adminID, "test-password-123", now); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("setup on enabled account = %v, want ErrTOTPAlreadyEnabled", err)
	}
}

func TestTOTPEnableValidation(t *testing.T) {
	svc, _, adminID := newTOTPEnv(t)
	now := time.Now()
	if _, err := svc.TOTPEnable(adminID, "123456", now); !errors.Is(err, ErrTOTPNotSetup) {
		t.Fatalf("enable without setup = %v, want ErrTOTPNotSetup", err)
	}

	secret, _, err := svc.TOTPSetup(adminID, "test-password-123", now)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := svc.TOTPEnable(adminID, "000000", now); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("wrong code = %v, want ErrInvalidTOTPCode", err)
	}
	// Still pending, still disabled after the failed confirmation.
	admin, _ := svc.AdminByID(adminID)
	if admin.TOTPEnabled || admin.TOTPPendingSecret != secret {
		t.Fatalf("failed confirmation changed state: %+v", admin)
	}

	code, _ := auth.TOTPCode(secret, now)
	codes, err := svc.TOTPEnable(adminID, code, now)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes), auth.RecoveryCodeCount)
	}
	admin, _ = svc.AdminByID(adminID)
	if !admin.TOTPEnabled || admin.TOTPSecret != secret || admin.TOTPPendingSecret != "" {
		t.Fatalf("unexpected enabled state: %+v", admin)
	}
	// The confirming code is consumed at enrollment, so it cannot be replayed at
	// a login that follows immediately.
	if err := svc.TOTPVerifyLogin(adminID, code, now); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("enrollment code replayed at login = %v", err)
	}
}

func TestTOTPVerifyLoginSingleUseAndRecovery(t *testing.T) {
	svc, st, adminID := newTOTPEnv(t)
	now := time.Now()
	enableTOTP(t, svc, adminID, now)

	// A code from the next step is accepted once and then rejected.
	next := now.Add(auth.TOTPPeriod)
	admin, _ := svc.AdminByID(adminID)
	code, err := auth.TOTPCode(admin.TOTPSecret, next)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	if err := svc.TOTPVerifyLogin(adminID, code, next); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
	if err := svc.TOTPVerifyLogin(adminID, code, next); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("replayed code = %v, want ErrInvalidTOTPCode", err)
	}

	// A recovery code works exactly once, whether or not it is typed grouped.
	// The plaintext codes are only available at enrollment, so the test rotates
	// the set to work with a known plaintext.
	admin, _ = svc.AdminByID(adminID)
	later := next.Add(2 * auth.TOTPPeriod)
	rotated, err := svc.TOTPRegenerateRecovery(adminID, "test-password-123", mustCode(t, admin.TOTPSecret, later), later)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	grouped := auth.FormatRecoveryCode(rotated[0])
	if err := svc.TOTPVerifyLogin(adminID, grouped, later); err != nil {
		t.Fatalf("grouped recovery code rejected: %v", err)
	}
	if err := svc.TOTPVerifyLogin(adminID, grouped, later); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("recovery code replay = %v, want ErrInvalidTOTPCode", err)
	}
	var left int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM admin_recovery_codes WHERE admin_id = ?`, adminID).Scan(&left); err != nil {
		t.Fatalf("count recovery codes: %v", err)
	}
	if left != auth.RecoveryCodeCount-1 {
		t.Fatalf("recovery codes left = %d, want %d", left, auth.RecoveryCodeCount-1)
	}
}

func TestTOTPStateFailsClosed(t *testing.T) {
	svc, _, adminID := newTOTPEnv(t)
	now := time.Now()
	// Simulate a damaged row: enabled without a secret.
	if err := svc.store.EnableAdminTOTP(adminID, "", 0, nil); err != nil {
		t.Fatalf("force state: %v", err)
	}
	if err := svc.TOTPVerifyLogin(adminID, "123456", now); !errors.Is(err, ErrTOTPState) {
		t.Fatalf("TOTPVerifyLogin on a damaged row = %v, want ErrTOTPState", err)
	}
}

func TestTOTPDisableRequiresPasswordAndCode(t *testing.T) {
	svc, _, adminID := newTOTPEnv(t)
	now := time.Now()
	secret, _ := enableTOTP(t, svc, adminID, now)

	if err := svc.TOTPDisable(adminID, "wrong", "123456", now); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("wrong password = %v", err)
	}
	if err := svc.TOTPDisable(adminID, "test-password-123", "000000", now); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("wrong code = %v", err)
	}
	// Still enabled after the rejected attempts.
	if admin, _ := svc.AdminByID(adminID); !admin.TOTPEnabled {
		t.Fatal("a rejected disable turned two-step verification off")
	}
	code := mustCode(t, secret, now.Add(3*auth.TOTPPeriod))
	if err := svc.TOTPDisable(adminID, "test-password-123", code, now.Add(3*auth.TOTPPeriod)); err != nil {
		t.Fatalf("disable: %v", err)
	}
	admin, _ := svc.AdminByID(adminID)
	if admin.TOTPEnabled || admin.TOTPSecret != "" {
		t.Fatalf("disable left state behind: %+v", admin)
	}
	var left int
	if err := svc.store.DB().QueryRow(`SELECT COUNT(*) FROM admin_recovery_codes WHERE admin_id = ?`, adminID).Scan(&left); err != nil {
		t.Fatalf("count recovery codes: %v", err)
	}
	if left != 0 {
		t.Fatalf("disable left %d recovery codes", left)
	}
	// After disabling, the login path reports "not enabled" rather than a code
	// error, which is what the second login step checks.
	if err := svc.TOTPVerifyLogin(adminID, "123456", now); !errors.Is(err, ErrTOTPNotEnabled) {
		t.Fatalf("verify after disable = %v, want ErrTOTPNotEnabled", err)
	}
}

func TestTOTPRegenerateRecoveryInvalidatesOldCodes(t *testing.T) {
	svc, _, adminID := newTOTPEnv(t)
	now := time.Now()
	secret, old := enableTOTP(t, svc, adminID, now)

	later := now.Add(5 * auth.TOTPPeriod)
	codes, err := svc.TOTPRegenerateRecovery(adminID, "test-password-123", mustCode(t, secret, later), later)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("got %d codes", len(codes))
	}
	if err := svc.TOTPVerifyLogin(adminID, old[0], now); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("old recovery code after rotation = %v", err)
	}
	if err := svc.TOTPVerifyLogin(adminID, codes[0], now); err != nil {
		t.Fatalf("new recovery code rejected: %v", err)
	}
}

// mustCode computes a code or fails the test.
func mustCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := auth.TOTPCode(secret, at)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	return code
}
