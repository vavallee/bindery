package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// mergeFn adapts the three merge placement functions to one signature so the
// shared guard/skip behaviour can be table-tested across all of them.
type mergeFn struct {
	name string
	run  func(ctx context.Context, src, dst string) ([]string, error)
}

func mergeFns() []mergeFn {
	return []mergeFn{
		{"CopyDirMergeCtx", CopyDirMergeCtx},
		{"HardlinkDirMerge", func(_ context.Context, src, dst string) ([]string, error) {
			return HardlinkDirMerge(src, dst)
		}},
		{"MoveDirMergeCtx", MoveDirMergeCtx},
	}
}

// mustDir creates dir and fails the test if it cannot.
func mustDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	return path
}

// mustFile writes body to path, creating parents.
func mustFile(t *testing.T, path, body string) string {
	t.Helper()
	mustDir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMergePlacement_RejectsMissingOrNonDirSource(t *testing.T) {
	for _, fn := range mergeFns() {
		t.Run(fn.name, func(t *testing.T) {
			dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))

			if _, err := fn.run(context.Background(), filepath.Join(t.TempDir(), "nope"), dst); err == nil {
				t.Error("want an error for a source that does not exist, got nil")
			}

			file := mustFile(t, filepath.Join(t.TempDir(), "a-file"), "x")
			if _, err := fn.run(context.Background(), file, dst); err == nil {
				t.Error("want an error for a source that is not a directory, got nil")
			}
		})
	}
}

func TestMergePlacement_RejectsDestinationInsideSource(t *testing.T) {
	for _, fn := range mergeFns() {
		t.Run(fn.name, func(t *testing.T) {
			src := mustDir(t, filepath.Join(t.TempDir(), "src"))
			dst := mustDir(t, filepath.Join(src, "nested-dst"))
			_, err := fn.run(context.Background(), src, dst)
			if !errors.Is(err, ErrDestInsideSource) {
				t.Errorf("want ErrDestInsideSource, got %v", err)
			}
		})
	}
}

func TestMergePlacement_HonoursCancelledContext(t *testing.T) {
	// HardlinkDirMerge takes no context, so only the two context-aware
	// placements are exercised here.
	for _, fn := range []mergeFn{
		{"CopyDirMergeCtx", CopyDirMergeCtx},
		{"MoveDirMergeCtx", MoveDirMergeCtx},
	} {
		t.Run(fn.name, func(t *testing.T) {
			src := mustDir(t, filepath.Join(t.TempDir(), "src"))
			mustFile(t, filepath.Join(src, "a.m4b"), "a")
			dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := fn.run(ctx, src, dst); !errors.Is(err, context.Canceled) {
				t.Errorf("want context.Canceled, got %v", err)
			}
			// A cancelled merge must not have consumed the source, even for
			// move: the placement never completed.
			if _, err := os.Stat(filepath.Join(src, "a.m4b")); err != nil {
				t.Errorf("source file gone after a cancelled merge: %v", err)
			}
		})
	}
}

func TestMergePlacement_SkipsSymlinksAndDownloadArtifacts(t *testing.T) {
	for _, fn := range mergeFns() {
		t.Run(fn.name, func(t *testing.T) {
			src := mustDir(t, filepath.Join(t.TempDir(), "src"))
			mustFile(t, filepath.Join(src, "book.m4b"), "audio")
			// Download-client receipts must never enter the library (#1542).
			for _, artifact := range []string{"job.nzb", "vol01.par2", "check.sfv", "info.diz"} {
				mustFile(t, filepath.Join(src, artifact), "artifact")
			}
			// A symlink is not a regular file and must be skipped rather than
			// followed out of the tree.
			if err := os.Symlink("/etc/passwd", filepath.Join(src, "escape.epub")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))

			if _, err := fn.run(context.Background(), src, dst); err != nil {
				t.Fatalf("merge failed: %v", err)
			}

			entries, err := os.ReadDir(dst)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			sort.Strings(got)
			if len(got) != 1 || got[0] != "book.m4b" {
				t.Errorf("destination = %v, want only [book.m4b]: receipts and symlinks must not be placed", got)
			}
		})
	}
}

func TestMergePlacement_CreatesAndMergesSubdirectories(t *testing.T) {
	for _, fn := range mergeFns() {
		t.Run(fn.name, func(t *testing.T) {
			src := mustDir(t, filepath.Join(t.TempDir(), "src"))
			mustFile(t, filepath.Join(src, "disc1", "01.mp3"), "one")
			mustFile(t, filepath.Join(src, "extras", "tracklist.txt"), "tracks")
			dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))
			// extras/ already exists at the destination with its own file, so
			// the walker must descend into it rather than skip the subtree.
			mustFile(t, filepath.Join(dst, "extras", "liner.txt"), "liner")

			skipped, err := fn.run(context.Background(), src, dst)
			if err != nil {
				t.Fatalf("merge failed: %v", err)
			}
			if len(skipped) != 0 {
				t.Errorf("skipped = %v, want none: no name collided", skipped)
			}
			for _, want := range []string{
				filepath.Join(dst, "disc1", "01.mp3"),         // newly created subdir
				filepath.Join(dst, "extras", "tracklist.txt"), // merged into an existing subdir
				filepath.Join(dst, "extras", "liner.txt"),     // pre-existing, untouched
			} {
				if _, err := os.Stat(want); err != nil {
					t.Errorf("missing %s after merge: %v", want, err)
				}
			}
		})
	}
}

func TestMergePlacement_ReportsSkippedCollisionsWithoutOverwriting(t *testing.T) {
	for _, fn := range mergeFns() {
		t.Run(fn.name, func(t *testing.T) {
			src := mustDir(t, filepath.Join(t.TempDir(), "src"))
			mustFile(t, filepath.Join(src, "cover.jpg"), "source-cover")
			mustFile(t, filepath.Join(src, "book.m4b"), "audio")
			dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))
			mustFile(t, filepath.Join(dst, "cover.jpg"), "existing-cover")

			skipped, err := fn.run(context.Background(), src, dst)
			if err != nil {
				t.Fatalf("merge failed: %v", err)
			}
			if len(skipped) != 1 || skipped[0] != "cover.jpg" {
				t.Errorf("skipped = %v, want [cover.jpg]", skipped)
			}
			got, err := os.ReadFile(filepath.Join(dst, "cover.jpg"))
			if err != nil || string(got) != "existing-cover" {
				t.Errorf("existing file was overwritten: %q err=%v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dst, "book.m4b")); err != nil {
				t.Errorf("non-colliding file was not placed: %v", err)
			}
		})
	}
}

// TestMoveDirMergeCtx_SourceLifecycle pins the rule that only move mode has:
// the source is consumed when the merge placed everything, and preserved when
// anything was skipped, because a skipped file's only copy still lives there.
func TestMoveDirMergeCtx_SourceLifecycle(t *testing.T) {
	t.Run("clean merge consumes the source", func(t *testing.T) {
		src := mustDir(t, filepath.Join(t.TempDir(), "src"))
		mustFile(t, filepath.Join(src, "book.m4b"), "audio")
		dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))

		skipped, err := MoveDirMergeCtx(context.Background(), src, dst)
		if err != nil {
			t.Fatal(err)
		}
		if len(skipped) != 0 {
			t.Fatalf("skipped = %v, want none", skipped)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Errorf("source should be gone after a clean move merge, stat err = %v", err)
		}
	})

	t.Run("skipped file preserves the source", func(t *testing.T) {
		src := mustDir(t, filepath.Join(t.TempDir(), "src"))
		mustFile(t, filepath.Join(src, "cover.jpg"), "source-cover")
		dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))
		mustFile(t, filepath.Join(dst, "cover.jpg"), "existing-cover")

		skipped, err := MoveDirMergeCtx(context.Background(), src, dst)
		if err != nil {
			t.Fatal(err)
		}
		if len(skipped) == 0 {
			t.Fatal("want a skip")
		}
		if _, err := os.Stat(filepath.Join(src, "cover.jpg")); err != nil {
			t.Errorf("skipped file's only copy was destroyed with the source: %v", err)
		}
	})
}

// TestMergePlacement_NeverRemovesTheDestination pins the invariant the merge
// functions document: unlike CopyDirCtx/HardlinkDir/moveDirCtx, they must not
// unwind by deleting dst, which in a shared-folder merge holds the book's
// other format. Exercised by making a placement fail partway (an unwritable
// destination subdirectory) and asserting the pre-existing file survives.
func TestMergePlacement_NeverRemovesTheDestination(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	for _, fn := range mergeFns() {
		t.Run(fn.name, func(t *testing.T) {
			src := mustDir(t, filepath.Join(t.TempDir(), "src"))
			mustFile(t, filepath.Join(src, "locked", "new.mp3"), "new")
			dst := mustDir(t, filepath.Join(t.TempDir(), "dst"))
			existing := mustFile(t, filepath.Join(dst, "keep.epub"), "the other format")
			// A pre-existing, non-writable subdirectory of the same name makes
			// the recursive placement fail once it tries to write into it.
			locked := mustDir(t, filepath.Join(dst, "locked"))
			if err := os.Chmod(locked, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })

			if _, err := fn.run(context.Background(), src, dst); err == nil {
				t.Skip("placement unexpectedly succeeded; cannot exercise the failure path here")
			}
			if _, err := os.Stat(existing); err != nil {
				t.Errorf("a failed merge deleted the destination's pre-existing file: %v", err)
			}
		})
	}
}
