//go:build !windows

package importer

import (
	"os"
	"syscall"
)

// sameDevice reports whether a and b reside on the same filesystem by
// comparing their OS device IDs. Returns false on any stat error.
func sameDevice(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
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
