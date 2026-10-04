// Package auth implements password hashing, signed session cookies,
// the composite auth middleware (cookie OR API key OR local-bypass),
// and the small helpers those require.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters. 64 MiB memory, 1 iteration, 4 lanes is OWASP-recommended
// (2024 cheat sheet) for interactive logins. 16-byte salt, 32-byte output.
const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// kdfSlots bounds how many argon2 computations (hash or verify) run at once in
// the process. Each one allocates argonMemory (64 MiB), and the login, setup
// and OPDS Basic paths all reach the KDF before the caller is authenticated,
// so without a bound a few hundred parallel requests allocate tens of GB and
// the process is OOM killed. The per-IP login limiter cannot prevent that on
// its own: it bounds attempts per address, not across addresses.
//
// The size is GOMAXPROCS clamped to [2, 4]. The KDF is CPU bound and already
// spreads over argonThreads lanes, so slots beyond the core count add memory
// without adding throughput. The ceiling of 4 caps the KDF's peak at 256 MiB
// however many cores the host has; the floor of 2 keeps one slow request from
// serialising every login on a single core box. Interactive logins are rare
// and take tens of milliseconds, so the queue is empty in normal use.
var kdfSlots = make(chan struct{}, kdfConcurrency())

func kdfConcurrency() int {
	return min(max(runtime.GOMAXPROCS(0), 2), 4)
}

// acquireKDF waits for a KDF slot, giving up when ctx is done. The returned
// release must be called exactly once.
func acquireKDF(ctx context.Context) (release func(), err error) {
	select {
	case kdfSlots <- struct{}{}:
		return func() { <-kdfSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// HashPassword returns a PHC-formatted argon2id string:
//
//	$argon2id$v=19$m=65536,t=1,p=4$<b64 salt>$<b64 hash>
//
// It waits for a KDF slot (see kdfSlots) with no deadline; request handlers
// that should stop waiting when the client goes away use HashPasswordContext.
func HashPassword(password string) (string, error) {
	return HashPasswordContext(context.Background(), password)
}

// HashPasswordContext is HashPassword that stops waiting for a KDF slot when
// ctx is done, returning ctx's error.
func HashPasswordContext(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	release, err := acquireKDF(ctx)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	release()
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword returns true when password matches the stored PHC hash.
// Returns false on mismatch or any parse error. It waits for a KDF slot (see
// kdfSlots) with no deadline; request handlers use VerifyPasswordContext so a
// caller that goes away stops waiting.
func VerifyPassword(password, phc string) bool {
	ok, _ := VerifyPasswordContext(context.Background(), password, phc)
	return ok
}

// VerifyPasswordContext is VerifyPassword under the process-wide KDF bound. It
// returns (false, ctx's error) when ctx is done before a KDF slot frees up; a
// mismatch or parse failure is (false, nil). The PHC string is parsed before
// waiting, so a malformed hash never occupies a slot.
func VerifyPasswordContext(ctx context.Context, password, phc string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, nil
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, nil
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, nil
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, nil
	}
	release, err := acquireKDF(ctx)
	if err != nil {
		return false, err
	}
	// len(want) is bounded by the PHC hash length we wrote (argonKeyLen = 32),
	// so the uint32 conversion cannot overflow. gosec G115 is a false positive.
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want))) //nolint:gosec // bounded by argonKeyLen
	release()
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyPasswordHash lazily builds a valid argon2id PHC hash (with the current
// parameters) that no real password matches. It's computed once, on first use,
// so importing this package costs nothing.
var dummyPasswordHash = sync.OnceValue(func() string {
	h, err := HashPassword("not-a-real-credential-only-for-timing")
	if err != nil {
		// rand.Read is the only failure path and effectively never fails; fall
		// back to a structurally valid PHC with the same params so
		// VerifyPassword still runs the full KDF (equal timing) and returns false.
		return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
			argon2.Version, argonMemory, argonTime, argonThreads,
			base64.RawStdEncoding.EncodeToString(make([]byte, argonSaltLen)),
			base64.RawStdEncoding.EncodeToString(make([]byte, argonKeyLen)),
		)
	}
	return h
})

// DummyPasswordHash returns a valid argon2id hash that no password matches, for
// equalizing login timing when the supplied username does not exist. Verifying
// any password against it runs the full KDF and fails, so a missing-user login
// costs the same as a wrong-password login and usernames cannot be enumerated
// by measuring response time.
func DummyPasswordHash() string { return dummyPasswordHash() }

// RandomHex returns 2*n hex characters from crypto/rand.
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out), nil
}

// RandomBase64 returns n bytes of randomness as unpadded base64.
func RandomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

// RandomBase64URL returns n bytes of randomness as unpadded base64url, which
// never contains a '.', so it is safe inside a dot-separated session cookie.
func RandomBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ErrInvalidCredentials is returned by Login when username or password is wrong.
var ErrInvalidCredentials = errors.New("invalid credentials")
