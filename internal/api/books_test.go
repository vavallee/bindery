package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveBookPath_File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeBookPath(p); err != nil {
		t.Fatalf("removeBookPath: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err=%v", err)
	}
}

func TestRemoveBookPath_Dir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Author", "Title (2020)")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "part1.mp3"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "part2.mp3"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeBookPath(sub); err != nil {
		t.Fatalf("removeBookPath: %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatalf("expected dir removed, stat err=%v", err)
	}
}

func TestRemoveBookPath_Missing(t *testing.T) {
	// Missing paths should be treated as already-deleted, not an error.
	// The net state the caller wants (path absent) is already true.
	if err := removeBookPath(filepath.Join(t.TempDir(), "nope.epub")); err != nil {
		t.Fatalf("missing path should return nil, got %v", err)
	}
}
