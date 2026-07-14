//go:build windows

package config

import "os"

// writabilityIdentity has no useful uid/gid story on Windows — os.Getuid
// returns -1 and FileInfo.Sys carries no POSIX ownership — so the enriched
// "who am I vs who owns it" diagnostics (#1427) are a no-op there.
func writabilityIdentity(_ os.FileInfo) string {
	return ""
}
