package importer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizePath_StripsControlChars(t *testing.T) {
	cases := map[string]string{
		"a\x00b": "ab", // NUL — would make os.* fail EINVAL
		"a\tb":   "ab", // tab
		"a\nb":   "ab", // newline
		"a\x7fc": "ac", // DEL
	}
	for in, want := range cases {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizePath_CapsComponentLength(t *testing.T) {
	got := sanitizePath(strings.Repeat("x", 500))
	if n := utf8.RuneCountInString(got); n != maxPathComponentLen {
		t.Errorf("component length = %d, want %d", n, maxPathComponentLen)
	}
}

func TestSanitizePath_CapIsRuneSafe(t *testing.T) {
	// Multi-byte runes must not be split mid-encoding by the length cap.
	got := sanitizePath(strings.Repeat("é", 500))
	if !utf8.ValidString(got) {
		t.Errorf("truncation produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != maxPathComponentLen {
		t.Errorf("rune count = %d, want %d", n, maxPathComponentLen)
	}
}
