//go:build windows

package api

// hardlinkableReason always reports not-hardlinkable on Windows because
// device-ID comparison via syscall.Stat_t is not available there — mirrors
// importer.sameDevice's platform split, with the explanation the #1427
// storage-health surface expects.
func hardlinkableReason(_, _ string) (bool, string) {
	return false, "hardlink detection is not supported on Windows; imports will copy instead"
}
