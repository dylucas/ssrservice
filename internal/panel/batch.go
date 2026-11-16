package panel

import (
	"strconv"
	"strings"

	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

// handleBatchClients applies one action to several users at once.
func (s *Server) handleBatchClients(c *gin.Context) {
	var req service.BatchRequest
	if !bindJSON(c, &req) {
		return
	}
	result, err := s.svc.BatchClients(req)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	s.audit(c, auditEntry{
		Action: model.AuditClientBatch, TargetType: "client",
		// The ids are summarised rather than listed in full: an entry has to stay
		// readable, and the failure list in the response is where the per-user
		// detail lives.
		Detail: auditFields("action", result.Action, "selected", len(req.IDs),
			"applied", result.Applied, "failed", len(result.Failed),
			"ids", summarizeIDs(req.IDs)),
	})
	c.JSON(http.StatusOK, result)
}

// summarizeIDs renders a bounded id list for the audit detail.
func summarizeIDs(ids []int64) string {
	const shown = 20
	if len(ids) <= shown {
		return joinIDs(ids)
	}
	return joinIDs(ids[:shown]) + "…"
}

// joinIDs renders ids as a comma separated list.
func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}
