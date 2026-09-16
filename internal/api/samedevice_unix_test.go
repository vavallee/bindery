//go:build !windows

package api

import (
	"path/filepath"
	"testing"
)

// TestConfirmedCrossDevice_SamePathNeverConfirmedCross verifies the
// conservative default: two paths on the same device (the common case in
// tests, and for the tracked-file stat sweep's own root) are never falsely
// reported as confirmed cross-device (#2480).
func TestConfirmedCrossDevice_SamePathNeverConfirmedCross(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := filepath.Join(root, "a.epub")
	b := filepath.Join(root, "b.epub")
	writeTestFile(t, a)
	writeTestFile(t, b)

	if confirmedCrossDevice(a, b) {
		t.Error("two paths under the same tempdir should share a device")
	}
	if confirmedCrossDevice("", b) {
		t.Error("empty path should not be confirmed cross-device")
	}
	if confirmedCrossDevice(a, filepath.Join(root, "missing.epub")) {
		t.Error("a path whose nearest existing ancestor is still on the same device should not be confirmed cross-device")
	}
	if confirmedCrossDevice(filepath.Join(root, "missing-a.epub"), b) {
		t.Error("stat error on a should report false, not a false positive")
	}
}

// TestDeviceID_SameDirSameDevice verifies deviceID reports a usable, stable
// identity for paths that do share a device, and (false) for a path that
// doesn't exist — the manual-import scan's cache key (#2480) relies on both.
func TestDeviceID_SameDirSameDevice(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := filepath.Join(root, "a.epub")
	writeTestFile(t, a)

	aID, aOK := deviceID(a)
	rootID, rootOK := deviceID(root)
	if !aOK || !rootOK {
		t.Fatalf("expected deviceID to resolve for existing paths, got aOK=%v rootOK=%v", aOK, rootOK)
	}
	if aID != rootID {
		t.Errorf("a file and its parent dir should report the same device: %d != %d", aID, rootID)
	}

	if _, ok := deviceID(filepath.Join(root, "missing.epub")); ok {
		t.Error("deviceID for a nonexistent path should report ok=false")
	}
}
