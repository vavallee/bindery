package importer

import (
	"os"
	"path/filepath"
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

// TestSanitizePath_CapsComponentLength pins the cap as a BYTE budget (#1982).
// It used to assert RuneCountInString(got) == maxPathComponentLen, which is the
// defect itself: filesystems measure NAME_MAX in bytes, so a rune cap let a
// non-ASCII component through at up to 4x the budget. For pure ASCII the byte
// cap is numerically identical to the old rune cap, which is why this case is
// kept unchanged in value — it is the guarantee that existing libraries' paths
// do not move.
func TestSanitizePath_CapsComponentLength(t *testing.T) {
	got := sanitizePath(strings.Repeat("x", 500))
	if n := len(got); n != maxPathComponentBytes {
		t.Errorf("component length = %d bytes, want %d", n, maxPathComponentBytes)
	}
	// ASCII: byte-for-byte identical to the pre-#1982 200-rune behaviour.
	if n := utf8.RuneCountInString(got); n != maxPathComponentBytes {
		t.Errorf("ASCII rune count = %d, want %d (ASCII must not change)", n, maxPathComponentBytes)
	}
}

// TestSanitizePath_CapIsRuneSafe keeps the half of the old test that was always
// right — truncation must not split a character mid-encoding — and replaces the
// half that encoded the bug (rune count == cap) with the byte budget.
func TestSanitizePath_CapIsRuneSafe(t *testing.T) {
	for name, unit := range map[string]string{
		"accented latin": "é", // 2 bytes
		"cyrillic":       "щ", // 2 bytes
		"cjk":            "転", // 3 bytes
		"emoji":          "📚", // 4 bytes
	} {
		t.Run(name, func(t *testing.T) {
			got := sanitizePath(strings.Repeat(unit, 500))
			if !utf8.ValidString(got) {
				t.Errorf("truncation produced invalid UTF-8: %q", got)
			}
			if n := len(got); n > maxPathComponentBytes {
				t.Errorf("component = %d bytes, want <= %d", n, maxPathComponentBytes)
			}
			// Cut on a rune boundary means we lose at most one unit's worth of
			// bytes off the budget — not that we silently return far less.
			if n := len(got); n <= maxPathComponentBytes-len(unit) {
				t.Errorf("component = %d bytes, truncated more than one rune below the %d-byte budget", n, maxPathComponentBytes)
			}
			// Every rune must be whole: the string is a clean repeat of unit.
			if want := strings.Repeat(unit, utf8.RuneCountInString(got)); got != want {
				t.Errorf("truncation split a rune: got %q", got)
			}
		})
	}
}

// TestSanitizePath_ComponentIsCreatableOnDisk is the test the string-length
// assertions could never be: it actually calls os.Create with the sanitised
// name plus a realistic extension. The #1982 failure was ENAMETOOLONG
// (errno 36) from the kernel, which no RuneCountInString check can observe —
// the old tests passed on exactly the inputs that could not be written.
func TestSanitizePath_ComponentIsCreatableOnDisk(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"ascii":          "x",
		"accented latin": "é",
		"cyrillic":       "Достоевский",
		"greek":          "Ω",
		"cjk":            "転",
		"hangul":         "한",
		"emoji":          "📚",
	}
	// The longest extension the importer appends, plus the widest uniqueness
	// suffix UniqueDir can add — the value is only ever part of a real name.
	const suffix = " (999).epub"
	for name, unit := range cases {
		t.Run(name, func(t *testing.T) {
			got := sanitizePath(strings.Repeat(unit, 500))
			f, err := os.Create(filepath.Join(dir, got+suffix)) // #nosec G304 -- test-local temp dir
			if err != nil {
				t.Fatalf("os.Create with sanitised component (%d bytes + %d-byte suffix): %v",
					len(got), len(suffix), err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

// TestSanitizePath_DirectoryComponentIsCreatableOnDisk covers the other half of
// the import: the destination folder, which is a component in its own right.
func TestSanitizePath_DirectoryComponentIsCreatableOnDisk(t *testing.T) {
	root := t.TempDir()
	author := sanitizePath(strings.Repeat("転", 500))
	title := sanitizePath(strings.Repeat("Достоевский", 100))
	dst := filepath.Join(root, author, title)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatalf("MkdirAll(%d-byte author / %d-byte title): %v", len(author), len(title), err)
	}
	f, err := os.Create(filepath.Join(dst, title+".m4b")) // #nosec G304 -- test-local temp dir
	if err != nil {
		t.Fatalf("os.Create inside deep non-ASCII path: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
