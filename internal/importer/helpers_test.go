package importer

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestAudiobookDestDir verifies the audiobook template drops the per-file
// parts (no {ext}) so the caller can move a whole download folder into it.
func TestAudiobookDestDir(t *testing.T) {
	r := NewRenamer("") // both templates default
	releaseDate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	author := &models.Author{Name: "Ursula K. Le Guin"}
	book := &models.Book{Title: "The Dispossessed", ReleaseDate: &releaseDate}

	got, err := r.AudiobookDestDir("/audio", author, book)
	if err != nil {
		t.Fatalf("AudiobookDestDir: %v", err)
	}
	want := filepath.Join("/audio", "Ursula K. Le Guin", "The Dispossessed (2020)")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestAudiobookDestDir_NilAuthor(t *testing.T) {
	// When author is unknown we fall back to "Unknown Author" so imports
	// still land under a predictable folder rather than panicking.
	r := NewRenamer("")
	book := &models.Book{Title: "Mystery"}
	got, err := r.AudiobookDestDir("/audio", nil, book)
	if err != nil {
		t.Fatalf("AudiobookDestDir: %v", err)
	}
	want := filepath.Join("/audio", "Unknown Author", "Mystery ()")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAudiobookDestDir_CustomTemplate(t *testing.T) {
	r := NewRenamerWithAudiobook("", "Audiobooks/{SortAuthor}/{Title}")
	author := &models.Author{Name: "Andy Weir"}
	book := &models.Book{Title: "Project Hail Mary"}
	got, err := r.AudiobookDestDir("/root", author, book)
	if err != nil {
		t.Fatalf("AudiobookDestDir: %v", err)
	}
	want := filepath.Join("/root", "Audiobooks", "Weir, Andy", "Project Hail Mary")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDefaultNamingTemplate(t *testing.T) {
	tmpl := DefaultNamingTemplate()
	if tmpl == "" {
		t.Fatal("expected a non-empty default template")
	}
	// All four substitutions must be present — callers (config UI, defaults
	// loader) rely on the template exposing these placeholders.
	for _, sub := range []string{"{Author}", "{Title}", "{Year}", "{ext}"} {
		found := false
		for i := 0; i+len(sub) <= len(tmpl); i++ {
			if tmpl[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default template missing %s: %q", sub, tmpl)
		}
	}
}

func TestNowYear(t *testing.T) {
	got := NowYear()
	n, err := strconv.Atoi(got)
	if err != nil {
		t.Fatalf("NowYear should return a numeric string, got %q: %v", got, err)
	}
	curr := time.Now().Year()
	if n < curr-1 || n > curr+1 {
		t.Errorf("NowYear returned %d; expected near %d", n, curr)
	}
}

// TestCopyFile exercises the cross-filesystem fallback that MoveFile
// uses when os.Rename fails. Called directly here because rename always
// succeeds within a single temp dir — the slow path would otherwise be
// unreachable in unit tests.
func TestCopyFile(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.epub")
	dst := filepath.Join(tmp, "out", "dst.epub")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Errorf("content mismatch: %q", string(got))
	}
	// Source still exists because copyFile alone doesn't delete it.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source should still exist after copyFile: %v", err)
	}
}

func TestCopyFile_BadSource(t *testing.T) {
	if err := copyFile("/nope/does-not-exist", "/tmp/out"); err == nil {
		t.Error("expected error copying missing source")
	}
}

func TestCopyFile_BadDest(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Dest dir doesn't exist — os.Create fails.
	if err := copyFile(src, filepath.Join(tmp, "missing", "out")); err == nil {
		t.Error("expected error creating dest under missing dir")
	}
}

// TestCopyDir verifies recursive copy preserves the directory layout.
func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.m4b"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.jpg"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dest")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	for _, name := range []string{"a.m4b", "sub/b.jpg"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

func TestCopyDir_MissingSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dest")
	if err := copyDir("/nope/does-not-exist", dst); err == nil {
		t.Error("expected error copying missing source")
	}
}

func TestMoveFile_RenameOverwriteDir(t *testing.T) {
	// Dst already exists as a directory → os.Rename fails; copyFile also
	// fails because os.Create can't create a file over a directory. This
	// drives MoveFile down its error-handling branch.
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.epub")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MoveFile(src, dst); err == nil {
		t.Error("expected error moving file over existing directory")
	}
}

func TestMoveDir_SourceNotDir(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MoveDir(file, filepath.Join(tmp, "dst")); err == nil {
		t.Error("expected error when source is a file, not a dir")
	}
}

func TestMoveDir_MissingSource(t *testing.T) {
	if err := MoveDir("/nope/does-not-exist", filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("expected error for missing source")
	}
}
