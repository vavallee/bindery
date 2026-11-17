package models

import (
	"slices"
	"testing"
)

// TestHandedOffSourcesRoundTrip: one path is stored as itself, so a plain
// import_path written by any other writer reads back unchanged, and several
// paths survive as a JSON array, names with brackets and quotes included.
func TestHandedOffSourcesRoundTrip(t *testing.T) {
	cases := map[string]struct {
		in   []string
		want string
	}{
		"none":         {in: nil, want: ""},
		"only blanks":  {in: []string{"", "  "}, want: ""},
		"one":          {in: []string{"/dl/Moby Dick.epub"}, want: "/dl/Moby Dick.epub"},
		"one of blank": {in: []string{" ", "/dl/a.epub"}, want: "/dl/a.epub"},
		"several":      {in: []string{"/dl/CD1/01.mp3", `/dl/[x] "y".mp3`}, want: `["/dl/CD1/01.mp3","/dl/[x] \"y\".mp3"]`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := EncodeHandedOffSources(tc.in)
			if got != tc.want {
				t.Fatalf("encoded = %q, want %q", got, tc.want)
			}
			d := &Download{ImportPath: got}
			back := d.HandedOffSources()
			var kept []string
			for _, p := range tc.in {
				if p != "" && p != "  " && p != " " {
					kept = append(kept, p)
				}
			}
			if !slices.Equal(back, kept) {
				t.Errorf("decoded = %q, want %q", back, kept)
			}
		})
	}
	if got := (&Download{ImportPath: "[not json"}).HandedOffSources(); got != nil {
		t.Errorf("malformed array decoded to %q, want nil", got)
	}
}
