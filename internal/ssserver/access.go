package ssserver

import (
	"errors"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/ydong/ssrservice/internal/model"
)

// Access recording is best effort: the queue is bounded, a full queue drops
// records and counts them, and a failing sink only produces a log line. Relay
// traffic and traffic accounting must never wait for analytics.
const (
	accessQueueSize    = 8192
	accessBatchSize    = 512
	accessFlushEvery   = 2 * time.Second
	accessMaxHostBytes = 255
	accessWarnEvery    = 30 * time.Second
	// A UDP destination that keeps receiving packets would otherwise produce no
	// record until the NAT session dies, which can be hours. A destination's
	// accumulated bytes are flushed when it has been quiet for this long, or
	// after a continuously active window of accessUDPMaxWindow, whichever comes
	// first.
	accessUDPQuiet     = 2 * time.Second
	accessUDPMaxWindow = 10 * time.Second
)

// accessRecorder batches access records onto the sink.
//
// It runs its own goroutine instead of sharing the traffic flush loop: a slow
// access insert must not delay the counters that quota enforcement depends on.
type accessRecorder struct {
	logger *slog.Logger
	sink   AccessSink

	enabled atomic.Bool
	loc     atomic.Pointer[time.Location]
	dropped atomic.Uint64

	records chan model.AccessRecord
	flush   chan chan struct{}
	stop    chan struct{}
	done    chan struct{}

	// udpSessions holds the trackers of live UDP NAT sessions. The protocol
	// library only closes a NAT session when the same session id sends again
	// (or at shutdown), so a destination's record would otherwise wait for
	// traffic that may never come; the sweep below finishes the entries that
	// have been idle for the UDP timeout.
	udpMu       sync.Mutex
	udpSessions map[*udpAccess]struct{}

	// live holds the entries of connections that are still open. Switching
	// recording off flushes what they accounted for and mutes them, so a
	// transfer that started before the switch cannot keep writing records for
	// bytes moved after it. The set is only maintained while recording is on:
	// the disabled data path returns before it is touched.
	liveMu sync.Mutex
	live   map[*accessEntry]struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
	// stopping is set before the loop leaves: at that point a submission would
	// vanish without a trace, so it is counted as a drop instead.
	stopping   atomic.Bool
	warnMu     sync.Mutex
	lastWarnAt time.Time
}

func newAccessRecorder(logger *slog.Logger, sink AccessSink, loc *time.Location) *accessRecorder {
	if loc == nil {
		loc = time.Local
	}
	r := &accessRecorder{
		logger:      logger,
		sink:        sink,
		records:     make(chan model.AccessRecord, accessQueueSize),
		flush:       make(chan chan struct{}),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		udpSessions: make(map[*udpAccess]struct{}),
		live:        make(map[*accessEntry]struct{}),
	}
	r.loc.Store(loc)
	return r
}

func (r *accessRecorder) setEnabled(enabled bool) {
	on := enabled && r.sink != nil
	was := r.enabled.Swap(on)
	if was && !on {
		// Turning recording off has to reach the connections and UDP
		// destinations that are already live, otherwise a long download would
		// keep contributing records for hours after the operator said stop.
		r.flushLive()
	}
}

// flushLive persists what the open connections and UDP destinations have
// accounted for so far, and stops them from accounting for anything else.
//
// The trackers stay registered with the sweeper (a UDP NAT session outlives the
// switch) so that turning recording back on resumes reporting for the rest of
// that session instead of leaving it silent until it dies.
func (r *accessRecorder) flushLive() {
	now := time.Now()
	r.udpMu.Lock()
	trackers := make([]*udpAccess, 0, len(r.udpSessions))
	for tracker := range r.udpSessions {
		trackers = append(trackers, tracker)
	}
	r.udpMu.Unlock()
	for _, tracker := range trackers {
		tracker.flushPending(now)
	}

	r.liveMu.Lock()
	entries := make([]*accessEntry, 0, len(r.live))
	for entry := range r.live {
		entries = append(entries, entry)
	}
	r.liveMu.Unlock()
	for _, entry := range entries {
		// finish right after the flush retires the entry: what it already
		// reported is complete, and a connection that closes hours later must not
		// emit a second row.
		entry.flushAndMute(now)
		entry.finish()
	}
}

// registerLive tracks an entry so it can be flushed when recording is switched
// off. It is called from beginAccess, which only runs while recording is on.
func (r *accessRecorder) registerLive(e *accessEntry) {
	r.liveMu.Lock()
	r.live[e] = struct{}{}
	r.liveMu.Unlock()
}

func (r *accessRecorder) unregisterLive(e *accessEntry) {
	r.liveMu.Lock()
	delete(r.live, e)
	r.liveMu.Unlock()
}

func (r *accessRecorder) isEnabled() bool {
	return r.enabled.Load() && r.sink != nil
}

func (r *accessRecorder) setLocation(loc *time.Location) {
	if loc == nil {
		loc = time.Local
	}
	r.loc.Store(loc)
}

func (r *accessRecorder) start() {
	if r.sink == nil {
		return
	}
	r.startOnce.Do(func() {
		r.started.Store(true)
		go r.run()
	})
}

// shutdown stops the loop after draining what is already queued.
//
// It waits for the drain to finish and then accounts for anything that raced the
// final drain: a record that reached the channel after the loop left would
// otherwise disappear without appearing in the dropped counter, and the panel
// promises that counter describes every lost record.
func (r *accessRecorder) shutdown() {
	if r.sink == nil || !r.started.Load() {
		return
	}
	r.stopping.Store(true)
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	<-r.done
	var left int64
	for {
		select {
		case <-r.records:
			left++
		default:
			r.drop(left)
			return
		}
	}
}

// flushNow asks the loop to persist the queued records immediately. It is used
// by tests and by the final shutdown; the panel never waits for it.
func (r *accessRecorder) flushNow() {
	if r.sink == nil || !r.started.Load() {
		return
	}
	ack := make(chan struct{})
	select {
	case r.flush <- ack:
	case <-r.done:
		return
	}
	select {
	case <-ack:
	case <-r.done:
	}
}

func (r *accessRecorder) run() {
	defer close(r.done)
	ticker := time.NewTicker(accessFlushEvery)
	defer ticker.Stop()

	batch := make([]model.AccessRecord, 0, accessBatchSize)
	write := func() {
		if len(batch) == 0 {
			return
		}
		r.write(batch)
		batch = batch[:0]
	}
	// drain moves everything already queued into the batch. A flush request must
	// drain first: the select can pick the request while a record submitted just
	// before it is still sitting in the channel, and writing an empty batch
	// would report success without persisting it.
	drain := func() {
		for {
			select {
			case record := <-r.records:
				batch = append(batch, record)
				if len(batch) >= accessBatchSize {
					write()
				}
			default:
				return
			}
		}
	}
	for {
		select {
		case <-r.stop:
			// Drain what the last sessions submitted before their close, then
			// persist and leave.
			drain()
			write()
			return
		case record := <-r.records:
			batch = append(batch, record)
			if len(batch) >= accessBatchSize {
				write()
			}
		case ack := <-r.flush:
			drain()
			write()
			close(ack)
		case <-ticker.C:
			r.sweepUDP()
			write()
		}
	}
}

// write normalizes the batch and hands it to the sink. A failure is logged and
// counted like a queue overflow: the panel promises that dropped_records
// describes everything lost, whether the loss happened on the way in or on the
// way to disk. Analytics never turns into a proxy error either way.
func (r *accessRecorder) write(batch []model.AccessRecord) {
	if r.sink == nil {
		return
	}
	loc := r.loc.Load()
	if loc == nil {
		loc = time.Local
	}
	for i := range batch {
		batch[i].Host = normalizeAccessHost(batch[i].Host)
		if batch[i].Day == "" {
			batch[i].Day = time.Unix(batch[i].StartedAt, 0).In(loc).Format("2006-01-02")
		}
	}
	if err := r.sink.SaveAccess(batch); err != nil {
		lost := r.drop(int64(len(batch)))
		if r.logger != nil {
			r.logger.Warn("save access records failed", "records", len(batch),
				"dropped_total", lost, "error", err)
		}
	}
}

// drop counts lost records and rate limits the warning. It is the single entry
// point for both ways a record can be lost (full queue, failing sink, exceeded
// destination cap), so the warning and the surfaced counter cannot disagree.
func (r *accessRecorder) drop(n int64) uint64 {
	if n <= 0 {
		return r.dropped.Load()
	}
	total := r.dropped.Add(uint64(n))
	r.warnDropped(total)
	return total
}

// submit queues one record, dropping it (and counting the drop) when the queue
// is full or the loop has already stopped.
func (r *accessRecorder) submit(record model.AccessRecord) {
	if r.stopping.Load() {
		r.drop(1)
		return
	}
	select {
	case r.records <- record:
	default:
		r.drop(1)
	}
}

// warnDropped logs at most one warning per accessWarnEvery. The interval is the
// whole rate limit: a full queue drops thousands of records per second, and a
// per-record modulo would either flood the log or hide a real incident.
func (r *accessRecorder) warnDropped(total uint64) {
	r.warnMu.Lock()
	defer r.warnMu.Unlock()
	now := time.Now()
	if now.Sub(r.lastWarnAt) < accessWarnEvery {
		return
	}
	r.lastWarnAt = now
	if r.logger != nil {
		r.logger.Warn("access records dropped (queue full or storage failing)",
			"dropped", total, "queue", accessQueueSize)
	}
}

func (r *accessRecorder) droppedCount() uint64 { return r.dropped.Load() }

// registerUDP starts tracking an idle UDP session so its entries can be
// finished even when no further packet closes it.
func (r *accessRecorder) registerUDP(u *udpAccess) {
	if r == nil || u == nil {
		return
	}
	r.udpMu.Lock()
	r.udpSessions[u] = struct{}{}
	r.udpMu.Unlock()
}

func (r *accessRecorder) unregisterUDP(u *udpAccess) {
	if r == nil || u == nil {
		return
	}
	r.udpMu.Lock()
	delete(r.udpSessions, u)
	r.udpMu.Unlock()
}

// sweepUDP finishes every destination that has been idle for its session's UDP
// timeout. A NAT session itself can outlive its destinations by minutes, and a
// record that only appears at shutdown would make the page look dead.
func (r *accessRecorder) sweepUDP() {
	if r == nil {
		return
	}
	r.udpMu.Lock()
	sessions := make([]*udpAccess, 0, len(r.udpSessions))
	for session := range r.udpSessions {
		sessions = append(sessions, session)
	}
	r.udpMu.Unlock()
	for _, session := range sessions {
		session.sweep(time.Now())
	}
}

// normalizeAccessHost lower-cases and shortens a destination name. The client
// controls this string, so it is sanitized before it reaches the database and
// the panel.
func normalizeAccessHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	host = strings.ToValidUTF8(host, "")
	if len(host) > accessMaxHostBytes {
		host = strings.ToValidUTF8(host[:accessMaxHostBytes], "")
	}
	return strings.ToLower(host)
}

// accessEntry tracks one recorded connection (TCP) or one destination within a
// UDP NAT session. Its methods are safe on a nil receiver, which is what lets
// the disabled path stay allocation free: beginAccess returns nil and the
// handler code does not branch.
type accessEntry struct {
	rec       *accessRecorder
	clientID  int64
	inboundID int64
	network   string

	started  time.Time
	lastUsed atomic.Int64 // unix nanoseconds
	up       atomic.Int64
	down     atomic.Int64
	// muted stops the entry from accumulating or reporting anything else. It is
	// set when recording is switched off, so bytes moved after the switch are
	// not attributed to a row the operator already saw.
	muted atomic.Bool

	// mu guards the fields that are written once while the connection is being
	// set up (and re-written when a UDP destination's requested name arrives)
	// and read by the emit paths, which can run from the sweeper while the
	// session is still live. The byte counters stay atomic so the relay path
	// never takes it.
	mu          sync.Mutex
	host        string
	port        int
	windowStart time.Time
	emitted     bool
	ip          string
	outcome     string
	reason      string

	finished atomic.Bool
}

// beginAccess starts tracking one access, or returns nil when recording is
// disabled. The caller must call finish (a deferred call is typical).
//
// The destination is passed unresolved on purpose: formatting it into a string
// allocates, and the disabled path must not pay for analytics it will not keep.
func (m *Manager) beginAccess(clientID, inboundID int64, network string, destination M.Socksaddr) *accessEntry {
	if m.access == nil || !m.access.isEnabled() {
		return nil
	}
	if network == "" {
		network = "tcp"
	}
	requested := accessDestination(destination)
	now := time.Now()
	entry := &accessEntry{
		rec:       m.access,
		clientID:  clientID,
		inboundID: inboundID,
		network:   network,
		host:      normalizeAccessHost(requested.host),
		port:      requested.port,
		started:   now,
	}
	entry.lastUsed.Store(now.UnixNano())
	entry.windowStart = now
	m.access.registerLive(entry)
	return entry
}

// allow marks the access as connected to the given address.
func (e *accessEntry) allow(ip string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.ip = ip
	e.outcome = model.AccessAllowed
	e.mu.Unlock()
}

// allowAddr is allow with the address formatting done here rather than at the
// call site: formatting allocates, and the disabled path must return before it.
func (e *accessEntry) allowAddr(addr netip.Addr) {
	if e == nil {
		return
	}
	e.allow(addr.Unmap().String())
}

// block marks the access as denied before a connection was attempted.
func (e *accessEntry) block(reason string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.outcome = model.AccessBlocked
	e.reason = reason
	e.mu.Unlock()
}

// fail marks the access as attempted but unsuccessful.
func (e *accessEntry) fail(reason string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.outcome = model.AccessFailed
	e.reason = reason
	e.mu.Unlock()
}

// relabel upgrades a destination that was first seen as a resolved address to
// the name the client asked for. A reply can create the entry before the send
// path reports the request, and the panel has to show the name, not the address.
func (e *accessEntry) relabel(destination M.Socksaddr) {
	if e == nil {
		return
	}
	destination = destination.Unwrap()
	if destination.Fqdn == "" {
		return
	}
	host := normalizeAccessHost(destination.Fqdn)
	if host == "" {
		return
	}
	e.mu.Lock()
	if e.host != host {
		e.host = host
		e.port = int(destination.Port)
	}
	e.mu.Unlock()
}

func (e *accessEntry) touch() {
	if e == nil {
		return
	}
	e.lastUsed.Store(time.Now().UnixNano())
}

func (e *accessEntry) addUp(n int64) {
	if e == nil || n <= 0 || e.muted.Load() {
		return
	}
	e.touch()
	e.up.Add(n)
}

func (e *accessEntry) addDown(n int64) {
	if e == nil || n <= 0 || e.muted.Load() {
		return
	}
	e.touch()
	e.down.Add(n)
}

// record emits one record for the bytes accumulated since the previous emit and
// advances the window. The caller holds e.mu and has already swapped the byte
// counters out.
func (e *accessEntry) record(started time.Time, now time.Time, up, down int64) {
	outcome := e.outcome
	reason := e.reason
	if outcome == "" {
		outcome = model.AccessFailed
		reason = model.AccessReasonDial
	}
	duration := now.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	e.emitted = true
	e.rec.submit(model.AccessRecord{
		ClientID:   e.clientID,
		InboundID:  e.inboundID,
		Network:    e.network,
		Host:       e.host,
		Port:       e.port,
		IP:         e.ip,
		Outcome:    outcome,
		Reason:     reason,
		UpBytes:    up,
		DownBytes:  down,
		StartedAt:  started.Unix(),
		DurationMS: duration,
	})
}

// flushLocked emits the bytes accumulated since the previous emit and advances
// the window. It reports whether a record went out. emitEmpty also reports an
// entry that never emitted anything, which is what lets a blocked connection
// and a connected-but-silent one still appear; without it an idle destination
// stays silent, because it has nothing to report. The caller holds e.mu.
func (e *accessEntry) flushLocked(now time.Time, emitEmpty bool) bool {
	up := e.up.Swap(0)
	down := e.down.Swap(0)
	if up == 0 && down == 0 && !(emitEmpty && !e.emitted) {
		return false
	}
	e.record(e.windowStart, now, up, down)
	e.windowStart = now
	return true
}

// windowAge reports how long the current reporting window has been open.
func (e *accessEntry) windowAge(now time.Time) time.Duration {
	e.mu.Lock()
	start := e.windowStart
	e.mu.Unlock()
	return now.Sub(start)
}

// flushDelta emits the bytes accumulated so far and starts a new window. An
// entry with no bytes since the last emit stays silent: an idle destination has
// nothing to report.
func (e *accessEntry) flushDelta(now time.Time) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.flushLocked(now, false)
	e.mu.Unlock()
}

// flushAndMute emits whatever is pending and stops the entry from accounting
// for anything else. It is the switch-off path.
func (e *accessEntry) flushAndMute(now time.Time) {
	if e == nil || e.muted.Swap(true) {
		return
	}
	e.mu.Lock()
	e.flushLocked(now, true)
	e.mu.Unlock()
}

// finish emits whatever is still pending. A path that never set an outcome is
// reported as a failure rather than an allowed connection, which is the honest
// default; an entry that already emitted its bytes does not repeat them.
func (e *accessEntry) finish() {
	if e == nil || !e.finished.CompareAndSwap(false, true) {
		return
	}
	if e.muted.Load() {
		// flushAndMute already reported this access; what arrived after the
		// switch was moved while recording was off and is not attributed.
		e.rec.unregisterLive(e)
		return
	}
	e.mu.Lock()
	e.flushLocked(time.Now(), true)
	e.mu.Unlock()
	e.rec.unregisterLive(e)
}

// udpAccess records the destinations of one UDP NAT session. Each destination
// produces one record per session, carrying the bytes and duration the session
// accumulated for it; a policy-blocked destination produces one blocked record
// per session rather than one per packet.
type udpAccess struct {
	m           *Manager
	clientID    int64
	inboundID   int64
	idleTimeout time.Duration

	mu      sync.Mutex
	targets map[netip.AddrPort]*accessEntry
	// blocked remembers the destinations already reported for this session.
	blocked map[string]struct{}
}

func (m *Manager) newUDPAccess(clientID, inboundID int64) *udpAccess {
	if m.access == nil || !m.access.isEnabled() {
		return nil
	}
	timeout := m.opts.UDPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	tracker := &udpAccess{
		m:           m,
		clientID:    clientID,
		inboundID:   inboundID,
		idleTimeout: timeout,
		targets:     make(map[netip.AddrPort]*accessEntry),
		blocked:     make(map[string]struct{}),
	}
	m.access.registerUDP(tracker)
	return tracker
}

// sweep flushes the bytes of active destinations and finishes the ones that have
// been idle for the session's UDP timeout.
//
// A UDP NAT session only ends when the same session id sends again or at
// shutdown, so waiting for the session would leave a continuously active
// destination unreported indefinitely. An entry is therefore flushed when it has
// been quiet for accessUDPQuiet or after a accessUDPMaxWindow window, and
// removed once it is idle for the configured UDP timeout.
func (u *udpAccess) sweep(now time.Time) {
	if u == nil {
		return
	}
	idleCutoff := now.Add(-u.idleTimeout).UnixNano()
	// The lock is held while entries flush and finish: the sweeper and the
	// session's own close path can reach the same entry, and emitting twice or
	// updating the window from two goroutines would double count. emit is a
	// non-blocking channel send, so this never waits on the database.
	u.mu.Lock()
	defer u.mu.Unlock()
	for target, entry := range u.targets {
		idle := now.Sub(time.Unix(0, entry.lastUsed.Load()))
		age := entry.windowAge(now)
		if entry.up.Load()+entry.down.Load() > 0 &&
			(idle >= accessUDPQuiet || age >= accessUDPMaxWindow) {
			entry.flushDelta(now)
		}
		if entry.lastUsed.Load() <= idleCutoff {
			delete(u.targets, target)
			entry.finish()
		}
	}
}

// flushPending reports the bytes every destination has accumulated so far and
// forgets the destinations, so the rest of the session starts a fresh window.
// The tracker itself stays registered with the sweeper: the NAT session outlives
// the switch, and turning recording back on has to resume reporting for it.
func (u *udpAccess) flushPending(now time.Time) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for target, entry := range u.targets {
		entry.flushAndMute(now)
		entry.finish()
		delete(u.targets, target)
	}
}

// observe returns the entry of an allowed destination, creating it on first use.
// The destination set is bounded like the NAT table itself; beyond the cap the
// record is counted as dropped instead of growing without limit.
//
// The caller must hold u.mu: entries are created, updated and finished under the
// same lock, so a byte can never be added to an entry the sweeper has just
// reported and removed.
//
// requested is only read when the entry is created and when a client names a
// domain for a destination that was first seen as a resolved address (see
// relabel); every other packet pays nothing for it.
func (u *udpAccess) observe(target netip.AddrPort, requested M.Socksaddr) *accessEntry {
	if entry := u.targets[target]; entry != nil {
		entry.relabel(requested)
		return entry
	}
	if len(u.targets) >= maxDestinations {
		if u.m.access != nil {
			u.m.access.drop(1)
		}
		return nil
	}
	destination := M.SocksaddrFromNetIP(target).Unwrap()
	if requested.Fqdn != "" {
		destination = requested
	}
	entry := u.m.beginAccess(u.clientID, u.inboundID, "udp", destination)
	if entry == nil {
		return nil
	}
	entry.allow(target.Addr().Unmap().String())
	u.targets[target] = entry
	return entry
}

// recordBlocked remembers one policy-blocked destination of this session. It is
// emitted right away and only the key is kept, so a stream of rejected packets
// produces one record per destination instead of one per packet.
//
// The destination is passed unresolved because formatting it allocates and the
// disabled path must return first. reason separates a policy refusal from a
// destination that could not be resolved.
func (u *udpAccess) recordBlocked(destination M.Socksaddr, reason string) {
	if u == nil || !u.m.access.isEnabled() {
		return
	}
	requested := accessDestination(destination)
	key := normalizeAccessHost(requested.host) + ":" + strconv.Itoa(requested.port)
	u.mu.Lock()
	if _, seen := u.blocked[key]; seen {
		u.mu.Unlock()
		return
	}
	if len(u.blocked) >= maxDestinations {
		u.mu.Unlock()
		if u.m.access != nil {
			u.m.access.drop(1)
		}
		return
	}
	// The key is reserved under the lock so one blocked destination is emitted
	// at most once, then released again if no entry was created: beginAccess
	// returns nil while recording is switched off, and keeping the key would
	// swallow this destination for the rest of the session even after recording
	// comes back on.
	u.blocked[key] = struct{}{}
	u.mu.Unlock()

	entry := u.m.beginAccess(u.clientID, u.inboundID, "udp", destination)
	if entry == nil {
		u.mu.Lock()
		delete(u.blocked, key)
		u.mu.Unlock()
		return
	}
	entry.block(reason)
	entry.finish()
}

// addUp records the payload sent to target. requested is the destination the
// client asked for; when it names a domain, that name is what the panel shows
// and the resolved address stays in the entry's ip.
func (u *udpAccess) addUp(target netip.AddrPort, requested M.Socksaddr, n int64) {
	if u == nil || n <= 0 || !u.m.access.isEnabled() {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.observe(target, requested).addUp(n)
}

func (u *udpAccess) addDown(target netip.AddrPort, n int64) {
	if u == nil || n <= 0 || !u.m.access.isEnabled() {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.observe(target, M.Socksaddr{}).addDown(n)
}

// finishAll emits every entry of the session and stops the idle sweeps.
func (u *udpAccess) finishAll() {
	if u == nil {
		return
	}
	u.m.access.unregisterUDP(u)
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, entry := range u.targets {
		entry.finish()
	}
	u.targets = make(map[netip.AddrPort]*accessEntry)
	u.blocked = make(map[string]struct{})
}

// accessReasonFor maps a gate error to the stable reason identifiers the panel
// filters on.
func accessReasonFor(err error) string {
	switch {
	case errors.Is(err, ErrClientDisabled):
		return model.AccessReasonDisabled
	case errors.Is(err, ErrClientExpired):
		return model.AccessReasonExpired
	case errors.Is(err, ErrQuotaExceeded):
		return model.AccessReasonQuota
	case errors.Is(err, ErrIPLimit):
		return model.AccessReasonIPLimit
	case errors.Is(err, ErrTooManySessions):
		return model.AccessReasonSessions
	default:
		return model.AccessReasonPolicy
	}
}

// policyReasonFor distinguishes a destination the policy refuses from one that
// could not be resolved. Both surface as a resolve error, but they mean very
// different things to whoever reads the page: a blocked target is working as
// configured, a resolve failure is usually the client's DNS being wrong.
func policyReasonFor(err error) string {
	if errors.Is(err, ErrDestinationBlocked) {
		return model.AccessReasonPolicy
	}
	return model.AccessReasonResolve
}
