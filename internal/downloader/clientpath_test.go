package downloader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/fsutil"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

func TestRemapClientPath_ReportsWhichRuleApplied(t *testing.T) {
	global := pathmap.Parse("/global:/local-global")
	tests := []struct {
		name, clientRemap, raw, wantPath, wantRule string
	}{
		{"client rule", "/remote:/local", "/remote/books", "/local/books", RemapRuleClient},
		{"client rule misses, global applies", "/remote:/local", "/global/books", "/local-global/books", RemapRuleGlobal},
		{"no client rule, global applies", "", "/global/books", "/local-global/books", RemapRuleGlobal},
		{"nothing applies", "/remote:/local", "/elsewhere/books", "/elsewhere/books", RemapRuleNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &models.DownloadClient{PathRemap: tt.clientRemap}
			gotPath, gotRule := RemapClientPath(client, tt.raw, global)
			if gotPath != tt.wantPath || gotRule != tt.wantRule {
				t.Errorf("RemapClientPath(%q) = (%q, %q), want (%q, %q)", tt.raw, gotPath, gotRule, tt.wantPath, tt.wantRule)
			}
		})
	}
	if got, rule := RemapClientPath(nil, "/x", nil); got != "/x" || rule != RemapRuleNone {
		t.Errorf("nil client and remapper = (%q, %q)", got, rule)
	}
}

func TestFindCaseInsensitivePathUnder(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "Books", "Audio"), 0o750); err != nil {
		t.Fatal(err)
	}

	// On a case-insensitive filesystem the literal lowercase path resolves on
	// its own, so there is no divergence to report and the helper correctly
	// answers with nothing. Only a case-sensitive filesystem can produce the
	// case-corrected answer this helper exists for.
	resolved, diverged := FindCaseInsensitivePathUnder(base, filepath.Join(base, "books", "audio"))
	if fsutil.IsCaseSensitiveFSForTests(t, base) {
		if resolved != filepath.Join(base, "Books", "Audio") || diverged != filepath.Join(base, "Books") {
			t.Errorf("got (%q, %q)", resolved, diverged)
		}
	} else if resolved != "" || diverged != "" {
		t.Errorf("on a case-insensitive filesystem %q resolves as written, so the helper should report nothing; got (%q, %q)", filepath.Join(base, "books", "audio"), resolved, diverged)
	}

	// A path outside base is refused without looking.
	if r, _ := FindCaseInsensitivePathUnder(filepath.Join(base, "Books"), filepath.Join(base, "other")); r != "" {
		t.Errorf("path outside base resolved to %q", r)
	}

	// A symlink under base is not followed out of it.
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "Secret"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "Link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if r, _ := FindCaseInsensitivePathUnder(base, filepath.Join(base, "link", "secret")); r != "" {
		t.Errorf("followed a case-matched symlink out of base to %q", r)
	}
	if r, _ := FindCaseInsensitivePathUnder(base, filepath.Join(base, "Link", "secret")); r != "" {
		t.Errorf("followed an exact-named symlink out of base to %q", r)
	}
}

func TestClientPathsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/downloads", "/downloads", true},
		{"/downloads/books", "/downloads", true},
		{"/downloads", "/downloads/incomplete/", true},
		{"/data", "/downloads", false},
		{"/down", "/downloads", false},
		{"/", "/downloads", true},
		{`D:\Torrents\books`, `d:/torrents`, true},
		{`\\nas\share\books`, `\\NAS\share`, true},
		{`D:\Torrents`, `E:\Torrents`, false},
	}
	for _, tc := range tests {
		if got := clientPathsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("clientPathsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestNoteAgainstDefault: the warning fires only for the mixed setup, a
// client whose default sits on Bindery's own folder while the global remap
// sends it somewhere else, and stays quiet on correct setups (#2665).
func TestNoteAgainstDefault(t *testing.T) {
	tests := []struct {
		name string
		sent sentSavePath
		def  string
		warn bool
	}{
		{"mixed setup: default is Bindery's folder", sentSavePath{path: "/data", local: "/downloads", viaGlobal: true}, "/downloads", true},
		{"mixed setup: default inside Bindery's folder", sentSavePath{path: "/data/books", local: "/downloads/books", viaGlobal: true}, "/downloads/books/complete", true},
		{"mixed setup: default above Bindery's folder", sentSavePath{path: "/data/books", local: "/downloads/books", viaGlobal: true}, "/downloads", true},
		{"default on the sent folder", sentSavePath{path: "/data/books", local: "/downloads/books", viaGlobal: true}, "/data", false},
		{"dedicated folder next to the client default", sentSavePath{path: "/downloads/books", local: "/media/books", viaGlobal: true}, "/downloads/complete", false},
		{"rTorrent split incomplete and complete", sentSavePath{path: "/seedbox/complete/books", local: "/media/books", viaGlobal: true}, "/seedbox/incomplete", false},
		{"stock rTorrent relative default", sentSavePath{path: "/data", local: "/downloads", viaGlobal: true}, "./", false},
		{"tilde default", sentSavePath{path: "/data", local: "/downloads", viaGlobal: true}, "~/download", false},
		{"no default reported", sentSavePath{path: "/data", local: "/downloads", viaGlobal: true}, "", false},
		{"folder from the client's own remap", sentSavePath{path: "/data", local: "/downloads", viaGlobal: false}, "/downloads", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var info ClientPathInfo
			tc.sent.noteAgainstDefault(&info, "rTorrent", tc.def)
			if got := info.Note != ""; got != tc.warn {
				t.Fatalf("warned = %v, want %v (note %q)", got, tc.warn, info.Note)
			}
			if tc.warn && (!strings.Contains(info.NoteFix, tc.sent.local+":"+tc.sent.local) || !strings.Contains(info.Note, "ignore this")) {
				t.Errorf("note = %q, fix = %q", info.Note, info.NoteFix)
			}
		})
	}
}
