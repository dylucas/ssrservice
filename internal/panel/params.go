package panel

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Optional query parameters shared by the list endpoints. They live here rather
// than next to one feature so the audit log, the client export and the access
// page all validate the same way instead of each drifting.

// parseOptionalID reads an optional positive id query parameter.
func parseOptionalID(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		writeBadRequest(c, fmt.Sprintf("%s must be a non-negative integer", name))
		return 0, false
	}
	return id, true
}

// parseOptionalUnix reads an optional unix-seconds query parameter.
func parseOptionalUnix(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		writeBadRequest(c, fmt.Sprintf("%s must be a non-negative unix timestamp", name))
		return 0, false
	}
	return value, true
}
