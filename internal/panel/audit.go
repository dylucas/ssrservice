package panel

import (
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
func (s *Server) auditLogin(c *gin.Context, username string, admin *model.Admin, action string) {
	entry := model.AuditLog{
		CreatedAt: nowFunc().Unix(),
		Username:  username,
		Action:    action,
		Detail:    auditFields("username", username),
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
	if value := c.Query("limit"); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			filter.Limit = n
		}
	}
	if value := c.Query("offset"); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			filter.Offset = n
		}
	}
	if value := c.Query("since"); value != "" {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			filter.Since = n
		}
	}
	if value := c.Query("until"); value != "" {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			filter.Until = n
		}
	}
	items, total, err := s.svc.AuditLogs(filter)
	if err != nil {
		writeServiceError(c, err)
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
