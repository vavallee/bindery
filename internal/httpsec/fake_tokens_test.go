package httpsec

import "strings"

// The redaction tests need values shaped like real passkeys, RSS keys and
// info hashes. They are built here at run time from short repeated pieces,
// so no high entropy literal sits in the source for a secret scanner to
// mistake for a leaked credential. Test strings name them with a {MARKER}
// that fx expands.
var fakeTokens = strings.NewReplacer(
	"{HEX32}", strings.Repeat("0a1b", 8), // 32 hex: a passkey or RSS key
	"{HEX32TAIL}", strings.Repeat("0a1b", 8)[1:], // {HEX32} minus its first character
	"{HEX32U}", strings.ToUpper(strings.Repeat("0a1b", 8)), // upper case hex
	"{HEX32B}", strings.Repeat("ab01", 8), // a second 32 hex key
	"{HEX20}", strings.Repeat("a1b2", 5), // 20 hex: a short RSS key
	"{HEX40}", strings.Repeat("9e8d", 10), // 40 hex: a newznab release GUID
	"{HASH40A}", strings.Repeat("c12f", 10), // 40 hex: an info hash
	"{HASH40B}", strings.Repeat("d34e", 10), // a second info hash
	"{B62_24}", strings.Repeat("Xk9m", 6), // 24 base62: an RSS key
	"{B62_32}", strings.Repeat("aB3d", 8), // 32 base62: a UNIT3D rsskey
	"{B64URL}", "aZ3_kP9-"+strings.Repeat("xQ2m", 4), // 24 base64url, with - and _
)

// fx expands the {MARKER} names in s to their fake token.
func fx(s string) string { return fakeTokens.Replace(s) }
