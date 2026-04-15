//go:build !windows

package api

import "syscall"

func statFreeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	// stat.Bavail is uint64; cap to avoid int64 overflow on FS sizes > 9 EB.
	const maxInt64 uint64 = 1<<63 - 1
	bsize := uint64(stat.Bsize)
	if bsize > 0 && stat.Bavail > maxInt64/bsize {
		return 1<<63 - 1, nil
	}
	return int64(stat.Bavail * bsize), nil //nolint:gosec // bounded above
}
