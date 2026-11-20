//go:build !windows

package importer

import "syscall"

// noFollowFlag makes an open fail (ELOOP) when the last path component is a
// symlink.
const noFollowFlag = syscall.O_NOFOLLOW

// refuseLinkSource is a no-op where O_NOFOLLOW does the work.
func refuseLinkSource(string) error { return nil }
