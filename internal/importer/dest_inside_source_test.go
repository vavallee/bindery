package importer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runawayBudget bounds how long a directory placement may run in these tests.
// Every case here must be rejected before a single byte is copied, so the
// budget only exists to keep a regression (the #1809 self-recursing copy) from
// hanging CI and filling the disk: the ctx-aware primitives abort on it and the
// test fails on the wrong error instead of running forever.
const runawayBudget = 3 * time.Second

// maxDepthUnder returns the deepest path (in components below root) that exists
// under root. The self-recursing copy grows this without bound.
func maxDepthUnder(t *testing.T, root string) int {
	t.Helper()
	deepest := 0
	_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort depth probe
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if d := len(strings.Split(rel, string(filepath.Separator))); d > deepest {
			deepest = d
		}
		return nil
	})
	return deepest
}

// newAudiobookFolder builds <tmp>/Author/<name> holding one media file.
func newAudiobookFolder(t *testing.T, tmp, name string) string {
	t.Helper()
	src := filepath.Join(tmp, "Author", name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "book.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	return src
}

// TestDirPlacement_RefusesDestinationInsideSource covers #1809: a reorganize or
// import whose computed destination lands inside the source folder must fail
// fast. Before the guard, the rename fast path was refused by the kernel and
// the copy fallback descended into the destination it was creating, nesting
// directories until the disk filled and the container had to be killed.
func TestDirPlacement_RefusesDestinationInsideSource(t *testing.T) {
	cases := []struct {
		name    string
		dstRel  []string // destination components, relative to the source folder
		place   func(ctx context.Context, src, dst string) error
		wantOp  string
		srcName string
	}{
		{
			name:    "move into direct child",
			dstRel:  []string{"Title"},
			place:   MoveDirCtx,
			wantOp:  "move",
			srcName: "Title",
		},
		{
			// The reported layout change: /library/Author/Title →
			// /library/Author/<Series>/Title where the series is named after
			// the book, so the destination nests under the source.
			name:    "move into nested series folder",
			dstRel:  []string{"Series", "Title"},
			place:   MoveDirCtx,
			wantOp:  "move",
			srcName: "Title",
		},
		{
			name:    "copy into direct child",
			dstRel:  []string{"Title"},
			place:   CopyDirCtx,
			wantOp:  "copy",
			srcName: "Title",
		},
		{
			name:   "hardlink into direct child",
			dstRel: []string{"Title"},
			place: func(_ context.Context, src, dst string) error {
				return HardlinkDir(src, dst)
			},
			wantOp:  "hardlink",
			srcName: "Title",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			src := newAudiobookFolder(t, tmp, tc.srcName)
			dst := filepath.Join(append([]string{src}, tc.dstRel...)...)

			ctx, cancel := context.WithTimeout(context.Background(), runawayBudget)
			defer cancel()

			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- tc.place(ctx, src, dst) }()

			var err error
			select {
			case err = <-done:
			case <-time.After(runawayBudget + 2*time.Second):
				// HardlinkDir takes no context, so a regression there can only
				// be caught by the watchdog. Fail loudly rather than wait.
				t.Fatalf("%s %q → %q did not return within %s: it is recursing into its own destination (depth %d under the source)",
					tc.wantOp, src, dst, runawayBudget+2*time.Second, maxDepthUnder(t, src))
			}

			if !errors.Is(err, ErrDestInsideSource) {
				t.Fatalf("%s: got err %v after %s, want ErrDestInsideSource", tc.wantOp, err, time.Since(start))
			}
			msg := err.Error()
			for _, want := range []string{tc.wantOp, src, dst} {
				if !strings.Contains(msg, want) {
					t.Errorf("error message %q does not name %q", msg, want)
				}
			}

			// Nothing may have been created: the source keeps exactly the one
			// file it started with, at its original depth.
			if depth := maxDepthUnder(t, src); depth != 1 {
				t.Fatalf("source tree grew: max depth %d under %s, want 1", depth, src)
			}
			if _, statErr := os.Stat(filepath.Join(src, "book.m4b")); statErr != nil {
				t.Fatalf("source file missing after refused %s: %v", tc.wantOp, statErr)
			}
		})
	}
}

// TestDirPlacement_SiblingSharingPrefixIsNotContainment pins the component
// boundary: "…/Book" must not read as containing "…/Book Two".
func TestDirPlacement_SiblingSharingPrefixIsNotContainment(t *testing.T) {
	tmp := t.TempDir()
	src := newAudiobookFolder(t, tmp, "Book")
	dst := filepath.Join(tmp, "Author", "Book Two")

	if err := MoveDirCtx(t.Context(), src, dst); err != nil {
		t.Fatalf("MoveDirCtx to sibling with shared prefix: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "book.m4b")); err != nil {
		t.Fatalf("file not at destination: %v", err)
	}
}

// TestDirPlacement_SymlinkedSourceStillDetected: the library root reached
// through a symlink must not defeat the check, so both sides are resolved
// before comparing.
func TestDirPlacement_SymlinkedSourceStillDetected(t *testing.T) {
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real")
	src := newAudiobookFolder(t, realRoot, "Title")
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Same folder, reached two different ways.
	linked := filepath.Join(link, "Author", "Title")
	dst := filepath.Join(src, "Title")

	ctx, cancel := context.WithTimeout(context.Background(), runawayBudget)
	defer cancel()
	if err := MoveDirCtx(ctx, linked, dst); !errors.Is(err, ErrDestInsideSource) {
		t.Fatalf("got %v, want ErrDestInsideSource", err)
	}
}

// TestDirPlacement_EqualSourceAndDestination documents the decision that an
// equal source and destination is a distinct case from containment: it is left
// to the pre-existing "destination already exists" refusal (the reorganize
// layer classifies it as a noop long before reaching here), not reported as a
// containment error.
func TestDirPlacement_EqualSourceAndDestination(t *testing.T) {
	tmp := t.TempDir()
	src := newAudiobookFolder(t, tmp, "Title")

	err := MoveDirCtx(t.Context(), src, src)
	if err == nil {
		t.Fatal("MoveDirCtx(src, src) returned nil, want an error")
	}
	if errors.Is(err, ErrDestInsideSource) {
		t.Fatalf("equal paths reported as containment: %v", err)
	}
	if !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("got %v, want the destination-already-exists refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(src, "book.m4b")); statErr != nil {
		t.Fatalf("source file missing: %v", statErr)
	}
}
