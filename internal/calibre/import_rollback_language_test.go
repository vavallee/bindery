package calibre

import "testing"

// TestRestoreLanguage_MatchesAcrossTheBackfill: a run recorded before the
// language backfill (#3086) holds the spelling it wrote ("en"), while the row
// now holds the normalized one ("eng"). They are the same value, so the
// rollback still restores; a language somebody changed after the run is a
// different value and is left alone.
func TestRestoreLanguage_MatchesAcrossTheBackfill(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, current, before, after, want string
		changed                            bool
	}{
		{"run wrote en, backfilled to eng, restores the empty original", "eng", "", "en", "", true},
		{"run replaced fr with en, restores it normalized", "eng", "fr", "en", "fre", true},
		{"run left the language alone", "eng", "en", "en", "eng", false},
		{"edited after the run", "ger", "", "en", "ger", false},
		{"no backfill in between", "eng", "fre", "eng", "fre", true},
	} {
		current, changed := tc.current, false
		restoreLanguage(&current, tc.before, tc.after, &changed)
		if current != tc.want || changed != tc.changed {
			t.Errorf("%s: language = %q (changed %v), want %q (changed %v)", tc.name, current, changed, tc.want, tc.changed)
		}
	}
}
