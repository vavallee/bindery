package models

import "testing"

// TestMergeProfileField pins the rule that keeps an enriched photo or bio
// (#3287) from displacing one the primary provider or a user supplied.
func TestMergeProfileField(t *testing.T) {
	enriched := &ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k"}
	other := &ProfileSource{Provider: "wikipedia", URL: "https://en.wikipedia.example/k"}
	for _, tc := range []struct {
		name    string
		cur     string
		curSrc  *ProfileSource
		val     string
		src     *ProfileSource
		want    string
		wantSrc *ProfileSource
		changed bool
	}{
		{"primary value replaces a stored one", "old", nil, "new", nil, "new", nil, true},
		{"primary value replaces an enriched one and drops its credit", "wiki", enriched, "primary", nil, "primary", nil, true},
		{"enriched value fills an empty field", "", nil, "wiki", enriched, "wiki", enriched, true},
		{"enriched value never replaces a primary or user value", "user", nil, "wiki", enriched, "user", nil, false},
		{"enriched value replaces an earlier enriched one", "wiki", enriched, "wiki2", other, "wiki2", other, true},
		{"empty value changes nothing", "kept", nil, "", nil, "kept", nil, false},
		{"same primary value is no change", "same", nil, "same", nil, "same", nil, false},
		{"primary value never replaces the user's own", "mine", &ProfileSource{Provider: ProfileSourceUser}, "primary", nil, "mine", &ProfileSource{Provider: ProfileSourceUser}, false},
		{"enriched value never replaces the user's own", "mine", &ProfileSource{Provider: ProfileSourceUser}, "wiki", enriched, "mine", &ProfileSource{Provider: ProfileSourceUser}, false},
		{"same value with a changed credit takes the new credit", "wiki", enriched, "wiki", other, "wiki", other, true},
		{"same value now from the primary drops the credit", "wiki", enriched, "wiki", nil, "wiki", nil, true},
		{"same value and credit is no change", "wiki", enriched, "wiki", &ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k"}, "wiki", enriched, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cur, curSrc := tc.cur, tc.curSrc
			changed := MergeProfileField(&cur, &curSrc, tc.val, tc.src)
			if cur != tc.want || changed != tc.changed || (curSrc == nil) != (tc.wantSrc == nil) || (curSrc != nil && *curSrc != *tc.wantSrc) {
				t.Errorf("got %q %+v changed=%v; want %q %+v changed=%v", cur, curSrc, changed, tc.want, tc.wantSrc, tc.changed)
			}
		})
	}
}
