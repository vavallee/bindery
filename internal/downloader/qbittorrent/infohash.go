package qbittorrent

import (
	//nolint:gosec // G505: protocol-mandated SHA-1 for the BitTorrent v1 infohash, see the #nosec note
	"crypto/sha1" // #nosec G505 -- the BitTorrent v1 infohash is defined as SHA-1, not a security primitive
	"encoding/hex"
	"errors"
	"strconv"
)

// errBencode marks malformed bencode input. Callers treat it as "hash not
// recoverable" rather than a fatal error.
var errBencode = errors.New("invalid bencode")

// infoHashFromTorrentFile computes a torrent's v1 infohash — the SHA-1 of the
// bencoded "info" dictionary — from raw .torrent file bytes. It returns "" when
// data is not a bencoded dictionary containing an "info" key.
//
// qBittorrent's 409 "already present" response to POST /torrents/add carries no
// hash, so when a torrent is submitted as a file upload this lets AddTorrent
// recover the hash of the torrent qBittorrent already holds.
func infoHashFromTorrentFile(data []byte) string {
	start, end, ok := bencodeMemberSpan(data, "info")
	if !ok {
		return ""
	}
	//nolint:gosec // G401: protocol-mandated SHA-1 for the BitTorrent v1 infohash, see the #nosec note
	sum := sha1.Sum(data[start:end]) // #nosec G401 -- the BitTorrent v1 infohash is defined as SHA-1, not a security primitive
	return hex.EncodeToString(sum[:])
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
