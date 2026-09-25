package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// importEbookThenAudiobookFile imports an ebook for the fixture's book, then
// imports an audiobook whose source is a single FILE (a lone .m4b, what a
// manual import of one audiobook file and a single-file torrent both resolve
// to). It returns the folder the ebook landed in and the path recorded for the
// audiobook.
func importEbookThenAudiobookFile(t *testing.T, mode, audioName string, seedShared func(bookDir string)) (ebookDir, audiobookPath string) {
	t.Helper()
	sharedDir := t.TempDir()
	s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, sharedDir)
	if err := settingsRepo.Set(ctx, "import.mode", mode); err != nil {
		t.Fatal(err)
	}

	ebookDownloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ebookDownloadDir, "book.epub"), []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	ebookDL := &models.Download{
		GUID:   "guid-2686-ebook-" + t.Name(),
		Title:  "We Who Wrestle with God",
		BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, ebookDL); err != nil {
		t.Fatal(err)
	}
	s.tryImportInternal(ctx, ebookDL, ebookDownloadDir, "", "", "", nil, nil)
	gotEbook, err := dlRepo.GetByGUID(ctx, ebookDL.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if gotEbook.Status != models.StateImported {
		t.Fatalf("precondition: ebook import status = %q, want %q (error: %s)",
			gotEbook.Status, models.StateImported, gotEbook.ErrorMessage)
	}
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Format == models.MediaTypeEbook {
			ebookDir = filepath.Dir(f.Path)
		}
	}
	if ebookDir == "" {
		t.Fatal("precondition: no ebook file recorded after ebook import")
	}
	if seedShared != nil {
		seedShared(ebookDir)
	}

	// The audiobook source is the FILE itself, not a folder holding it.
	audioDownloadDir := t.TempDir()
	audioSrc := filepath.Join(audioDownloadDir, audioName)
	if err := os.WriteFile(audioSrc, []byte("m4b-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	audioDL := &models.Download{
		GUID:    "guid-2686-audiobook-" + t.Name(),
		Title:   "We Who Wrestle with God [M4B]",
		BookID:  &book.ID,
		Status:  models.StateCompleted,
		Quality: "m4b",
	}
	if err := dlRepo.Create(ctx, audioDL); err != nil {
		t.Fatal(err)
	}
	s.ImportFromPath(ctx, audioDL, audioSrc, "")

	gotAudio, err := dlRepo.GetByGUID(ctx, audioDL.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAudio.Status != models.StateImported {
		t.Fatalf("audiobook import status = %q, want %q (error: %s)",
			gotAudio.Status, models.StateImported, gotAudio.ErrorMessage)
	}
	files, err = bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			audiobookPath = f.Path // audiobook rows store the directory itself
		}
	}
	if audiobookPath == "" {
		t.Fatal("no audiobook file recorded after audiobook import")
	}
	return ebookDir, audiobookPath
}

// TestTryImportInternal_SingleFileAudiobookMergesIntoExistingEbookFolder is
// the #2686 regression. #1959 made an ebook and an audiobook for one book
// share a folder under the shared-folder layout, but wired the merge into the
// folder-source branch only. An audiobook whose source is a single file, the
// ordinary shape of a lone .m4b, still went through UniqueDir and landed in a
// sibling "Title (2)", which is the folder the book then recorded. Covered in
// all three transfer modes because each places the file differently.
func TestTryImportInternal_SingleFileAudiobookMergesIntoExistingEbookFolder(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			ebookDir, audiobookPath := importEbookThenAudiobookFile(t, mode, "book.m4b", nil)
			if audiobookPath != ebookDir {
				t.Errorf("#2686 regression (mode=%s): audiobook recorded at %q, ebook is in %q, want the same shared folder rather than a \"(2)\" sibling",
					mode, audiobookPath, ebookDir)
			}
			entries, err := os.ReadDir(ebookDir)
			if err != nil {
				t.Fatal(err)
			}
			var foundEpub, foundM4b bool
			for _, e := range entries {
				switch {
				case strings.HasSuffix(e.Name(), ".epub"):
					foundEpub = true
				case strings.HasSuffix(e.Name(), ".m4b"):
					foundM4b = true
				}
			}
			if !foundEpub {
				t.Errorf("the ebook is gone from %q after the merge", ebookDir)
			}
			if !foundM4b {
				t.Errorf("no .m4b in %q after the merge", ebookDir)
			}
			// Nothing may be left beside the shared folder.
			parent := filepath.Dir(ebookDir)
			siblings, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, sib := range siblings {
				if filepath.Join(parent, sib.Name()) != ebookDir {
					t.Errorf("unexpected sibling folder %q beside the shared folder", sib.Name())
				}
			}
		})
	}
}

// TestTryImportInternal_SingleFileAudiobookMergeSkipsSameNamedFile is the
// other half: a merge must never overwrite a file already in the book's
// folder. The import still succeeds, the existing bytes survive, the skip is
// recorded on History, and move mode leaves the source alone because the
// skipped file's copy there is then the only one.
func TestTryImportInternal_SingleFileAudiobookMergeSkipsSameNamedFile(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			existing := []byte("an entirely different m4b that was already here")
			var seeded string
			ebookDir, audiobookPath := importEbookThenAudiobookFile(t, mode, "book.m4b", func(bookDir string) {
				seeded = filepath.Join(bookDir, "book.m4b")
				if err := os.WriteFile(seeded, existing, 0o644); err != nil {
					t.Fatal(err)
				}
			})
			if audiobookPath != ebookDir {
				t.Errorf("audiobook recorded at %q, want the shared folder %q", audiobookPath, ebookDir)
			}
			got, err := os.ReadFile(seeded)
			if err != nil {
				t.Fatalf("the file already in the shared folder is gone: %v", err)
			}
			if string(got) != string(existing) {
				t.Errorf("a merge overwrote a file already in the book's folder, it must be skipped instead")
			}
		})
	}
}

// TestTryImportInternal_SingleFileAudiobookWithoutEbookKeepsUniqueDir is the
// boundary in the other direction: with no ebook of this book's in the way,
// an unrelated folder already sitting at the destination is still NOT merged
// into. That collision is a different book and UniqueDir must still split it.
func TestTryImportInternal_SingleFileAudiobookWithoutEbookKeepsUniqueDir(t *testing.T) {
	sharedDir := t.TempDir()
	s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, sharedDir)
	if err := settingsRepo.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	// Work out where the audiobook would go, then occupy it with a folder that
	// has nothing to do with this book.
	seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
	author, err := s.authors.GetByID(ctx, book.AuthorID)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := s.destRenamer(ctx).AudiobookDestDir(sharedDir, author, book, seriesTitle, seriesNum)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	strangerBytes := []byte("someone else's book")
	if err := os.WriteFile(filepath.Join(dest, "stranger.epub"), strangerBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	audioDownloadDir := t.TempDir()
	audioSrc := filepath.Join(audioDownloadDir, "book.m4b")
	if err := os.WriteFile(audioSrc, []byte("m4b-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	audioDL := &models.Download{
		GUID:    "guid-2686-stranger",
		Title:   "We Who Wrestle with God [M4B]",
		BookID:  &book.ID,
		Status:  models.StateCompleted,
		Quality: "m4b",
	}
	if err := dlRepo.Create(ctx, audioDL); err != nil {
		t.Fatal(err)
	}
	s.ImportFromPath(ctx, audioDL, audioSrc, "")

	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var audiobookPath string
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			audiobookPath = f.Path
		}
	}
	if audiobookPath == "" {
		t.Fatal("no audiobook recorded")
	}
	if audiobookPath == filepath.Clean(dest) {
		t.Errorf("the audiobook merged into %q, which holds an unrelated book; only this book's own ebook folder may be merged into", dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "book.m4b")); err == nil {
		t.Errorf("the audiobook was placed inside an unrelated book's folder %q", dest)
	}
}
