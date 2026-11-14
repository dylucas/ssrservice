package service

import (
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// startResetLoop launches the scheduled monthly traffic reset worker.
func (s *Service) startResetLoop() {
	s.resetWG.Add(1)
	go func() {
		defer s.resetWG.Done()
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		s.runScheduledResets()
		for {
			select {
			case <-s.resetStop:
				return
			case <-timer.C:
				s.runScheduledResets()
				timer.Reset(time.Minute)
			}
		}
	}()
}

// runScheduledResets resets clients whose monthly reset boundary has passed.
func (s *Service) runScheduledResets() {
	now := time.Now()
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
			// First tick for a pre-existing client: establish the baseline
			// instead of wiping traffic on upgrade.
			if err := s.store.ResetClientTraffic(client.ID, now.Unix()); err != nil {
				s.logger.Warn("initialize reset baseline failed", "client_id", client.ID, "error", err)
			}
			continue
		}
		if boundary <= client.LastResetAt {
			continue
		}
		if err := s.store.ResetClientTraffic(client.ID, boundary); err != nil {
			s.logger.Warn("scheduled reset failed", "client_id", client.ID, "error", err)
			continue
		}
		s.mgr.ResetClientTraffic(client.ID)
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
	if err := s.store.ResetClientTraffic(clientID, model.NowUnix()); err != nil {
		return err
	}
	s.mgr.ResetClientTraffic(clientID)
	return nil
}
