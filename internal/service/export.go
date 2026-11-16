package service

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"time"

	"github.com/ydong/ssrservice/internal/store"
)

// CSV exports.
//
// The client export already existed and holds the client PSKs, so it is the
// precedent for how an export behaves: text/csv, an attachment filename, and
// no-store. The audit trail and the access history follow the same shape, with
// one addition the client export does not need: they have a row cap, and a
// capped export says so instead of quietly handing over a partial file.

// exportFilenameFor builds the suggested download name of an export. It mirrors
// ExportFilename's shape so a directory full of downloads sorts together.
func exportFilenameFor(kind string, now time.Time) string {
	return "sspanel-" + kind + "-" + now.Format("20060102-150405") + ".csv"
}

// ExportResult is one rendered CSV plus whether the row cap cut it short.
type ExportResult struct {
	Content   []byte
	Truncated bool
	Filename  string
}

// ExportAuditCSV renders the audit rows matching filter.
func (s *Service) ExportAuditCSV(filter store.AuditFilter) (ExportResult, error) {
	logs, truncated, err := s.store.ListAuditForExport(filter, store.ExportAuditMaxRows)
	if err != nil {
		return ExportResult{}, err
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	// created_at is rendered both ways: the raw instant keeps the file
	// machine-readable, the local stamp keeps it readable without a converter
	// that knows the effective time zone.
	header := []string{
		"created_at", "created_at_local", "username", "action", "target_type",
		"target_id", "detail", "ip",
	}
	if err := writer.Write(header); err != nil {
		return ExportResult{}, err
	}
	loc := s.location()
	for _, entry := range logs {
		record := []string{
			strconv.FormatInt(entry.CreatedAt, 10),
			time.Unix(entry.CreatedAt, 0).In(loc).Format("2006-01-02 15:04:05"),
			entry.Username,
			entry.Action,
			entry.TargetType,
			strconv.FormatInt(entry.TargetID, 10),
			entry.Detail,
			entry.IP,
		}
		if err := writer.Write(record); err != nil {
			return ExportResult{}, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		Content:   buf.Bytes(),
		Truncated: truncated,
		Filename:  exportFilenameFor("audit", time.Now()),
	}, nil
}

// ExportAccessCSV renders the raw access records matching filter.
//
// The day form of the window is resolved the same way the list endpoint resolves
// it, so an export of "the day the operator picked" covers the same records the
// page showed.
func (s *Service) ExportAccessCSV(filter store.AccessEventFilter, sinceDay, untilDay string) (ExportResult, error) {
	if sinceDay != "" || untilDay != "" {
		since, until, err := dayWindow(sinceDay, untilDay, s.location())
		if err != nil {
			return ExportResult{}, err
		}
		filter.Since, filter.Until = since, until
	}
	events, truncated, err := s.store.ListAccessEventsForExport(filter, store.ExportAccessMaxRows)
	if err != nil {
		return ExportResult{}, err
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	header := []string{
		"started_at", "started_at_local", "client_id", "client_name", "inbound_id",
		"inbound_remark", "network", "host", "port", "ip", "outcome", "reason",
		"up_bytes", "down_bytes", "duration_ms",
	}
	if err := writer.Write(header); err != nil {
		return ExportResult{}, err
	}
	loc := s.location()
	for _, event := range events {
		record := []string{
			strconv.FormatInt(event.StartedAt, 10),
			time.Unix(event.StartedAt, 0).In(loc).Format("2006-01-02 15:04:05"),
			strconv.FormatInt(event.ClientID, 10),
			event.ClientName,
			strconv.FormatInt(event.InboundID, 10),
			event.InboundRemark,
			event.Network,
			event.Host,
			strconv.Itoa(event.Port),
			event.IP,
			event.Outcome,
			event.Reason,
			strconv.FormatInt(event.UpBytes, 10),
			strconv.FormatInt(event.DownBytes, 10),
			strconv.FormatInt(event.DurationMS, 10),
		}
		if err := writer.Write(record); err != nil {
			return ExportResult{}, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		Content:   buf.Bytes(),
		Truncated: truncated,
		Filename:  exportFilenameFor("access", time.Now()),
	}, nil
}
