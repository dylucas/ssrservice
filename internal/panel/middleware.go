package panel

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

func writeError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorEnvelope{Error: apiError{Code: code, Message: message}})
}

// writeServiceError maps a domain error onto the HTTP error envelope.
//
// Only errors the caller can act on carry their message: validation problems,
// a taken port, a permission denial. A conflict or an unexpected failure is
// answered with a stable, human-readable text instead, because the raw error
// would leak SQLite constraint names, filesystem paths (the backup endpoint
// builds a temp file) and version fingerprints to the browser, and the API
// contract promises a stable envelope. The original error is logged for the
// operator.
func (s *Server) writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrConflict):
		// The store wraps a ConflictError whose message names the collided
		// field; anything else is an unmapped constraint and keeps a generic
		// text while the detail goes to the log.
		var conflict *store.ConflictError
		if errors.As(err, &conflict) {
			writeError(c, http.StatusConflict, "conflict", conflict.Message)
			return
		}
		s.recordInternalError(c, err)
		writeError(c, http.StatusConflict, "conflict", "the resource conflicts with an existing one")
	case errors.Is(err, ssserver.ErrPortInUse):
		writeError(c, http.StatusConflict, "port_in_use", err.Error())
	case errors.Is(err, ssserver.ErrPermission):
		writeError(c, http.StatusForbidden, "permission_denied", err.Error())
	case errors.Is(err, ssserver.ErrInvalidAddr):
		writeError(c, http.StatusBadRequest, "invalid_address", err.Error())
	case service.IsValidation(err):
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.recordInternalError(c, err)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// recordInternalError logs the details the response deliberately omits.
func (s *Server) recordInternalError(c *gin.Context, err error) {
	s.logger.Error("request failed",
		"method", c.Request.Method,
		"path", redactSubscriptionPath(s.cfg.Panel.BasePath, c.Request.URL.Path),
		"error", err,
	)
}

func writeBadRequest(c *gin.Context, message string) {
	writeError(c, http.StatusBadRequest, "invalid_request", message)
}

// noStore marks API responses as uncacheable. They carry PSKs, subscription
// tokens and session state, so no browser or intermediary cache may keep a
// copy.
func noStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}

// limitRequestBody caps the size of request bodies. It is registered on the
// API group only so subscriptions and static assets are unaffected.
func limitRequestBody(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}

// bindJSON decodes a JSON request body, reporting oversized payloads with a
// 413 and malformed bodies with a 400. It returns false when the request was
// already answered.
func bindJSON(c *gin.Context, dst any) bool {
	return bindJSONMessage(c, dst, "invalid request body")
}

// bindJSONMessage is bindJSON with a custom message for malformed bodies.
func bindJSONMessage(c *gin.Context, dst any, message string) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(c, http.StatusRequestEntityTooLarge, "payload_too_large",
				"request body exceeds the 1 MiB limit")
			return false
		}
		writeBadRequest(c, message)
		return false
	}
	return true
}

// requireAuth validates the session cookie and the same-origin policy.
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie(auth.SessionCookieName)
		if err != nil || cookie == "" {
			writeError(c, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		adminID, version, err := s.sessions.Parse(cookie, nowFunc())
		if err != nil {
			s.clearSessionCookie(c)
			writeError(c, http.StatusUnauthorized, "unauthorized", "session expired")
			return
		}
		admin, err := s.svc.AdminByID(adminID)
		if err != nil {
			s.clearSessionCookie(c)
			writeError(c, http.StatusUnauthorized, "unauthorized", "account not found")
			return
		}
		if admin.MustChangePassword && !s.isPasswordChangePath(c.Request.URL.Path) {
			writeError(c, http.StatusForbidden, "password_change_required", "password change required")
			return
		}
		if !auth.ConstantTimeEqual(version, auth.SessionVersion(admin.PasswordHash, admin.TOTPEnabled)) {
			s.clearSessionCookie(c)
			writeError(c, http.StatusUnauthorized, "unauthorized", "session invalidated")
			return
		}
		if !isSafeMethod(c.Request.Method) {
			if err := checkOrigin(c.Request); err != nil {
				writeError(c, http.StatusForbidden, "invalid_origin", "cross-origin request rejected")
				return
			}
		}
		c.Set("admin", admin)
		c.Next()
	}
}

func currentAdmin(c *gin.Context) *model.Admin {
	value, ok := c.Get("admin")
	if !ok {
		return nil
	}
	admin, _ := value.(*model.Admin)
	return admin
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// checkOrigin rejects state changing requests coming from another site.
func checkOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non browser clients (curl, tests) do not send Origin. They still
		// need the session cookie, which same-site policy protects.
		return nil
	}
	host := r.Host
	if idx := strings.Index(origin, "://"); idx >= 0 {
		origin = origin[idx+3:]
	}
	origin = strings.TrimSuffix(origin, "/")
	if !strings.EqualFold(origin, host) {
		return errors.New("origin mismatch")
	}
	return nil
}

// isPasswordChangePath reports whether the request is one of the endpoints a
// user with a pending password change is still allowed to call.
func (s *Server) isPasswordChangePath(path string) bool {
	base := strings.TrimSuffix(s.cfg.Panel.BasePath, "/")
	switch path {
	case base + "/api/v1/auth/password", base + "/api/v1/auth/me", base + "/api/v1/auth/logout":
		return true
	}
	return false
}

func (s *Server) clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(auth.SessionCookieName, "", -1, "/", "", s.isSecureRequest(c), true)
}
