//go:build !windows

package importer

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only and fails when its last component is a
// symlink (ELOOP).
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
