//go:build !windows

package api

import (
	"os"
	"path/filepath"
	"syscall"
)

// statExistingDevice stats p, walking up to the nearest existing ancestor
// directory when p itself does not exist yet, so a not-yet-created library
// destination can still be compared against an existing download dir. This
// mirrors importer.statExisting; it is duplicated here (rather than imported)
// to avoid an api -> importer dependency.
func statExistingDevice(p string) (os.FileInfo, error) {
	for {
		fi, err := os.Stat(p)
		if err == nil {
			return fi, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil, err
		}
		p = parent
	}
}

// sameDevice reports whether a and b reside on the same filesystem by
// comparing OS device IDs — i.e. whether a hardlink import from a to b is
// possible. Returns false on any stat error or empty input. This duplicates
// importer.sameDevice intentionally to keep config/api free of an importer
// dependency.
func sameDevice(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := statExistingDevice(b)
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
