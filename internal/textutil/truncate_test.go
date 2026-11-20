package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exact", 5, "exact"},
		{"abcdef", 3, "abc"},
		{"xé", 2, "x"},   // é is two bytes; the cut would land inside it
		{"xéy", 3, "xé"}, // cut lands right after é
		{"日本語", 4, "日"},  // three-byte runes
		{"日本語", 2, ""},   // no whole rune fits
		{"anything", 0, ""},
		{"anything", -1, ""},
	}
	for _, c := range cases {
		got := TruncateUTF8(c.in, c.max)
		if got != c.want {
			t.Errorf("TruncateUTF8(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("TruncateUTF8(%q, %d) = %q is not valid UTF-8", c.in, c.max, got)
		}
	}
}

// FuzzTruncateUTF8: for valid input the result is valid UTF-8, a prefix of the
// input, and never longer than the cap.
func FuzzTruncateUTF8(f *testing.F) {
	f.Add("xéééé", 3)
	f.Add("日本語テキスト", 7)
	f.Fuzz(func(t *testing.T, s string, maxBytes int) {
		if !utf8.ValidString(s) {
			return
		}
		got := TruncateUTF8(s, maxBytes)
		if !utf8.ValidString(got) {
			t.Fatalf("TruncateUTF8(%q, %d) = %q is not valid UTF-8", s, maxBytes, got)
		}
		if len(got) > max(maxBytes, 0) && len(got) != len(s) {
			t.Fatalf("TruncateUTF8(%q, %d) = %q is longer than the cap", s, maxBytes, got)
		}
		if s[:len(got)] != got {
			t.Fatalf("TruncateUTF8(%q, %d) = %q is not a prefix", s, maxBytes, got)
		}
	})
}
