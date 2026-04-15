//go:build !windows

package api

import (
	"math"
	"syscall"
)

func statFreeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, nil
	}
	// Bavail is uint64, Bsize is int64. Multiply in float64 to avoid overflow;
	// cap to MaxInt64 — no real filesystem has more than 9 EB free.
	free := float64(stat.Bavail) * float64(stat.Bsize)
	if free > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(free), nil
}
