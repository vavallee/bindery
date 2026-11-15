//go:build unix

package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// lockedJobFolder builds a download folder Bindery can read but not delete
// from, the way SABnzbd leaves a finished job when its completed-download
// permissions are unset (#3897).
func lockedJobFolder(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	src := filepath.Join(t.TempDir(), "job")
	mustWrite(t, filepath.Join(src, "book.m4b"), "audio")
	mustWrite(t, filepath.Join(src, "book.nfo"), "nfo")
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o755) })
	return src
}

// A finished copy whose source cannot be deleted is an import with a leftover,
// not a failed import. Failing it left the copy in the library and the retry
// built "Title (2)" beside it (#3897).
func TestMoveDownloadDirSourceNotRemovableIsSuccess(t *testing.T) {
	src := lockedJobFolder(t)
	dst := filepath.Join(t.TempDir(), "Author", "Title")
	if err := MoveDownloadDirCtx(context.Background(), src, dst); err != nil {
		t.Fatalf("MoveDownloadDirCtx = %v, want nil", err)
	}
	for _, n := range []string{"book.m4b", "book.nfo"} {
		if _, err := os.Stat(filepath.Join(dst, n)); err != nil {
			t.Errorf("destination missing %s: %v", n, err)
		}
	}
}

func TestMoveDownloadDirMergeSourceNotRemovableIsSuccess(t *testing.T) {
	src := lockedJobFolder(t)
	dst := filepath.Join(t.TempDir(), "Author", "Title")
	if _, err := MoveDownloadDirMergeCtx(context.Background(), src, dst); err != nil {
		t.Fatalf("MoveDownloadDirMergeCtx = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "book.m4b")); err != nil {
		t.Errorf("destination missing book.m4b: %v", err)
	}
}

func TestMoveDownloadFileSourceNotRemovableIsSuccess(t *testing.T) {
	src := lockedJobFolder(t)
	old := moveFileRename
	moveFileRename = func(string, string) error { return errors.New("force copy path") }
	t.Cleanup(func() { moveFileRename = old })
	dst := filepath.Join(t.TempDir(), "Title", "book.m4b")
	if err := MoveDownloadFileCtx(context.Background(), filepath.Join(src, "book.m4b"), dst); err != nil {
		t.Fatalf("MoveDownloadFileCtx = %v, want nil", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination missing: %v", err)
	}
}

// Library-internal moves keep reporting the failure, with the sentinel so a
// caller can tell it from a failed copy.
func TestMoveDirCtxSourceNotRemovableIsDistinguishable(t *testing.T) {
	src := lockedJobFolder(t)
	err := moveDir(context.Background(), src, filepath.Join(t.TempDir(), "x"), false)
	if !errors.Is(err, ErrSourceNotRemoved) {
		t.Fatalf("err = %v, want ErrSourceNotRemoved", err)
	}
}
