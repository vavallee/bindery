package importer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// This file covers the mkdir-failure arms of the import placement primitives.
// The failure is injected deterministically by placing a regular file where a
// directory component of the destination is expected: os.MkdirAll and
// os.Root.Mkdir then fail with ENOTDIR, exercising the `err != nil` arm that
// the happy-path tests never reach. The wrapped message prefix ("create dir",
// "create parent dir", "create dest dir") identifies which arm fired, so the
// assertions match on it.

// writeBlockerDir places a regular file named "blocker" under base and
// returns a destination path nested under it, so creating the destination's
// directory components fails.
func writeBlockerDir(t *testing.T, base, leaf string) (dst string) {
	t.Helper()
	writeMoveFile(t, filepath.Join(base, "blocker"), "not a directory")
	return filepath.Join(base, "blocker", leaf)
}

func TestPlacementMkdirFails(t *testing.T) {
	tests := []struct {
		name string
		// dirSource: the source is a directory (dir placements) rather
		// than a single file (file placements).
		dirSource bool
		fn        func(src, dst string) error
		want      string
	}{
		{"MoveFile", false, func(src, dst string) error { return MoveFile(src, dst) }, "create dir"},
		{"MoveFileCtx", false, func(src, dst string) error { return MoveFileCtx(context.Background(), src, dst) }, "create dir"},
		{"CopyFile", false, func(src, dst string) error { return CopyFile(src, dst) }, "create dir"},
		{"CopyFileCtx", false, func(src, dst string) error { return CopyFileCtx(context.Background(), src, dst) }, "create dir"},
		{"HardlinkFile", false, func(src, dst string) error { return HardlinkFile(src, dst) }, "create dir"},
		{"StagedImport", false, func(src, dst string) error {
			_, commit, rollback, err := StagedImport(context.Background(), "move", src, dst)
			if commit != nil || rollback != nil {
				t.Fatal("StagedImport returned non-nil commit/rollback on failure")
			}
			return err
		}, "create dir"},
		{"MoveDir", true, func(src, dst string) error { return MoveDir(src, dst) }, "create parent dir"},
		{"MoveDirCtx", true, func(src, dst string) error { return MoveDirCtx(context.Background(), src, dst) }, "create parent dir"},
		{"CopyDirCtx", true, func(src, dst string) error { return CopyDirCtx(context.Background(), src, dst) }, "create parent dir"},
		{"HardlinkDir", true, func(src, dst string) error { return HardlinkDir(src, dst) }, "create dest dir"},
		{"HardlinkDirMerge", true, func(src, dst string) error {
			_, err := HardlinkDirMerge(src, dst)
			return err
		}, "create dest dir"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			var src string
			if tc.dirSource {
				src = filepath.Join(base, "src")
				writeMoveFile(t, filepath.Join(src, "book.epub"), "content")
			} else {
				src = filepath.Join(base, "source")
				writeMoveFile(t, src, "content")
			}
			dst := writeBlockerDir(t, base, "book")
			if err := tc.fn(src, dst); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s with a file blocking the destination dir: got %v, want %q error", tc.name, err, tc.want)
			}
		})
	}
}

// TestMergeIntoFileDestination covers mergeCopyDirContext and copyDirContext
// with a destination path that is itself a regular file: MkdirAll fails
// outright (there is no IsExist tolerance for a non-directory).
func TestMergeIntoFileDestination(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeMoveFile(t, filepath.Join(src, "book.epub"), "content")
	dst := filepath.Join(base, "dest-is-a-file")
	writeMoveFile(t, dst, "not a directory")

	skipped, err := mergeCopyDirContext(context.Background(), src, dst)
	if err == nil {
		t.Fatal("mergeCopyDirContext onto a file path: got nil error, want error")
	}
	if len(skipped) != 0 {
		t.Fatalf("mergeCopyDirContext onto a file path: skipped=%v, want none", skipped)
	}
	if err := copyDirContext(context.Background(), src, dst); err == nil {
		t.Fatal("copyDirContext onto a file path: got nil error, want error")
	}
}

// TestFlattenAudiobookDirNamed_MkdirFails covers the flattener's mkdir arm.
func TestFlattenAudiobookDirNamed_MkdirFails(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeMoveFile(t, filepath.Join(src, "track01.mp3"), "audio")
	writeMoveFile(t, filepath.Join(src, "track02.mp3"), "audio")
	dst := writeBlockerDir(t, base, "book")
	err := flattenAudiobookDirNamed(context.Background(), "copy", src, dst, func(index, count int, ext string) string {
		return "track"
	})
	if err == nil || !strings.Contains(err.Error(), "create dest dir") {
		t.Fatalf("flattenAudiobookDirNamed with a file blocking the destination parent: got %v, want 'create dest dir' error", err)
	}
}
