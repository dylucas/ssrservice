package panel

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
)

var nowFunc = time.Now

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

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
	admin, err := s.svc.Store().AdminByUsername(req.Username)
	if err != nil || !auth.VerifyPassword(admin.PasswordHash, req.Password) {
		s.limiter.Fail(ip, now)
		// The attempted password is never recorded.
		s.auditLogin(c, req.Username, nil, model.AuditAuthLoginFailed)
		writeError(c, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	s.limiter.Reset(ip)
	_ = s.svc.Store().TouchAdminLogin(admin.ID)
	s.auditLogin(c, admin.Username, admin, model.AuditAuthLogin)

	cookie, expires, err := s.sessions.Issue(admin.ID, admin.PasswordHash, now)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(auth.SessionCookieName, cookie, int(time.Until(expires).Seconds()), "/", "", s.isSecureRequest(c), true)
	c.JSON(http.StatusOK, gin.H{
		"admin": gin.H{
			"id":                   admin.ID,
			"username":             admin.Username,
			"must_change_password": admin.MustChangePassword,
		},
		"expires_at": expires.Unix(),
	})
}

func (s *Server) handleLogout(c *gin.Context) {
	s.audit(c, auditEntry{Action: model.AuditAuthLogout, TargetType: "admin"})
	s.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleMe(c *gin.Context) {
	admin := currentAdmin(c)
	if admin == nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":                   admin.ID,
		"username":             admin.Username,
		"must_change_password": admin.MustChangePassword,
		"created_at":           admin.CreatedAt,
		"last_login_at":        admin.LastLoginAt,
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
	if !auth.VerifyPassword(admin.PasswordHash, req.CurrentPassword) {
		writeError(c, http.StatusBadRequest, "invalid_password", "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeBadRequest(c, err.Error())
		return
	}
	if err := s.svc.Store().UpdateAdminPassword(admin.ID, hash, false); err != nil {
		writeServiceError(c, err)
		return
	}
	// Neither the old nor the new password is recorded.
	s.audit(c, auditEntry{Action: model.AuditAdminPassword, TargetType: "admin", TargetID: admin.ID})
	// Re-issue the cookie so the current session survives the version change.
	now := nowFunc()
	cookie, expires, err := s.sessions.Issue(admin.ID, hash, now)
	if err == nil {
		c.SetSameSite(http.SameSiteStrictMode)
		c.SetCookie(auth.SessionCookieName, cookie, int(time.Until(expires).Seconds()), "/", "", s.isSecureRequest(c), true)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
