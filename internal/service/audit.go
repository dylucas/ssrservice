package service

import (
	"errors"

	"github.com/ydong/ssrservice/internal/auth"
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

// Authenticate checks a username and password pair.
//
// Login used to be assembled in the panel from Store().AdminByUsername plus
// auth.VerifyPassword, which put an authentication decision outside the layer
// that owns the domain. The store lookup and the password check belong together:
// a failure must be indistinguishable for an unknown user and a wrong password.
func (s *Service) Authenticate(username, password string) (*model.Admin, error) {
	admin, err := s.store.AdminByUsername(username)
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(admin.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	return admin, nil
}

// ErrInvalidCredentials is returned by Authenticate for an unknown user or a
// wrong password. The two cases are deliberately not distinguished.
var ErrInvalidCredentials = errors.New("invalid credentials")

// TouchAdminLogin records a successful login timestamp.
func (s *Service) TouchAdminLogin(id int64) error { return s.store.TouchAdminLogin(id) }

// AdminByID returns one administrator.
func (s *Service) AdminByID(id int64) (*model.Admin, error) { return s.store.AdminByID(id) }

// ChangeAdminPassword verifies the current password and stores a new hash.
// It returns the new hash so the caller can re-issue a session cookie without
// reading the row again.
func (s *Service) ChangeAdminPassword(id int64, current, next string) (string, error) {
	admin, err := s.store.AdminByID(id)
	if err != nil {
		return "", err
	}
	if !auth.VerifyPassword(admin.PasswordHash, current) {
		return "", ErrInvalidPassword
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return "", &ValidationError{Message: err.Error()}
	}
	if err := s.store.UpdateAdminPassword(id, hash, false); err != nil {
		return "", err
	}
	return hash, nil
}

// ErrInvalidPassword is returned when the current password does not match.
var ErrInvalidPassword = errors.New("current password is incorrect")
