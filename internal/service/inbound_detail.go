package service

import (
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// InboundDetail is the inbound page: the row plus the four groups that describe
// how it is doing.
type InboundDetail struct {
	Inbound        InboundView           `json:"inbound"`
	Days           int                   `json:"days"`
	Daily          []model.DailyUsage    `json:"daily"`
	TopClients     []InboundDetailClient `json:"top_clients"`
	StatusCounts   InboundDetailStatus   `json:"status_counts"`
	RecentFailures []model.AccessEvent   `json:"recent_failures"`
	// AccessRecording tells the page whether the empty failure list means "none"
	// or "recording is off".
	AccessRecording bool `json:"access_recording"`
}

// InboundDetailClient is one row of the busiest-users list.
type InboundDetailClient struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	Enabled   bool   `json:"enabled"`
}

// InboundDetailStatus is the status distribution of the inbound's clients.
type InboundDetailStatus struct {
	Active        int64 `json:"active"`
	Disabled      int64 `json:"disabled"`
	Expired       int64 `json:"expired"`
	QuotaExceeded int64 `json:"quota_exceeded"`
}

// inboundDetailTopClients is how many users the busiest list shows.
const inboundDetailTopClients = 5

// inboundDetailFailures is how many recent blocked/failed accesses the page
// shows. The full history is one click away in the access page.
const inboundDetailFailures = 20

// InboundDetail assembles the detail page of one inbound.
//
// The traffic series is zero-filled to `days` points for the same reason the
// dashboard's is: the chart is drawn by index, so a day without traffic would
// compress the gap and mislabel the axis.
func (s *Service) InboundDetail(id int64, days int) (*InboundDetail, error) {
	view, err := s.GetInbound(id)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 30
	}
	if days > AccessMaxWindowDays {
		days = AccessMaxWindowDays
	}
	loc := s.location()
	now := time.Now().In(loc)
	first := now.AddDate(0, 0, -(days - 1))
	sinceDay := first.Format("2006-01-02")

	daily, err := s.store.InboundDailyTotals(id, sinceDay)
	if err != nil {
		return nil, err
	}
	top, err := s.store.InboundTopClients(id, inboundDetailTopClients)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.InboundStatusBreakdown(id, now.Unix())
	if err != nil {
		return nil, err
	}

	detail := &InboundDetail{
		Inbound:    *view,
		Days:       days,
		Daily:      fillDailyUsage(daily, first, days),
		TopClients: make([]InboundDetailClient, 0, len(top)),
		StatusCounts: InboundDetailStatus{
			Active:        counts.Active,
			Disabled:      counts.Disabled,
			Expired:       counts.Expired,
			QuotaExceeded: counts.QuotaExceeded,
		},
		// Always initialised: a nil slice marshals to null, and the page renders
		// a table from it.
		RecentFailures:  []model.AccessEvent{},
		AccessRecording: s.AccessEnabled(),
	}
	for _, client := range top {
		detail.TopClients = append(detail.TopClients, InboundDetailClient{
			ID: client.ID, Name: client.Name,
			UpBytes: client.UpBytes, DownBytes: client.DownBytes, Enabled: client.Enabled,
		})
	}

	// Failures are only interesting when recording is on; when it is off the page
	// says so instead of showing "none", which would read as "no failures".
	if detail.AccessRecording {
		recent, _, err := s.store.ListAccessEvents(store.AccessEventFilter{
			InboundID: id, Limit: inboundDetailFailures,
		})
		if err != nil {
			return nil, err
		}
		for _, event := range recent {
			if event.Outcome == model.AccessAllowed {
				continue
			}
			detail.RecentFailures = append(detail.RecentFailures, event)
			if len(detail.RecentFailures) >= inboundDetailFailures {
				break
			}
		}
	}
	return detail, nil
}

// fillDailyUsage expands a per-day traffic series to one row per day of the
// window, inserting zeroes for the days without a record.
//
// The dashboard and the access page already rely on the same invariant (the chart
// is drawn by index), so a per-inbound series is filled the same way rather than
// leaving a quiet day out of the plot.
func fillDailyUsage(daily []model.DailyUsage, first time.Time, days int) []model.DailyUsage {
	byDay := make(map[string]model.DailyUsage, len(daily))
	for _, row := range daily {
		byDay[row.Day] = row
	}
	filled := make([]model.DailyUsage, 0, days)
	for offset := 0; offset < days; offset++ {
		day := first.AddDate(0, 0, offset).Format("2006-01-02")
		if row, ok := byDay[day]; ok {
			filled = append(filled, row)
			continue
		}
		filled = append(filled, model.DailyUsage{Day: day})
	}
	return filled
}
