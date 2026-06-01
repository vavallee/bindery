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
// Current (v3), carries a per-user session epoch so a password change can
// revoke every outstanding cookie for that user without rotating the
// server-wide signing secret:
//
//	v3.<key_id>.<user_id>.<session_epoch>.<expires_unix>.<hmac(secret, "v3.<key_id>.<user_id>.<session_epoch>.<expires_unix>")>
//
// The middleware compares <session_epoch> against the live users.session_epoch
// column and rejects mismatched cookies. UpdatePassword bumps the column,
// which is how "log everyone out after a password change" is enforced
// (Wave 1 / Bundle C audit finding).
//
// Previous (v2), no epoch — still ACCEPTED on verification with epoch=0
// returned, but in production users.session_epoch defaults to 1 after the
// 047 migration, so every v2 cookie minted before the upgrade fails the
// epoch check. That is the deliberate forced-logout-on-upgrade behaviour:
//
//	v2.<key_id>.<user_id>.<expires_unix>.<hmac(secret, "v2.<key_id>.<user_id>.<expires_unix>")>
//
// Legacy (v1), no key-id and no epoch — same backwards-compat treatment as
// v2 (returns epoch=0, callers compare and reject):
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

	sessionCookieVersion       = "v3" // current format written by SignSessionWithEpoch
	sessionCookieVersionV2     = "v2" // pre-epoch format, accepted on verify
	sessionCookieVersionLegacy = "v1" // oldest format, accepted on verify

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

// SignSession returns a signed cookie value for the given user that expires
// at exp. It mints a v3 cookie with session_epoch=0 — provided as a
// convenience wrapper for tests and callers that do not yet plumb an epoch.
// PRODUCTION callers MUST use SignSessionWithEpoch with the user's current
// users.session_epoch value; otherwise the resulting cookie will fail the
// middleware's epoch check (which expects a non-zero default after migration
// 047). It returns an error wrapping ErrSessionInvalid if secret is nil or
// shorter than minSecretLen bytes.
func SignSession(secret []byte, userID int64, exp time.Time) (string, error) {
	return SignSessionWithEpoch(secret, userID, 0, exp)
}

// SignSessionWithEpoch returns a signed v3 cookie carrying the user's
// session epoch. PRODUCTION callers must source epoch from
// users.session_epoch so the middleware's per-request comparison succeeds.
// Returns an error wrapping ErrSessionInvalid if secret is nil or shorter
// than minSecretLen bytes.
func SignSessionWithEpoch(secret []byte, userID, epoch int64, exp time.Time) (string, error) {
	if len(secret) < minSecretLen {
		return "", fmt.Errorf("sign session: %w", ErrSessionInvalid)
	}
	payload := fmt.Sprintf("%s.%s.%d.%d.%d", sessionCookieVersion, keyID(secret), userID, epoch, exp.Unix())
	mac := hmacSum(secret, payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// VerifySession returns the user id carried by a valid, unexpired signed
// cookie, or an error on any tamper/expiry/parse failure. It accepts v3, v2
// and legacy v1 formats. The carried epoch (zero for v2/v1) is discarded; use
// VerifySessionWithEpoch when the caller needs to enforce a session-epoch
// match (the auth middleware does). Returns ErrSessionInvalid (wrapped) when
// secret is absent/too short.
func VerifySession(secret []byte, cookie string) (int64, error) {
	uid, _, err := VerifySessionMultiWithEpoch([][]byte{secret}, cookie)
	return uid, err
}

// VerifySessionWithEpoch is VerifySession that also returns the session_epoch
// embedded in the cookie. v3 cookies carry an explicit epoch; v2 and v1
// cookies decode as epoch=0, which lets the middleware reject them after the
// 047 migration sets every user's live epoch to >= 1 (the forced-logout-on-
// upgrade behaviour called out in the release notes).
func VerifySessionWithEpoch(secret []byte, cookie string) (int64, int64, error) {
	return VerifySessionMultiWithEpoch([][]byte{secret}, cookie)
}

// VerifySessionMulti is VerifySession against a small ordered set of candidate
// secrets — the rotation primitive. During a secret rotation the verifier can
// be handed {current, previous} so cookies signed under either are accepted;
// the writer (SignSession) always uses the first/current secret.
//
// Verification is never weakened: a cookie is accepted only if some candidate
// secret produces an HMAC equal (in constant time) to the cookie's signature.
func VerifySessionMulti(secrets [][]byte, cookie string) (int64, error) {
	uid, _, err := VerifySessionMultiWithEpoch(secrets, cookie)
	return uid, err
}

// VerifySessionMultiWithEpoch is the full verifier: returns (userID, epoch,
// err). For a v3 cookie the embedded key-id is used to pick the matching
// candidate directly (constant work, no HMAC against non-matching secrets);
// if no key-id matches, every candidate is still HMAC-checked so a key-id
// collision or a secret whose fingerprint is unknown cannot cause a spurious
// rejection. For legacy v2/v1 cookies each candidate is tried in turn and
// epoch=0 is returned.
//
// Verification is never weakened: a cookie is accepted only if some candidate
// secret produces an HMAC equal (in constant time) to the cookie's signature.
func VerifySessionMultiWithEpoch(secrets [][]byte, cookie string) (int64, int64, error) {
	// Keep only usable (long-enough) secrets. Fail closed if none remain.
	var usable [][]byte
	for _, s := range secrets {
		if len(s) >= minSecretLen {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		return 0, 0, fmt.Errorf("verify session: %w", ErrSessionInvalid)
	}

	parts := strings.Split(cookie, ".")
	var sig string
	var idIdx, expIdx, epochIdx int // indices into parts; -1 means absent
	var cookieKeyID string
	epochIdx = -1

	switch {
	case len(parts) == 6 && parts[0] == sessionCookieVersion:
		// v3.<key_id>.<user_id>.<session_epoch>.<expires>.<hmac>
		cookieKeyID = parts[1]
		idIdx, epochIdx, expIdx = 2, 3, 4
		sig = parts[5]
	case len(parts) == 5 && parts[0] == sessionCookieVersionV2:
		// v2.<key_id>.<user_id>.<expires>.<hmac>
		cookieKeyID = parts[1]
		idIdx, expIdx = 2, 3
		sig = parts[4]
	case len(parts) == 4 && parts[0] == sessionCookieVersionLegacy:
		// v1.<user_id>.<expires>.<hmac>
		idIdx, expIdx = 1, 2
		sig = parts[3]
	default:
		return 0, 0, fmt.Errorf("malformed session: %w", ErrSessionInvalid)
	}

	userID, err := strconv.ParseInt(parts[idIdx], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad user id: %w", ErrSessionInvalid)
	}
	var epoch int64
	if epochIdx >= 0 {
		epoch, err = strconv.ParseInt(parts[epochIdx], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("bad session epoch: %w", ErrSessionInvalid)
		}
	}
	expUnix, err := strconv.ParseInt(parts[expIdx], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad expiry: %w", ErrSessionInvalid)
	}
	payload := strings.Join(parts[:len(parts)-1], ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return 0, 0, fmt.Errorf("bad signature encoding: %w", ErrSessionInvalid)
	}

	// Try the candidate whose key-id matches first (v3/v2 fast path), then
	// fall back to every candidate. matched stays false until a constant-time
	// HMAC comparison succeeds.
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
		// Fallback: a v3/v2 cookie whose key-id matched no candidate
		// (collision, or a candidate set that does not announce its
		// fingerprints). Verify against every secret so a correct signature is
		// never rejected.
		for _, secret := range usable {
			if hmac.Equal(got, hmacSum(secret, payload)) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return 0, 0, fmt.Errorf("bad signature: %w", ErrSessionInvalid)
	}
	if time.Now().Unix() > expUnix {
		return 0, 0, fmt.Errorf("cookie expired: %w", ErrSessionExpired)
	}
	return userID, epoch, nil
}

func hmacSum(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
