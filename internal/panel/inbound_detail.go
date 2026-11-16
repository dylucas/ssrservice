package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// handleInboundDetail serves GET /inbounds/:id/detail: the inbound row plus its
// traffic series, busiest users, status distribution and recent failures.
func (s *Server) handleInboundDetail(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	days := 30
	if value := strings.TrimSpace(c.Query("days")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 90 {
			writeBadRequest(c, fmt.Sprintf("days must be between 1 and 90"))
			return
		}
		days = parsed
	}
	detail, err := s.svc.InboundDetail(id, days)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}
