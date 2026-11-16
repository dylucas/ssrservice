package panel

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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

// parseDayWindow reads the optional since_day/until_day pair.
//
// The date picker picks days and only the server knows which zone they belong
// to, so the day form is what the panel sends. Both endpoints that accept a
// window (the list and the export) share this validation: a typo in one must not
// be accepted by the other.
func parseDayWindow(c *gin.Context) (sinceDay, untilDay string, ok bool) {
	sinceDay = strings.TrimSpace(c.Query("since_day"))
	untilDay = strings.TrimSpace(c.Query("until_day"))
	for _, day := range []string{sinceDay, untilDay} {
		if day == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", day); err != nil {
			writeBadRequest(c, "since_day and until_day must be dates in YYYY-MM-DD form")
			return "", "", false
		}
	}
	if sinceDay != "" && untilDay != "" && untilDay < sinceDay {
		writeBadRequest(c, "until_day must not be before since_day")
		return "", "", false
	}
	return sinceDay, untilDay, true
}
