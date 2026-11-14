package service

import (
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// Audit retention. The trail is operational history, not billing data, so it is
// bounded in both age and size to keep the panel database small.
const (
	auditRetentionDays = 90
	auditMaxRows       = 50000
	// auditPruneInterval is how many minutes pass between prune runs. The
	// scheduler tick is one minute, so this keeps the pruning off the hot path.
	auditPruneInterval = 60
)

// RecordAudit appends one audit entry. A failure is logged and otherwise
// ignored: auditing must never turn a successful mutation into an error
// response.
func (s *Service) RecordAudit(entry model.AuditLog) {
	if err := s.store.InsertAudit(entry); err != nil {
		s.logger.Warn("record audit failed", "action", entry.Action, "error", err)
	}
}

// AuditLogs returns one page of the audit trail.
func (s *Service) AuditLogs(filter store.AuditFilter) ([]model.AuditLog, int64, error) {
	return s.store.ListAudit(filter)
}

// pruneAudit drops entries past the retention window and trims the trail to its
// row cap.
func (s *Service) pruneAudit() {
	cutoff := model.NowUnix() - auditRetentionDays*24*60*60
	if removed, err := s.store.PruneAudit(cutoff); err != nil {
		s.logger.Warn("prune audit failed", "error", err)
	} else if removed > 0 {
		s.logger.Info("pruned audit entries", "removed", removed, "older_than_days", auditRetentionDays)
	}
	if removed, err := s.store.TrimAudit(auditMaxRows); err != nil {
		s.logger.Warn("trim audit failed", "error", err)
	} else if removed > 0 {
		s.logger.Info("trimmed audit entries", "removed", removed, "max_rows", auditMaxRows)
	}
}
