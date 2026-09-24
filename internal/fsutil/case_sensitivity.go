// Package fsutil holds small filesystem helpers shared across packages.
package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// IsCaseSensitiveFSForTests reports whether the filesystem holding dir tells
// two names apart by case. It probes for real: it creates a file under a
// temporary directory inside dir and asks for the same name in a different
// case. A stat that succeeds means the filesystem folded the case.
//
// Case sensitivity belongs to the filesystem, not the operating system, so a
// runtime.GOOS check gets it wrong in both directions: macOS can format a
// case-sensitive APFS volume, and Linux can mount a case-insensitive ext4
// directory or an exFAT or SMB share. Tests that depend on a case-divergent
// path must ask the volume they are actually running on, which is why this
// takes dir rather than probing the process working directory.
//
// It fails the test if the probe cannot be carried out, because a test that
// silently assumed the wrong answer would be worse than a hard failure.
func IsCaseSensitiveFSForTests(tb testing.TB, dir string) bool {
	tb.Helper()
	// The random suffix MkdirTemp appends is digits only, so only the probe
	// file's name below varies by case.
	probeDir, err := os.MkdirTemp(dir, "bindery-case-probe")
	if err != nil {
		tb.Fatalf("case-sensitivity probe: create a probe directory under %q: %v", dir, err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(probeDir) })

	f, err := os.Create(filepath.Join(probeDir, "casecheck"))
	if err != nil {
		tb.Fatalf("case-sensitivity probe: create a probe file in %q: %v", probeDir, err)
	}
	_ = f.Close()

	switch _, err := os.Stat(filepath.Join(probeDir, "CASECHECK")); {
	case err == nil:
		return false
	case errors.Is(err, fs.ErrNotExist):
		return true
	default:
		tb.Fatalf("case-sensitivity probe: stat the upper-case name in %q: %v", probeDir, err)
		return false
	}
}
