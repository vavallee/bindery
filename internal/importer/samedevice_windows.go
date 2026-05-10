//go:build windows

package importer

// sameDevice always returns false on Windows because device-ID comparison
// via syscall.Stat_t is not available. importMode will fall back to "copy".
func sameDevice(_, _ string) bool {
	return false
}
