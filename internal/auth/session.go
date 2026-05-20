package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Session cookie format (all base64-url-nopad, dot-separated):
//
//	v1.<user_id>.<expires_unix>.<hmac(secret, "v1."+user_id+"."+expires)>
//
// Self-contained: no server-side session table. Rotating auth.session_secret
// invalidates every outstanding cookie.
const (
	SessionCookieName    = "bindery_session"
	SessionDuration      = 30 * 24 * time.Hour // when "remember me" is checked
	SessionDurationShort = 12 * time.Hour      // browser-session equivalent when not
	sessionCookieVersion = "v1"

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

// SignSession returns a signed cookie value for the given user that expires at exp.
// It returns an error if secret is nil or shorter than minSecretLen bytes.
func SignSession(secret []byte, userID int64, exp time.Time) (string, error) {
	if len(secret) < minSecretLen {
		return "", fmt.Errorf("sign session: %w", ErrSessionInvalid)
	}
	payload := fmt.Sprintf("%s.%d.%d", sessionCookieVersion, userID, exp.Unix())
	mac := hmacSum(secret, payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// VerifySession returns the user id carried by a valid, unexpired signed cookie,
// or an error on any tamper/expiry/parse failure.
// It returns ErrSessionInvalid (wrapped) when secret is absent/too short.
func VerifySession(secret []byte, cookie string) (int64, error) {
	if len(secret) < minSecretLen {
		return 0, fmt.Errorf("verify session: %w", ErrSessionInvalid)
	}
	parts := strings.Split(cookie, ".")
	if len(parts) != 4 || parts[0] != sessionCookieVersion {
		return 0, fmt.Errorf("malformed session: %w", ErrSessionInvalid)
	}
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad user id: %w", ErrSessionInvalid)
	}
	expUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad expiry: %w", ErrSessionInvalid)
	}
	payload := strings.Join(parts[:3], ".")
	want := hmacSum(secret, payload)
	got, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return 0, fmt.Errorf("bad signature encoding: %w", ErrSessionInvalid)
	}
	if !hmac.Equal(got, want) {
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
