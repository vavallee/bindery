//go:build !windows

package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// statExistingDevice stats p, walking up to the nearest existing ancestor
// directory when p itself does not exist yet, so a not-yet-created library
// destination can still be compared against an existing download dir. This
// mirrors importer.statExisting; it is duplicated here (rather than imported)
// to avoid an api -> importer dependency.
func statExistingDevice(p string) (os.FileInfo, error) {
	for {
		fi, err := os.Stat(p)
		if err == nil {
			return fi, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil, err
		}
		p = parent
	}
}

// nearestExistingDir returns p if it is an existing directory, otherwise the
// nearest existing ancestor directory, or "" if none exists.
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

// deviceIDOf extracts a's OS device ID from an os.FileInfo the caller already
// stat'd for another purpose — the manual-import scan's tracked-file rebuild
// (#2480) stats every tracked path once anyway (to detect it and, if it is a
// directory, walk it), so this reads the device id off that same FileInfo
// instead of stat'ing the path again just to compare devices. ok is false
// when the platform doesn't expose a device id (see the Windows build).
func deviceIDOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// deviceID returns path's OS device ID as a cache key: two paths returning
// (id, true) with equal id are stat-confirmed to share a device; ok is false
// when the ID is unknown (stat failed, or the platform doesn't expose one —
// see the Windows build of this function). Used to key the manual-import
// scan's tracked-file cache (#2480) by the scanned root's device — computed
// once per Scan request, not once per tracked row (a prior version compared
// devices via a per-row confirmedCrossDevice(scanRoot, trackedPath) call that
// re-stat'd scanRoot on every tracked row in the library; see trackedFileIndex
// for the review that caught it).
func deviceID(path string) (uint64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return deviceIDOf(fi)
}

// hardlinkableReason reports whether downloads in a can actually be
// hard-linked into b and, when they can't, WHY — the generic "will copy
// instead" banner sent users hunting through mount tables blind (#1427).
// Matching device IDs are necessary but NOT sufficient — two separate bind
// mounts (or Unraid /mnt/user shares) report the same st_dev yet os.Link
// across them fails with EXDEV. So after the cheap same-device gate this
// confirms with a real link probe (created and cleaned up under the nearest
// existing directories), which is what keeps the storage-health "share a
// filesystem" message honest. If a probe file cannot be written it trusts the
// same-device result rather than reporting a false negative. Mirrors
// importer.hardlinkable (duplicated to keep api free of an importer
// dependency).
func hardlinkableReason(a, b string) (bool, string) {
	if a == "" || b == "" {
		return false, "the download or library directory is not configured"
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false, fmt.Sprintf("cannot inspect the download directory: %v", err)
	}
	bi, err := statExistingDevice(b)
	if err != nil {
		return false, fmt.Sprintf("cannot inspect the library directory: %v", err)
	}
	aStat, aOK := ai.Sys().(*syscall.Stat_t)
	bStat, bOK := bi.Sys().(*syscall.Stat_t)
	if !aOK || !bOK {
		return false, "device comparison is not supported on this platform"
	}
	if aStat.Dev != bStat.Dev {
		return false, "the download directory and the library are on different filesystems, so imports copy instead of hardlinking (mount both from the same filesystem to fix)"
	}
	aDir := nearestExistingDir(a)
	bDir := nearestExistingDir(b)
	if aDir == "" || bDir == "" {
		return true, ""
	}
	probe, err := os.CreateTemp(aDir, ".bindery-hlprobe-*")
	if err != nil {
		return true, ""
	}
	name := probe.Name()
	_ = probe.Close()
	defer func() { _ = os.Remove(name) }()
	link := filepath.Join(bDir, filepath.Base(name)+".lnk")
	if err := os.Link(name, link); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return false, "the paths report the same device ID but hardlinks between them fail (EXDEV) — typical of mergerfs pools, separate Docker bind mounts, and Unraid /mnt/user shares; imports will copy instead"
		}
		if errors.Is(err, syscall.EPERM) {
			return false, "the filesystem refused the hardlink (operation not permitted) — common on exFAT, NTFS, and some network shares; imports will copy instead"
		}
		return false, fmt.Sprintf("a test hardlink between the two directories failed: %v — imports will copy instead", err)
	}
	_ = os.Remove(link)
	return true, ""
}
