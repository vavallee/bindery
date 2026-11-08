package textutil

import "testing"

func TestStripOpenLibraryDuplicateMarker(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"merge queue note", "Light Bringer (Duplicate of OL31211W)", "Light Bringer"},
		{"edition key", "Golden Son (Duplicate of OL26431102M)", "Golden Son"},
		{"lower case and spacing", "Morning Star  ( duplicate of ol123w )", "Morning Star"},
		{"works path", "Iron Gold (Duplicate of /works/OL17336021W)", "Iron Gold"},
		{"trailing space", "Dark Age (Duplicate of OL5W)  ", "Dark Age"},
		{"plain title", "Red Rising", "Red Rising"},
		{"other parenthetical kept", "Red Rising (Red Rising Saga #1)", "Red Rising (Red Rising Saga #1)"},
		{"not at the end", "Notes (Duplicate of OL1W) Revisited", "Notes (Duplicate of OL1W) Revisited"},
		{"not an OL key", "Copies (Duplicate of the original)", "Copies (Duplicate of the original)"},
		{"marker only keeps the title", "(Duplicate of OL1W)", "(Duplicate of OL1W)"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripOpenLibraryDuplicateMarker(tt.in); got != tt.want {
				t.Errorf("StripOpenLibraryDuplicateMarker(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
