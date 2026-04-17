package calibre

import "testing"

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"calibredb", ModeCalibredb},
		{"CALIBREDB", ModeCalibredb},
		{"  calibredb  ", ModeCalibredb},
		// drop_folder was removed in v0.17.0; legacy values must fall through to off.
		{"drop_folder", ModeOff},
		{"Drop_Folder", ModeOff},
		{"off", ModeOff},
		{"", ModeOff},
		{"something-else", ModeOff},
		// Legacy boolean values from v0.8.0 settings must NOT silently map —
		// the upgrade migration rewrites them to explicit mode strings, and
		// if one still leaks through we want it to be recognised as "off"
		// so the importer skips the Calibre call rather than defaulting to
		// calibredb on a setup that may no longer have it installed.
		{"true", ModeOff},
		{"false", ModeOff},
	}
	for _, tc := range cases {
		if got := ParseMode(tc.in); got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMode_Valid(t *testing.T) {
	for _, m := range []Mode{ModeOff, ModeCalibredb} {
		if !m.Valid() {
			t.Errorf("%q should be Valid", m)
		}
	}
	for _, m := range []Mode{"", "nope", "true", "CALIBREDB", "drop_folder"} {
		if m.Valid() {
			t.Errorf("%q should not be Valid (canonical casing only)", m)
		}
	}
}

func TestMode_String(t *testing.T) {
	if got := ModeCalibredb.String(); got != "calibredb" {
		t.Errorf("String() = %q, want %q", got, "calibredb")
	}
}
