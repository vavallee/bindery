package api

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A Linux file name can hold a backslash or a drive prefix. Stored verbatim in
// a zip, a naive Windows extractor reads them as separators and writes outside
// the target folder.
func TestStreamZip_EntryNamesAreSafeForWindowsExtractors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rel", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{`..\..\AppData\evil.bat`, `C:evil.txt`, "book.epub", "sub/ok.mp3", "ctl\x01name.txt"} {
		if err := os.WriteFile(filepath.Join(dir, "rel", filepath.FromSlash(n)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	rec := httptest.NewRecorder()
	streamZip(rec, root, "rel", "rel", nil)
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	for _, want := range []string{`.._.._AppData_evil.bat`, `C_evil.txt`, "book.epub", "sub/ok.mp3", "ctl_name.txt"} {
		if !got[want] {
			t.Errorf("zip is missing entry %q; have %v", want, got)
		}
	}
	for name := range got {
		for _, r := range name {
			if r == '\\' || r < 0x20 {
				t.Errorf("entry %q still holds %q", name, r)
			}
		}
	}
}

func TestZipEntryName(t *testing.T) {
	cases := map[string]string{
		"a/b/c.mp3":     "a/b/c.mp3",
		`a\b.mp3`:       "a_b.mp3",
		"C:x.txt":       "C_x.txt",
		"sub/C:x.txt":   "sub/C:x.txt",
		"a//b":          "a/b",
		"../x":          "x",
		"disc: 1/a.mp3": "disc: 1/a.mp3",
	}
	for in, want := range cases {
		if got := zipEntryName(in); got != want {
			t.Errorf("zipEntryName(%q) = %q, want %q", in, got, want)
		}
	}
}
