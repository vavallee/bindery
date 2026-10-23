//go:build windows

package api

import "os"

// dirAccessible on Windows checks that path is an existing directory whose
// listing can be opened. write is ignored: there is no side effect free
// writability probe, and the read only attribute (which Go reports as a missing
// write bit) is routinely set on writable folders and ignored by Windows for
// directories. A false alarm is worse than a missed one.
func dirAccessible(path string, _ bool) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
