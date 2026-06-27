//go:build windows

package importer

// sameDevice always returns false on Windows because device-ID comparison
// via syscall.Stat_t is not available. importMode will fall back to "copy".
func sameDevice(_, _ string) bool {
	return false
}

// hardlinkable mirrors sameDevice on Windows: device comparison is unavailable,
// so the auto import mode falls back to "copy" rather than risk a failing
// hardlink.
func hardlinkable(_, _ string) bool {
	return false
}

// crossDeviceErr is always false on Windows. Hardlink mode is never auto-
// selected here (hardlinkable returns false), so the copy fallback path that
// consults this is unreachable in practice; it exists so renamer.go compiles
// without a build tag.
func crossDeviceErr(_ error) bool {
	return false
}
