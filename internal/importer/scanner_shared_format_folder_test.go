package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// sharedFormatFixture is formatScopeFixture's sibling (see
// scanner_format_scope_test.go), extended with a SettingsRepo so tests can
// pin import.mode and exercise all three transfer modes (hardlink, copy,
// move) rather than whatever effectiveConfiguredMode's auto-probe happens
// to pick.
func sharedFormatFixture(t *testing.T, sharedDir string) (
	s *Scanner,
	book *models.Book,
	dlRepo *db.DownloadRepo,
	bookRepo *db.BookRepo,
	settingsRepo *db.SettingsRepo,
	database *sql.DB,
	ctx context.Context,
) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx = context.Background()
	bookRepo = db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)
	dlRepo = db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	settingsRepo = db.NewSettingsRepo(database)

	s = NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, sharedDir, sharedDir, "", "", "").WithSettings(settingsRepo)

	author := &models.Author{ForeignID: "OL-1959A", Name: "Jordan B. Peterson", SortName: "Peterson, Jordan B."}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book = &models.Book{
		ForeignID: "OL-1959W",
		AuthorID:  author.ID,
		Title:     "We Who Wrestle with God",
		Status:    models.BookStatusWanted,
		MediaType: models.MediaTypeBoth,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return s, book, dlRepo, bookRepo, settingsRepo, database, ctx
}

// TestTryImportInternal_AudiobookMergesIntoExistingEbookFolder reproduces
// #1959: when a book's ebook and audiobook are imported at different times
// and the library/audiobook roots point at the same path (the default
// shared-folder layout — see docs/DEPLOYMENT.md's Storyteller section, in
// effect whenever BINDERY_AUDIOBOOK_DIR is left unset), the audiobook import
// must land inside the SAME folder the ebook already created, not a sibling
// "Title (2)" folder produced by UniqueDir treating the collision as an
// unrelated book. Table-driven across all three transfer modes: the fix
// takes a different code path per mode (CopyDirMergeCtx, HardlinkDirMerge,
// MoveDirMergeCtx), so each needs its own exercise.
func TestTryImportInternal_AudiobookMergesIntoExistingEbookFolder(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			sharedDir := t.TempDir()
			s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, sharedDir)
			if err := settingsRepo.Set(ctx, "import.mode", mode); err != nil {
				t.Fatal(err)
			}

			// Import the ebook first — this creates the book's real folder.
			ebookDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(ebookDownloadDir, "book.epub"), []byte("epub-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			ebookDL := &models.Download{
				GUID:   "guid-1959-ebook-" + mode,
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
			var ebookDir string
			for _, f := range files {
				if f.Format == models.MediaTypeEbook {
					ebookDir = filepath.Dir(f.Path)
				}
			}
			if ebookDir == "" {
				t.Fatal("precondition: no ebook file recorded after ebook import")
			}

			// Now import the audiobook for the SAME book — this is the
			// collision #1959 describes.
			audioDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			audioDL := &models.Download{
				GUID:    "guid-1959-audiobook-" + mode,
				Title:   "We Who Wrestle with God [M4B]",
				BookID:  &book.ID,
				Status:  models.StateCompleted,
				Quality: "m4b",
			}
			if err := dlRepo.Create(ctx, audioDL); err != nil {
				t.Fatal(err)
			}
			s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)

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
			var audiobookDir string
			for _, f := range files {
				if f.Format == models.MediaTypeAudiobook {
					audiobookDir = f.Path // audiobook rows store the directory itself, not a file inside it
				}
			}
			if audiobookDir == "" {
				t.Fatal("no audiobook file recorded after audiobook import")
			}

			if audiobookDir != ebookDir {
				t.Errorf("#1959 regression (mode=%s): audiobook landed in %q, ebook is in %q — want the same shared folder, not a split \"(2)\" sibling",
					mode, audiobookDir, ebookDir)
			}

			// Both files must actually be on disk together in that one
			// folder. The ebook importer renames per the naming template
			// ({Title} - {Author}.ext, not the source's original
			// "book.epub"), so check by suffix.
			entries, err := os.ReadDir(audiobookDir)
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
				t.Errorf("no .epub file found in %q — the ebook should still be there after the audiobook merge", audiobookDir)
			}
			if !foundM4b {
				t.Errorf("no .m4b file found in %q after the merge", audiobookDir)
			}

			// move mode is destructive to the source on success (no skips),
			// so confirm the ebook download's source directory doesn't
			// somehow still hold the audiobook's un-transferred source too.
			if mode == "move" {
				if _, err := os.Stat(audioDownloadDir); !os.IsNotExist(err) {
					t.Errorf("move mode: audiobook source %q should have been removed after a clean (no-skip) merge, stat err = %v", audioDownloadDir, err)
				}
			}
		})
	}
}

// TestTryImportInternal_AudiobookMergeSkipsSameNamedFile covers the other
// half of the #1959 fix: when both formats independently carry a same-named
// file (e.g. each drops its own cover.jpg), the merge must skip — not
// overwrite — the colliding file, and the import must still succeed rather
// than fail outright. Table-driven across all three transfer modes for the
// same reason as the merge test above.
func TestTryImportInternal_AudiobookMergeSkipsSameNamedFile(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
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
				GUID:   "guid-1959b-ebook-" + mode,
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
			var ebookDir string
			for _, f := range files {
				if f.Format == models.MediaTypeEbook {
					ebookDir = filepath.Dir(f.Path)
				}
			}
			// Ebook import places only the single .epub, not a sidecar
			// cover — so to exercise the merge's per-file skip logic,
			// place a file directly in the book's folder to simulate one
			// already being there. The skip check is purely filesystem-
			// level (does a same-named file already exist at the
			// destination), so it doesn't matter that this wasn't placed
			// by an ebook import specifically.
			ebookCoverPath := filepath.Join(ebookDir, "cover.jpg")
			ebookCoverBytes := []byte("ebook-cover")
			if err := os.WriteFile(ebookCoverPath, ebookCoverBytes, 0o644); err != nil {
				t.Fatal(err)
			}

			audioDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			audiobookCoverBytes := []byte("a completely different audiobook cover")
			if err := os.WriteFile(filepath.Join(audioDownloadDir, "cover.jpg"), audiobookCoverBytes, 0o644); err != nil {
				t.Fatal(err)
			}
			audioDL := &models.Download{
				GUID:    "guid-1959b-audiobook-" + mode,
				Title:   "We Who Wrestle with God [M4B]",
				BookID:  &book.ID,
				Status:  models.StateCompleted,
				Quality: "m4b",
			}
			if err := dlRepo.Create(ctx, audioDL); err != nil {
				t.Fatal(err)
			}
			s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)

			gotAudio, err := dlRepo.GetByGUID(ctx, audioDL.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if gotAudio.Status != models.StateImported {
				t.Fatalf("audiobook import status = %q, want %q (error: %s) — a same-named-file collision during a merge must be skipped, not fail the whole import",
					gotAudio.Status, models.StateImported, gotAudio.ErrorMessage)
			}

			// The ebook's cover.jpg must be untouched — the audiobook's
			// differently-content cover must have been skipped, not
			// overwritten.
			gotCover, err := os.ReadFile(ebookCoverPath)
			if err != nil {
				t.Fatalf("cover.jpg missing after merge: %v", err)
			}
			if string(gotCover) != string(ebookCoverBytes) {
				t.Errorf("cover.jpg was overwritten by the audiobook's own cover during the merge — same-named files must be skipped, not clobbered")
			}

			// move mode must NOT delete the audiobook source when
			// something was skipped — the skipped cover.jpg's only copy
			// still lives there.
			if mode == "move" {
				if _, err := os.Stat(filepath.Join(audioDownloadDir, "cover.jpg")); err != nil {
					t.Errorf("move mode: skipped cover.jpg's source copy was removed (source dir deleted despite a skip) — its only copy is now lost: %v", err)
				}
			}

			// The skip must be recorded on the History event, not silently
			// dropped — Bindery has no active-notification channel for
			// this, so History (and the notifier payload, exercised
			// implicitly via the same code path) is the only place a user
			// could ever discover it happened.
			hist, err := s.history.ListByBook(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, h := range hist {
				if h.EventType == models.HistoryEventBookImported && strings.Contains(h.Data, "cover.jpg") {
					found = true
				}
			}
			if !found {
				t.Errorf("no history event recorded the skipped cover.jpg — a merge skip must be discoverable, not silent")
			}
		})
	}
}

// TestTryImportInternal_AudiobookMergeIntoExistingSubdirectory covers a
// multi-file audiobook (a real, common case — see the "multi-part m4b/mp3
// files, cover art, and cue sheets" comment on the audiobook import branch)
// merging into a folder that already has a same-named *subdirectory*, not
// just a same-named file. The merge walkers must recurse into it (treating
// an existing directory as "already exists, descend and merge its
// contents") rather than skip or fail the whole subtree.
func TestTryImportInternal_AudiobookMergeIntoExistingSubdirectory(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
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
				GUID:   "guid-1959c-ebook-" + mode,
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
			var ebookDir string
			for _, f := range files {
				if f.Format == models.MediaTypeEbook {
					ebookDir = filepath.Dir(f.Path)
				}
			}
			// Simulate a pre-existing "extras" subdirectory in the book's
			// folder (e.g. left over from a prior partial import), holding
			// a file the incoming audiobook's own "extras" subdir does not
			// carry.
			existingExtrasDir := filepath.Join(ebookDir, "extras")
			if err := os.MkdirAll(existingExtrasDir, 0o750); err != nil {
				t.Fatal(err)
			}
			preExistingBytes := []byte("liner-notes")
			if err := os.WriteFile(filepath.Join(existingExtrasDir, "liner-notes.txt"), preExistingBytes, 0o644); err != nil {
				t.Fatal(err)
			}

			audioDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			audioExtrasDir := filepath.Join(audioDownloadDir, "extras")
			if err := os.MkdirAll(audioExtrasDir, 0o750); err != nil {
				t.Fatal(err)
			}
			newBytes := []byte("track-list")
			if err := os.WriteFile(filepath.Join(audioExtrasDir, "tracklist.txt"), newBytes, 0o644); err != nil {
				t.Fatal(err)
			}
			audioDL := &models.Download{
				GUID:    "guid-1959c-audiobook-" + mode,
				Title:   "We Who Wrestle with God [M4B]",
				BookID:  &book.ID,
				Status:  models.StateCompleted,
				Quality: "m4b",
			}
			if err := dlRepo.Create(ctx, audioDL); err != nil {
				t.Fatal(err)
			}
			s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)

			gotAudio, err := dlRepo.GetByGUID(ctx, audioDL.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if gotAudio.Status != models.StateImported {
				t.Fatalf("audiobook import status = %q, want %q (error: %s) — merging into a folder with a pre-existing same-named subdirectory must succeed by recursing into it",
					gotAudio.Status, models.StateImported, gotAudio.ErrorMessage)
			}

			// The pre-existing file in the subdirectory must survive
			// untouched, and the audiobook's own new file in that same
			// subdirectory must have been added alongside it — the
			// recursive merge, not a skip of the whole subtree.
			gotPreExisting, err := os.ReadFile(filepath.Join(existingExtrasDir, "liner-notes.txt"))
			if err != nil {
				t.Fatalf("pre-existing liner-notes.txt lost after merge: %v", err)
			}
			if string(gotPreExisting) != string(preExistingBytes) {
				t.Errorf("pre-existing liner-notes.txt content changed during merge")
			}
			gotNew, err := os.ReadFile(filepath.Join(existingExtrasDir, "tracklist.txt"))
			if err != nil {
				t.Fatalf("audiobook's own tracklist.txt was not merged into the existing extras/ subdirectory: %v", err)
			}
			if string(gotNew) != string(newBytes) {
				t.Errorf("tracklist.txt content mismatch after merge")
			}
		})
	}
}

// TestTryImportInternal_AudiobookDoesNotMergeIntoStaleEbookRow covers the
// case where the book still has an ebook book_files row but the file itself
// is gone from disk (deleted by hand, or on a since-unmounted volume).
// There is nothing to merge into, so the audiobook must be placed the
// ordinary way rather than the import being pointed at a folder on the
// strength of a database row alone.
func TestTryImportInternal_AudiobookDoesNotMergeIntoStaleEbookRow(t *testing.T) {
	sharedDir := t.TempDir()
	s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, sharedDir)
	if err := settingsRepo.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	ebookDownloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ebookDownloadDir, "book.epub"), []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	ebookDL := &models.Download{
		GUID:   "guid-1959e-ebook",
		Title:  "We Who Wrestle with God",
		BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, ebookDL); err != nil {
		t.Fatal(err)
	}
	s.tryImportInternal(ctx, ebookDL, ebookDownloadDir, "", "", "", nil, nil)

	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ebookPath, ebookDir string
	for _, f := range files {
		if f.Format == models.MediaTypeEbook {
			ebookPath = f.Path
			ebookDir = filepath.Dir(f.Path)
		}
	}
	if ebookPath == "" {
		t.Fatal("precondition: no ebook file recorded after ebook import")
	}
	// Delete the epub but leave its book_files row and the folder behind.
	if err := os.Remove(ebookPath); err != nil {
		t.Fatal(err)
	}

	audioDownloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	audioDL := &models.Download{
		GUID:    "guid-1959e-audiobook",
		Title:   "We Who Wrestle with God [M4B]",
		BookID:  &book.ID,
		Status:  models.StateCompleted,
		Quality: "m4b",
	}
	if err := dlRepo.Create(ctx, audioDL); err != nil {
		t.Fatal(err)
	}
	s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)

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
	var audiobookDir string
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			audiobookDir = f.Path
		}
	}
	if audiobookDir == ebookDir {
		t.Errorf("audiobook merged into %q on the strength of a stale ebook row whose file no longer exists", audiobookDir)
	}
}

// TestTryImportInternal_AudiobookTemplateFlattenDoesNotMerge pins the
// deliberate limit of the #1959 fix: the per-file naming-template flatten
// path keeps the historical UniqueDir behaviour and still splits into a
// "Title (2)" sibling, rather than merging into the book's existing shared
// folder.
//
// This is not an oversight to be "fixed" by handing flattenAudiobookDirNamed
// the existing folder: it documents that it creates destDir itself, and its
// rollback removes destDir wholesale (os.RemoveAll) on any placement error.
// Pointed at a shared folder that would delete the co-located ebook. Merging
// here needs that rollback reworked to remove only what it placed; until
// then this asserts the import still SUCCEEDS (split, as today) rather than
// failing or destroying the ebook.
func TestTryImportInternal_AudiobookTemplateFlattenDoesNotMerge(t *testing.T) {
	sharedDir := t.TempDir()
	s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, sharedDir)
	if err := settingsRepo.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, "naming.audiobook_file_template", "{Title} - Part {Part:3}.{ext}"); err != nil {
		t.Fatal(err)
	}

	ebookDownloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ebookDownloadDir, "book.epub"), []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	ebookDL := &models.Download{
		GUID:   "guid-1959d-ebook",
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
	var ebookPath, ebookDir string
	for _, f := range files {
		if f.Format == models.MediaTypeEbook {
			ebookPath = f.Path
			ebookDir = filepath.Dir(f.Path)
		}
	}
	if ebookDir == "" {
		t.Fatal("precondition: no ebook file recorded after ebook import")
	}

	audioDownloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	audioDL := &models.Download{
		GUID:    "guid-1959d-audiobook",
		Title:   "We Who Wrestle with God [M4B]",
		BookID:  &book.ID,
		Status:  models.StateCompleted,
		Quality: "m4b",
	}
	if err := dlRepo.Create(ctx, audioDL); err != nil {
		t.Fatal(err)
	}
	s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)

	gotAudio, err := dlRepo.GetByGUID(ctx, audioDL.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAudio.Status != models.StateImported {
		t.Fatalf("audiobook import status = %q, want %q (error: %s) — the template-flatten path must keep working when the ebook is co-located, even though it does not merge",
			gotAudio.Status, models.StateImported, gotAudio.ErrorMessage)
	}

	// The ebook must be untouched — flatten's RemoveAll rollback must never
	// have been pointed at the shared folder.
	if _, err := os.Stat(ebookPath); err != nil {
		t.Fatalf("the co-located ebook was destroyed by the audiobook import: %v", err)
	}

	files, err = bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var audiobookDir string
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			audiobookDir = f.Path
		}
	}
	if audiobookDir == "" {
		t.Fatal("no audiobook file recorded after audiobook import")
	}
	if audiobookDir == ebookDir {
		t.Errorf("template-flatten unexpectedly merged into %q — if flatten was made merge-aware, its os.RemoveAll rollback must have been reworked first; see this test's doc comment",
			audiobookDir)
	}
}

// TestTryImportInternal_MergeRollbackDoesNotDeleteEbook covers the hazard
// vavallee spotted in review: when the merge reassigns destDir to the book's
// existing folder, the post-placement rollback on a SetFormatFilePath failure
// was still os.RemoveAll(destDir) for copy/hardlink, which deletes the ebook
// sitting in that shared folder.
//
// The database is flipped read-only after the ebook import so the audiobook's
// book_files write fails all three attempts, which is what drives that
// rollback. The import is expected to fail; what must NOT happen is the ebook
// being destroyed on the way out.
func TestTryImportInternal_MergeRollbackDoesNotDeleteEbook(t *testing.T) {
	for _, mode := range []string{"copy", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			sharedDir := t.TempDir()
			s, book, dlRepo, bookRepo, settingsRepo, database, ctx := sharedFormatFixture(t, sharedDir)
			if err := settingsRepo.Set(ctx, "import.mode", mode); err != nil {
				t.Fatal(err)
			}

			ebookDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(ebookDownloadDir, "book.epub"), []byte("epub-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			ebookDL := &models.Download{
				GUID:   "guid-1959f-ebook-" + mode,
				Title:  "We Who Wrestle with God",
				BookID: &book.ID,
				Status: models.StateCompleted,
			}
			if err := dlRepo.Create(ctx, ebookDL); err != nil {
				t.Fatal(err)
			}
			s.tryImportInternal(ctx, ebookDL, ebookDownloadDir, "", "", "", nil, nil)

			files, err := bookRepo.ListFiles(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			var ebookPath string
			for _, f := range files {
				if f.Format == models.MediaTypeEbook {
					ebookPath = f.Path
				}
			}
			if ebookPath == "" {
				t.Fatal("precondition: no ebook file recorded after ebook import")
			}
			if _, err := os.Stat(ebookPath); err != nil {
				t.Fatalf("precondition: ebook not on disk: %v", err)
			}

			audioDownloadDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(audioDownloadDir, "book.m4b"), []byte("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			audioDL := &models.Download{
				GUID:    "guid-1959f-audiobook-" + mode,
				Title:   "We Who Wrestle with God [M4B]",
				BookID:  &book.ID,
				Status:  models.StateCompleted,
				Quality: "m4b",
			}
			if err := dlRepo.Create(ctx, audioDL); err != nil {
				t.Fatal(err)
			}

			// Force every SetFormatFilePath attempt to fail.
			if _, err := database.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
				t.Fatal(err)
			}
			s.tryImportInternal(ctx, audioDL, audioDownloadDir, "", "", "", nil, nil)
			if _, err := database.ExecContext(ctx, "PRAGMA query_only = OFF"); err != nil {
				t.Fatal(err)
			}

			// The import is expected to have failed. The ebook must survive it.
			if _, err := os.Stat(ebookPath); err != nil {
				t.Fatalf("the co-located ebook was deleted by the failed audiobook import's rollback: %v", err)
			}
			if got, err := os.ReadFile(ebookPath); err != nil || string(got) != "epub-bytes" {
				t.Fatalf("ebook content damaged after rollback: %q err=%v", got, err)
			}
		})
	}
}

// TestExistingEbookDir_NilBook covers the documented guard: a download with no
// book attached has no folder to merge into, so the caller must fall through
// to ordinary placement rather than dereferencing nil.
func TestExistingEbookDir_NilBook(t *testing.T) {
	sharedDir := t.TempDir()
	s, _, _, _, _, _, ctx := sharedFormatFixture(t, sharedDir)
	if dir, ok := s.existingEbookDir(ctx, nil); ok || dir != "" {
		t.Errorf("existingEbookDir(nil) = (%q, %v), want (\"\", false)", dir, ok)
	}
}
