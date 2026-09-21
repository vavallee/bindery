//go:build windows

package api

import "os"

// hardlinkableReason always reports not-hardlinkable on Windows because
// device-ID comparison via syscall.Stat_t is not available there — mirrors
// importer.sameDevice's platform split, with the explanation the #1427
// storage-health surface expects.
func hardlinkableReason(_, _ string) (bool, string) {
	return false, "hardlink detection is not supported on Windows; imports will copy instead"
}

// deviceIDOf always reports unknown on Windows, for the same reason as
// deviceID below.
func deviceIDOf(_ os.FileInfo) (uint64, bool) {
	return 0, false
}

// deviceID always reports unknown on Windows because device-ID comparison via
// syscall.Stat_t is not available there. The manual-import scan's
// tracked-file cache (#2480) treats "unknown" as a single shared cache key,
// which is correct here since every tracked path still gets the full
// stat/hardlink check on this platform regardless of the scanned root's
// device — the built index never actually depends on it.
func deviceID(_ string) (uint64, bool) {
	return 0, false
}
