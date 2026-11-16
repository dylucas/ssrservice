package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// auditMaxDetailLen bounds the detail column so a long user note cannot bloat
// the trail.
const auditMaxDetailLen = 512

// auditEntry is what a handler wants recorded.
//
// Detail is assembled by the handler from an explicit whitelist of fields. It
// must never contain a password, a client PSK, a server PSK or a subscription
// token: the audit trail is readable by any administrator and downloadable, so a
// secret that leaks in here leaks permanently.
type auditEntry struct {
	Action     string
	TargetType string
	TargetID   int64
	Detail     string
}

// auditFields renders a key=value detail string from a fixed set of field names.
// Passing a value for an unlisted key is impossible by construction because the
// callers build the string here rather than serializing a struct: a new secret
// field added to a request struct cannot end up in the trail by accident.
func auditFields(pairs ...any) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok || key == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(key)
		b.WriteByte('=')
		switch value := pairs[i+1].(type) {
		case string:
			b.WriteString(strconv.Quote(value))
		case int:
			b.WriteString(strconv.Itoa(value))
		case int64:
			b.WriteString(strconv.FormatInt(value, 10))
		case bool:
			b.WriteString(strconv.FormatBool(value))
		default:
			continue
		}
	}
	detail := b.String()
	if len(detail) > auditMaxDetailLen {
		detail = detail[:auditMaxDetailLen]
	}
	return detail
}

// audit records a successful mutation. It is a no-op when the request has no
// administrator (which cannot happen behind requireAuth, but keeps the helper
// safe to call from login handling too).
func (s *Server) audit(c *gin.Context, entry auditEntry) {
	admin := currentAdmin(c)
	if admin == nil {
		return
	}
	s.svc.RecordAudit(model.AuditLog{
		CreatedAt:  nowFunc().Unix(),
		AdminID:    admin.ID,
		Username:   admin.Username,
		Action:     entry.Action,
		TargetType: entry.TargetType,
		TargetID:   entry.TargetID,
		Detail:     entry.Detail,
		IP:         c.ClientIP(),
	})
}

// auditLogin records a login attempt, which happens before a session exists.
//
// extra is an optional fixed key=value fragment such as "stage=totp". It is
// assembled from literal call sites, never from the request body, so a password
// or a verification code cannot end up in the trail through this path.
func (s *Server) auditLogin(c *gin.Context, username string, admin *model.Admin, action, extra string) {
	detail := auditFields("username", username)
	if extra != "" {
		detail += " " + extra
	}
	entry := model.AuditLog{
		CreatedAt: nowFunc().Unix(),
		Username:  username,
		Action:    action,
		Detail:    detail,
		IP:        c.ClientIP(),
	}
	if admin != nil {
		entry.AdminID = admin.ID
	}
	s.svc.RecordAudit(entry)
}

// responseLimit mirrors store's limit normalization for the response body.
func responseLimit(limit int) int {
	if limit <= 0 {
		return store.AuditDefaultLimit
	}
	if limit > store.AuditMaxLimit {
		return store.AuditMaxLimit
	}
	return limit
}

// responseOffset mirrors store's offset normalization for the response body.
func responseOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// handleListAudit serves GET /audit with optional filters.
func (s *Server) handleListAudit(c *gin.Context) {
	filter := store.AuditFilter{
		Action:   strings.TrimSpace(c.Query("action")),
		Username: strings.TrimSpace(c.Query("username")),
	}
	// A malformed paging or time parameter is rejected like the client and access
	// lists do, instead of quietly falling back to the default page: a typo in a
	// filter would otherwise look like "nothing matched".
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > store.AuditMaxLimit {
			writeBadRequest(c, fmt.Sprintf("limit must be between 1 and %d", store.AuditMaxLimit))
			return
		}
		filter.Limit = n
	}
	if value := strings.TrimSpace(c.Query("offset")); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			writeBadRequest(c, "offset must be a non-negative integer")
			return
		}
		filter.Offset = n
	}
	since, ok := parseOptionalUnix(c, "since")
	if !ok {
		return
	}
	until, ok := parseOptionalUnix(c, "until")
	if !ok {
		return
	}
	if since > 0 && until > 0 && until <= since {
		writeBadRequest(c, "until must be greater than since")
		return
	}
	filter.Since = since
	filter.Until = until
	items, total, err := s.svc.AuditLogs(filter)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	// Echo the effective paging values (the store applies its own defaults and
	// bounds), so the client can render the pager without guessing.
	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"total":   total,
		"limit":   responseLimit(filter.Limit),
		"offset":  responseOffset(filter.Offset),
		"actions": model.AuditActions,
	})
}
