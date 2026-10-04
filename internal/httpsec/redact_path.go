package httpsec

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Private trackers and some indexers put the passkey, RSS key or a per user
// download token in the URL path rather than the query, where no parameter
// name marks it:
//
//	/download/123/0a1b2c3d4e5f60718293a4b5c6d7e8f9/file.torrent
//	/rss/download/12345/<rss key>
//	/torrent/download/12345.<32 character rss key>   (UNIT3D)
//	/tor/download.php/<download token>
//
// redactURLPath finds those by shape. A path is cut into runs of the
// characters a key is made of ([A-Za-z0-9_-], compared after percent
// decoding), so dots, slashes, '=', ';', spaces and anything outside ASCII
// separate runs. A run is a secret when it is
//
//   - hex of 16 or more characters with at least one digit and one letter (a
//     passkey, RSS key or info hash), or
//   - 20 or more characters, where some piece between '-' and '_' switches
//     between letters and digits at least minTokenAlternations (4) times (a
//     base62 or base64url token), or
//   - the value of a name=value pair whose name is on the secret parameter
//     list (/rss/passkey=<value>/, ;torrent_pass=<value>), whatever its shape.
//
// The alternation count is what separates a random token from a release name.
// Slugs switch once or twice per piece (Stormlight4, Retail2010, 128kbps,
// HarryPotter1PhilosophersStone, wayofkings0000sand_x1y2 at most 3), while a
// random base62 token switches about every third character. Measured on
// random tokens, the share that reaches 4 alternations in one piece:
//
//	length            20     24     32
//	base62           77%    87%    96%
//	base64url        66%    75%    88%
//	base36 (a-z0-9)  96%    99%   100%
//
// So a short token with few digits can slip through; most path passkeys are
// hex, which the first rule catches whatever its digits. Episode style tags
// (S01E02E03E04, Vol1Ch2Pt3Book4) do reach 4 and are redacted in a long run.
//
// The segment straight after /details/ or /getnzb/ is a newznab release GUID
// (40 hex characters) that the indexer's detail page and NZB link need, so
// only the name=value rule applies there. Numeric ids, words, slugs, release
// and file names and percent encoded Unicode never match. A UUID usually
// does, like any other random token, and so does an info hash: a download URL
// or GUID built on one shows a placeholder, which costs readability only,
// since grabs take the raw URL from the search result registry. Detail page
// links are not run through this rule at all (see StripDetailURLSecrets),
// because people click them and their ids are often hex.
//
// Not covered: keys shorter than these lengths, keys made only of letters or
// only of digits, and anything in a URL fragment.
//
// A secret run is replaced by REDACTED-<12 hex characters>, a keyed hash of
// the run. The hash key is random per process, so it reveals nothing about
// the secret, but two different secrets get different placeholders. That
// matters: the search result registry and the web UI tell releases apart by
// their redacted GUID, and a torznab GUID is often a URL whose only varying
// part is an info hash. A fixed placeholder would merge them, and a grab
// would fetch whichever release was recorded last. A run that already starts
// with REDACTED is left as is, so redacting twice is the same as once.
func redactURLPath(path string) (string, bool) {
	if path == "" {
		return path, false
	}
	segs := strings.Split(path, "/")
	changed := false
	prev := ""
	for i, seg := range segs {
		exempt := strings.EqualFold(prev, "details") || strings.EqualFold(prev, "getnzb")
		if out, ok := redactPathSegment(seg, exempt); ok {
			segs[i] = out
			changed = true
		}
		prev = decodeLoose(seg)
	}
	if !changed {
		return path, false
	}
	return strings.Join(segs, "/"), true
}

// pathRun is one run of key characters inside a path segment: its byte range
// in the segment as written and its percent decoded text.
type pathRun struct {
	start, end int
	text       string
}

// redactPathSegment applies the run rules to one path segment, as written
// (percent escapes intact), and reports whether anything was replaced.
func redactPathSegment(seg string, exempt bool) (string, bool) {
	runs := pathRuns(seg)
	if len(runs) == 0 {
		return seg, false
	}
	var b strings.Builder
	last := 0
	changed := false
	for i, r := range runs {
		secret := false
		switch {
		case strings.HasPrefix(r.text, "REDACTED"):
		case i > 0 && seg[runs[i-1].end:r.start] == "=" && IsSecretParam(runs[i-1].text):
			secret = true
		case !exempt && looksLikePathSecret(r.text):
			secret = true
		}
		if !secret {
			continue
		}
		b.WriteString(seg[last:r.start])
		b.WriteString(pathPlaceholder(r.text))
		last = r.end
		changed = true
	}
	if !changed {
		return seg, false
	}
	b.WriteString(seg[last:])
	return b.String(), true
}

// pathRuns splits seg into maximal runs of key characters. A percent escape
// counts as the character it encodes, so %30 joins a run and %20 or an
// escaped UTF-8 byte ends one.
func pathRuns(seg string) []pathRun {
	var runs []pathRun
	var text strings.Builder
	start := -1
	flush := func(end int) {
		if start >= 0 {
			runs = append(runs, pathRun{start: start, end: end, text: text.String()})
			start = -1
			text.Reset()
		}
	}
	for i := 0; i < len(seg); {
		c, width := seg[i], 1
		if c == '%' && i+2 < len(seg) && isHex(seg[i+1]) && isHex(seg[i+2]) {
			c, width = unhex(seg[i+1])<<4|unhex(seg[i+2]), 3
		}
		if isKeyChar(c) {
			if start < 0 {
				start = i
			}
			text.WriteByte(c)
		} else {
			flush(i)
		}
		i += width
	}
	flush(len(seg))
	return runs
}

// looksLikePathSecret is the shape test from redactURLPath's comment.
func looksLikePathSecret(s string) bool {
	if !hasLetterAndDigit(s) {
		return false
	}
	if len(s) >= 16 && isAllHex(s) {
		return true
	}
	if len(s) < 20 {
		return false
	}
	for _, piece := range strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' }) {
		if letterDigitAlternations(piece) >= minTokenAlternations {
			return true
		}
	}
	return false
}

// minTokenAlternations is how many times one piece of a run must switch
// between letters and digits to count as a random token; see redactURLPath.
const minTokenAlternations = 4

// letterDigitAlternations counts the switches between a letter and a digit
// in s. Case changes and other characters do not count.
func letterDigitAlternations(s string) int {
	n := 0
	var prev byte // 0 none yet, 'l' letter, 'd' digit
	for i := 0; i < len(s); i++ {
		var k byte
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			k = 'd'
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			k = 'l'
		default:
			continue
		}
		if prev != 0 && k != prev {
			n++
		}
		prev = k
	}
	return n
}

// pathFingerprintKey keys the placeholder hash. It is random per process: the
// placeholders only have to agree within one run of the server, which is as
// long as the search result registry lives.
var pathFingerprintKey = func() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}()

// pathPlaceholder is what a secret path run is replaced with.
func pathPlaceholder(secret string) string {
	m := hmac.New(sha256.New, pathFingerprintKey)
	m.Write([]byte(secret))
	return "REDACTED-" + hex.EncodeToString(m.Sum(nil)[:6])
}

// urlPathInTextRE finds the path of each absolute URL in free text. The path
// ends where RedactSecrets' other patterns end a URL: at the query, fragment,
// whitespace, a quote or an angle bracket.
var urlPathInTextRE = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^/\s"'<>?#]+(/[^?#\s"'<>]*)`)

// redactURLPathsInText applies redactURLPath to every URL path in s.
func redactURLPathsInText(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	locs := urlPathInTextRE.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		ps, pe := loc[2], loc[3]
		out, ok := redactURLPath(s[ps:pe])
		if !ok {
			continue
		}
		b.WriteString(s[last:ps])
		b.WriteString(out)
		last = pe
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// decodeLoose percent decodes s, leaving any malformed escape as written.
func decodeLoose(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func isAllHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

func hasLetterAndDigit(s string) bool {
	letter, digit := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			letter = true
		}
	}
	return letter && digit
}
