package textutil

import "unicode/utf8"

// TruncateUTF8 returns at most maxBytes bytes of s, cut back to the start of a
// rune so a multi-byte character is never split. Slicing at a fixed byte
// offset leaves an invalid UTF-8 tail, which then travels into error messages
// stored on download rows, history entries and webhook payloads.
func TruncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := max(maxBytes, 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
