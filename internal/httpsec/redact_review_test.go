package httpsec

import (
	"strings"
	"testing"
)

// IPTorrents style links separate query parameters with ';' and call the
// passkey tp.
func TestStripURLSecrets_SemicolonParamsAndTP(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://iptorrents.example/download.php/123/Book.torrent?u=1;tp={HEX32}",
			"https://iptorrents.example/download.php/123/Book.torrent?u=1"},
		{"https://tracker.example/dl?id=1;passkey=SECRETPK;cat=2", "https://tracker.example/dl?id=1;cat=2"},
		{"https://tracker.example/dl?tp=SECRETPK;id=1&cat=2", "https://tracker.example/dl?id=1&cat=2"},
		{"https://tracker.example/dl?id=1&tp=SECRETPK", "https://tracker.example/dl?id=1"},
	}
	for _, tc := range cases {
		in := fx(tc.in)
		if got := StripURLSecrets(in); got != tc.want {
			t.Errorf("StripURLSecrets(%q) = %q, want %q", in, got, tc.want)
		}
	}
	in := `Get "https://iptorrents.example/download.php/123/Book.torrent?u=1;tp=SECRETPK": EOF`
	if got := RedactSecrets(in); strings.Contains(got, "SECRETPK") || !strings.Contains(got, "?u=1;tp=") {
		t.Errorf("RedactSecrets(%q) = %q", in, got)
	}
}

// A displayed magnet drops its source URLs (xs=, as=) like its trackers: any
// of them can be a private tracker link with the passkey in it.
func TestStripURLSecrets_MagnetSources(t *testing.T) {
	in := fx("magnet:?xt=urn:btih:{HASH40A}&dn=Dune" +
		"&xs=https%3A%2F%2Ftracker.example%2Fdl%2F1.torrent%3Fpasskey%3DSECRETPK" +
		"&as=https%3A%2F%2Fmirror.example%2FSECRETPK2%2Fdune.epub" +
		"&tr=https%3A%2F%2Ftracker.example%2Fannounce%3Fpasskey%3DSECRETPK")
	got := StripURLSecrets(in)
	if strings.Contains(got, "SECRETPK") || strings.Contains(got, "xs=") || strings.Contains(got, "as=") {
		t.Fatalf("StripURLSecrets kept a magnet source: %q", got)
	}
	if !strings.Contains(got, fx("xt=urn:btih:{HASH40A}")) || !strings.Contains(got, "dn=Dune") {
		t.Fatalf("StripURLSecrets removed more than the sources: %q", got)
	}
}

// Magnet tracker and source URLs are percent encoded inside the magnet, so
// the log redactor decodes them before looking for a passkey.
func TestRedactSecrets_EncodedMagnetTrackers(t *testing.T) {
	for _, in := range []string{
		"grab magnet:?xt=urn:btih:{HASH40A}&dn=Dune&tr=https%3A%2F%2Ftracker.example%2Fannounce.php%3Fpasskey%3DSECRETPK failed",
		"grab magnet:?xt=urn:btih:{HASH40A}&tr=https%3A%2F%2Ftracker.example%2F{HEX32}%2Fannounce failed",
		"grab magnet:?xt=urn:btih:{HASH40A}&xs=https%3A%2F%2Fbob%3ASECRETPK%40tracker.example%2Fdl failed",
		// A private tracker listed after a public one.
		"grab magnet:?xt=urn:btih:{HASH40A}&tr=udp%3A%2F%2Fpublic.example%3A1337%2Fannounce&tr=https%3A%2F%2Ftracker.example%2Fannounce.php%3Fpasskey%3DSECRETPK failed",
	} {
		in = fx(in)
		got := RedactSecrets(in)
		if strings.Contains(got, "SECRETPK") || strings.Contains(got, fx("{HEX32}")) {
			t.Errorf("RedactSecrets(%q) = %q, encoded tracker secret survived", in, got)
		}
		if !strings.HasPrefix(got, fx("grab magnet:?xt=urn:btih:{HASH40A}")) || !strings.HasSuffix(got, " failed") {
			t.Errorf("RedactSecrets(%q) = %q, damaged the rest", in, got)
		}
	}
	// A public tracker URL with nothing secret in it is left as written.
	in := fx("magnet:?xt=urn:btih:{HASH40A}&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce")
	if got := RedactSecrets(in); got != in {
		t.Errorf("RedactSecrets(%q) = %q, want it unchanged", in, got)
	}
}
