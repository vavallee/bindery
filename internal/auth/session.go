package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Session cookie format (all dot-separated).
//
// Current (v2), carries a key-id so the secret can be rotated with an
// overlapping window:
//
//	v2.<key_id>.<user_id>.<expires_unix>.<hmac(secret, "v2.<key_id>.<user_id>.<expires_unix>")>
//
// Legacy (v1), no key-id — still accepted on verification so a deploy that
// introduces v2 does not invalidate every outstanding cookie at once:
//
//	v1.<user_id>.<expires_unix>.<hmac(secret, "v1.<user_id>.<expires_unix>")>
//
// <key_id> is a short, deterministic fingerprint of the signing secret (see
// keyID). Because it is derived from the secret itself there is nothing extra
// to store: rotating the secret changes the key-id automatically, and a
// verifier handed several candidate secrets can match the cookie's key-id to
// pick the right one (or fall back to trying each).
//
// Self-contained: no server-side session table.
const (
	SessionCookieName    = "bindery_session"
	SessionDuration      = 30 * 24 * time.Hour // when "remember me" is checked
	SessionDurationShort = 12 * time.Hour      // browser-session equivalent when not

	sessionCookieVersion       = "v2" // current format written by SignSession
	sessionCookieVersionLegacy = "v1" // older format still accepted by VerifySession

	// keyIDLen is the number of hex chars of the secret fingerprint embedded
	// in the cookie. 8 hex chars = 32 bits — enough to distinguish a handful of
	// rotation-window secrets; it is not security-relevant (the HMAC is).
	keyIDLen = 8

	// minSecretLen is the minimum length (in bytes) required for a session
	// signing secret. Shorter secrets are rejected fail-closed.
	minSecretLen = 32
)

// Sentinel errors returned (wrapped) by VerifySession and SignSession.
// Callers can use errors.Is to distinguish expiry from tamper/format failures.
//
//   - ErrSessionExpired  — the cookie is structurally valid but its expiry has passed.
//   - ErrSessionInvalid  — the cookie is malformed, has a bad signature, or the
//     session secret is absent / too short to be safe.
var (
	ErrSessionExpired = errors.New("session expired")
	ErrSessionInvalid = errors.New("session invalid")
)

// keyID returns a short, deterministic, non-secret fingerprint of secret,
// used as the cookie's key-id. It is HMAC-SHA256 keyed by the secret over a
// fixed domain-separation label, truncated to keyIDLen hex chars. Using an
// HMAC (rather than a bare hash of the secret) avoids publishing a plain
// digest of the signing key in every cookie.
func keyID(secret []byte) string {
	mac := hmacSum(secret, "bindery-session-key-id")
	return hex.EncodeToString(mac)[:keyIDLen]
}

// SignSession returns a signed cookie value (current v2 format, with key-id)
// for the given user that expires at exp. It returns an error wrapping
// ErrSessionInvalid if secret is nil or shorter than minSecretLen bytes.
func SignSession(secret []byte, userID int64, exp time.Time) (string, error) {
	if len(secret) < minSecretLen {
		return "", fmt.Errorf("sign session: %w", ErrSessionInvalid)
	}
	payload := fmt.Sprintf("%s.%s.%d.%d", sessionCookieVersion, keyID(secret), userID, exp.Unix())
	mac := hmacSum(secret, payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// VerifySession returns the user id carried by a valid, unexpired signed
// cookie, or an error on any tamper/expiry/parse failure. It accepts both the
// current v2 (key-id) format and the legacy v1 format. It returns
// ErrSessionInvalid (wrapped) when secret is absent/too short.
func VerifySession(secret []byte, cookie string) (int64, error) {
	return VerifySessionMulti([][]byte{secret}, cookie)
}

// VerifySessionMulti is VerifySession against a small ordered set of candidate
// secrets — the rotation primitive. During a secret rotation the verifier can
// be handed {current, previous} so cookies signed under either are accepted;
// the writer (SignSession) always uses the first/current secret.
//
// For a v2 cookie the embedded key-id is used to pick the matching candidate
// directly (constant work, no HMAC against non-matching secrets); if no key-id
// matches, every candidate is still HMAC-checked so a key-id collision or a
// secret whose fingerprint is unknown cannot cause a spurious rejection. For a
// legacy v1 cookie (no key-id) each candidate is tried in turn.
//
// Verification is never weakened: a cookie is accepted only if some candidate
// secret produces an HMAC equal (in constant time) to the cookie's signature.
func VerifySessionMulti(secrets [][]byte, cookie string) (int64, error) {
	// Keep only usable (long-enough) secrets. Fail closed if none remain.
	var usable [][]byte
	for _, s := range secrets {
		if len(s) >= minSecretLen {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		return 0, fmt.Errorf("verify session: %w", ErrSessionInvalid)
	}

	parts := strings.Split(cookie, ".")
	var sig string
	var idIdx, expIdx int // indices into parts of user-id and expiry
	var cookieKeyID string

	switch {
	case len(parts) == 5 && parts[0] == sessionCookieVersion:
		// v2.<key_id>.<user_id>.<expires>.<hmac>
		cookieKeyID = parts[1]
		idIdx, expIdx = 2, 3
		sig = parts[4]
	case len(parts) == 4 && parts[0] == sessionCookieVersionLegacy:
		// v1.<user_id>.<expires>.<hmac>
		idIdx, expIdx = 1, 2
		sig = parts[3]
	default:
		return 0, fmt.Errorf("malformed session: %w", ErrSessionInvalid)
	}

	userID, err := strconv.ParseInt(parts[idIdx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad user id: %w", ErrSessionInvalid)
	}
	expUnix, err := strconv.ParseInt(parts[expIdx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad expiry: %w", ErrSessionInvalid)
	}
	payload := strings.Join(parts[:len(parts)-1], ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return 0, fmt.Errorf("bad signature encoding: %w", ErrSessionInvalid)
	}

	// Try the candidate whose key-id matches first (v2 fast path), then fall
	// back to every candidate. matched stays false until a constant-time HMAC
	// comparison succeeds.
	matched := false
	for _, secret := range usable {
		if cookieKeyID != "" && keyID(secret) != cookieKeyID {
			// key-id mismatch — still checked in the fallback pass below, but
			// skipped here to keep the common case cheap.
			continue
		}
		if hmac.Equal(got, hmacSum(secret, payload)) {
			matched = true
			break
		}
	}
	if !matched && cookieKeyID != "" {
		// Fallback: a v2 cookie whose key-id matched no candidate (collision,
		// or a candidate set that does not announce its fingerprints). Verify
		// against every secret so a correct signature is never rejected.
		for _, secret := range usable {
			if hmac.Equal(got, hmacSum(secret, payload)) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return 0, fmt.Errorf("bad signature: %w", ErrSessionInvalid)
	}
	if time.Now().Unix() > expUnix {
		return 0, fmt.Errorf("cookie expired: %w", ErrSessionExpired)
	}
	return userID, nil
}

func hmacSum(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
