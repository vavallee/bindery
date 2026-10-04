package newznab

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/httpsec"
)

// A download URL from Jackett or a private tracker carries credentials Bindery
// cannot re-derive at grab time: Jackett's /dl/ links need jackett_apikey, a
// tracker link needs the user's passkey. Search results go to non-admin users
// and come straight back on a grab, so those values are sealed in the response
// rather than dropped (dropping them would break every such grab) and unsealed
// server-side when the URL is posted back.
//
// The seal is AES-GCM under a key generated at process start and never stored,
// bound to the URL's host and the parameter name so a sealed value only ever
// opens for the URL it came from. The nonce is derived from the plaintext
// (synthetic IV) so the same URL redacts to the same string on every response,
// which reveals only that two results share a credential. A restart invalidates
// outstanding sealed URLs; the grab then fails with errSealExpired and the user
// searches again.

// sealPrefix marks a sealed parameter value. Every character after it is
// unreserved in a URL, so the value survives query re-encoding unchanged.
const sealPrefix = "bindery-sealed."

// ErrSealExpired is returned by UnsealDownloadURL when a sealed value does not
// open: Bindery restarted since the search, or the URL was altered.
var ErrSealExpired = errors.New("this search result has expired, search again")

var (
	sealOnce   sync.Once
	sealAEAD   cipher.AEAD
	sealMACKey []byte
	sealErr    error
)

func sealKeys() (cipher.AEAD, []byte, error) {
	sealOnce.Do(func() {
		k := make([]byte, 64)
		if _, err := rand.Read(k); err != nil {
			sealErr = fmt.Errorf("seal key: %w", err)
			return
		}
		block, err := aes.NewCipher(k[:32])
		if err != nil {
			sealErr = fmt.Errorf("seal key: %w", err)
			return
		}
		sealAEAD, sealErr = cipher.NewGCM(block)
		sealMACKey = k[32:]
	})
	return sealAEAD, sealMACKey, sealErr
}

func sealAAD(host, name string) []byte {
	return []byte("bindery download url v1\x00" + strings.ToLower(host) + "\x00" + name)
}

func isSealed(rawValue string) bool {
	return strings.HasPrefix(rawValue, sealPrefix)
}

// sealParam returns the sealed form of rawValue for parameter name on host.
// If sealing is unavailable the value is replaced with REDACTED: losing a grab
// is the lesser failure next to leaking the credential.
func sealParam(host, name, rawValue string) string {
	aead, macKey, err := sealKeys()
	if err != nil {
		return "REDACTED"
	}
	aad := sealAAD(host, name)
	mac := hmac.New(sha256.New, macKey)
	mac.Write(aad)
	mac.Write([]byte{0})
	mac.Write([]byte(rawValue))
	nonce := mac.Sum(nil)[:aead.NonceSize()]
	ct := aead.Seal(nil, nonce, []byte(rawValue), aad)
	return sealPrefix + base64.RawURLEncoding.EncodeToString(append(nonce, ct...))
}

func openParam(host, name, sealed string) (string, error) {
	aead, _, err := sealKeys()
	if err != nil {
		return "", ErrSealExpired
	}
	blob, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil || len(blob) < aead.NonceSize() {
		return "", ErrSealExpired
	}
	pt, err := aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], sealAAD(host, name))
	if err != nil {
		return "", ErrSealExpired
	}
	return string(pt), nil
}

// UnsealDownloadURL restores the credentials RedactDownloadURL sealed into a
// download URL. A URL with nothing sealed in it, which includes every URL that
// never left the server, is returned unchanged. It does not restore the
// indexer apikey; SignDownloadURLFor does that.
func UnsealDownloadURL(raw string) (string, error) {
	if !strings.Contains(raw, sealPrefix) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrSealExpired
	}
	var openErr error
	rq := httpsec.MapSecretQueryParams(u.RawQuery, func(name, rawValue string) (string, bool) {
		if !isSealed(rawValue) {
			return rawValue, true
		}
		v, err := openParam(u.Host, name, rawValue)
		if err != nil {
			openErr = err
			return rawValue, true
		}
		return v, true
	})
	if openErr != nil {
		return "", openErr
	}
	u.RawQuery = rq
	return u.String(), nil
}
