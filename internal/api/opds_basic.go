package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/auth"
)

// OPDS readers send HTTP Basic credentials on every request and fetch covers
// in parallel, so a bare per-request argon2 check is both expensive and, with
// the limiter counting attempts in flight, would refuse the excess of a
// legitimate parallel burst. opdsBasicVerifier fixes both without loosening
// the guessing limit:
//
//   - A short lived cache of SUCCESSFUL verifications. Hits skip the limiter
//     and the KDF. The key is an HMAC, under a key that never leaves the
//     process, over the username, the password and the user's stored hash, so
//     the password itself is never stored and a password change (new stored
//     hash) misses the cache at once. Failures are never cached.
//   - Single flight for identical credentials: concurrent requests with the
//     same key wait for one verification and share its outcome, so a cold
//     burst of N identical requests costs one limiter reservation and one KDF.
//     Different passwords never share a flight, so every distinct guess still
//     reserves an attempt and the burst limit for guessing is unchanged.
const (
	opdsBasicCacheTTL = 10 * time.Minute
	opdsBasicCacheMax = 1024
)

type opdsBasicOutcome int

const (
	opdsBasicFail      opdsBasicOutcome = iota // verified, credentials wrong
	opdsBasicOK                                // verified, credentials right
	opdsBasicLimited                           // refused by the per address limiter
	opdsBasicAbandoned                         // caller went away before any verification ran
)

type opdsBasicFlight struct {
	done    chan struct{}
	ip      string
	outcome opdsBasicOutcome
}

type opdsBasicVerifier struct {
	key    []byte
	ttl    time.Duration
	max    int
	now    func() time.Time
	verify func(ctx context.Context, password, phc string) (bool, error)
	// kdfRuns counts verifications that actually ran the KDF, for tests.
	kdfRuns atomic.Int64

	mu       sync.Mutex
	cache    map[string]time.Time // credential key -> expiry
	inflight map[string]*opdsBasicFlight
}

func newOPDSBasicVerifier() *opdsBasicVerifier {
	key := make([]byte, 32)
	// crypto/rand.Read never returns an error on supported platforms (it
	// aborts the process instead), so there is no failure path to handle.
	_, _ = rand.Read(key)
	return &opdsBasicVerifier{
		key:      key,
		ttl:      opdsBasicCacheTTL,
		max:      opdsBasicCacheMax,
		now:      time.Now,
		verify:   auth.VerifyPasswordContext,
		cache:    map[string]time.Time{},
		inflight: map[string]*opdsBasicFlight{},
	}
}

// credKey is HMAC-SHA256 over length prefixed fields, so no choice of
// username and password can collide with another split of the same bytes.
func (v *opdsBasicVerifier) credKey(username, password, storedHash string) string {
	m := hmac.New(sha256.New, v.key)
	for _, f := range []string{username, password, storedHash} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		m.Write(n[:])
		m.Write([]byte(f))
	}
	return hex.EncodeToString(m.Sum(nil))
}

// check decides a Basic credential. storedHash is the user's password hash,
// or the dummy hash when userKnown is false (the KDF still runs, so timing
// does not reveal which usernames exist, and the result is never OK).
func (v *opdsBasicVerifier) check(ctx context.Context, limiter *auth.LoginLimiter, ip, username, password, storedHash string, userKnown bool) opdsBasicOutcome {
	k := v.credKey(username, password, storedHash)
	for {
		v.mu.Lock()
		if userKnown && v.cachedLocked(k) {
			v.mu.Unlock()
			return opdsBasicOK
		}
		if f := v.inflight[k]; f != nil {
			v.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return opdsBasicAbandoned
			}
			switch {
			case f.outcome == opdsBasicAbandoned:
				continue // the leader gave up before verifying; try again
			case f.outcome == opdsBasicLimited && f.ip != ip:
				continue // the leader's address was limited, not ours
			}
			return f.outcome
		}
		// Cheap early refusal before taking the lead, as before this cache
		// existed. Cache hits above never reach it.
		if limiter != nil && !limiter.Allow(ip) {
			v.mu.Unlock()
			return opdsBasicLimited
		}
		f := &opdsBasicFlight{done: make(chan struct{}), ip: ip}
		v.inflight[k] = f
		v.mu.Unlock()

		out := v.lead(ctx, limiter, ip, password, storedHash, userKnown)

		v.mu.Lock()
		f.outcome = out
		delete(v.inflight, k)
		if out == opdsBasicOK {
			v.storeLocked(k)
		}
		v.mu.Unlock()
		close(f.done)
		return out
	}
}

// lead runs the one verification for a flight: reserve the attempt, run the
// KDF, and settle the reservation (refund if nothing ran, reset on success).
func (v *opdsBasicVerifier) lead(ctx context.Context, limiter *auth.LoginLimiter, ip, password, storedHash string, userKnown bool) opdsBasicOutcome {
	if limiter != nil && !limiter.Acquire(ip) {
		return opdsBasicLimited
	}
	ok, err := v.verify(ctx, password, storedHash)
	if err != nil {
		// Gave up waiting for a KDF slot: nothing was verified, so the
		// attempt must not count toward locking the address out.
		if limiter != nil {
			limiter.Release(ip)
		}
		return opdsBasicAbandoned
	}
	v.kdfRuns.Add(1)
	if ok && userKnown {
		if limiter != nil {
			limiter.Reset(ip)
		}
		return opdsBasicOK
	}
	return opdsBasicFail
}

// cachedLocked reports a live cache entry for k; caller holds v.mu.
func (v *opdsBasicVerifier) cachedLocked(k string) bool {
	exp, ok := v.cache[k]
	if !ok {
		return false
	}
	if !exp.After(v.now()) {
		delete(v.cache, k)
		return false
	}
	return true
}

// storeLocked caches k, keeping the map within v.max; caller holds v.mu.
func (v *opdsBasicVerifier) storeLocked(k string) {
	now := v.now()
	if len(v.cache) >= v.max {
		for ck, exp := range v.cache {
			if !exp.After(now) {
				delete(v.cache, ck)
			}
		}
	}
	for ck := range v.cache {
		if len(v.cache) < v.max {
			break
		}
		delete(v.cache, ck) // still full of live entries: drop an arbitrary one
	}
	v.cache[k] = now.Add(v.ttl)
}
