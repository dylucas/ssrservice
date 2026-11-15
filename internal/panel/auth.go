package panel

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

var nowFunc = time.Now

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// adminJSON is the administrator shape returned by login and /auth/me. The TOTP
// secrets are deliberately absent: they are credentials and must never leave the
// server once enrollment is done.
func adminJSON(admin *model.Admin) gin.H {
	return gin.H{
		"id":                   admin.ID,
		"username":             admin.Username,
		"must_change_password": admin.MustChangePassword,
		"totp_enabled":         admin.TOTPEnabled,
	}
}

// completeLogin finishes a login that has passed every required factor: it
// records the login, issues the session cookie and returns the response body.
func (s *Server) completeLogin(c *gin.Context, admin *model.Admin, now time.Time) {
	_ = s.svc.TouchAdminLogin(admin.ID)
	s.auditLogin(c, admin.Username, admin, model.AuditAuthLogin, "")
	cookie, expires, err := s.sessions.Issue(admin.ID, admin.PasswordHash, now)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	s.setSessionCookie(c, cookie, expires)
	c.JSON(http.StatusOK, gin.H{
		"admin":      adminJSON(admin),
		"expires_at": expires.Unix(),
	})
}

func (s *Server) setSessionCookie(c *gin.Context, cookie string, expires time.Time) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(auth.SessionCookieName, cookie, int(time.Until(expires).Seconds()), "/", "", s.isSecureRequest(c), true)
}

// handleLogin is the password step. When the administrator has two-step
// verification enabled it does not issue a session: it returns a short lived
// challenge that the code step (handleTOTPLogin) must present.
func (s *Server) handleLogin(c *gin.Context) {
	ip := c.ClientIP()
	now := nowFunc()
	if !s.limiter.Allow(ip, now) {
		writeError(c, http.StatusTooManyRequests, "too_many_attempts", "too many failed login attempts, try again later")
		return
	}
	var req loginRequest
	if !bindJSONMessage(c, &req, "username and password are required") {
		return
	}
	admin, err := s.svc.Authenticate(req.Username, req.Password)
	if err != nil {
		s.limiter.Fail(ip, now)
		// The attempted password is never recorded, and an unknown user is
		// answered exactly like a wrong password.
		s.auditLogin(c, req.Username, nil, model.AuditAuthLoginFailed, "")
		writeError(c, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if !admin.TOTPEnabled {
		s.limiter.Reset(ip)
		s.completeLogin(c, admin, now)
		return
	}
	// The failure budget is deliberately NOT reset here. The password step only
	// proves knowledge of the password, which is exactly what an attacker who is
	// stuck on the second factor already has: resetting would let them refill
	// five code attempts by repeating the password step, and a six digit code
	// would then fall within reach. The budget is cleared once a login completes
	// (completeLogin's callers), so a normal operator never carries old failures
	// past a successful sign in.
	challenge, expires, err := s.sessions.IssueChallenge(admin.ID, admin.PasswordHash, now)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	// Deliberately no cookie, no last-login timestamp and no auth.login audit
	// entry: this login has not passed the second factor yet.
	c.JSON(http.StatusOK, gin.H{
		"totp_required": true,
		"challenge":     challenge,
		"expires_at":    expires.Unix(),
	})
}

type totpLoginRequest struct {
	Challenge string `json:"challenge" binding:"required"`
	Code      string `json:"code" binding:"required"`
}

// handleTOTPLogin is the second step: the challenge proves the password was
// accepted, and the code proves possession of the authenticator (or of a
// recovery code).
func (s *Server) handleTOTPLogin(c *gin.Context) {
	ip := c.ClientIP()
	now := nowFunc()
	if !s.limiter.Allow(ip, now) {
		writeError(c, http.StatusTooManyRequests, "too_many_attempts", "too many failed login attempts, try again later")
		return
	}
	var req totpLoginRequest
	if !bindJSONMessage(c, &req, "challenge and code are required") {
		return
	}
	adminID, version, err := s.sessions.ParseChallenge(req.Challenge, now)
	if err != nil {
		if errors.Is(err, auth.ErrExpiredChallenge) {
			writeError(c, http.StatusUnauthorized, "totp_challenge_expired", "the verification window expired, sign in again")
			return
		}
		writeError(c, http.StatusUnauthorized, "totp_challenge_invalid", "invalid verification challenge, sign in again")
		return
	}
	// The challenge is bound to the password hash, so a password change between
	// the two steps invalidates it instead of letting the old password's proof
	// complete a login against the new one.
	admin, err := s.svc.AdminByID(adminID)
	if err != nil || !auth.ConstantTimeEqual(version, auth.SessionVersion(admin.PasswordHash)) {
		writeError(c, http.StatusUnauthorized, "totp_challenge_invalid", "invalid verification challenge, sign in again")
		return
	}
	err = s.svc.TOTPVerifyLogin(admin.ID, req.Code, now)
	switch {
	case errors.Is(err, service.ErrInvalidTOTPCode), errors.Is(err, service.ErrTOTPNotEnabled):
		s.limiter.Fail(ip, now)
		// The code itself never reaches the trail; the stage tells an operator
		// that the password was right and the second factor was not.
		s.auditLogin(c, admin.Username, admin, model.AuditAuthLoginFailed, "stage=totp")
		writeError(c, http.StatusUnauthorized, "invalid_totp", "invalid verification code")
		return
	case errors.Is(err, service.ErrTOTPState):
		// Fail closed: never issue a session for a row that claims two-step
		// verification is on while holding no usable secret.
		s.logger.Error("two-step verification state is inconsistent",
			"admin_id", admin.ID, "username", admin.Username, "error", err)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	case err != nil:
		s.writeServiceError(c, err)
		return
	}
	// The full login succeeded, so this IP's earlier failures are history.
	s.limiter.Reset(ip)
	s.completeLogin(c, admin, now)
}

func (s *Server) handleLogout(c *gin.Context) {
	s.audit(c, auditEntry{Action: model.AuditAuthLogout, TargetType: "admin"})
	s.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type meResponse struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
	TOTPEnabled        bool   `json:"totp_enabled"`
	CreatedAt          int64  `json:"created_at"`
	LastLoginAt        int64  `json:"last_login_at"`
}

func (s *Server) handleMe(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	c.JSON(http.StatusOK, meResponse{
		ID:                 admin.ID,
		Username:           admin.Username,
		MustChangePassword: admin.MustChangePassword,
		TOTPEnabled:        admin.TOTPEnabled,
		CreatedAt:          admin.CreatedAt,
		LastLoginAt:        admin.LastLoginAt,
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

func (s *Server) handleChangePassword(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req changePasswordRequest
	if !bindJSONMessage(c, &req, "current_password and new_password are required") {
		return
	}
	hash, err := s.svc.ChangeAdminPassword(admin.ID, req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, service.ErrInvalidPassword):
		writeError(c, http.StatusBadRequest, "invalid_password", "current password is incorrect")
		return
	case service.IsValidation(err):
		writeBadRequest(c, err.Error())
		return
	case err != nil:
		s.writeServiceError(c, err)
		return
	}
	// Neither the old nor the new password is recorded.
	s.audit(c, auditEntry{Action: model.AuditAdminPassword, TargetType: "admin", TargetID: admin.ID})
	// Re-issue the cookie so the current session survives the version change.
	now := nowFunc()
	cookie, expires, err := s.sessions.Issue(admin.ID, hash, now)
	if err == nil {
		s.setSessionCookie(c, cookie, expires)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// writeTOTPError maps the two-step verification domain errors onto stable API
// codes. They are explicit rather than going through writeServiceError so the
// settings dialog can react per case (wrong password vs wrong code vs already
// enabled) instead of showing a generic message.
func writeTOTPError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPassword):
		writeError(c, http.StatusBadRequest, "invalid_password", "current password is incorrect")
	case errors.Is(err, service.ErrInvalidTOTPCode):
		writeError(c, http.StatusBadRequest, "invalid_totp", "invalid verification code")
	case errors.Is(err, service.ErrTOTPAlreadyEnabled):
		writeError(c, http.StatusConflict, "totp_already_enabled", "two-step verification is already enabled")
	case errors.Is(err, service.ErrTOTPNotSetup):
		writeError(c, http.StatusBadRequest, "totp_not_setup", "start the enrollment again")
	case errors.Is(err, service.ErrTOTPNotEnabled):
		writeError(c, http.StatusBadRequest, "totp_not_enabled", "two-step verification is not enabled")
	case service.IsValidation(err):
		writeBadRequest(c, err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

type totpSetupRequest struct {
	Password string `json:"password" binding:"required"`
}

// handleTOTPSetup starts enrollment: it returns the proposed secret and the
// otpauth:// URI to scan. Login is unaffected until handleTOTPEnable confirms it.
func (s *Server) handleTOTPSetup(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req totpSetupRequest
	if !bindJSONMessage(c, &req, "password is required") {
		return
	}
	secret, uri, err := s.svc.TOTPSetup(admin.ID, req.Password, nowFunc())
	if err != nil {
		if errors.Is(err, service.ErrInvalidPassword) || errors.Is(err, service.ErrTOTPAlreadyEnabled) {
			writeTOTPError(c, err)
			return
		}
		s.logger.Error("start two-step verification enrollment failed", "admin_id", admin.ID, "error", err)
		writeTOTPError(c, err)
		return
	}
	// The secret is part of the response by necessity; it is never audited.
	c.JSON(http.StatusOK, gin.H{"secret": secret, "otpauth_url": uri})
}

type totpCodeRequest struct {
	Code string `json:"code" binding:"required"`
}

// handleTOTPEnable confirms enrollment with a live code and returns the recovery
// codes. This is the only response that ever carries them in plaintext.
func (s *Server) handleTOTPEnable(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req totpCodeRequest
	if !bindJSONMessage(c, &req, "code is required") {
		return
	}
	codes, err := s.svc.TOTPEnable(admin.ID, req.Code, nowFunc())
	if err != nil {
		if errors.Is(err, service.ErrInvalidTOTPCode) || errors.Is(err, service.ErrTOTPAlreadyEnabled) ||
			errors.Is(err, service.ErrTOTPNotSetup) {
			writeTOTPError(c, err)
			return
		}
		s.logger.Error("confirm two-step verification enrollment failed", "admin_id", admin.ID, "error", err)
		writeTOTPError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditAdminTOTPEnable, TargetType: "admin", TargetID: admin.ID,
		Detail: auditFields("recovery_codes", len(codes)),
	})
	admin.TOTPEnabled = true
	c.JSON(http.StatusOK, gin.H{
		"totp_enabled":   true,
		"recovery_codes": codes,
	})
}

type totpPasswordCodeRequest struct {
	Password string `json:"password" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

// handleTOTPDisable turns the second factor off. It requires the password and a
// live code, so a hijacked session cannot remove it.
func (s *Server) handleTOTPDisable(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req totpPasswordCodeRequest
	if !bindJSONMessage(c, &req, "password and code are required") {
		return
	}
	err := s.svc.TOTPDisable(admin.ID, req.Password, req.Code, nowFunc())
	if err != nil {
		if isTOTPDomainError(err) {
			writeTOTPError(c, err)
			return
		}
		s.logger.Error("disable two-step verification failed", "admin_id", admin.ID, "error", err)
		writeTOTPError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditAdminTOTPDisable, TargetType: "admin", TargetID: admin.ID})
	admin.TOTPEnabled = false
	c.JSON(http.StatusOK, gin.H{"ok": true, "totp_enabled": false})
}

// handleTOTPRecoveryCodes rotates the recovery code set. The old codes stop
// working immediately.
func (s *Server) handleTOTPRecoveryCodes(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req totpPasswordCodeRequest
	if !bindJSONMessage(c, &req, "password and code are required") {
		return
	}
	codes, err := s.svc.TOTPRegenerateRecovery(admin.ID, req.Password, req.Code, nowFunc())
	if err != nil {
		if isTOTPDomainError(err) {
			writeTOTPError(c, err)
			return
		}
		s.logger.Error("regenerate recovery codes failed", "admin_id", admin.ID, "error", err)
		writeTOTPError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditAdminTOTPRecovery, TargetType: "admin", TargetID: admin.ID,
		Detail: auditFields("recovery_codes", len(codes)),
	})
	c.JSON(http.StatusOK, gin.H{"recovery_codes": codes})
}

// isTOTPDomainError reports whether err is one of the expected two-step
// verification errors, which are not worth an error log line.
func isTOTPDomainError(err error) bool {
	return errors.Is(err, service.ErrInvalidPassword) ||
		errors.Is(err, service.ErrInvalidTOTPCode) ||
		errors.Is(err, service.ErrTOTPNotEnabled) ||
		errors.Is(err, service.ErrTOTPAlreadyEnabled) ||
		errors.Is(err, service.ErrTOTPNotSetup) ||
		service.IsValidation(err)
}
