// Package infohash derives BitTorrent v1 infohashes from the two things a
// download client is ever handed: a magnet URI, or the raw bytes of a
// .torrent file.
//
// It exists because two download clients need the same derivation for
// different reasons. qBittorrent's 409 "already present" reply to
// POST /torrents/add carries no hash, so the hash has to be recovered
// locally. rTorrent's load.start / load.raw_start commands return 0 on
// success and never report a hash at all, so the hash must be computed
// before the torrent is submitted or Bindery has nothing to poll on.
package infohash

import (
	//nolint:gosec // G505: protocol-mandated SHA-1 for the BitTorrent v1 infohash, see the #nosec note
	"crypto/sha1" // #nosec G505 -- the BitTorrent v1 infohash is defined as SHA-1, not a security primitive
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// errBencode marks malformed bencode input. Callers treat it as "hash not
// recoverable" rather than a fatal error.
var errBencode = errors.New("invalid bencode")

// FromTorrentFile computes a torrent's v1 infohash — the SHA-1 of the bencoded
// "info" dictionary — from raw .torrent file bytes. It returns "" when data is
// not a bencoded dictionary containing an "info" key.
func FromTorrentFile(data []byte) string {
	start, end, ok := bencodeMemberSpan(data, "info")
	if !ok {
		return ""
	}
	//nolint:gosec // G401: protocol-mandated SHA-1 for the BitTorrent v1 infohash, see the #nosec note
	sum := sha1.Sum(data[start:end]) // #nosec G401 -- the BitTorrent v1 infohash is defined as SHA-1, not a security primitive
	return hex.EncodeToString(sum[:])
}

// FromMagnet extracts the btih topic of a magnet URI, lower-cased and otherwise
// verbatim. The result may be either 40-char hex or 32-char base32; call
// Normalize when a canonical hex form is required. Returns "" for anything that
// is not a magnet URI carrying an xt=urn:btih: topic.
func FromMagnet(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "magnet" {
		return ""
	}
	xt := u.Query().Get("xt")
	if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
		return ""
	}
	h := strings.TrimSpace(xt[len("urn:btih:"):])
	if h == "" {
		return ""
	}
	return strings.ToLower(h)
}

// Normalize canonicalises an infohash to 40 lower-case hex characters.
//
// Both spellings BEP 9 permits are accepted: 40-char hex, and the 32-char
// base32 form that older trackers still emit in magnet links. Anything else
// (a v2 multihash, a truncated hash, junk) returns "" — callers must treat
// that as "cannot track this torrent" rather than guessing, because rTorrent
// addresses every d.* command by exact hash.
func Normalize(raw string) string {
	h := strings.TrimSpace(raw)
	switch len(h) {
	case 40:
		if _, err := hex.DecodeString(h); err != nil {
			return ""
		}
		return strings.ToLower(h)
	case 32:
		// base32 alphabet is upper-case; magnet links are commonly lower-cased
		// in transit (FromMagnet does exactly that), so upper-case before decoding.
		decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
		if err != nil || len(decoded) != 20 {
			return ""
		}
		return hex.EncodeToString(decoded)
	default:
		return ""
	}
}

// bencodeMemberSpan returns the [start,end) byte span of the value mapped to
// key in the top-level bencoded dictionary in data. The span covers the value's
// exact bytes, delimiters included, which is what the v1 infohash is taken over.
func bencodeMemberSpan(data []byte, key string) (start, end int, ok bool) {
	if len(data) == 0 || data[0] != 'd' {
		return 0, 0, false
	}
	pos := 1
	for pos < len(data) && data[pos] != 'e' {
		k, afterKey, err := bencodeReadString(data, pos)
		if err != nil {
			return 0, 0, false
		}
		valEnd, err := bencodeSkipValue(data, afterKey)
		if err != nil {
			return 0, 0, false
		}
		if string(k) == key {
			return afterKey, valEnd, true
		}
		pos = valEnd
	}
	return 0, 0, false
}

// bencodeReadString reads a bencoded byte string ("<len>:<bytes>") at pos and
// returns its bytes and the index immediately after it.
func bencodeReadString(data []byte, pos int) ([]byte, int, error) {
	colon := pos
	for colon < len(data) && data[colon] != ':' {
		if data[colon] < '0' || data[colon] > '9' {
			return nil, 0, errBencode
		}
		colon++
	}
	if colon == pos || colon >= len(data) {
		return nil, 0, errBencode
	}
	n, err := strconv.Atoi(string(data[pos:colon]))
	if err != nil || n < 0 {
		return nil, 0, errBencode
	}
	start := colon + 1
	end := start + n
	if end > len(data) || end < start {
		return nil, 0, errBencode
	}
	return data[start:end], end, nil
}

// bencodeSkipValue advances past one bencoded value of any type at pos and
// returns the index immediately after it.
func bencodeSkipValue(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, errBencode
	}
	switch c := data[pos]; {
	case c == 'i': // integer: i<digits>e
		end := pos + 1
		for end < len(data) && data[end] != 'e' {
			end++
		}
		if end >= len(data) {
			return 0, errBencode
		}
		return end + 1, nil
	case c == 'l' || c == 'd': // list (l...e) or dict (d...e)
		p := pos + 1
		for p < len(data) && data[p] != 'e' {
			var err error
			if c == 'd' {
				// Dictionary keys are bencoded byte strings.
				if _, p, err = bencodeReadString(data, p); err != nil {
					return 0, err
				}
			}
			if p, err = bencodeSkipValue(data, p); err != nil {
				return 0, err
			}
		}
		if p >= len(data) {
			return 0, errBencode
		}
		return p + 1, nil
	case c >= '0' && c <= '9': // byte string: <len>:<bytes>
		_, end, err := bencodeReadString(data, pos)
		return end, err
	default:
		return 0, errBencode
	}
}
