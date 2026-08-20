package auth

import (
	"sync"
	"time"
)

// LoginLimiter is a tiny in-memory per-IP sliding-window limiter for /login.
// A successful login resets the counter; consecutive failures push it up.
// Exceeding `max` within `window` returns Allowed()=false until the oldest
// failure ages out. Memory is bounded by sweep, which expires whole buckets
// once per window so the map tracks only addresses seen within it (#2137).
type LoginLimiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	max    int
	window time.Duration
	// lastSweep is when sweep last walked the whole map. Without it, gc only
	// ever expires the one IP being looked at, so an address that is never
	// seen again keeps its slice for the life of the process (#2137).
	lastSweep time.Time
}

// NewLoginLimiter returns a limiter allowing `max` failed attempts per `window`.
// Sonarr-ish defaults: 5 per 15 min.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		events:    make(map[string][]time.Time),
		max:       max,
		window:    window,
		lastSweep: time.Now(),
	}
}

// Allow returns true if the caller may attempt another login. Record a failure
// via Record(); reset on success via Reset().
func (l *LoginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	l.gc(ip, now)
	return len(l.events[ip]) < l.max
}

// Record increments the failure counter for ip.
func (l *LoginLimiter) Record(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	l.gc(ip, now)
	l.events[ip] = append(l.events[ip], now)
}

// Reset clears the failure counter for ip (call on successful login).
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.events, ip)
	l.mu.Unlock()
}

// gc expires events older than the window for one ip; caller holds l.mu.
func (l *LoginLimiter) gc(ip string, now time.Time) {
	cutoff := now.Add(-l.window)
	events := l.events[ip]
	i := 0
	for ; i < len(events); i++ {
		if events[i].After(cutoff) {
			break
		}
	}
	if i == len(events) {
		delete(l.events, ip)
		return
	}
	l.events[ip] = events[i:]
}

// sweep expires every bucket whose events have all aged out, bounding the map
// to the number of distinct IPs seen within one window. gc alone cannot do
// this: it only touches the IP currently being looked up, so a caller that
// rotates source addresses (trivial inside a single IPv6 /64) would otherwise
// grow l.events without limit. Runs at most once per window, so the O(n) walk
// is amortised to near nothing on the login path. Caller holds l.mu.
func (l *LoginLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	cutoff := now.Add(-l.window)
	for ip, events := range l.events {
		if len(events) == 0 || !events[len(events)-1].After(cutoff) {
			delete(l.events, ip)
		}
	}
}
