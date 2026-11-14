package auth

import (
	"sync"
	"time"
)

// LoginLimiter implements a per-IP sliding window failure counter.
type LoginLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	failures map[string][]time.Time
	lastGC   time.Time
}

// NewLoginLimiter creates a limiter allowing limit failures per window.
func NewLoginLimiter(limit int, window time.Duration) *LoginLimiter {
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = time.Minute
	}
	return &LoginLimiter{
		limit:    limit,
		window:   window,
		failures: make(map[string][]time.Time),
		lastGC:   time.Now(),
	}
}

// Allow reports whether another login attempt is allowed for ip.
func (l *LoginLimiter) Allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(ip, now)
	return len(l.failures[ip]) < l.limit
}

// Fail records a failed attempt.
func (l *LoginLimiter) Fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.failures[ip], now)
	if now.Sub(l.lastGC) > 10*time.Minute {
		l.gcLocked(now)
	}
}

// Reset clears the failure history for ip after a successful login.
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

func (l *LoginLimiter) pruneLocked(ip string, now time.Time) {
	entries := l.failures[ip]
	cutoff := now.Add(-l.window)
	idx := 0
	for idx < len(entries) && entries[idx].Before(cutoff) {
		idx++
	}
	if idx > 0 {
		if idx == len(entries) {
			delete(l.failures, ip)
		} else {
			l.failures[ip] = entries[idx:]
		}
	}
}

func (l *LoginLimiter) gcLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	for ip, entries := range l.failures {
		idx := 0
		for idx < len(entries) && entries[idx].Before(cutoff) {
			idx++
		}
		if idx >= len(entries) {
			delete(l.failures, ip)
			continue
		}
		l.failures[ip] = entries[idx:]
	}
	l.lastGC = now
}
