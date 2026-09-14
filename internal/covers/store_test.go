package covers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jpegHeader is enough of a JPEG for http.DetectContentType to call it one.
var jpegHeader = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}

func writeTemp(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStore_PutAndResolve(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "covers"))
	src := writeTemp(t, "cover.jpg", jpegHeader)

	ref, err := s.Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !IsRef(ref) || !strings.HasSuffix(ref, ".jpg") {
		t.Fatalf("ref = %q, want %s<hex>.jpg", ref, Scheme)
	}
	path, ct, ok := s.Resolve(ref)
	if !ok {
		t.Fatalf("Resolve(%q) not ok", ref)
	}
	if ct != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg", ct)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(jpegHeader) {
		t.Errorf("stored bytes differ: %v", err)
	}

	// Same bytes, same reference, no second file.
	again, err := s.Put(writeTemp(t, "other.jpg", jpegHeader))
	if err != nil || again != ref {
		t.Errorf("second Put = %q, %v; want %q", again, err, ref)
	}
	entries, _ := os.ReadDir(s.Dir())
	if len(entries) != 1 {
		t.Errorf("store holds %d files, want 1", len(entries))
	}
}

func TestStore_PutRefusesNonImage(t *testing.T) {
	s := NewStore(t.TempDir())
	src := writeTemp(t, "cover.jpg", []byte("<!doctype html><html></html>"))
	if _, err := s.Put(src); !errors.Is(err, ErrNotImage) {
		t.Errorf("Put(html) err = %v, want ErrNotImage", err)
	}
	if _, err := s.Put(filepath.Join(t.TempDir(), "missing.jpg")); err == nil {
		t.Error("Put(missing) should fail")
	}
}

func TestStore_ResolveRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "covers"))
	// A real file outside the store that a tampered reference might aim at.
	outside := filepath.Join(dir, "secret.jpg")
	if err := os.WriteFile(outside, jpegHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"",
		"https://example.com/a.jpg",
		"/etc/passwd",
		Scheme + "../secret.jpg",
		Scheme + "../../etc/passwd",
		Scheme + "/etc/passwd",
		Scheme + strings.Repeat("a", 64) + ".jpg/../../secret.jpg",
		Scheme + strings.Repeat("a", 64) + ".html",
		Scheme + strings.Repeat("A", 64) + ".jpg",
		Scheme + strings.Repeat("a", 63) + ".jpg",
		Scheme + strings.Repeat("a", 64) + ".jpg", // well formed but absent
	}
	for _, ref := range bad {
		if _, _, ok := s.Resolve(ref); ok {
			t.Errorf("Resolve(%q) ok, want refused", ref)
		}
	}
	if _, _, ok := (*Store)(nil).Resolve(Scheme + strings.Repeat("a", 64) + ".jpg"); ok {
		t.Error("nil store resolved a reference")
	}
}
