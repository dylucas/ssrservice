package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/store"
)

// accessSummaryResponse is the overview returned by GET /access/summary. The
// policy fields travel with the numbers so the page never has to guess whether
// it is describing a live feed or history.
type accessSummaryResponse struct {
	Enabled                bool                      `json:"enabled"`
	Days                   int                       `json:"days"`
	RawRetentionDays       int                       `json:"raw_retention_days"`
	AggregateRetentionDays int                       `json:"aggregate_retention_days"`
	DroppedRecords         uint64                    `json:"dropped_records"`
	TotalHits              int64                     `json:"total_hits"`
	AllowedHits            int64                     `json:"allowed_hits"`
	BlockedHits            int64                     `json:"blocked_hits"`
	FailedHits             int64                     `json:"failed_hits"`
	UniqueHosts            int64                     `json:"unique_hosts"`
	UpBytes                int64                     `json:"up_bytes"`
	DownBytes              int64                     `json:"down_bytes"`
	Daily                  []model.AccessDayTotal    `json:"daily"`
	TopHosts               []model.AccessHostTotal   `json:"top_hosts"`
	TopClients             []model.AccessClientTotal `json:"top_clients"`
}

// accessEventsResponse is one page of raw records.
type accessEventsResponse struct {
	Items  []model.AccessEvent `json:"items"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

// validAccessOutcome reports whether the outcome filter names a stored value.
func validAccessOutcome(outcome string) bool {
	switch outcome {
	case "", "all", model.AccessAllowed, model.AccessBlocked, model.AccessFailed:
		return true
	}
	return false
}

// validAccessNetwork reports whether the network filter names a transport.
func validAccessNetwork(network string) bool {
	switch network {
	case "", "all", "tcp", "udp":
		return true
	}
	return false
}

// handleAccessSummary serves GET /access/summary.
func (s *Server) handleAccessSummary(c *gin.Context) {
	days := service.AccessRawRetentionDays
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > service.AccessMaxWindowDays {
			writeBadRequest(c, fmt.Sprintf("days must be between 1 and %d", service.AccessMaxWindowDays))
			return
		}
		days = value
	}
	clientID, ok := parseOptionalID(c, "client_id")
	if !ok {
		return
	}
	inboundID, ok := parseOptionalID(c, "inbound_id")
	if !ok {
		return
	}
	summary, err := s.svc.AccessSummary(days, clientID, inboundID)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	rawDays, aggregateDays := service.AccessRetention()
	c.JSON(http.StatusOK, accessSummaryResponse{
		Enabled:                s.svc.AccessEnabled(),
		Days:                   days,
		RawRetentionDays:       rawDays,
		AggregateRetentionDays: aggregateDays,
		DroppedRecords:         s.svc.AccessDropped(),
		TotalHits:              summary.TotalHits,
		AllowedHits:            summary.AllowedHits,
		BlockedHits:            summary.BlockedHits,
		FailedHits:             summary.FailedHits,
		UniqueHosts:            summary.UniqueHosts,
		UpBytes:                summary.UpBytes,
		DownBytes:              summary.DownBytes,
		Daily:                  summary.Daily,
		TopHosts:               summary.TopHosts,
		TopClients:             summary.TopClients,
	})
}

// handleAccessEvents serves GET /access/events.
func (s *Server) handleAccessEvents(c *gin.Context) {
	filter := store.AccessEventFilter{}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > store.AccessEventMaxLimit {
			writeBadRequest(c, fmt.Sprintf("limit must be between 1 and %d", store.AccessEventMaxLimit))
			return
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > store.AccessEventMaxOffset {
			writeBadRequest(c, fmt.Sprintf("offset must be between 0 and %d", store.AccessEventMaxOffset))
			return
		}
		filter.Offset = offset
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
	if utf8.RuneCountInString(query) > store.AccessQueryMaxLen {
		writeBadRequest(c, fmt.Sprintf("q must be at most %d characters", store.AccessQueryMaxLen))
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
	// The day form exists because the picker picks days, and only the server
	// knows which zone those days are in. It takes precedence when present.
	sinceDay := strings.TrimSpace(c.Query("since_day"))
	untilDay := strings.TrimSpace(c.Query("until_day"))
	for _, day := range []string{sinceDay, untilDay} {
		if day == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", day); err != nil {
			writeBadRequest(c, "since_day and until_day must be dates in YYYY-MM-DD form")
			return
		}
	}
	if sinceDay != "" && untilDay != "" && untilDay < sinceDay {
		writeBadRequest(c, "until_day must not be before since_day")
		return
	}
	filter.ClientID = clientID
	filter.InboundID = inboundID
	filter.Outcome = outcome
	filter.Network = network
	filter.Query = query
	filter.Since = since
	filter.Until = until
	filter.SinceDay = sinceDay
	filter.UntilDay = untilDay

	events, total, err := s.svc.AccessEvents(filter)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	limit, offset := store.NormalizeAccessPage(filter.Limit, filter.Offset)
	c.JSON(http.StatusOK, accessEventsResponse{
		Items:  events,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}
