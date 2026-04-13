package importer

import "testing"

func TestRemapperApply(t *testing.T) {
	tests := []struct {
		name string
		spec string
		in   string
		want string
	}{
		{"empty spec is passthrough", "", "/downloads/complete/x", "/downloads/complete/x"},
		{"single rule rewrites prefix", "/downloads:/media", "/downloads/complete/x", "/media/complete/x"},
		{"exact match", "/downloads:/media", "/downloads", "/media"},
		{"no match returns input", "/downloads:/media", "/srv/other/thing", "/srv/other/thing"},
		{"longest prefix wins regardless of order",
			"/downloads:/media,/downloads/complete:/media/complete",
			"/downloads/complete/x", "/media/complete/x"},
		{"shorter prefix still works after longer fails",
			"/downloads:/media,/downloads/complete:/media/complete",
			"/downloads/incomplete/x", "/media/incomplete/x"},
		{"partial word not treated as prefix match",
			"/downloads:/media",
			"/downloads-old/x", "/downloads-old/x"},
		{"trailing slashes tolerated in spec", "/downloads/ : /media/ ", "/downloads/x", "/media/x"},
		{"multiple comma-separated rules",
			"/sab:/mnt/sab,/downloads:/media",
			"/sab/complete/y", "/mnt/sab/complete/y"},
		{"malformed entry skipped",
			"bogus,/downloads:/media",
			"/downloads/x", "/media/x"},
		{"empty path returns empty", "/downloads:/media", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseRemap(tc.spec)
			if got := r.Apply(tc.in); got != tc.want {
				t.Errorf("Apply(%q) with spec %q = %q, want %q", tc.in, tc.spec, got, tc.want)
			}
		})
	}
}

func TestRemapperNilSafe(t *testing.T) {
	var r *Remapper
	if got := r.Apply("/x"); got != "/x" {
		t.Errorf("nil Remapper should passthrough, got %q", got)
	}
	if !r.Empty() {
		t.Error("nil Remapper should be Empty")
	}
}

func TestParseRemapSkipsJunk(t *testing.T) {
	r := ParseRemap(",,:,foo:,:bar,   ,")
	if !r.Empty() {
		t.Errorf("expected empty remapper, got %+v", r.rules)
	}
}
