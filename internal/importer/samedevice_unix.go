//go:build !windows

package importer

import (
	"os"
	"path/filepath"
	"syscall"
)

// statExisting stats path p, walking up to the nearest existing ancestor
// directory when p itself does not exist yet. This allows comparing the device
// of a not-yet-created destination path against an already-existing source.
func statExisting(p string) (os.FileInfo, error) {
	for {
		fi, err := os.Stat(p)
		if err == nil {
			return fi, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			// Reached filesystem root with no success.
			return nil, err
		}
		p = parent
	}
}

// sameDevice reports whether a and b reside on the same filesystem by
// comparing their OS device IDs. When b does not exist, its nearest existing
// ancestor directory is used so that not-yet-created destination paths are
// handled correctly. Returns false on any stat error.
func sameDevice(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := statExisting(b)
	if err != nil {
		return false
	}
	aStat, ok := ai.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	bStat, ok := bi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return aStat.Dev == bStat.Dev
}
