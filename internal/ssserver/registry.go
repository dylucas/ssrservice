package ssserver

import (
	"sort"
	"sync"
)

// session tracks one live TCP connection or UDP NAT session so that it can be
// closed when its client is disabled, expires or exceeds quota.
type session struct {
	id        uint64
	clientID  int64
	inboundID int64
	ip        string
	closers   []func()
	closed    bool
}

// registry tracks live sessions and the distinct source IPs per client.
type registry struct {
	mu       sync.Mutex
	nextID   uint64
	sessions map[uint64]*session
	byClient map[int64]map[uint64]*session
	ips      map[int64]map[string]int
}

func newRegistry() *registry {
	return &registry{
		sessions: make(map[uint64]*session),
		byClient: make(map[int64]map[uint64]*session),
		ips:      make(map[int64]map[string]int),
	}
}

// acquire registers a new session unless the client's IP or session limits
// would be exceeded.
func (r *registry) acquire(clientID, inboundID int64, ip string, ipLimit, maxSessions int) (*session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if maxSessions > 0 && len(r.byClient[clientID]) >= maxSessions {
		return nil, ErrTooManySessions
	}
	if ipLimit > 0 {
		if existing := r.ips[clientID]; existing != nil && len(existing) >= ipLimit {
			if existing[ip] == 0 {
				return nil, ErrIPLimit
			}
		}
	}

	r.nextID++
	s := &session{id: r.nextID, clientID: clientID, inboundID: inboundID, ip: ip}
	r.sessions[s.id] = s
	if r.byClient[clientID] == nil {
		r.byClient[clientID] = make(map[uint64]*session)
	}
	r.byClient[clientID][s.id] = s
	if r.ips[clientID] == nil {
		r.ips[clientID] = make(map[string]int)
	}
	r.ips[clientID][ip]++
	return s, nil
}

// onClose registers a closer for the session. It runs immediately if the
// session was already closed.
func (r *registry) onClose(s *session, fn func()) {
	r.mu.Lock()
	if s.closed {
		r.mu.Unlock()
		fn()
		return
	}
	s.closers = append(s.closers, fn)
	r.mu.Unlock()
}

// release removes a session and runs its closers.
func (r *registry) release(s *session) {
	r.mu.Lock()
	closers := r.removeLocked(s)
	r.mu.Unlock()
	for _, fn := range closers {
		fn()
	}
}

func (r *registry) removeLocked(s *session) []func() {
	if s.closed {
		return nil
	}
	s.closed = true
	delete(r.sessions, s.id)
	if sessions := r.byClient[s.clientID]; sessions != nil {
		delete(sessions, s.id)
		if len(sessions) == 0 {
			delete(r.byClient, s.clientID)
		}
	}
	if ips := r.ips[s.clientID]; ips != nil {
		ips[s.ip]--
		if ips[s.ip] <= 0 {
			delete(ips, s.ip)
		}
		if len(ips) == 0 {
			delete(r.ips, s.clientID)
		}
	}
	closers := s.closers
	s.closers = nil
	return closers
}

// closeClient closes every session of one client.
func (r *registry) closeClient(clientID int64) {
	r.mu.Lock()
	var closers []func()
	for _, s := range r.byClient[clientID] {
		closers = append(closers, r.removeLocked(s)...)
	}
	r.mu.Unlock()
	for _, fn := range closers {
		fn()
	}
}

// closeInbound closes every session belonging to one inbound.
func (r *registry) closeInbound(inboundID int64) {
	r.mu.Lock()
	var closers []func()
	for _, s := range r.sessions {
		if s.inboundID == inboundID {
			closers = append(closers, r.removeLocked(s)...)
		}
	}
	r.mu.Unlock()
	for _, fn := range closers {
		fn()
	}
}

// closeAll closes every session.
func (r *registry) closeAll() {
	r.mu.Lock()
	var closers []func()
	for _, s := range r.sessions {
		closers = append(closers, r.removeLocked(s)...)
	}
	r.mu.Unlock()
	for _, fn := range closers {
		fn()
	}
}

// onlineIPs returns the distinct source IPs currently connected per client.
func (r *registry) onlineIPs() map[int64][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[int64][]string, len(r.ips))
	for clientID, ips := range r.ips {
		list := make([]string, 0, len(ips))
		for ip := range ips {
			list = append(list, ip)
		}
		sort.Strings(list)
		result[clientID] = list
	}
	return result
}

// onlineIPsByInbound returns the number of distinct source IPs per inbound.
//
// It is derived from the live sessions, which already carry both the inbound id
// and the source IP, so there is no second counter that could drift away from
// the per-client one. The result is the same as unioning the per-client IP sets
// of an inbound's clients: a client belongs to exactly one inbound.
func (r *registry) onlineIPsByInbound() map[int64]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	sets := make(map[int64]map[string]struct{})
	for _, s := range r.sessions {
		set := sets[s.inboundID]
		if set == nil {
			set = make(map[string]struct{})
			sets[s.inboundID] = set
		}
		set[s.ip] = struct{}{}
	}
	counts := make(map[int64]int, len(sets))
	for inboundID, set := range sets {
		counts[inboundID] = len(set)
	}
	return counts
}

// sessionCount returns the number of live sessions.
func (r *registry) sessionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}
