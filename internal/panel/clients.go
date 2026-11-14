package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/store"
)

// parseClientListFilter reads the paging and filter arguments shared by the two
// client list endpoints.
//
// Values are validated rather than clamped: a caller asking for 100_000 rows or
// for an unknown status gets a 400 instead of silently different data than it
// asked for. inboundID is the path-bound inbound; when it is 0 the query
// parameter is honored instead.
func parseClientListFilter(c *gin.Context, inboundID int64) (service.ClientListFilter, bool) {
	filter := service.ClientListFilter{InboundID: inboundID}
	if inboundID == 0 {
		if raw := c.Query("inbound_id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id < 0 {
				writeBadRequest(c, "inbound_id must be a non-negative integer")
				return filter, false
			}
			filter.InboundID = id
		}
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	if !store.ValidClientStatus(status) {
		writeBadRequest(c, "status must be one of all, active, disabled, expired, quota_exceeded")
		return filter, false
	}
	filter.Status = status
	query := strings.TrimSpace(c.Query("q"))
	if utf8.RuneCountInString(query) > store.ClientQueryMaxLen {
		writeBadRequest(c, fmt.Sprintf("q must be at most %d characters", store.ClientQueryMaxLen))
		return filter, false
	}
	filter.Query = query
	if raw := c.Query("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > store.ClientPageMaxLimit {
			writeBadRequest(c, fmt.Sprintf("limit must be between 1 and %d", store.ClientPageMaxLimit))
			return filter, false
		}
		filter.Limit = limit
	}
	if raw := c.Query("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > store.ClientPageMaxOffset {
			writeBadRequest(c, fmt.Sprintf("offset must be between 0 and %d", store.ClientPageMaxOffset))
			return filter, false
		}
		filter.Offset = offset
	}
	return filter, true
}

func (s *Server) handleListClients(c *gin.Context) {
	filter, ok := parseClientListFilter(c, 0)
	if !ok {
		return
	}
	page, err := s.svc.ListClientsPage(filter)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// The client list carries no credentials, so it is safe to compress; see
	// jsonWriter.writeJSON.
	s.json.writeJSON(c, http.StatusOK, page)
}

func (s *Server) handleGetClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	view, err := s.svc.GetClient(id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleUpdateClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input service.ClientInput
	if !bindJSON(c, &input) {
		return
	}
	client, err := s.svc.UpdateClient(id, input)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// Note: the PSK is deliberately absent from the detail.
	s.audit(c, auditEntry{
		Action: model.AuditClientUpdate, TargetType: "client", TargetID: client.ID,
		Detail: auditFields("name", client.Name, "enabled", client.Enabled,
			"quota_bytes", client.QuotaBytes, "expires_at", client.ExpiresAt,
			"ip_limit", client.IPLimit, "reset_policy", client.ResetPolicy,
			"reset_day", client.ResetDay),
	})
	c.JSON(http.StatusOK, client)
}

func (s *Server) handleDeleteClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.DeleteClient(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditClientDelete, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleToggleClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req toggleRequest
	if !bindJSONMessage(c, &req, "enabled is required") {
		return
	}
	if err := s.svc.ToggleClient(id, req.Enabled); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditClientToggle, TargetType: "client", TargetID: id,
		Detail: auditFields("enabled", req.Enabled),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleResetClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.ResetClient(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditClientReset, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleResetClientToken(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	token, err := s.svc.ResetClientToken(id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	// Note: the new token is deliberately absent from the detail.
	s.audit(c, auditEntry{Action: model.AuditClientToken, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"sub_token": token, "subscription_url": "/sub/" + token})
}

func (s *Server) handleKickClient(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.svc.KickClient(id); err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{Action: model.AuditClientKick, TargetType: "client", TargetID: id})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleClientLink(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	link, subURL, err := s.svc.ClientLink(id, c.Query("host"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"link": link, "subscription_url": subURL})
}

func (s *Server) handleClientUsage(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	usage, err := s.svc.Usage(id, days)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": usage})
}
