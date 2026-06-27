//go:build !windows

package api

import (
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

// sameDevice reports whether a and b reside on the same filesystem by
// comparing OS device IDs — i.e. whether a hardlink import from a to b is
// possible. Returns false on any stat error or empty input. This duplicates
// importer.sameDevice intentionally to keep config/api free of an importer
// dependency.
func sameDevice(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := statExistingDevice(b)
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

// hardlinkable reports whether downloads in a can actually be hard-linked into
// b. Matching device IDs are necessary but NOT sufficient — two separate bind
// mounts (or Unraid /mnt/user shares) report the same st_dev yet os.Link across
// them fails with EXDEV. So after the cheap same-device gate this confirms with
// a real link probe (created and cleaned up under the nearest existing
// directories), which is what keeps the storage-health "share a filesystem"
// message honest. If a probe file cannot be written it trusts the same-device
// result rather than reporting a false negative. Mirrors importer.hardlinkable
// (duplicated to keep api free of an importer dependency).
func hardlinkable(a, b string) bool {
	if !sameDevice(a, b) {
		return false
	}
	aDir := nearestExistingDir(a)
	bDir := nearestExistingDir(b)
	if aDir == "" || bDir == "" {
		return true
	}
	probe, err := os.CreateTemp(aDir, ".bindery-hlprobe-*")
	if err != nil {
		return true
	}
	name := probe.Name()
	_ = probe.Close()
	defer func() { _ = os.Remove(name) }()
	link := filepath.Join(bDir, filepath.Base(name)+".lnk")
	if err := os.Link(name, link); err != nil {
		return false
	}
	_ = os.Remove(link)
	return true
}
