//go:build windows

package api

// sameDevice always returns false on Windows because device-ID comparison via
// syscall.Stat_t is not available — mirrors importer.sameDevice so the storage
// health endpoint reports downloads/library as not hardlink-able there.
func sameDevice(_, _ string) bool {
	return false
}
