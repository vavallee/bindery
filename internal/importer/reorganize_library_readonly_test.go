package importer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// treeSnapshot renders every path under root with its kind, size and contents,
// so any creation, deletion, move or rewrite shows up as a diff. Modes and
// mtimes are deliberately left out: those are noisy on some filesystems and a
// preview that changed one would still have to create or write something to be
// interesting.
func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			lines = append(lines, "dir  "+rel)
			return nil
		}
		body, rerr := os.ReadFile(path) // #nosec G304 (path is a test fixture under t.TempDir())
		if rerr != nil {
			return rerr
		}
		lines = append(lines, fmt.Sprintf("file %s %d %q", rel, len(body), body))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// A library wide reorganize preview is the one that is worth being sure about:
// it is reachable from a single button in Settings (#2296) and it visits every
// tracked file in the library at once, so if preview wrote anything it would
// write it everywhere before the operator had agreed to a thing.
//
// PreviewReorganizeLibrary must therefore be a pure read: it classifies moves
// by stat'ing paths and never creates, moves, writes or deletes. ApplyReorganize
// is the only writer. This asserts both halves against a byte level snapshot of
// the whole tree rather than against the paths the test happens to think about.
func TestPreviewReorganizeLibrary_IsAPureRead(t *testing.T) {
	env, libraryDir, audiobookDir, ctx := reorgFixture(t)

	author := env.seedAuthor(t, ctx, "Jane Doe")
	// A spread of the classifications preview has to reach, so the read only
	// claim covers the branches that stat a destination, not just the easy one.
	//
	// movable: sits off template, nothing in the way.
	movable := env.seedBook(t, ctx, author, "Book One")
	movablePath := filepath.Join(libraryDir, "flat", "Book One.epub")
	writeFileAt(t, movablePath)
	if err := env.books.AddBookFile(ctx, movable.ID, models.MediaTypeEbook, movablePath); err != nil {
		t.Fatal(err)
	}
	// collision: something already occupies the templated destination.
	collide := env.seedBook(t, ctx, author, "Book Two")
	collidePath := filepath.Join(libraryDir, "flat", "Book Two.epub")
	writeFileAt(t, collidePath)
	writeFileAt(t, filepath.Join(libraryDir, "Jane Doe", "Book Two (2020)", "Book Two - Jane Doe.epub"))
	if err := env.books.AddBookFile(ctx, collide.ID, models.MediaTypeEbook, collidePath); err != nil {
		t.Fatal(err)
	}
	// missing: tracked in the index, not on disk.
	gone := env.seedBook(t, ctx, author, "Book Three")
	if err := env.books.AddBookFile(ctx, gone.ID, models.MediaTypeEbook, filepath.Join(libraryDir, "flat", "Book Three.epub")); err != nil {
		t.Fatal(err)
	}
	// noop: already exactly where the template puts it.
	settled := env.seedBook(t, ctx, author, "Book Four")
	settledPath := filepath.Join(libraryDir, "Jane Doe", "Book Four (2020)", "Book Four - Jane Doe.epub")
	writeFileAt(t, settledPath)
	if err := env.books.AddBookFile(ctx, settled.ID, models.MediaTypeEbook, settledPath); err != nil {
		t.Fatal(err)
	}

	libBefore := treeSnapshot(t, libraryDir)
	audioBefore := treeSnapshot(t, audiobookDir)

	moves, err := env.s.PreviewReorganizeLibrary(ctx)
	if err != nil {
		t.Fatalf("PreviewReorganizeLibrary: %v", err)
	}

	byStatus := map[string]int{}
	for _, m := range moves {
		byStatus[m.Status]++
	}
	for _, want := range []string{ReorgStatusMove, ReorgStatusCollision, ReorgStatusMissing, ReorgStatusNoop} {
		if byStatus[want] == 0 {
			t.Fatalf("preview produced no %q row, so the read-only claim does not cover that branch: %v", want, byStatus)
		}
	}

	if got := treeSnapshot(t, libraryDir); got != libBefore {
		t.Errorf("library scoped preview changed the library tree.\nbefore:\n%s\nafter:\n%s", libBefore, got)
	}
	if got := treeSnapshot(t, audiobookDir); got != audioBefore {
		t.Errorf("library scoped preview changed the audiobook tree.\nbefore:\n%s\nafter:\n%s", audioBefore, got)
	}

	// The paired positive: apply is the writer, and only for the clean move.
	var movableFileID int64
	for _, m := range moves {
		if m.BookID == movable.ID {
			movableFileID = m.FileID
		}
	}
	if movableFileID == 0 {
		t.Fatal("no previewed row for the movable book")
	}
	env.s.ApplyReorganize(ctx, []int64{movableFileID})
	if got := treeSnapshot(t, libraryDir); got == libBefore {
		t.Error("apply left the tree untouched, so this test could not tell a writer from a reader")
	}
	if _, err := os.Stat(movablePath); !os.IsNotExist(err) {
		t.Errorf("apply left the source in place, stat err = %v", err)
	}

	// And a preview run after the apply is still a pure read.
	libAfterApply := treeSnapshot(t, libraryDir)
	if _, err := env.s.PreviewReorganizeLibrary(ctx); err != nil {
		t.Fatalf("second PreviewReorganizeLibrary: %v", err)
	}
	if got := treeSnapshot(t, libraryDir); got != libAfterApply {
		t.Errorf("preview after apply changed the tree.\nbefore:\n%s\nafter:\n%s", libAfterApply, got)
	}
}
