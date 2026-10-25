package importer

import (
	"path/filepath"
	"runtime"
	"strings"
)

// onWindows is the default path flavour of the running process. It is read
// once, where a Scanner or Renamer is built, and never again, so tests can
// inject either flavour instead of depending on the OS the suite runs on.
var onWindows = runtime.GOOS == "windows"

// trimWindowsName removes the trailing dots and spaces Windows silently strips
// from a file or folder name (#3468). Without this a book titled "H.R.H."
// renders a folder "H.R.H." that the OS creates as "H.R.H": the stored path
// still opens, but a walk of the library reports "H.R.H" and the scan sees a
// tracked file as new. Rendering the name the OS will keep makes the stored
// path and the on-disk path the same string.
func trimWindowsName(name string) string {
	return strings.TrimRight(name, ". ")
}

// pathKey is the form two paths are compared in when asking whether a walked
// file is one the library already tracks.
//
// On a POSIX path the key is the cleaned path itself, so two names that differ
// by case or by a trailing dot stay different files, as they are on disk.
//
// On Windows the key folds what the filesystem folds: letter case, the choice
// of separator, and trailing dots and spaces on every component. That makes a
// row stored as ".../WASTE LANDS FLIP/..." or ".../H.R.H./..." match the
// folder Windows actually holds, so existing libraries need no rewrite.
// Case sensitivity is really a property of the volume, not the OS, but a
// Windows host is the case the reports describe and the only one where the
// folding is known to be right.
func pathKey(p string, windows bool) string {
	if !windows {
		return filepath.Clean(p)
	}
	p = strings.ReplaceAll(p, `\`, "/")
	parts := strings.Split(p, "/")
	out := parts[:0]
	for i, part := range parts {
		// Keep the empty leading part of a rooted path and a drive "C:".
		if part != "" && part != "." && part != ".." {
			if trimmed := trimWindowsName(part); trimmed != "" {
				part = trimmed
			}
		}
		if part == "" && i != 0 {
			continue
		}
		out = append(out, part)
	}
	return strings.ToLower(strings.Join(out, "/"))
}

// pathKey is pathKey in the Scanner's path flavour.
func (s *Scanner) pathKey(p string) string { return pathKey(p, s.windowsPaths) }
