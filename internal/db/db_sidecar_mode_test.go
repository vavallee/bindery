//go:build unix

package db

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// SQLite creates -wal and -shm with the main file's mode, so a database created
// under a permissive umask must still leave all three files private.
func TestOpen_SidecarFilesArePrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	p := filepath.Join(t.TempDir(), "sub", "bindery.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS zz(x); INSERT INTO zz VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p, p + "-wal", p + "-shm"} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %v, want no group/other access", filepath.Base(f), fi.Mode().Perm())
		}
	}
}
