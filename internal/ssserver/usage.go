package ssserver

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

type dayDelta struct {
	day  string
	up   int64
	down int64
}

// account holds the live counters and access policy of one client.
type account struct {
	mu sync.Mutex

	id int64

	up   int64
	down int64

	dayStart int64
	dayUp    int64
	dayDown  int64

	// flushed* track what has been persisted so far.
	flushUp   int64
	flushDown int64
	flushDayU int64
	flushDayD int64
	flushDay  string
	pending   []dayDelta

	quota     int64
	expiresAt int64
	ipLimit   int
	enabled   bool
	exceeded  bool

	// dirty is set whenever add updates the counters. flush clears it only
	// after the exact snapshot it persisted matches the live counters again,
	// so an idle account costs one atomic load instead of a lock per tick.
	dirty atomic.Bool
}

func (a *account) updatePolicy(c ClientSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.quota = c.QuotaBytes
	a.expiresAt = c.ExpiresAt
	a.ipLimit = c.IPLimit
	a.enabled = c.Enabled
	if a.quota == 0 || a.up+a.down < a.quota {
		a.exceeded = false
	}
}

// usageManager tracks per-client traffic and gates connections.
type usageManager struct {
	accounts sync.Map // int64 -> *account

	sink       UsageSink
	now        func() time.Time
	onExceeded func(clientID int64)

	// mu serializes flush and day rollover. It is never taken on the hot
	// counting path unless the day changed.
	mu sync.Mutex

	// loc is the accounting time zone. It can be changed at runtime from the
	// panel settings without restarting the server.
	loc atomic.Pointer[time.Location]

	// dayStart/dayNext cache the current day boundary so the per-packet path
	// does not recompute it with time zone conversions. dayMu only guards the
	// slow path taken when the cached boundary has passed.
	dayMu    sync.Mutex
	dayStart atomic.Int64
	dayNext  atomic.Int64
}

func newUsageManager(sink UsageSink, location *time.Location, onExceeded func(int64)) *usageManager {
	if location == nil {
		location = time.Local
	}
	u := &usageManager{
		sink:       sink,
		now:        time.Now,
		onExceeded: onExceeded,
	}
	u.loc.Store(location)
	return u
}

// location returns the current accounting time zone.
func (u *usageManager) location() *time.Location {
	return u.loc.Load()
}

// setLocation replaces the accounting time zone and invalidates the cached
// day boundary so the next accounting call recomputes it. It holds dayMu so a
// slow path that is already recomputing the boundary cannot publish a value
// derived from the previous time zone after the switch.
func (u *usageManager) setLocation(location *time.Location) {
	if location == nil {
		location = time.Local
	}
	u.dayMu.Lock()
	defer u.dayMu.Unlock()
	if u.loc.Swap(location) == location {
		return
	}
	u.dayNext.Store(0)
}

// dayStartAt returns the unix timestamp of the start of the local day that
// contains now. The result is cached: the expensive time zone conversion runs
// once per day (and after a time zone change), not once per packet.
func (u *usageManager) dayStartAt(now time.Time) int64 {
	if now.Unix() < u.dayNext.Load() {
		return u.dayStart.Load()
	}
	u.dayMu.Lock()
	defer u.dayMu.Unlock()
	if next := u.dayNext.Load(); now.Unix() < next {
		return u.dayStart.Load()
	}
	loc := u.loc.Load()
	start := dayStartUnix(now, loc)
	local := time.Unix(start, 0).In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).Unix()
	if next <= now.Unix() {
		// A time zone transition that skips a whole calendar day can leave
		// the computed boundary in the past. Re-check in an hour instead of
		// recomputing on every packet.
		next = now.Unix() + 3600
	}
	u.dayStart.Store(start)
	u.dayNext.Store(next)
	return start
}

// ensure returns the account for the client, creating it with the given
// persisted baseline when needed.
func (u *usageManager) ensure(c ClientSnapshot, up, down int64) *account {
	if v, ok := u.accounts.Load(c.ID); ok {
		a := v.(*account)
		a.updatePolicy(c)
		return a
	}
	start := u.dayStartAt(u.now())
	a := &account{
		id:        c.ID,
		up:        up,
		down:      down,
		flushUp:   up,
		flushDown: down,
		dayStart:  start,
		flushDay:  dayKeyOf(start, u.location()),
		quota:     c.QuotaBytes,
		expiresAt: c.ExpiresAt,
		ipLimit:   c.IPLimit,
		enabled:   c.Enabled,
	}
	actual, loaded := u.accounts.LoadOrStore(c.ID, a)
	if loaded {
		existing := actual.(*account)
		existing.updatePolicy(c)
		return existing
	}
	return a
}

func (u *usageManager) remove(clientID int64) {
	u.accounts.Delete(clientID)
}

func (u *usageManager) ipLimit(clientID int64) int {
	if v, ok := u.accounts.Load(clientID); ok {
		a := v.(*account)
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.ipLimit
	}
	return 0
}

func (u *usageManager) checkActive(clientID int64, now time.Time) error {
	v, ok := u.accounts.Load(clientID)
	if !ok {
		return ErrUnknownClient
	}
	a := v.(*account)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.enabled {
		return ErrClientDisabled
	}
	if a.expiresAt > 0 && now.Unix() >= a.expiresAt {
		return ErrClientExpired
	}
	if a.quota > 0 && a.up+a.down >= a.quota {
		return ErrQuotaExceeded
	}
	return nil
}

// add records traffic. It reports the client id when a quota was just
// exceeded.
func (u *usageManager) add(clientID int64, up bool, n int64) {
	if n <= 0 {
		return
	}
	v, ok := u.accounts.Load(clientID)
	if !ok {
		return
	}
	a := v.(*account)
	start := u.dayStartAt(u.now())

	a.mu.Lock()
	if a.dayStart != start {
		a.mu.Unlock()
		u.rollover(a, start)
		a.mu.Lock()
	}
	if up {
		a.up += n
		a.dayUp += n
	} else {
		a.down += n
		a.dayDown += n
	}
	a.dirty.Store(true)
	justExceeded := false
	if a.quota > 0 && a.up+a.down > a.quota && !a.exceeded {
		a.exceeded = true
		justExceeded = true
	}
	a.mu.Unlock()

	if justExceeded && u.onExceeded != nil {
		u.onExceeded(clientID)
	}
}

// rollover flushes the previous day and starts a new one.
func (u *usageManager) rollover(a *account, start int64) {
	u.mu.Lock()
	defer u.mu.Unlock()

	loc := u.location()
	a.mu.Lock()
	if a.dayStart == start {
		a.mu.Unlock()
		return
	}
	oldDay := dayKeyOf(a.dayStart, loc)
	// A previous flush may have failed, leaving the day delta queued in
	// pending. Subtract that part so the rollover does not count it twice.
	var pendingUp, pendingDown int64
	for _, p := range a.pending {
		if p.day == oldDay {
			pendingUp += p.up
			pendingDown += p.down
		}
	}
	deltaUp := a.dayUp - a.flushDayU - pendingUp
	deltaDown := a.dayDown - a.flushDayD - pendingDown
	totalUp, totalDown := a.up, a.down
	a.dayStart = start
	a.dayUp = 0
	a.dayDown = 0
	a.flushDayU = 0
	a.flushDayD = 0
	a.flushDay = dayKeyOf(start, loc)
	a.mu.Unlock()

	if deltaUp <= 0 && deltaDown <= 0 || u.sink == nil {
		return
	}
	snap := model.UsageSnapshot{
		ClientID:     a.id,
		UpBytes:      totalUp,
		DownBytes:    totalDown,
		Day:          oldDay,
		DayUpBytes:   deltaUp,
		DayDownBytes: deltaDown,
	}
	if err := u.sink.SaveUsage([]model.UsageSnapshot{snap}); err != nil {
		// Keep the day delta for the next flush.
		a.mu.Lock()
		if deltaUp > 0 || deltaDown > 0 {
			a.pending = append(a.pending, dayDelta{day: oldDay, up: deltaUp, down: deltaDown})
		}
		a.dirty.Store(true)
		a.mu.Unlock()
		return
	}
	// The rollover snapshot also persisted the absolute totals; record them so
	// the next flush does not rewrite the same values. Concurrent increments
	// after the snapshot keep the account dirty and are flushed later.
	a.mu.Lock()
	a.flushUp = totalUp
	a.flushDown = totalDown
	a.mu.Unlock()
}

// flush persists all pending counters. It is safe to call concurrently with
// counting.
func (u *usageManager) flush() error {
	if u.sink == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	loc := u.location()
	type commit struct {
		a            *account
		totalUp      int64
		totalDown    int64
		dayStart     int64
		dayUp        int64
		dayDown      int64
		pendingCount int
	}
	var (
		snaps   []model.UsageSnapshot
		commits []commit
	)
	u.accounts.Range(func(_, value any) bool {
		a := value.(*account)
		// Idle accounts are the common case: skip them with one atomic load
		// instead of taking the account lock and building a snapshot.
		if !a.dirty.Load() {
			return true
		}
		a.mu.Lock()
		if !a.dirty.Load() {
			a.mu.Unlock()
			return true
		}
		c := commit{
			a:            a,
			totalUp:      a.up,
			totalDown:    a.down,
			dayStart:     a.dayStart,
			dayUp:        a.dayUp,
			dayDown:      a.dayDown,
			pendingCount: len(a.pending),
		}
		for _, p := range a.pending {
			snaps = append(snaps, model.UsageSnapshot{
				ClientID:     a.id,
				UpBytes:      a.up,
				DownBytes:    a.down,
				Day:          p.day,
				DayUpBytes:   p.up,
				DayDownBytes: p.down,
			})
		}
		deltaUp := a.dayUp - a.flushDayU
		deltaDown := a.dayDown - a.flushDayD
		if deltaUp != 0 || deltaDown != 0 {
			snaps = append(snaps, model.UsageSnapshot{
				ClientID:     a.id,
				UpBytes:      a.up,
				DownBytes:    a.down,
				Day:          dayKeyOf(a.dayStart, loc),
				DayUpBytes:   deltaUp,
				DayDownBytes: deltaDown,
			})
		}
		commits = append(commits, c)
		a.mu.Unlock()
		return true
	})
	if len(commits) == 0 {
		return nil
	}
	// A dirty account with nothing to persist is possible right after a
	// successful rollover; it still needs its dirty flag cleared below.
	if len(snaps) > 0 {
		if err := u.sink.SaveUsage(snaps); err != nil {
			return err
		}
	}
	for _, c := range commits {
		a := c.a
		a.mu.Lock()
		a.flushUp = c.totalUp
		a.flushDown = c.totalDown
		if a.dayStart == c.dayStart {
			a.flushDayU = c.dayUp
			a.flushDayD = c.dayDown
		}
		if c.pendingCount > 0 && c.pendingCount <= len(a.pending) {
			a.pending = append([]dayDelta(nil), a.pending[c.pendingCount:]...)
		}
		// Clear dirty only when the live counters still match the persisted
		// snapshot; traffic that arrived during the write is flushed next tick.
		if a.up == c.totalUp && a.down == c.totalDown &&
			a.dayStart == c.dayStart && a.dayUp == c.dayUp && a.dayDown == c.dayDown {
			a.dirty.Store(false)
		}
		a.mu.Unlock()
	}
	return nil
}

// reset zeroes a client's counters.
func (u *usageManager) reset(clientID int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.accounts.Load(clientID)
	if !ok {
		return
	}
	a := v.(*account)
	a.mu.Lock()
	defer a.mu.Unlock()
	loc := u.location()
	start := u.dayStartAt(u.now())
	a.up = 0
	a.down = 0
	a.flushUp = 0
	a.flushDown = 0
	a.dayStart = start
	a.dayUp = 0
	a.dayDown = 0
	a.flushDayU = 0
	a.flushDayD = 0
	a.flushDay = dayKeyOf(start, loc)
	a.pending = nil
	a.exceeded = false
	a.dirty.Store(false)
}

// totals returns the current counters including unflushed traffic.
func (u *usageManager) totals(clientID int64) (up, down int64) {
	v, ok := u.accounts.Load(clientID)
	if !ok {
		return 0, 0
	}
	a := v.(*account)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.up, a.down
}

// totalsAll returns the sum of the live counters of every known client.
func (u *usageManager) totalsAll() (up, down int64) {
	u.accounts.Range(func(_, value any) bool {
		a := value.(*account)
		a.mu.Lock()
		up += a.up
		down += a.down
		a.mu.Unlock()
		return true
	})
	return up, down
}

// expiredClients returns the ids of accounts that may no longer serve
// traffic.
func (u *usageManager) expiredClients(now time.Time) []int64 {
	var ids []int64
	u.accounts.Range(func(key, value any) bool {
		a := value.(*account)
		a.mu.Lock()
		expired := !a.enabled ||
			(a.expiresAt > 0 && now.Unix() >= a.expiresAt) ||
			(a.quota > 0 && a.up+a.down >= a.quota)
		a.mu.Unlock()
		if expired {
			ids = append(ids, key.(int64))
		}
		return true
	})
	return ids
}

func dayStartUnix(t time.Time, loc *time.Location) int64 {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc).Unix()
}

func dayKeyOf(startUnix int64, loc *time.Location) string {
	return time.Unix(startUnix, 0).In(loc).Format("2006-01-02")
}

// ensureClients updates the runtime policy of every client in the snapshot.
func (u *usageManager) ensureClients(clients []ClientSnapshot) {
	for _, c := range clients {
		u.ensure(c, 0, 0)
	}
}
