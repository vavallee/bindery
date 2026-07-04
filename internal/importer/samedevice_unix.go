//go:build !windows

package importer

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// statExisting stats path p, walking up to the nearest existing ancestor
// directory when p itself does not exist yet. This allows comparing the device
// of a not-yet-created destination path against an already-existing source.
func statExisting(p string) (os.FileInfo, error) {
	for {
		fi, err := os.Stat(p)
		if err == nil {
			return fi, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			// Reached filesystem root with no success.
			return nil, err
		}
		p = parent
	}
}

// sameDevice reports whether a and b reside on the same filesystem by
// comparing their OS device IDs. When b does not exist, its nearest existing
// ancestor directory is used so that not-yet-created destination paths are
// handled correctly. Returns false on any stat error.
func sameDevice(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := statExisting(b)
	if err != nil {
		return false
	}
	aStat, ok := ai.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	bStat, ok := bi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return aStat.Dev == bStat.Dev
}

// nearestExistingDir returns p if it is an existing directory, otherwise the
// nearest ancestor directory that exists. Returns "" if none is found. A file
// path resolves to its containing directory (the file itself is not a dir).
func nearestExistingDir(p string) string {
	for p != "" {
		fi, err := os.Stat(p)
		if err == nil && fi.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
	return ""
}

// hardlinkable reports whether a hardlink import from src into dst would
// actually succeed. Matching device IDs (sameDevice) are necessary but NOT
// sufficient: two separate bind mounts — or Unraid /mnt/user shares — report
// the same st_dev yet os.Link across them fails with EXDEV ("invalid
// cross-device link"). So after the cheap same-device gate it confirms with a
// real link probe between the nearest existing directories of src and dst,
// cleaning up the probe artifacts. If a probe file cannot be created (e.g. a
// read-only download dir), it trusts the same-device result rather than
// reporting a false negative.
func hardlinkable(src, dst string) bool {
	if !sameDevice(src, dst) {
		return false
	}
	srcDir := nearestExistingDir(src)
	dstDir := nearestExistingDir(dst)
	if srcDir == "" || dstDir == "" {
		return true
	}
	probe, err := os.CreateTemp(srcDir, ".bindery-hlprobe-*")
	if err != nil {
		return true
	}
	name := probe.Name()
	_ = probe.Close()
	defer func() { _ = os.Remove(name) }()
	link := filepath.Join(dstDir, filepath.Base(name)+".lnk")
	if err := os.Link(name, link); err != nil {
		return false
	}
	_ = os.Remove(link)
	return true
}

// crossDeviceErr reports whether err is an EXDEV ("invalid cross-device link")
// failure — os.Link's signal that src and dst are on different mounts even when
// they share a device id. Used to trigger the seeding-safe copy fallback.
func crossDeviceErr(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
