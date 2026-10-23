//go:build unix

package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// withClearedUmaskAndMode runs the body with the umask cleared and
// BINDERY_DIR_MODE set to 0755. Without the umask cleared the process umask
// would mask the comparison, which is why the accepted value set only holds
// umask-surviving modes in production.
func withClearedUmaskAndMode(t *testing.T, body func()) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	t.Setenv("BINDERY_DIR_MODE", "0755")
	body()
}

// TestPlacementHonorsDirMode runs an import placement with the umask cleared
// and BINDERY_DIR_MODE set, then stats the created author and book
// directories to confirm the configured mode is applied exactly.
func TestPlacementHonorsDirMode(t *testing.T) {
	withClearedUmaskAndMode(t, func() {
		base := t.TempDir()
		src := filepath.Join(base, "downloads", "book.epub")
		writeMoveFile(t, src, "content")
		authorDir := filepath.Join(base, "library", "Sarah J. Maas")
		dst := filepath.Join(authorDir, "A Court of Thorns and Roses (2013)", "book.epub")

		if err := MoveFile(src, dst); err != nil {
			t.Fatalf("MoveFile: %v", err)
		}

		for _, dir := range []string{authorDir, filepath.Dir(dst)} {
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("stat %s: %v", dir, err)
			}
			if got := info.Mode().Perm(); got != 0o755 {
				t.Fatalf("%s mode = %#o, want 0755", dir, got)
			}
		}
	})
}

// assertAllDirsMode requires every directory at and below root to carry
// exactly the want bits. Point root at the top directory the placement
// created, not at the t.TempDir fixture root, which is 0700 and not
// Bindery's doing.
func assertAllDirsMode(t *testing.T, root string, want os.FileMode) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatalf("walk %s: %v", p, err)
		}
		if !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %#o, want %#o", p, got, want)
		}
		return nil
	})
}

// TestPlacementModesWithClearedUmask runs each placement entry point with the
// umask cleared and BINDERY_DIR_MODE=0755, then checks the mode of every
// directory it created. This pins the mode at the sites nothing else
// exercises, so replacing dirperm.Library() with a literal at any of them
// fails this table instead of passing silently.
func TestPlacementModesWithClearedUmask(t *testing.T) {
	filePlacements := []struct {
		name string
		fn   func(src, dst string) error
	}{
		{"MoveFile", func(src, dst string) error { return MoveFile(src, dst) }},
		{"MoveFileCtx", func(src, dst string) error { return MoveFileCtx(context.Background(), src, dst) }},
		{"CopyFile", func(src, dst string) error { return CopyFile(src, dst) }},
		{"CopyFileCtx", func(src, dst string) error { return CopyFileCtx(context.Background(), src, dst) }},
		{"HardlinkFile", func(src, dst string) error { return HardlinkFile(src, dst) }},
		{"dropPlaceFile", func(src, dst string) error { return dropPlaceFile(context.Background(), src, dst, "copy") }},
		{"StagedImport", func(src, dst string) error {
			_, commit, rollback, err := StagedImport(context.Background(), "move", src, dst)
			if err != nil {
				return err
			}
			if commit != nil {
				return commit()
			}
			if rollback != nil {
				rollback()
			}
			return nil
		}},
	}
	dirPlacements := []struct {
		name string
		fn   func(src, dst string) error
	}{
		{"MoveDir", func(src, dst string) error { return MoveDir(src, dst) }},
		{"MoveDirCtx", func(src, dst string) error { return MoveDirCtx(context.Background(), src, dst) }},
		{"CopyDirCtx", func(src, dst string) error { return CopyDirCtx(context.Background(), src, dst) }},
		{"HardlinkDir", func(src, dst string) error { return HardlinkDir(src, dst) }},
		{"CopyDirMergeCtx", func(src, dst string) error {
			_, err := CopyDirMergeCtx(context.Background(), src, dst)
			return err
		}},
		{"HardlinkDirMerge", func(src, dst string) error {
			_, err := HardlinkDirMerge(src, dst)
			return err
		}},
		{"MoveDirMergeCtx", func(src, dst string) error {
			_, err := MoveDirMergeCtx(context.Background(), src, dst)
			return err
		}},
	}

	withClearedUmaskAndMode(t, func() {
		for _, tc := range filePlacements {
			t.Run(tc.name, func(t *testing.T) {
				base := t.TempDir()
				src := filepath.Join(base, "downloads", "book.epub")
				writeMoveFile(t, src, "content")
				authorDir := filepath.Join(base, "library", "Sarah J. Maas")
				dst := filepath.Join(authorDir, "Book (2013)", "book.epub")
				if err := tc.fn(src, dst); err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				assertAllDirsMode(t, authorDir, 0o755)
			})
		}

		for _, tc := range dirPlacements {
			t.Run(tc.name, func(t *testing.T) {
				base := t.TempDir()
				// A nested source tree drives the os.Root.Mkdir arms: the
				// walkers must create src's subdirectories at the
				// destination with the configured mode. Note the move
				// helpers' rename fast path carries the source dir's mode
				// through, so for those the parent directory (created by
				// MkdirAll) is the assertion point.
				src := filepath.Join(base, "downloads", "book")
				writeMoveFile(t, filepath.Join(src, "part01", "audio.m4b"), "audio")
				writeMoveFile(t, filepath.Join(src, "book.epub"), "content")
				authorDir := filepath.Join(base, "library", "Sarah J. Maas")
				dst := filepath.Join(authorDir, "Book (2013)")
				if err := tc.fn(src, dst); err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				assertAllDirsMode(t, authorDir, 0o755)
			})
		}
	})
}

// TestDropAudiobookHonorsDirMode covers the drop handoff's per-file branch:
// with explicit files in sibling subdirectories of the download root,
// resolveAudiobookSource falls through to per-file placement, so
// dropPlaceAudiobook's own MkdirAll (not the placement helpers') creates the
// destination directories.
func TestDropAudiobookHonorsDirMode(t *testing.T) {
	withClearedUmaskAndMode(t, func() {
		s, _, _, _, _, _, _, ctx := dropFixture(t, models.MediaTypeAudiobook)
		downloadPath := t.TempDir()
		first := filepath.Join(downloadPath, "Part 1", "track01.mp3")
		second := filepath.Join(downloadPath, "Part 2", "track02.mp3")
		writeMoveFile(t, first, "audio")
		writeMoveFile(t, second, "audio")
		bookFiles := discoverBookFiles(ctx, downloadPath, []string{first, second})
		destDir := filepath.Join(t.TempDir(), "Author A", "Title T")
		if err := s.dropPlaceAudiobook(ctx, downloadPath, bookFiles, []string{first, second}, destDir, "copy"); err != nil {
			t.Fatalf("dropPlaceAudiobook: %v", err)
		}
		assertAllDirsMode(t, destDir, 0o755)
	})
}

// TestTryImportInternalHonorsDirMode pins the two audiobook MkdirAll sites
// inside tryImportInternal that no other test reaches: the per-file branch
// (explicit files resolving to per-file placement) and the single-file-source
// branch. Both run a full managed import with the umask cleared and
// BINDERY_DIR_MODE=0755, then check every directory placed in the audiobook
// root.
func TestTryImportInternalHonorsDirMode(t *testing.T) {
	tests := []struct {
		name           string
		files          []string // relative paths under the download root
		explicitFiles  bool
		downloadIsFile bool // the download path is the m4b itself
	}{
		{"per-file placement", []string{"Part 1/track01.mp3", "Part 2/track02.mp3"}, true, false},
		{"single-file source", []string{"audiobook.m4b"}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoutingFixture(t, models.MediaTypeAudiobook, map[string]string{
				"import.mode": "copy",
			})
			withClearedUmaskAndMode(t, func() {
				downloadPath := t.TempDir()
				var explicit []string
				for _, rel := range tc.files {
					p := filepath.Join(downloadPath, rel)
					writeMoveFile(t, p, "audio")
					if tc.explicitFiles {
						explicit = append(explicit, p)
					}
				}
				if tc.downloadIsFile {
					// A single-file download: the download path is the m4b
					// itself, so the audiobook source is a file rather than
					// a folder and the import takes the single-file branch.
					downloadPath = filepath.Join(downloadPath, tc.files[0])
				}
				dl := newPairDownload(t, f.downloads, f.book, models.MediaTypeAudiobook, f.ctx)
				f.s.tryImportInternal(f.ctx, dl, downloadPath, "", "", "", nil, explicit)
				assertAllDirsMode(t, f.abDir, 0o755)
			})
		})
	}
}

// TestFlattenAudiobookDirNamedHonorsDirMode pins the flattener's MkdirAll.
func TestFlattenAudiobookDirNamedHonorsDirMode(t *testing.T) {
	withClearedUmaskAndMode(t, func() {
		base := t.TempDir()
		src := filepath.Join(base, "downloads", "book")
		writeMoveFile(t, filepath.Join(src, "track01.mp3"), "audio")
		writeMoveFile(t, filepath.Join(src, "track02.mp3"), "audio")
		destDir := filepath.Join(base, "library", "Sarah J. Maas", "Book (2013)")
		err := flattenAudiobookDirNamed(context.Background(), "copy", src, destDir, func(index, count int, ext string) string {
			return fmt.Sprintf("track%02d%s", index+1, ext)
		})
		if err != nil {
			t.Fatalf("flattenAudiobookDirNamed: %v", err)
		}
		assertAllDirsMode(t, destDir, 0o755)
	})
}

// TestWriteOPFSidecarFileHonorsDirMode pins the sidecar writer's MkdirAll.
func TestWriteOPFSidecarFileHonorsDirMode(t *testing.T) {
	withClearedUmaskAndMode(t, func() {
		base := t.TempDir()
		roots := []string{filepath.Join(base, "library")}
		bookDir := filepath.Join(base, "library", "Brandon Sanderson", "The Way of Kings (2020)")
		book, author, edition := fullBookFixture()
		if err := WriteOPFSidecarFile(bookDir, roots, book, author, edition, "", ""); err != nil {
			t.Fatalf("WriteOPFSidecarFile: %v", err)
		}
		assertAllDirsMode(t, bookDir, 0o755)
	})
}

// TestMoveTrackedFileHonorsDirMode pins the reorganize move's MkdirAll.
func TestMoveTrackedFileHonorsDirMode(t *testing.T) {
	withClearedUmaskAndMode(t, func() {
		s, _, _, _, libraryDir, _, _, ctx := dropFixture(t, models.MediaTypeEbook)
		src := filepath.Join(libraryDir, "existing", "book.epub")
		writeMoveFile(t, src, "content")
		file := models.BookFile{Path: src, Format: "ebook"}
		dest := filepath.Join(libraryDir, "Sarah J. Maas", "Book (2013)", "book.epub")
		if err := s.moveTrackedFile(ctx, file, dest); err != nil {
			t.Fatalf("moveTrackedFile: %v", err)
		}
		assertAllDirsMode(t, filepath.Dir(dest), 0o755)
	})
}
