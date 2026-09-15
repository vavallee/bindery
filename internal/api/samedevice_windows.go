//go:build windows

package api

// hardlinkableReason always reports not-hardlinkable on Windows because
// device-ID comparison via syscall.Stat_t is not available there — mirrors
// importer.sameDevice's platform split, with the explanation the #1427
// storage-health surface expects.
func hardlinkableReason(_, _ string) (bool, string) {
	return false, "hardlink detection is not supported on Windows; imports will copy instead"
}

// confirmedCrossDevice always returns false on Windows because device-ID
// comparison via syscall.Stat_t is not available there, so the manual-import
// scan's cross-device stat skip (#2480) can never positively confirm two
// paths are on different filesystems — every tracked path still gets the
// full stat/hardlink check, matching pre-#2480 behavior on this platform.
func confirmedCrossDevice(_, _ string) bool {
	return false
}

// deviceID always reports unknown on Windows, for the same reason as
// confirmedCrossDevice. The manual-import scan's tracked-file cache (#2480)
// treats "unknown" as a single shared cache key, which is correct here since
// confirmedCrossDevice never skips the expensive check on this platform
// anyway — the built index never actually depends on the scanned root.
func deviceID(_ string) (uint64, bool) {
	return 0, false
}
