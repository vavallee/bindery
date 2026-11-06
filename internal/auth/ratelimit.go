package auth

import (
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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

	// maxKeys caps len(events) for login attempts, see AcquireLogin.
	maxKeys int
	// purges counts whole map walks, for tests.
	purges int
	// lastCapPurge and capPurgeEvery throttle the purge made when the key
	// map is full, see makeRoom.
	lastCapPurge  time.Time
	capPurgeEvery time.Duration
	// groupKeys counts the accounts each address group started tracking in
	// the current window, see maxPairKeysPerGroup.
	groupKeys map[string]groupCount
}

type groupCount struct {
	start time.Time
	n     int
}

// maxPairKeysPerGroup is the number of distinct accounts one address group may
// start tracking within a window.
const maxPairKeysPerGroup = 64

const (
	// defaultMaxKeys bounds the bucket map. Each login attempt can create up
	// to two keys, so without a bound a caller rotating addresses and names
	// grows it for as long as the window lasts.
	defaultMaxKeys = 100000
)

// NewLoginLimiter returns a limiter allowing `max` failed attempts per `window`.
// Sonarr-ish defaults: 5 per 15 min.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		events:    make(map[string][]time.Time),
		max:       max,
		window:    window,
		lastSweep: time.Now(),
		maxKeys:   defaultMaxKeys,

		capPurgeEvery: 5 * time.Second,
		groupKeys:     make(map[string]groupCount),
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

// Acquire reserves one attempt for ip and reports whether the caller may go on
// to verify the credential. The check and the count happen under one lock, so
// at most `max` attempts per window are ever let through for an address, no
// matter how many arrive at once. The reservation stands as a failure unless
// the caller clears it with Reset after a successful verification.
//
// Allow followed by Record cannot give that bound: every request in a
// concurrent burst passes Allow before the first Record lands, so a burst ran
// as many password verifications as it had requests. Credential checks that
// run an expensive KDF must use Acquire; Allow stays useful as a cheap early
// refusal before any work is done.
func (l *LoginLimiter) Acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	l.gc(ip, now)
	if len(l.events[ip]) >= l.max {
		return false
	}
	l.events[ip] = append(l.events[ip], now)
	return true
}

// Release refunds one Acquire reservation for ip. Call it only when no
// verification ran after the Acquire, for example because the caller went
// away while queued for a KDF slot: such a request learned nothing about the
// credential, so it must not count toward locking the address out. A
// reservation that was followed by a real check stays counted.
func (l *LoginLimiter) Release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events[ip]
	if len(events) == 0 {
		return
	}
	if len(events) == 1 {
		delete(l.events, ip)
		return
	}
	l.events[ip] = events[:len(events)-1]
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
	l.purge(now)
}

// purge expires every bucket whose events have all aged out; caller holds l.mu.
func (l *LoginLimiter) purge(now time.Time) {
	l.purges++
	l.lastSweep = now
	cutoff := now.Add(-l.window)
	for ip, events := range l.events {
		if len(events) == 0 || !events[len(events)-1].After(cutoff) {
			delete(l.events, ip)
		}
	}
}

// Login failure tracking.
//
// A login attempt is counted in two buckets of the same limiter, both with
// the limiter's window and the same refusal behaviour (once a bucket is full
// the attempt is refused before the password is checked, and refused attempts
// are not counted again):
//
//   - the address and the account together, at the configured maximum. A
//     successful login clears only this pair, so signing in to one account
//     never wipes the failures recorded against another.
//   - the address alone, across all accounts, at four times the maximum. It
//     bounds how many different accounts one address can try, and a success
//     refunds only its own attempt.
//
// Account names are trimmed and lowercased before they are used in a key, so
// spelling variants share a bucket.
//
// The number of tracked keys is capped (maxKeys), and one address group may
// start tracking at most maxPairKeysPerGroup accounts per window. When a new
// attempt would exceed the cap, makeRoom purges expired buckets and then
// evicts the least recently touched ones, so new sign ins keep working.
//
// IPv6 addresses are grouped by /64, the smallest allocation a client is
// normally given, so rotating inside one prefix shares a bucket.
const (
	loginAddressFactor = 4
	// maxLoginNameLen bounds the key size built from client supplied text.
	maxLoginNameLen = 128
)

type loginBucket struct {
	key string
	max int
}

// LoginAddressGroup returns the string an address is tracked under: the
// address itself for IPv4, the /64 prefix for IPv6. Unparseable input is
// returned unchanged.
func LoginAddressGroup(ip string) string {
	a, err := netip.ParseAddr(strings.Trim(ip, "[]"))
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

func (l *LoginLimiter) loginBuckets(ip, username string) []loginBucket {
	// Trim and cut before lowercasing so the work done on client supplied
	// text is bounded by maxLoginNameLen, not by the size of the request.
	// Callers build the buckets before taking l.mu.
	name := strings.TrimSpace(username)
	if len(name) > maxLoginNameLen {
		// A rune is at most 4 bytes, so look back at most 3. Input that is
		// not valid UTF-8 keeps the plain cut instead of walking to zero.
		cut := maxLoginNameLen
		for i := 0; i < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(name[cut]); i++ {
			cut--
		}
		if !utf8.RuneStart(name[cut]) {
			cut = maxLoginNameLen
		}
		name = name[:cut]
	}
	name = strings.ToLower(name)
	grp := LoginAddressGroup(ip)
	return []loginBucket{
		{"login-pair:" + grp + "\x00" + name, l.max},
		{"login-addr:" + grp, l.max * loginAddressFactor},
	}
}

// AllowLogin is the cheap early refusal for a login attempt: it reports
// whether none of the login buckets is full, without counting anything.
func (l *LoginLimiter) AllowLogin(ip, username string) bool {
	buckets := l.loginBuckets(ip, username)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	return l.loginRoomLocked(buckets, now)
}

// loginRoomLocked reports whether every bucket has room; caller holds l.mu.
func (l *LoginLimiter) loginRoomLocked(buckets []loginBucket, now time.Time) bool {
	for _, b := range buckets {
		l.gc(b.key, now)
		if len(l.events[b.key]) >= b.max {
			return false
		}
	}
	return true
}

// AcquireLogin reserves one attempt in every login bucket, or none of them
// when any is full. As with Acquire the reservation stands as a failure unless
// LoginSucceeded or ReleaseLogin settles it.
func (l *LoginLimiter) AcquireLogin(ip, username string) bool {
	buckets := l.loginBuckets(ip, username)
	grp := LoginAddressGroup(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	if !l.loginRoomLocked(buckets, now) {
		return false
	}
	// Bound how many accounts one address group can start tracking.
	if _, ok := l.events[buckets[0].key]; !ok {
		g := l.groupKeys[grp]
		if now.Sub(g.start) >= l.window {
			g = groupCount{start: now}
		}
		if g.n >= maxPairKeysPerGroup {
			return false
		}
		g.n++
		if len(l.groupKeys) >= l.maxKeys {
			l.groupKeys = make(map[string]groupCount)
		}
		l.groupKeys[grp] = g
	}
	if n := l.newKeys(buckets); n > 0 && len(l.events)+n > l.maxKeys {
		l.makeRoom(now, n)
	}
	for _, b := range buckets {
		l.events[b.key] = append(l.events[b.key], now)
	}
	return true
}

// makeRoom frees space for need new keys; caller holds l.mu. It purges
// expired buckets at most once per capPurgeEvery (the walk is O(keys) under
// the lock, so it must not run per attempt), then, if the map is still full,
// evicts the least recently touched buckets in one batch of at least a tenth
// of the cap, so the next few thousand new keys need no walk at all.
//
// Tradeoff: evicting a live bucket forgets its failures. That only happens
// when the map holds maxKeys live buckets within one window, and the oldest
// go first.
func (l *LoginLimiter) makeRoom(now time.Time, need int) {
	if now.Sub(l.lastCapPurge) >= l.capPurgeEvery {
		l.lastCapPurge = now
		l.purge(now)
	}
	over := len(l.events) + need - l.maxKeys
	if over <= 0 {
		return
	}
	type aged struct {
		key string
		at  time.Time
	}
	cands := make([]aged, 0, len(l.events))
	for k, ev := range l.events {
		if len(ev) == 0 {
			continue
		}
		cands = append(cands, aged{k, ev[len(ev)-1]})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at.Before(cands[j].at) })
	n := over + l.maxKeys/10
	if n > len(cands) {
		n = len(cands)
	}
	for _, c := range cands[:n] {
		delete(l.events, c.key)
	}
}

// newKeys counts the buckets that do not exist yet; caller holds l.mu.
func (l *LoginLimiter) newKeys(buckets []loginBucket) int {
	n := 0
	for _, b := range buckets {
		if _, ok := l.events[b.key]; !ok {
			n++
		}
	}
	return n
}

// ReleaseLogin refunds an AcquireLogin reservation when no verification ran.
func (l *LoginLimiter) ReleaseLogin(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.loginBuckets(ip, username)
	if l.releaseLocked(b[0].key) {
		l.refundGroupLocked(LoginAddressGroup(ip))
	}
	l.releaseLocked(b[1].key)
}

// refundGroupLocked gives back one account slot of an address group, for a
// pair key that went away without leaving failures behind. Without it a
// client that signs in correctly many times would use up the cap. Caller
// holds l.mu.
func (l *LoginLimiter) refundGroupLocked(grp string) {
	if g, ok := l.groupKeys[grp]; ok && g.n > 0 {
		g.n--
		l.groupKeys[grp] = g
	}
}

// LoginSucceeded settles a reservation after a correct password: it clears the
// failures for this address and account pair and refunds this attempt in the
// address bucket, leaving every other failure counted.
func (l *LoginLimiter) LoginSucceeded(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.loginBuckets(ip, username)
	if _, ok := l.events[b[0].key]; ok {
		delete(l.events, b[0].key)
		l.refundGroupLocked(LoginAddressGroup(ip))
	}
	l.releaseLocked(b[1].key)
}

// releaseLocked drops the newest event of key and reports whether that
// removed the key; caller holds l.mu.
func (l *LoginLimiter) releaseLocked(key string) bool {
	events := l.events[key]
	switch len(events) {
	case 0:
	case 1:
		delete(l.events, key)
		return true
	default:
		l.events[key] = events[:len(events)-1]
	}
	return false
}
