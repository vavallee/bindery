package models

import (
	"slices"
	"strings"
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

// TestHandedOffSourcesKeepsNonUTF8Paths: a Linux path is any bytes but NUL, so
// a Latin-1 name from an old archive is not valid UTF-8. encoding/json turns
// such bytes into U+FFFD, which names no real file, so the recorded source
// would stop matching and the folder scan would offer it again.
func TestHandedOffSourcesKeepsNonUTF8Paths(t *testing.T) {
	cases := map[string][]string{
		"single":             {"/dl/a\xffb.epub"},
		"several":            {"/dl/a\xffb.epub", "/dl/c\xe9d.epub"},
		"mixed":              {"/dl/plain.epub", "/dl/a\xffb.epub", "/dl/café.epub", "/dl/�.epub"},
		"truncated sequence": {"/dl/a\xe2\x82.epub", "/dl/b.epub"},
		"hostile text":       {"/dl/a\\b\"c\nd.epub", "/dl/x\xffy.epub", `/dl/{"b64":"AA=="}.epub`, "/dl/[x].epub"},
		"marker as name":     {`{"b64":"/w=="}`, "/dl/z.epub"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			enc := EncodeHandedOffSources(in)
			if !strings.HasPrefix(enc, "/") && !strings.HasPrefix(enc, "[") {
				t.Fatalf("encoded %q starts with neither a slash nor a bracket", enc)
			}
			got := (&Download{ImportPath: enc}).HandedOffSources()
			if !slices.Equal(got, in) {
				t.Fatalf("round trip lost bytes: got %q, want %q (stored %q)", got, in, enc)
			}
		})
	}
}

// TestHandedOffSourcesLegacyValuesDecodeUnchanged: every value a released
// version wrote still reads exactly as before, and valid UTF-8 paths encode
// byte for byte as they did, so a downgrade reads the common case.
func TestHandedOffSourcesLegacyValuesDecodeUnchanged(t *testing.T) {
	read := func(v string) []string { return (&Download{ImportPath: v}).HandedOffSources() }
	if got := read("/dl/Moby Dick.epub"); !slices.Equal(got, []string{"/dl/Moby Dick.epub"}) {
		t.Errorf("legacy plain path = %q", got)
	}
	if got := read("/data/downloads/Some Book"); !slices.Equal(got, []string{"/data/downloads/Some Book"}) {
		t.Errorf("legacy folder = %q", got)
	}
	legacy := `["/dl/CD1/01.mp3","/dl/[x] \"y\".mp3","/dl/café.mp3"]`
	if got := read(legacy); !slices.Equal(got, []string{"/dl/CD1/01.mp3", `/dl/[x] "y".mp3`, "/dl/café.mp3"}) {
		t.Errorf("legacy array = %q", got)
	}
	if got := EncodeHandedOffSources([]string{"/dl/CD1/01.mp3", "/dl/café.mp3", "/dl/a<b>&c.mp3"}); got != "[\"/dl/CD1/01.mp3\",\"/dl/caf\u00e9.mp3\",\"/dl/a\\u003cb\\u003e\\u0026c.mp3\"]" {
		t.Errorf("valid UTF-8 paths no longer encode as before: %q", got)
	}
	if got := read(`["/dl/a.epub", 5]`); got != nil {
		t.Errorf("malformed entry decoded to %q, want nil", got)
	}
}
