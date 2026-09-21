//go:build !windows

package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeviceIDOf_MatchesDeviceID verifies deviceIDOf, given an os.FileInfo the
// caller already has, agrees with deviceID stat'ing the same path fresh — the
// manual-import scan's tracked-file rebuild (#2480 review) relies on reading
// the device id off a FileInfo it already stat'd instead of stat'ing again.
func TestDeviceIDOf_MatchesDeviceID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := filepath.Join(root, "a.epub")
	writeTestFile(t, a)

	fi, err := os.Stat(a)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	fromInfo, okInfo := deviceIDOf(fi)
	fromPath, okPath := deviceID(a)
	if !okInfo || !okPath {
		t.Fatalf("expected both to resolve, got okInfo=%v okPath=%v", okInfo, okPath)
	}
	if fromInfo != fromPath {
		t.Errorf("deviceIDOf(stat) = %d, deviceID(path) = %d, want equal", fromInfo, fromPath)
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
