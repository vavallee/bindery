//go:build windows

package importer

import (
	"fmt"
	"os"
)

// openNoFollow has no O_NOFOLLOW on Windows, so it Lstats first and refuses a
// link; the caller's Stat of the open handle still requires a regular file.
func openNoFollow(path string) (*os.File, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source is a symlink: %s", path)
	}
	return os.Open(path)
}
