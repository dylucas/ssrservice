package ssserver

import (
	"sync"
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
	location   *time.Location
	now        func() time.Time
	onExceeded func(clientID int64)

	// mu serializes flush and day rollover. It is never taken on the hot
	// counting path unless the day changed.
	mu sync.Mutex
}

func newUsageManager(sink UsageSink, location *time.Location, onExceeded func(int64)) *usageManager {
	if location == nil {
		location = time.Local
	}
	return &usageManager{
		sink:       sink,
		location:   location,
		now:        time.Now,
		onExceeded: onExceeded,
	}
}

// ensure returns the account for the client, creating it with the given
// persisted baseline when needed.
func (u *usageManager) ensure(c ClientSnapshot, up, down int64) *account {
	if v, ok := u.accounts.Load(c.ID); ok {
		a := v.(*account)
		a.updatePolicy(c)
		return a
	}
	start := dayStartUnix(u.now(), u.location)
	a := &account{
		id:        c.ID,
		up:        up,
		down:      down,
		flushUp:   up,
		flushDown: down,
		dayStart:  start,
		flushDay:  dayKeyOf(start, u.location),
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
	start := dayStartUnix(u.now(), u.location)

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

	a.mu.Lock()
	if a.dayStart == start {
		a.mu.Unlock()
		return
	}
	oldDay := dayKeyOf(a.dayStart, u.location)
	deltaUp := a.dayUp - a.flushDayU
	deltaDown := a.dayDown - a.flushDayD
	totalUp, totalDown := a.up, a.down
	a.dayStart = start
	a.dayUp = 0
	a.dayDown = 0
	a.flushDayU = 0
	a.flushDayD = 0
	a.flushDay = dayKeyOf(start, u.location)
	a.mu.Unlock()

	if deltaUp == 0 && deltaDown == 0 {
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
		a.pending = append(a.pending, dayDelta{day: oldDay, up: deltaUp, down: deltaDown})
		a.mu.Unlock()
	}
}

// flush persists all pending counters. It is safe to call concurrently with
// counting.
func (u *usageManager) flush() error {
	if u.sink == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()

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
		a.mu.Lock()
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
				Day:          dayKeyOf(a.dayStart, u.location),
				DayUpBytes:   deltaUp,
				DayDownBytes: deltaDown,
			})
		}
		commits = append(commits, c)
		a.mu.Unlock()
		return true
	})
	if len(snaps) == 0 {
		return nil
	}
	if err := u.sink.SaveUsage(snaps); err != nil {
		return err
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
		a.mu.Unlock()
	}
	return nil
}

// reset zeroes a client's counters.
func (u *usageManager) reset(clientID int64, location *time.Location) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.accounts.Load(clientID)
	if !ok {
		return
	}
	a := v.(*account)
	a.mu.Lock()
	defer a.mu.Unlock()
	start := dayStartUnix(u.now(), location)
	a.up = 0
	a.down = 0
	a.flushUp = 0
	a.flushDown = 0
	a.dayStart = start
	a.dayUp = 0
	a.dayDown = 0
	a.flushDayU = 0
	a.flushDayD = 0
	a.flushDay = dayKeyOf(start, location)
	a.pending = nil
	a.exceeded = false
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
