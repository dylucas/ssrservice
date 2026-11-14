package service

import (
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// startResetLoop launches the scheduled monthly traffic reset worker. The same
// one minute tick also drives the (much rarer) audit retention sweep, so no
// extra goroutine is needed for it.
func (s *Service) startResetLoop() {
	s.resetWG.Add(1)
	go func() {
		defer s.resetWG.Done()
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		s.runScheduledResets(time.Now())
		s.pruneAudit()
		ticks := 0
		for {
			select {
			case <-s.resetStop:
				return
			case <-timer.C:
				s.runScheduledResets(time.Now())
				ticks++
				if ticks%auditPruneInterval == 0 {
					s.pruneAudit()
				}
				timer.Reset(time.Minute)
			}
		}
	}()
}

// runScheduledResets resets clients whose monthly reset boundary has passed.
//
// now is a parameter rather than a call to time.Now so the three branches that
// matter -- establishing a baseline, skipping an already applied boundary and
// performing the reset -- are testable without waiting for a calendar.
func (s *Service) runScheduledResets(now time.Time) {
	clients, err := s.store.ClientsForReset(now.Unix())
	if err != nil {
		s.logger.Warn("load reset candidates failed", "error", err)
		return
	}
	for _, client := range clients {
		day := client.ResetDay
		if day == 0 {
			s.mu.RLock()
			day = s.resetDay
			s.mu.RUnlock()
		}
		if day < 1 || day > 28 {
			continue
		}
		boundary := LastResetBoundary(now, s.location(), day)
		if client.LastResetAt == 0 {
			// First tick for a pre-existing client: adopt it where it stands.
			// This used to call ResetClientTraffic, which zeroes the counters --
			// so an upgrade wiped every user's usage on the next tick, exactly
			// what the comment above promised not to do.
			if err := s.store.SetClientLastReset(client.ID, now.Unix()); err != nil {
				s.logger.Warn("initialize reset baseline failed", "client_id", client.ID, "error", err)
			}
			continue
		}
		if boundary <= client.LastResetAt {
			continue
		}
		// Persist and clear the live counters as one operation: a flush that
		// already snapshotted the old totals must not be able to write them back
		// after the reset commits. See usageManager.resetPersist.
		if err := s.mgr.ResetClientTrafficPersist(client.ID, func() error {
			return s.store.ResetClientTraffic(client.ID, boundary)
		}); err != nil {
			s.logger.Warn("scheduled reset failed", "client_id", client.ID, "error", err)
			continue
		}
		s.logger.Info("monthly traffic reset", "client_id", client.ID, "name", client.Name,
			"boundary", time.Unix(boundary, 0).In(s.location()).Format(time.RFC3339))
	}
}

// LastResetBoundary returns the most recent occurrence of day-of-month at
// 00:00 in loc that is not after now.
func LastResetBoundary(now time.Time, loc *time.Location, day int) int64 {
	if loc == nil {
		loc = time.Local
	}
	if day < 1 {
		day = 1
	}
	if day > 28 {
		day = 28
	}
	local := now.In(loc)
	candidate := time.Date(local.Year(), local.Month(), day, 0, 0, 0, 0, loc)
	if candidate.After(local) {
		// Previous month. day is at most 28 so the date always exists.
		prev := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 0, -1)
		candidate = time.Date(prev.Year(), prev.Month(), day, 0, 0, 0, 0, loc)
	}
	return candidate.Unix()
}

// ResetClient zeroes a client's traffic counters and records the reset time.
func (s *Service) ResetClient(clientID int64) error {
	if _, err := s.store.ClientByID(clientID); err != nil {
		return err
	}
	// One operation, so the flush loop cannot restore the counters it
	// snapshotted before the reset.
	return s.mgr.ResetClientTrafficPersist(clientID, func() error {
		return s.store.ResetClientTraffic(clientID, model.NowUnix())
	})
}
