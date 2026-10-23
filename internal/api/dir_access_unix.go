//go:build !windows

package api

import (
	"os"
	"syscall"
)

// dirAccessible reports whether path is an existing directory this process can
// read, and also write when write is true. It never creates a file: access(2)
// answers from permissions and mount flags (a read only mount reports EROFS),
// which is what lets the setup-state request path stay free of side effects
// (config.CheckDir writes and removes a probe file in the directory).
func dirAccessible(path string, write bool) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return false
	}
	const rOK, wOK = 4, 2
	mode := uint32(rOK)
	if write {
		mode |= wOK
	}
	return syscall.Access(path, mode) == nil
}
