package panel

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/store"
)

// CSV exports.
//
// Both endpoints reuse the filter parsing of their list counterparts, so an
// export covers exactly the rows the page shows for the same query. They live
// under /export like the client export, which keeps them clear of the /clients
// and /access wildcards.

// handleExportAudit serves GET /export/audit with the same filters as /audit.
func (s *Server) handleExportAudit(c *gin.Context) {
	filter := store.AuditFilter{
		Action:   strings.TrimSpace(c.Query("action")),
		Username: strings.TrimSpace(c.Query("username")),
	}
	// Paging is meaningless in a file: an export of "page 3" would be a
	// surprising subset, so limit/offset are rejected rather than ignored.
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		writeBadRequest(c, "limit is not supported on an export")
		return
	}
	if value := strings.TrimSpace(c.Query("offset")); value != "" {
		writeBadRequest(c, "offset is not supported on an export")
		return
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

	result, err := s.svc.ExportAuditCSV(filter)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditExportAudit, TargetType: "audit",
		Detail: auditFields("bytes", len(result.Content), "truncated", result.Truncated),
	})
	writeExport(c, result)
}

// handleExportAccess serves GET /export/access with the same filters as
// /access/events.
func (s *Server) handleExportAccess(c *gin.Context) {
	filter := store.AccessEventFilter{}
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		writeBadRequest(c, "limit is not supported on an export")
		return
	}
	if value := strings.TrimSpace(c.Query("offset")); value != "" {
		writeBadRequest(c, "offset is not supported on an export")
		return
	}
	clientID, ok := parseOptionalID(c, "client_id")
	if !ok {
		return
	}
	inboundID, ok := parseOptionalID(c, "inbound_id")
	if !ok {
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(c.Query("outcome")))
	if !validAccessOutcome(outcome) {
		writeBadRequest(c, "outcome must be one of all, allowed, blocked, failed")
		return
	}
	network := strings.ToLower(strings.TrimSpace(c.Query("network")))
	if !validAccessNetwork(network) {
		writeBadRequest(c, "network must be one of all, tcp, udp")
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if len([]rune(query)) > store.AccessQueryMaxLen {
		writeBadRequest(c, "q is too long")
		return
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
	sinceDay, untilDay, ok := parseDayWindow(c)
	if !ok {
		return
	}
	filter.ClientID = clientID
	filter.InboundID = inboundID
	filter.Outcome = outcome
	filter.Network = network
	filter.Query = query
	filter.Since = since
	filter.Until = until

	result, err := s.svc.ExportAccessCSV(filter, sinceDay, untilDay)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditExportAccess, TargetType: "access",
		Detail: auditFields("bytes", len(result.Content), "truncated", result.Truncated),
	})
	writeExport(c, result)
}

// writeExport writes a rendered CSV with the headers an attachment needs.
//
// The audit export may describe who did what and the access export names the
// destinations users visited, so both are no-store like every other response
// that carries account data.
func writeExport(c *gin.Context, result service.ExportResult) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Content-Disposition", `attachment; filename="`+result.Filename+`"`)
	// A capped export says so in a header rather than in the file body, so the
	// CSV stays a plain CSV that a spreadsheet can open.
	if result.Truncated {
		c.Header("X-Export-Truncated", "true")
	}
	c.Data(http.StatusOK, "text/csv; charset=utf-8", result.Content)
}
