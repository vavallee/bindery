//go:build windows

package importer

import (
	"fmt"
	"os"
)

// noFollowFlag is zero: Windows has no O_NOFOLLOW.
const noFollowFlag = 0

// refuseLinkSource Lstats the source and refuses a link; the caller's check of
// the open handle still requires a regular file.
func refuseLinkSource(path string) error {
	li, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source is a symlink: %s", path)
	}
	return nil
}
