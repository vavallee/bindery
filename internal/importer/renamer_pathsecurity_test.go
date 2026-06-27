package importer

import "testing"

// Path-traversal / containment regression layer for sanitizePath and
// ensureContained. These pin the security-relevant behaviour: book-derived
// path components (author/title strings seeded from remote metadata) must not
// be able to walk out of the configured library root, and a destination path
// that merely shares a string prefix with the base (e.g. "/lib" vs "/library")
// must not be treated as contained.
//
// NOTE: on main, sanitizePath does NOT strip control/NUL characters, so this
// file deliberately asserts only the character-replacement and segment-
// stripping behaviour that the current implementation actually provides.

func TestSanitizePathTraversal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// A pure ".." segment is dropped entirely: it cannot contribute a
		// traversal component to the rendered path.
		{"pure_dotdot", "..", ""},
		{"pure_dot", ".", ""},
		{"empty", "", ""},
		// A leading-slash absolute injection is neutralised: every "/" is
		// replaced with "-", so no leading separator survives to anchor the
		// path at the filesystem root.
		{"absolute_injection", "/etc/passwd", "-etc-passwd"},
		// Embedded forward and back slashes are both replaced with "-", so a
		// single field cannot introduce additional path segments.
		{"embedded_forward_slash", "a/b", "a-b"},
		{"embedded_back_slash", "a\\b", "a-b"},
		// "a/../b" must not yield a usable traversal: the slashes are replaced
		// with "-" before any segment splitting, so the ".." never sits as an
		// isolated path segment and the whole thing collapses to a flat name.
		{"dotdot_between_segments", "a/../b", "a-..-b"},
		// Matches the existing preview drift guard so the two stay in sync.
		{"mixed_specials", "A: B / C? <D>", "A- B - C D"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizePath(tc.in); got != tc.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEnsureContained(t *testing.T) {
	const base = "/library"

	cases := []struct {
		name    string
		dest    string
		base    string
		want    string
		wantErr bool
	}{
		// A destination genuinely inside the base passes and is returned cleaned.
		{"inside_base", "/library/Author/Book", base, "/library/Author/Book", false},
		// The exact base itself is treated as contained.
		{"exact_base", "/library", base, "/library", false},
		// A ".." escape that resolves above the base must error.
		{"dotdot_escape", "/library/../etc/passwd", base, "", true},
		// Sibling-prefix case: "/lib/evil" string-starts-with "/lib" but is NOT
		// under "/library". A naive HasPrefix(base) check (without the trailing
		// separator) would wrongly pass this; ensureContained must error.
		{"sibling_prefix", "/lib/evil", base, "", true},
		// And the inverse direction: a base of "/lib" must not swallow a
		// "/library" destination either.
		{"sibling_prefix_inverse", "/library/x", "/lib", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ensureContained(tc.dest, tc.base)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ensureContained(%q, %q) = (%q, nil), want error", tc.dest, tc.base, got)
				}
				if got != "" {
					t.Errorf("ensureContained(%q, %q) returned %q on error, want \"\"", tc.dest, tc.base, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureContained(%q, %q) unexpected error: %v", tc.dest, tc.base, err)
			}
			if got != tc.want {
				t.Errorf("ensureContained(%q, %q) = %q, want %q", tc.dest, tc.base, got, tc.want)
			}
		})
	}
}
