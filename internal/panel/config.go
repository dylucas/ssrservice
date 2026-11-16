package panel

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
)

// Configuration migration endpoints.
//
// Both live outside /clients and /inbounds so they never collide with the
// :id wildcards there. The export is an attachment with no-store because the
// file carries every PSK and the administrator's password hash.

// handleExportConfig serves GET /export/config: a portable configuration
// snapshot as an attachment.
func (s *Server) handleExportConfig(c *gin.Context) {
	snapshot, err := s.svc.ExportConfig()
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	content, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditExportConfig, TargetType: "config",
		Detail: auditFields("inbounds", len(snapshot.Inbounds), "bytes", len(content)),
	})
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Content-Disposition", `attachment; filename="`+
		"sspanel-config-"+time.Now().Format("20060102-150405")+`.json"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", content)
}

// configImportRequest is the body of POST /import/config.
type configImportRequest struct {
	// Confirm is the explicit acknowledgement that the current configuration
	// will be replaced. Without it the request is refused before validation.
	Confirm bool                 `json:"confirm"`
	Config  model.ConfigSnapshot `json:"config"`
}

// handleImportConfig serves POST /import/config: validate, then replace the
// whole configuration in one transaction.
func (s *Server) handleImportConfig(c *gin.Context) {
	var req configImportRequest
	if !bindJSON(c, &req) {
		return
	}
	result, err := s.svc.ImportConfig(req.Config, req.Confirm)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	// The audit entry records only counts and warnings: the snapshot itself
	// contains credentials and must never appear in the trail.
	s.audit(c, auditEntry{
		Action: model.AuditImportConfig, TargetType: "config",
		Detail: auditFields("inbounds", result.Inbounds, "clients", result.Clients,
			"reloads", result.Reloads, "warnings", len(result.Warnings)),
	})
	c.JSON(http.StatusOK, result)
}
