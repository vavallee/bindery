package infohash

import (
	"errors"
	"strings"
	"testing"
)

// sampleInfoDict is a realistic single-file bencoded "info" dictionary, and
// sampleInfoHash is its v1 infohash (SHA-1 of those bytes) as a golden value —
// hardcoded so the test does not itself depend on crypto/sha1.
const (
	sampleInfoDict = "d6:lengthi5e4:name8:test.txt12:piece lengthi16384e6:pieces20:" +
		"\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14e"
	sampleInfoHash = "9ca9aea0e4d50429f039ca828f52ec49283f36bb"
)

func TestFromTorrentFile(t *testing.T) {
	cases := map[string]string{
		"info is the first key":       "d4:info" + sampleInfoDict + "8:announce10:udp://t/ane",
		"info after other keys":       "d8:announce10:udp://t/an13:creation datei1700000000e4:info" + sampleInfoDict + "e",
		"info followed by another":    "d4:info" + sampleInfoDict + "7:comment5:helloe",
		"info after a nested list":    "d5:nodesll4:hosti1eee4:info" + sampleInfoDict + "e",
		"info after a nested subdict": "d4:metad3:key3:vale4:info" + sampleInfoDict + "e",
	}
	for name, torrent := range cases {
		t.Run(name, func(t *testing.T) {
			if got := FromTorrentFile([]byte(torrent)); got != sampleInfoHash {
				t.Fatalf("got %q, want %q", got, sampleInfoHash)
			}
		})
	}

	negatives := map[string]string{
		"empty":             "",
		"not a dictionary":  "i5e",
		"no info key":       "d8:announce10:udp://t/ane",
		"truncated":         "d4:info",
		"bad string length": "d99:infod" + sampleInfoDict,
	}
	for name, in := range negatives {
		t.Run(name, func(t *testing.T) {
			if got := FromTorrentFile([]byte(in)); got != "" {
				t.Fatalf("got %q, want empty string", got)
			}
		})
	}
}

// TestFromTorrentFile_DeeplyNestedIsRefusedNotFatal is the regression guard for
// the unbounded bencode recursion.
//
// The walk runs over bytes an indexer chose — rTorrent hashes every .torrent
// Bindery fetches before submitting it, and qBittorrent's 409 recovery does the
// same. Before the depth cap, "d4:info" + N×"l" + N×"e" + "e" recursed N times;
// at N ≈ 26 million (a ~52 MB file, inside the 50 MiB download cap) the
// goroutine stack overflowed. That is runtime.throw, not a panic: no recover()
// catches it, the process dies, and the grab retries into a crash loop.
//
// The payload here is deliberately tiny — a few levels past the cap is enough
// to prove the guard fires, so the test costs microseconds instead of
// allocating tens of megabytes.
func TestFromTorrentFile_DeeplyNestedIsRefusedNotFatal(t *testing.T) {
	const nesting = maxBencodeDepth + 10

	// d4:junk<nesting × 'l'><nesting × 'e'>4:info<valid info dict>e
	//
	// The junk value must sit BEFORE the info dict: bencodeMemberSpan returns
	// as soon as it finds "info", so a deep value placed after it is never
	// walked and the assertion would pass vacuously. Placing it first forces
	// the walk to skip over the nesting to reach info, which is exactly the
	// recursion being bounded. The info dict that follows is well-formed and
	// yields sampleInfoHash, so a build without the cap returns that hash —
	// the refusal below can only come from the depth check.
	payload := "d4:junk" +
		strings.Repeat("l", nesting) + strings.Repeat("e", nesting) +
		"4:info" + sampleInfoDict + "e"
	if got := FromTorrentFile([]byte(payload)); got != "" {
		t.Fatalf("expected refusal past the %d-level depth cap, got hash %q", maxBencodeDepth, got)
	}

	// Pin the boundary directly on the walker so the guard cannot be silently
	// widened or removed: exactly at the cap must still parse, one past must
	// report the depth sentinel rather than recursing.
	atCap := []byte(strings.Repeat("l", maxBencodeDepth) + strings.Repeat("e", maxBencodeDepth))
	if _, err := bencodeSkipValue(atCap, 0, 0); err != nil {
		t.Fatalf("nesting exactly at maxBencodeDepth should parse, got %v", err)
	}
	overCap := []byte(strings.Repeat("l", maxBencodeDepth+2) + strings.Repeat("e", maxBencodeDepth+2))
	_, err := bencodeSkipValue(overCap, 0, 0)
	if !errors.Is(err, errBencodeTooDeep) {
		t.Fatalf("nesting past maxBencodeDepth should report errBencodeTooDeep, got %v", err)
	}
}

// TestFromTorrentFile_RealisticNestingStillHashes guards the other side of the
// cap: a multi-file torrent's "files" list nests dict → list → dict → list, and
// a depth guard set too tight would refuse every real torrent.
func TestFromTorrentFile_RealisticNestingStillHashes(t *testing.T) {
	const multiFileInfo = "d5:filesld6:lengthi5e4:pathl3:sub8:test.txteee4:name4:book" +
		"12:piece lengthi16384e6:pieces20:" +
		"\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14e"
	got := FromTorrentFile([]byte("d8:announce10:udp://t/an4:info" + multiFileInfo + "e"))
	if got == "" {
		t.Fatal("a normal multi-file torrent must still hash")
	}
	if len(got) != 40 {
		t.Fatalf("got %q, want a 40-char hex hash", got)
	}
}

func TestFromMagnet(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"lower-case hex":  {"magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Book", "abcdef0123456789abcdef0123456789abcdef01"},
		"upper-case hex":  {"magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", "abcdef0123456789abcdef0123456789abcdef01"},
		"urn is mixed":    {"magnet:?xt=URN:BTIH:abcdef0123456789abcdef0123456789abcdef01", "abcdef0123456789abcdef0123456789abcdef01"},
		"topic elsewhere": {"magnet:?dn=Book&tr=udp%3A%2F%2Ft%2Fan&xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", "abcdef0123456789abcdef0123456789abcdef01"},
		// A v1/v2 hybrid magnet carries two xt topics. Indexers emit them in
		// either order, and reading only the first refused every release whose
		// btmh happened to come first — rTorrent then rejected the grab outright
		// as "carries no usable btih infohash".
		"hybrid, btmh first": {"magnet:?xt=urn:btmh:1220caf1e1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d&xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Book", "abcdef0123456789abcdef0123456789abcdef01"},
		"hybrid, btih first": {"magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&xt=urn:btmh:1220caf1e1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d", "abcdef0123456789abcdef0123456789abcdef01"},
		// v2-only genuinely has no v1 hash: still refused, never guessed.
		"v2 only":          {"magnet:?xt=urn:btmh:1220caf1e1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d", ""},
		"empty btih first": {"magnet:?xt=urn:btih:&xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", "abcdef0123456789abcdef0123456789abcdef01"},
		"not a magnet":     {"https://indexer.example/dl?id=1", ""},
		"no xt topic":      {"magnet:?dn=Book", ""},
		"wrong urn":        {"magnet:?xt=urn:sha1:abcdef", ""},
		"empty topic":      {"magnet:?xt=urn:btih:", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := FromMagnet(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNormalize is the guard on the rTorrent path specifically: rTorrent
// addresses every d.* command by exact hash, so a base32 magnet that is not
// converted to hex, or a malformed hash that is passed through anyway, means
// every subsequent poll silently misses the torrent.
func TestNormalize(t *testing.T) {
	// The base32 and hex spellings below are the same 20 bytes.
	const (
		hexForm    = "0102030405060708090a0b0c0d0e0f1011121314"
		base32Form = "AEBAGBAFAYDQQCIKBMGA2DQPCAIREEYU"
	)
	cases := map[string]struct{ in, want string }{
		"hex passes through":          {hexForm, hexForm},
		"hex is lower-cased":          {"0102030405060708090A0B0C0D0E0F1011121314", hexForm},
		"surrounding space trimmed":   {"  " + hexForm + "  ", hexForm},
		"base32 upper decodes to hex": {base32Form, hexForm},
		"base32 lower decodes to hex": {"aebagbafaydqqcikbmga2dqpcaireeyu", hexForm},
		"empty":                       {"", ""},
		"too short":                   {"abcdef", ""},
		"40 chars but not hex":        {"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", ""},
		"32 chars but not base32":     {"11111111111111111111111111111111", ""},
		"v2 style 64 hex":             {"0102030405060708090a0b0c0d0e0f10111213140102030405060708090a0b0c", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
