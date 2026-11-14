package panel

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

// handleImportClients creates users in one inbound from CSV text or ss:// links.
//
// Partial success is a normal outcome: the response carries the created count and
// a per-line failure list, so a 200 with failures does not mean nothing
// happened.
func (s *Server) handleImportClients(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req service.ImportRequest
	if !bindJSON(c, &req) {
		return
	}
	result, err := s.svc.ImportClients(id, req)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditClientImport, TargetType: "inbound", TargetID: id,
		Detail: auditFields("format", req.Format, "created", result.Created,
			"failed", len(result.Failed)),
	})
	c.JSON(http.StatusOK, result)
}

// handleExportClients streams the clients of one inbound (or all) as CSV.
func (s *Server) handleExportClients(c *gin.Context) {
	inboundID, _ := strconv.ParseInt(c.Query("inbound_id"), 10, 64)
	content, err := s.svc.ExportClientsCSV(inboundID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	name := service.ExportFilename(time.Now())
	// The file contains client PSKs, so it must never be cached.
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	// Note: the export's content is deliberately absent from the audit detail.
	s.audit(c, auditEntry{
		Action: model.AuditClientExport, TargetType: "inbound", TargetID: inboundID,
		Detail: auditFields("inbound_id", inboundID, "bytes", len(content)),
	})
	c.Data(http.StatusOK, "text/csv; charset=utf-8", content)
}
