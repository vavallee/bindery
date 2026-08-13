package infohash

import "testing"

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

func TestFromMagnet(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"lower-case hex":  {"magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Book", "abcdef0123456789abcdef0123456789abcdef01"},
		"upper-case hex":  {"magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", "abcdef0123456789abcdef0123456789abcdef01"},
		"urn is mixed":    {"magnet:?xt=URN:BTIH:abcdef0123456789abcdef0123456789abcdef01", "abcdef0123456789abcdef0123456789abcdef01"},
		"topic elsewhere": {"magnet:?dn=Book&tr=udp%3A%2F%2Ft%2Fan&xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", "abcdef0123456789abcdef0123456789abcdef01"},
		"not a magnet":    {"https://indexer.example/dl?id=1", ""},
		"no xt topic":     {"magnet:?dn=Book", ""},
		"wrong urn":       {"magnet:?xt=urn:sha1:abcdef", ""},
		"empty topic":     {"magnet:?xt=urn:btih:", ""},
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
