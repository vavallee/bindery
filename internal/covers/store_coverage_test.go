package covers

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	pngHeader  = []byte("\x89PNG\x0D\x0A\x1A\x0A\x00\x00\x00\x0DIHDR")
	gifHeader  = []byte("GIF89a\x01\x00\x01\x00")
	webpHeader = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
)

func TestStore_NilAndUnconfigured(t *testing.T) {
	var nilStore *Store
	if got := nilStore.Dir(); got != "" {
		t.Errorf("nil Dir() = %q, want empty", got)
	}
	src := writeTemp(t, "cover.jpg", jpegHeader)
	if _, err := nilStore.Put(src); err == nil {
		t.Error("nil store Put should fail")
	}
	empty := NewStore("")
	if _, err := empty.Put(src); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unconfigured Put err = %v, want not configured", err)
	}
	if _, _, ok := empty.Resolve(Scheme + strings.Repeat("a", 64) + ".jpg"); ok {
		t.Error("unconfigured store resolved a reference")
	}
}

func TestIsRef(t *testing.T) {
	cases := map[string]bool{
		Scheme + "abc.jpg":         true,
		"  " + Scheme + "abc.jpg ": true,
		"https://x/y.jpg":          false,
		"":                         false,
		"/covers/abc.jpg":          false,
	}
	for in, want := range cases {
		if got := IsRef(in); got != want {
			t.Errorf("IsRef(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStore_PutEachFormat(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "covers"))
	cases := []struct {
		name string
		body []byte
		ext  string
		ct   string
	}{
		{"cover.png", pngHeader, ".png", "image/png"},
		{"cover.gif", gifHeader, ".gif", "image/gif"},
		{"cover.webp", webpHeader, ".webp", "image/webp"},
		// The extension on disk does not matter: a PNG named .jpg is stored as .png.
		{"cover.jpg", append(append([]byte{}, pngHeader...), 'x'), ".png", "image/png"},
	}
	for _, tc := range cases {
		ref, err := s.Put(writeTemp(t, tc.name, tc.body))
		if err != nil {
			t.Fatalf("Put(%s): %v", tc.name, err)
		}
		if !strings.HasSuffix(ref, tc.ext) {
			t.Errorf("Put(%s) ref = %q, want suffix %s", tc.name, ref, tc.ext)
		}
		path, ct, ok := s.Resolve(ref)
		if !ok || ct != tc.ct {
			t.Fatalf("Resolve(%q) = %q, %q, %v; want ct %s", ref, path, ct, ok, tc.ct)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, tc.body) {
			t.Errorf("stored bytes for %s differ (err %v)", tc.name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != fileMode {
			t.Errorf("stored file mode = %o, want %o", info.Mode().Perm(), fileMode)
		}
	}
	// No temp files are left behind after successful writes.
	entries, _ := os.ReadDir(s.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cover-") {
			t.Errorf("temp file %q left in store", e.Name())
		}
	}
}

func TestStore_PutTooLarge(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "covers"))
	body := make([]byte, MaxBytes+1)
	copy(body, jpegHeader)
	if _, err := s.Put(writeTemp(t, "huge.jpg", body)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put(oversize) err = %v, want ErrTooLarge", err)
	}
	if _, err := os.Stat(s.Dir()); !os.IsNotExist(err) {
		t.Errorf("oversize Put created the store dir (stat err %v)", err)
	}

	// Exactly MaxBytes is accepted.
	exact := body[:MaxBytes]
	if _, err := s.Put(writeTemp(t, "exact.jpg", exact)); err != nil {
		t.Errorf("Put(MaxBytes) err = %v, want nil", err)
	}
}

func TestStore_PutSourceIsDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "covers"))
	if _, err := s.Put(t.TempDir()); err == nil {
		t.Error("Put(directory) should fail reading it")
	}
}

func TestStore_PutRewritesTruncatedFile(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "covers"))
	src := writeTemp(t, "cover.jpg", jpegHeader)
	ref, err := s.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := s.Resolve(ref)
	// Simulate a damaged file under the digest name: wrong size forces a rewrite.
	if err := os.WriteFile(path, []byte{0xFF}, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := s.Put(src)
	if err != nil || again != ref {
		t.Fatalf("re-Put = %q, %v; want %q", again, err, ref)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, jpegHeader) {
		t.Errorf("damaged file not rewritten: %v", got)
	}
}

func TestStore_PutDirIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "covers")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(blocker)
	if _, err := s.Put(writeTemp(t, "cover.jpg", jpegHeader)); err == nil {
		t.Error("Put into a store whose dir is a regular file should fail")
	}
}

func TestStore_PutReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "covers")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s := NewStore(dir)
	if _, err := s.Put(writeTemp(t, "cover.jpg", jpegHeader)); err == nil {
		t.Error("Put into a read-only store should fail")
	}
}

func TestStore_ResolveRefusesDirectory(t *testing.T) {
	s := NewStore(t.TempDir())
	name := strings.Repeat("c", 64) + ".png"
	if err := os.Mkdir(filepath.Join(s.Dir(), name), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Resolve(Scheme + name); ok {
		t.Error("Resolve served a directory")
	}
}

func TestStore_PutRenameOntoDirectoryFails(t *testing.T) {
	s := NewStore(t.TempDir())
	src := writeTemp(t, "cover.jpg", jpegHeader)
	ref, err := s.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(s.Dir(), strings.TrimPrefix(ref, Scheme))
	// Replace the stored file with a non-empty directory under the same
	// digest name: the size check cannot short circuit and the rename fails.
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(src); err == nil {
		t.Fatal("Put should fail when the destination is a directory")
	}
	entries, _ := os.ReadDir(s.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cover-") {
			t.Errorf("failed Put left temp file %q", e.Name())
		}
	}
}

func TestExtForAndContentTypeFor(t *testing.T) {
	ext := map[string]string{
		"image/jpeg":               ".jpg",
		"IMAGE/JPG":                ".jpg",
		" image/png ; charset=x":   ".png",
		"image/webp":               ".webp",
		"image/gif":                ".gif",
		"text/html; charset=utf-8": "",
		"image/svg+xml":            "",
		"":                         "",
	}
	for in, want := range ext {
		if got := extFor(in); got != want {
			t.Errorf("extFor(%q) = %q, want %q", in, got, want)
		}
	}
	ct := map[string]string{
		".jpg":  "image/jpeg",
		".png":  "image/png",
		".webp": "image/webp",
		".gif":  "image/gif",
		".svg":  "application/octet-stream",
		"":      "application/octet-stream",
	}
	for in, want := range ct {
		if got := contentTypeFor(in); got != want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", in, got, want)
		}
	}
}
