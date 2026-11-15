package service

import (
	"errors"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
)

// Errors returned by the two-step verification operations. The panel maps each
// one onto a stable API error code so the settings dialog can tell a stale
// enrollment apart from a wrong password.
var (
	// ErrTOTPNotEnabled is returned when the account has no active second factor.
	ErrTOTPNotEnabled = errors.New("two-step verification is not enabled")
	// ErrTOTPAlreadyEnabled is returned when enrollment is attempted twice.
	ErrTOTPAlreadyEnabled = errors.New("two-step verification is already enabled")
	// ErrTOTPNotSetup is returned when confirmation arrives without a pending
	// enrollment.
	ErrTOTPNotSetup = errors.New("no pending two-step verification enrollment")
	// ErrInvalidTOTPCode covers a wrong code, a reused code and an unknown
	// recovery code: distinguishing them would only help an attacker.
	ErrInvalidTOTPCode = errors.New("invalid verification code")
	// ErrTOTPState reports a database row that claims two-step verification is
	// on but has no usable secret. Login fails closed on it; the operator can
	// clear the state with `sspanel admin reset-totp`.
	ErrTOTPState = errors.New("two-step verification state is inconsistent")
)

// TOTPSetup begins enrollment: it re-authenticates the administrator with the
// current password and stores a proposed secret. The pending secret has no
// effect on login until TOTPEnable confirms it with a live code.
//
// Re-authenticating here is what stops a hijacked session from binding an
// attacker's authenticator as a permanent backdoor: the password is the one
// credential the session cookie does not carry.
func (s *Service) TOTPSetup(id int64, password string, now time.Time) (secret, otpauthURL string, err error) {
	admin, err := s.store.AdminByID(id)
	if err != nil {
		return "", "", err
	}
	if !auth.VerifyPassword(admin.PasswordHash, password) {
		return "", "", ErrInvalidPassword
	}
	if admin.TOTPEnabled {
		return "", "", ErrTOTPAlreadyEnabled
	}
	secret, err = auth.GenerateTOTPSecret()
	if err != nil {
		return "", "", err
	}
	if err := s.store.SetAdminTOTPPending(id, secret); err != nil {
		return "", "", err
	}
	// The secret travels back to the browser once, over the panel connection, so
	// the operator can add it to an authenticator. It is not written to the log,
	// the audit trail or any response body elsewhere.
	return secret, auth.TOTPURI(secret, admin.Username), nil
}

// TOTPEnable confirms a pending enrollment with a code from the authenticator
// and switches the second factor on. It returns the recovery codes, which are
// the only time the plaintext codes exist: the database keeps hashes only.
func (s *Service) TOTPEnable(id int64, code string, now time.Time) (recoveryCodes []string, err error) {
	admin, err := s.store.AdminByID(id)
	if err != nil {
		return nil, err
	}
	if admin.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	if admin.TOTPPendingSecret == "" {
		return nil, ErrTOTPNotSetup
	}
	step, ok := auth.VerifyTOTP(admin.TOTPPendingSecret, code, now)
	if !ok {
		return nil, ErrInvalidTOTPCode
	}
	codes, err := auth.GenerateRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	// The confirming code is recorded as consumed, so it cannot be replayed at a
	// login that follows shortly after enrollment.
	if err := s.store.EnableAdminTOTP(id, admin.TOTPPendingSecret, step, hashRecoveryCodes(codes)); err != nil {
		return nil, err
	}
	return codes, nil
}

// TOTPVerifyLogin checks the second factor of a login. It accepts both a TOTP
// code and a one-time recovery code, and consumes whichever matched.
func (s *Service) TOTPVerifyLogin(id int64, code string, now time.Time) error {
	admin, err := s.store.AdminByID(id)
	if err != nil {
		return err
	}
	if !admin.TOTPEnabled {
		return ErrTOTPNotEnabled
	}
	return s.verifySecondFactor(admin, code, now)
}

// TOTPDisable turns the second factor off after re-authenticating with both the
// password and a live second factor. Requiring both means a stolen session
// cannot silently remove the protection it was meant to survive.
func (s *Service) TOTPDisable(id int64, password, code string, now time.Time) error {
	admin, err := s.store.AdminByID(id)
	if err != nil {
		return err
	}
	if !auth.VerifyPassword(admin.PasswordHash, password) {
		return ErrInvalidPassword
	}
	if !admin.TOTPEnabled {
		return ErrTOTPNotEnabled
	}
	if err := s.verifySecondFactor(admin, code, now); err != nil {
		return err
	}
	return s.store.DisableAdminTOTP(id)
}

// TOTPRegenerateRecovery replaces the recovery code set after re-authenticating
// with the password and a live second factor. The previous codes stop working
// immediately.
func (s *Service) TOTPRegenerateRecovery(id int64, password, code string, now time.Time) ([]string, error) {
	admin, err := s.store.AdminByID(id)
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(admin.PasswordHash, password) {
		return nil, ErrInvalidPassword
	}
	if !admin.TOTPEnabled {
		return nil, ErrTOTPNotEnabled
	}
	if err := s.verifySecondFactor(admin, code, now); err != nil {
		return nil, err
	}
	codes, err := auth.GenerateRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceAdminTOTPRecoveryCodes(id, hashRecoveryCodes(codes)); err != nil {
		return nil, err
	}
	return codes, nil
}

// verifySecondFactor accepts a TOTP code or a recovery code and consumes it.
func (s *Service) verifySecondFactor(admin *model.Admin, code string, now time.Time) error {
	if auth.LooksLikeTOTPCode(code) {
		if admin.TOTPSecret == "" {
			return ErrTOTPState
		}
		step, ok := auth.VerifyTOTP(admin.TOTPSecret, code, now)
		if !ok {
			return ErrInvalidTOTPCode
		}
		// Consuming the step makes the code single use even inside its own
		// 30 second window.
		consumed, err := s.store.ConsumeAdminTOTPStep(admin.ID, step)
		if err != nil {
			return err
		}
		if !consumed {
			return ErrInvalidTOTPCode
		}
		return nil
	}
	normalized := auth.NormalizeRecoveryCode(code)
	if normalized == "" {
		return ErrInvalidTOTPCode
	}
	consumed, err := s.store.ConsumeAdminTOTPRecoveryCode(admin.ID, auth.HashRecoveryCode(normalized))
	if err != nil {
		return err
	}
	if !consumed {
		return ErrInvalidTOTPCode
	}
	return nil
}

// hashRecoveryCodes converts plaintext codes into their stored form.
func hashRecoveryCodes(codes []string) []string {
	hashes := make([]string, 0, len(codes))
	for _, code := range codes {
		hashes = append(hashes, auth.HashRecoveryCode(code))
	}
	return hashes
}
