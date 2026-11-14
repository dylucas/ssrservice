package ssserver

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// recordingSink captures every SaveUsage call and can be told to fail.
type recordingSink struct {
	mu      sync.Mutex
	batches [][]model.UsageSnapshot
	fail    error
}

func (s *recordingSink) SaveUsage(snaps []model.UsageSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	batch := make([]model.UsageSnapshot, len(snaps))
	copy(batch, snaps)
	s.batches = append(s.batches, batch)
	return nil
}

func (s *recordingSink) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

func (s *recordingSink) last(t *testing.T) []model.UsageSnapshot {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.batches) == 0 {
		return nil
	}
	return s.batches[len(s.batches)-1]
}

func (s *recordingSink) setFail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

func (s *recordingSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = nil
	s.fail = nil
}

// fakeClock is a settable clock for the accounting manager.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// newTestUsageManager builds a usage manager with a fake clock in UTC unless a
// different location is supplied.
func newTestUsageManager(t *testing.T, loc *time.Location, start time.Time) (*usageManager, *recordingSink, *fakeClock) {
	t.Helper()
	sink := &recordingSink{}
	clock := &fakeClock{now: start}
	u := newUsageManager(sink, loc, nil)
	u.now = clock.Now
	return u, sink, clock
}

func snapshotFor(snaps []model.UsageSnapshot, clientID int64, day string) (model.UsageSnapshot, bool) {
	for _, snap := range snaps {
		if snap.ClientID == clientID && snap.Day == day {
			return snap, true
		}
	}
	return model.UsageSnapshot{}, false
}

func TestUsageDayRolloverPersistsPreviousDay(t *testing.T) {
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	u, sink, clock := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 7, Enabled: true}, 0, 0)

	u.add(7, true, 100)
	u.add(7, false, 40)
	if sink.calls() != 0 {
		t.Fatalf("counting must not persist: %d calls", sink.calls())
	}

	// Cross midnight and record one more packet.
	clock.now = time.Date(2026, 3, 16, 0, 0, 1, 0, time.UTC)
	u.add(7, true, 50)

	if sink.calls() != 1 {
		t.Fatalf("expected the rollover to flush once, got %d", sink.calls())
	}
	old, ok := snapshotFor(sink.last(t), 7, "2026-03-15")
	if !ok {
		t.Fatalf("missing previous-day snapshot: %+v", sink.last(t))
	}
	if old.DayUpBytes != 100 || old.DayDownBytes != 40 {
		t.Fatalf("unexpected previous-day delta: up=%d down=%d", old.DayUpBytes, old.DayDownBytes)
	}
	if old.UpBytes != 100 || old.DownBytes != 40 {
		t.Fatalf("unexpected absolute totals at rollover: up=%d down=%d", old.UpBytes, old.DownBytes)
	}

	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	current, ok := snapshotFor(sink.last(t), 7, "2026-03-16")
	if !ok {
		t.Fatalf("missing current-day snapshot: %+v", sink.last(t))
	}
	if current.DayUpBytes != 50 || current.DayDownBytes != 0 {
		t.Fatalf("unexpected current-day delta: up=%d down=%d", current.DayUpBytes, current.DayDownBytes)
	}
	if current.UpBytes != 150 || current.DownBytes != 40 {
		t.Fatalf("unexpected absolute totals: up=%d down=%d", current.UpBytes, current.DownBytes)
	}

	// A second flush without new traffic must not write again.
	calls := sink.calls()
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.calls() != calls {
		t.Fatalf("idle flush wrote again: %d -> %d", calls, sink.calls())
	}
	up, down := u.totals(7)
	if up != 150 || down != 40 {
		t.Fatalf("unexpected live totals: up=%d down=%d", up, down)
	}
}

func TestUsageFlushSkipsIdleAccounts(t *testing.T) {
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	u, sink, _ := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 1, Enabled: true}, 0, 0)
	u.ensure(ClientSnapshot{ID: 2, Enabled: true}, 0, 0)
	u.ensure(ClientSnapshot{ID: 3, Enabled: true}, 500, 500)

	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.calls() != 0 {
		t.Fatalf("idle accounts must not be persisted, got %d batches", sink.calls())
	}

	u.add(2, true, 10)
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.calls() != 1 {
		t.Fatalf("expected one batch, got %d", sink.calls())
	}
	last := sink.last(t)
	if len(last) != 1 || last[0].ClientID != 2 {
		t.Fatalf("only the changed client must be persisted, got %+v", last)
	}
}

func TestUsageFlushRetriesPendingDayAfterSinkFailure(t *testing.T) {
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	u, sink, clock := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 4, Enabled: true}, 0, 0)
	u.add(4, true, 10)

	sink.setFail(errors.New("database is locked"))
	clock.now = time.Date(2026, 3, 16, 0, 0, 5, 0, time.UTC)
	u.add(4, true, 1)
	if sink.calls() != 0 {
		t.Fatalf("failed writes must not be recorded, got %d", sink.calls())
	}

	sink.setFail(nil)
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.calls() != 1 {
		t.Fatalf("expected one successful batch, got %d", sink.calls())
	}
	last := sink.last(t)
	old, ok := snapshotFor(last, 4, "2026-03-15")
	if !ok {
		t.Fatalf("pending previous-day delta was lost: %+v", last)
	}
	if old.DayUpBytes != 10 {
		t.Fatalf("pending delta mismatch: %d", old.DayUpBytes)
	}
	current, ok := snapshotFor(last, 4, "2026-03-16")
	if !ok {
		t.Fatalf("missing current-day delta: %+v", last)
	}
	if current.DayUpBytes != 1 || current.UpBytes != 11 {
		t.Fatalf("unexpected current-day snapshot: %+v", current)
	}

	// The pending entry must be consumed exactly once.
	calls := sink.calls()
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.calls() != calls {
		t.Fatalf("pending delta was persisted twice")
	}
}

func TestUsageAddDuringFlushIsNotLost(t *testing.T) {
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	u, sink, _ := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 5, Enabled: true}, 0, 0)
	u.add(5, true, 100)

	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := sink.last(t)[0].UpBytes; got != 100 {
		t.Fatalf("expected 100 persisted bytes, got %d", got)
	}

	// Traffic that arrives after the snapshot keeps the account dirty.
	u.add(5, true, 7)
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := sink.last(t)[0].UpBytes; got != 107 {
		t.Fatalf("expected 107 persisted bytes, got %d", got)
	}
}

func TestUsageSetLocationShiftsDayBoundary(t *testing.T) {
	// 20:00 UTC is already 04:00 on the next day in UTC+8.
	start := time.Date(2026, 3, 15, 20, 0, 0, 0, time.UTC)
	u, sink, _ := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 8, Enabled: true}, 0, 0)
	u.add(8, true, 10)

	u.setLocation(time.FixedZone("CST", 8*3600))
	u.add(8, true, 5)

	if sink.calls() != 1 {
		t.Fatalf("expected a rollover after the time zone change, got %d", sink.calls())
	}
	last := sink.last(t)
	if _, ok := snapshotFor(last, 8, "2026-03-15"); !ok {
		t.Fatalf("missing UTC day bucket before the switch: %+v", last)
	}
	current, ok := snapshotFor(last, 8, "2026-03-16")
	if !ok {
		// The rollover persists the previous bucket; the new one is flushed
		// on the next tick.
		if err := u.flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		current, ok = snapshotFor(sink.last(t), 8, "2026-03-16")
	}
	if !ok {
		t.Fatalf("missing shifted day bucket: %+v", sink.last(t))
	}
	if current.DayUpBytes != 5 || current.UpBytes != 15 {
		t.Fatalf("unexpected shifted snapshot: %+v", current)
	}
}

func TestUsageDayStartAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("time zone data unavailable: %v", err)
	}
	u := newUsageManager(nil, loc, nil)
	// 2026-03-08 12:00 EST; clocks jump forward at 02:00 local that day.
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)
	start := u.dayStartAt(now)
	wantStart := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	if got := time.Unix(start, 0).In(loc); !got.Equal(wantStart) {
		t.Fatalf("day start %s, want %s", got, wantStart)
	}
	wantNext := time.Date(2026, 3, 9, 0, 0, 0, 0, loc).Unix()
	if next := u.dayNext.Load(); next != wantNext {
		t.Fatalf("next boundary %s, want %s", time.Unix(next, 0).In(loc), time.Unix(wantNext, 0).In(loc))
	}
}

func TestUsageQuotaCallbackFiresOnce(t *testing.T) {
	var (
		mu  sync.Mutex
		ids []int64
	)
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	sink := &recordingSink{}
	u := newUsageManager(sink, time.UTC, func(id int64) {
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
	})
	u.now = func() time.Time { return start }
	u.ensure(ClientSnapshot{ID: 9, Enabled: true, QuotaBytes: 100}, 0, 0)

	u.add(9, true, 80)
	u.add(9, true, 30) // exceeds
	u.add(9, true, 1)  // still exceeded, must not fire again

	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("expected a single quota callback for client 9, got %v", ids)
	}
	if err := u.checkActive(9, start); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestUsageResetClearsCounters(t *testing.T) {
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	u, sink, _ := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 10, Enabled: true}, 0, 0)
	u.add(10, true, 42)

	u.reset(10)
	if up, down := u.totals(10); up != 0 || down != 0 {
		t.Fatalf("reset left counters: up=%d down=%d", up, down)
	}
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.calls() != 0 {
		t.Fatalf("reset counters must not be re-persisted, got %d batches", sink.calls())
	}
}

func TestUsageTotalsAll(t *testing.T) {
	start := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	u, _, _ := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 11, Enabled: true}, 100, 200)
	u.ensure(ClientSnapshot{ID: 12, Enabled: true}, 1, 2)
	u.add(11, true, 5)

	up, down := u.totalsAll()
	if up != 106 || down != 202 {
		t.Fatalf("unexpected totals: up=%d down=%d", up, down)
	}
}

// TestUsageRolloverAfterFailedFlushDoesNotDoubleCount covers a database write
// failure right before midnight: the failed day delta is queued in pending and
// the next rollover must not persist it a second time.
func TestUsageRolloverAfterFailedFlushDoesNotDoubleCount(t *testing.T) {
	start := time.Date(2026, 3, 15, 23, 50, 0, 0, time.UTC)
	u, sink, clock := newTestUsageManager(t, time.UTC, start)
	u.ensure(ClientSnapshot{ID: 20, Enabled: true}, 0, 0)
	u.add(20, true, 10)
	u.add(20, false, 5)

	sink.setFail(errors.New("disk full"))
	if err := u.flush(); err == nil {
		t.Fatal("flush should report the sink error")
	}
	if sink.calls() != 0 {
		t.Fatalf("failed writes must not be recorded")
	}

	// Cross midnight while the sink is still failing.
	clock.now = time.Date(2026, 3, 16, 0, 0, 10, 0, time.UTC)
	u.add(20, true, 3)

	sink.setFail(nil)
	if err := u.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var up15, down15, up16 int64
	for _, batch := range sink.batches {
		for _, snap := range batch {
			switch snap.Day {
			case "2026-03-15":
				up15 += snap.DayUpBytes
				down15 += snap.DayDownBytes
			case "2026-03-16":
				up16 += snap.DayUpBytes
			}
		}
	}
	if up15 != 10 || down15 != 5 {
		t.Fatalf("previous day counted more than once: up=%d down=%d", up15, down15)
	}
	if up16 != 3 {
		t.Fatalf("unexpected current-day traffic: %d", up16)
	}
	if up, down := u.totals(20); up != 13 || down != 5 {
		t.Fatalf("unexpected live totals: up=%d down=%d", up, down)
	}
}
