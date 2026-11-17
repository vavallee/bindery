package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// handoffFixture is a real importer behind the manual import handler, set up
// the way the dev cluster was when the re-offer was found: External mode, a
// drop folder, flat layout, copy placement, and an intake folder scanned with
// From a folder.
type handoffFixture struct {
	h         *ManualImportHandler
	scanner   *importer.Scanner
	downloads *db.DownloadRepo
	settings  *db.SettingsRepo
	book      *models.Book
	intake    string
	dock      string
}

func newHandoffFixture(t *testing.T, mode string) *handoffFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	downloads := db.NewDownloadRepo(database)
	settings := db.NewSettingsRepo(database)

	author := &models.Author{ForeignID: "OL-MOBY-A", Name: "Herman Melville", SortName: "Melville, Herman", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-MOBY-B", AuthorID: author.ID, Title: "Moby Dick", SortTitle: "moby dick",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
		MetadataProvider: "openlibrary", Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	base := resolvedTempDir(t)
	library := filepath.Join(base, "library")
	intake := filepath.Join(base, "downloads")
	dock := filepath.Join(base, "book-dock")
	for _, d := range []string{library, intake, dock} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	kv := map[string]string{"import.mode": mode}
	if mode == "external" {
		kv["import.drop_folder"] = dock
		kv["import.drop_layout"] = "flat"
		kv["import.drop_link_mode"] = "copy"
	}
	for k, v := range kv {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	scanner := importer.NewScanner(downloads, db.NewDownloadClientRepo(database), books, authors, db.NewHistoryRepo(database), library, "", "", "", "")
	scanner.WithSettings(settings)
	h := NewManualImportHandler(scanner, downloads, books).WithRoots(NewLibraryRoots(nil, base))
	return &handoffFixture{h: h, scanner: scanner, downloads: downloads, settings: settings, book: book, intake: intake, dock: dock}
}

func (f *handoffFixture) write(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(f.intake, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// importAndWait runs a manual import of src through the handler and waits for
// the download to settle in want.
func (f *handoffFixture) importAndWait(t *testing.T, src, format string, want models.DownloadState) models.Download {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"path": src, "bookId": f.book.ID, "format": format})
	rec := httptest.NewRecorder()
	f.h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(raw)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import status = %d, body = %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		all, err := f.downloads.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(all) == 1 && all[0].Status == want {
			return all[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	all, _ := f.downloads.List(context.Background())
	t.Fatalf("download did not settle in %q: %+v", want, all)
	return models.Download{}
}

func (f *handoffFixture) scan(t *testing.T, includeImported bool) map[string]ScanItem {
	t.Helper()
	req := scanRequest(f.intake)
	if includeImported {
		req = scanRequestIncludeImported(f.intake)
	}
	rec := httptest.NewRecorder()
	f.h.Scan(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ScanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]ScanItem, len(resp.Items))
	for _, it := range resp.Items {
		out[it.Path] = it
	}
	return out
}

func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

const mobyRel = "Moby Dick - Herman Melville [epub]/Moby Dick - Herman Melville.epub"

// TestManualImportScan_HidesSourceHandedOffToExternalTool is the dev cluster
// repro: a manual import in External mode copies the file into the drop
// folder and keeps the source, and a rescan used to list that source under
// Matched, ticked, so one click dropped a second copy.
func TestManualImportScan_HidesSourceHandedOffToExternalTool(t *testing.T) {
	f := newHandoffFixture(t, "external")
	src := f.write(t, mobyRel, "moby bytes")
	other := f.write(t, "Other Book/Other Book.epub", "other bytes")

	dl := f.importAndWait(t, src, models.MediaTypeEbook, models.StateImportExternal)
	if dl.ImportPath != src {
		t.Errorf("import_path = %q, want the handed off source %q", dl.ImportPath, src)
	}
	if got := filesIn(t, f.dock); len(got) != 1 {
		t.Fatalf("dock holds %v, want the one dropped copy", got)
	}

	items := f.scan(t, false)
	if it, ok := items[src]; ok {
		t.Fatalf("handed off source offered again: %+v", it)
	}
	if _, ok := items[other]; !ok {
		t.Errorf("a file that was never handed off is missing from the scan: %+v", items)
	}

	labelled := f.scan(t, true)
	it, ok := labelled[src]
	if !ok {
		t.Fatalf("includeImported should list the handed off source, got %+v", labelled)
	}
	if !it.AlreadyImported || !it.HandedOff {
		t.Errorf("handed off source = %+v, want AlreadyImported and HandedOff so it starts unticked", it)
	}
	if o := labelled[other]; o.AlreadyImported || o.HandedOff {
		t.Errorf("untouched file labelled %+v, want neither flag", o)
	}
}

// TestManualImportScan_HidesAudiobookFolderHandedOff covers a folder unit: the
// recorded source is the folder, and the scan lists the folder as one unit.
func TestManualImportScan_HidesAudiobookFolderHandedOff(t *testing.T) {
	f := newHandoffFixture(t, "external")
	f.write(t, "Moby Dick Audio/01.mp3", "track one")
	f.write(t, "Moby Dick Audio/02.mp3", "track two")
	folder := filepath.Join(f.intake, "Moby Dick Audio")

	f.importAndWait(t, folder, models.MediaTypeAudiobook, models.StateImportExternal)

	if it, ok := f.scan(t, false)[folder]; ok {
		t.Fatalf("handed off audiobook folder offered again: %+v", it)
	}
	if it := f.scan(t, true)[folder]; !it.HandedOff || !it.AlreadyImported {
		t.Errorf("audiobook folder = %+v, want labelled handed off", it)
	}
}

// TestManualImportScan_OffersSourceAgainOnceHandOffEnds: the source is hidden
// only while the hand-off is outstanding. A retried hand-off that then failed
// or was blocked keeps its recorded source but is no longer handed off, and a
// row removed from the queue records nothing at all, so each offers the
// source again as a normal match.
func TestManualImportScan_OffersSourceAgainOnceHandOffEnds(t *testing.T) {
	ctx := context.Background()
	step := func(t *testing.T, f *handoffFixture, id int64, states ...models.DownloadState) {
		t.Helper()
		for _, st := range states {
			if err := f.downloads.UpdateStatus(ctx, id, st); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := map[string]func(t *testing.T, f *handoffFixture, id int64){
		"retried and failed": func(t *testing.T, f *handoffFixture, id int64) {
			step(t, f, id, models.StateImportPending)
			if err := f.downloads.SetErrorWithStatus(ctx, id, models.StateImportFailed, "drop folder handoff failed"); err != nil {
				t.Fatal(err)
			}
		},
		"retried and blocked": func(t *testing.T, f *handoffFixture, id int64) {
			step(t, f, id, models.StateImportPending, models.StateImporting)
			if err := f.downloads.SetErrorWithStatus(ctx, id, models.StateImportBlocked, "drop folder destination invalid"); err != nil {
				t.Fatal(err)
			}
		},
		"removed from the queue": func(t *testing.T, f *handoffFixture, id int64) {
			if err := f.downloads.Delete(ctx, id); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFixture(t, "external")
			src := f.write(t, mobyRel, "moby bytes")
			dl := f.importAndWait(t, src, models.MediaTypeEbook, models.StateImportExternal)
			if _, ok := f.scan(t, false)[src]; ok {
				t.Fatal("source offered while the hand-off is outstanding")
			}

			end(t, f, dl.ID)

			it, ok := f.scan(t, false)[src]
			if !ok {
				t.Fatal("source not offered again after the hand-off ended")
			}
			if it.AlreadyImported || it.HandedOff {
				t.Errorf("re-offered source = %+v, want a plain match", it)
			}
		})
	}
}

// TestManualImportScan_OffersSourceAfterFailedDrop: a drop that could not be
// placed never handed anything off, so the source stays on offer.
func TestManualImportScan_OffersSourceAfterFailedDrop(t *testing.T) {
	f := newHandoffFixture(t, "external")
	// A file where the drop folder should be: nothing can be placed in it.
	blocker := filepath.Join(filepath.Dir(f.dock), "not-a-folder")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(context.Background(), "import.drop_folder", blocker); err != nil {
		t.Fatal(err)
	}
	src := f.write(t, mobyRel, "moby bytes")

	f.importAndWait(t, src, models.MediaTypeEbook, models.StateImportFailed)

	if it, ok := f.scan(t, false)[src]; !ok || it.AlreadyImported || it.HandedOff {
		t.Fatalf("source after a failed drop = %+v (listed %v), want a plain match", it, ok)
	}
}

// TestManualImportScan_AutoCopyStillHidesCopiedSource: Auto mode with copy
// placement is unchanged. The copied source is hidden because its book has
// the format on disk, and it is labelled already imported, not handed off.
func TestManualImportScan_AutoCopyStillHidesCopiedSource(t *testing.T) {
	f := newHandoffFixture(t, "copy")
	src := f.write(t, mobyRel, "moby bytes")

	f.importAndWait(t, src, models.MediaTypeEbook, models.StateImported)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("copy mode removed the source: %v", err)
	}

	if it, ok := f.scan(t, false)[src]; ok {
		t.Fatalf("copied source offered again in Auto mode: %+v", it)
	}
	it, ok := f.scan(t, true)[src]
	if !ok {
		t.Fatal("includeImported should list the copied source")
	}
	if !it.AlreadyImported || it.HandedOff {
		t.Errorf("copied source = %+v, want AlreadyImported and not HandedOff", it)
	}
}

// TestHandedOffIndex_FolderUnitNeedsEveryFileCovered: a folder unit whose own
// path was not recorded counts as handed off only when every importable file
// below it was, as directoryMatchesTracked does for book_files.
func TestHandedOffIndex_FolderUnitNeedsEveryFileCovered(t *testing.T) {
	root := resolvedTempDir(t)
	folder := filepath.Join(root, "Audio")
	one := filepath.Join(folder, "01.mp3")
	two := filepath.Join(folder, "CD2", "02.mp3")
	writeTestFile(t, one)
	writeTestFile(t, two)
	writeTestFile(t, filepath.Join(folder, "cover.jpg"))

	partial := &handedOffIndex{paths: map[string]struct{}{one: {}}}
	if partial.coversUnit(folder, true) {
		t.Error("folder with a track that was never handed off counted as handed off")
	}
	// A recorded folder does not cover what is inside it.
	viaFolder := &handedOffIndex{paths: map[string]struct{}{one: {}, filepath.Join(folder, "CD2"): {}}}
	if viaFolder.coversUnit(folder, true) {
		t.Error("a recorded subfolder covered the track inside it")
	}
	full := &handedOffIndex{paths: map[string]struct{}{one: {}, two: {}}}
	if !full.coversUnit(folder, true) {
		t.Error("folder whose every track was handed off should count as handed off")
	}
	if full.coversUnit(filepath.Join(root, "Other"), true) {
		t.Error("a folder with nothing recorded below it counted as handed off")
	}
	if full.coversUnit(filepath.Join(root, "loose.epub"), false) {
		t.Error("an unrelated file counted as handed off")
	}
	var empty *handedOffIndex
	if empty.covers(one) {
		t.Error("a nil index covered a path")
	}
}

// TestHandedOffSources_IgnoresBroadAndRelativePaths: a recorded path that is a
// library root or a download folder would hide everything below it, and a
// relative one cannot be anchored, so neither enters the index.
func TestHandedOffSources_IgnoresBroadAndRelativePaths(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)
	root := resolvedTempDir(t)
	intake := filepath.Join(root, "downloads")
	book := filepath.Join(intake, "Book", "book.epub")

	for i, p := range []string{root, intake, filepath.Dir(root), "relative/book.epub", book, ""} {
		dl := &models.Download{GUID: "handoff-" + string(rune('a'+i)), Title: "t", Status: models.StateImportExternal}
		if err := downloads.Create(ctx, dl); err != nil {
			t.Fatal(err)
		}
		if err := downloads.SetImportPath(ctx, dl.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, nil).
		WithRoots(NewLibraryRoots(nil, root)).WithExcludedDirs(intake)

	idx, err := h.handedOffSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.paths) != 1 {
		t.Fatalf("index = %v, want only %q", idx.paths, book)
	}
	if !idx.covers(book) || idx.covers(filepath.Join(intake, "Other", "other.epub")) {
		t.Errorf("index %v covers the wrong paths", idx.paths)
	}
}

// manualDownload creates the row a manual import runs under.
func (f *handoffFixture) manualDownload(t *testing.T) *models.Download {
	t.Helper()
	bookID := f.book.ID
	dl := &models.Download{GUID: importer.ManualDownloadGUIDPrefix + t.Name(), BookID: &bookID, Title: f.book.Title, Status: models.StateCompleted}
	if err := f.downloads.Create(context.Background(), dl); err != nil {
		t.Fatal(err)
	}
	return dl
}

// TestManualImportScan_TransmissionHandOffHidesOnlyItsOwnFile: Transmission
// reports a torrent's path as the shared category folder and passes the
// torrent's own files separately. Recording the folder hid every unrelated
// book in it. Only the handed off file may be hidden.
func TestManualImportScan_TransmissionHandOffHidesOnlyItsOwnFile(t *testing.T) {
	f := newHandoffFixture(t, "external")
	shared := filepath.Join(f.intake, "complete", "books")
	mine := f.write(t, "complete/books/Moby Dick - Herman Melville.epub", "moby bytes")
	sibling := f.write(t, "complete/books/Unrelated Novel - Someone Else.epub", "other bytes")
	f.write(t, "complete/books/Unrelated Audio/01.mp3", "track one")
	f.write(t, "complete/books/Unrelated Audio/02.mp3", "track two")
	siblingAudio := filepath.Join(shared, "Unrelated Audio")

	dl := f.manualDownload(t)
	f.scanner.ImportFilesFromPath(context.Background(), dl, shared, []string{mine}, models.MediaTypeEbook)
	got, err := f.downloads.GetByID(context.Background(), dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportExternal {
		t.Fatalf("status = %q (%s), want %q", got.Status, got.ErrorMessage, models.StateImportExternal)
	}
	if srcs := got.HandedOffSources(); len(srcs) != 1 || srcs[0] != mine {
		t.Errorf("recorded sources = %v, want only %q", srcs, mine)
	}

	items := f.scan(t, false)
	if _, ok := items[mine]; ok {
		t.Error("the handed off file was offered again")
	}
	if _, ok := items[sibling]; !ok {
		t.Errorf("an unrelated epub beside it was hidden: %+v", items)
	}
	if _, ok := items[siblingAudio]; !ok {
		t.Errorf("an unrelated audiobook folder beside it was hidden: %+v", items)
	}
}

// TestManualImportScan_LooseTracksHideOnlyThoseTracks: tracks submitted as
// separate rows import as one audiobook from their deepest common folder.
// That folder must not hide anything else inside it.
func TestManualImportScan_LooseTracksHideOnlyThoseTracks(t *testing.T) {
	f := newHandoffFixture(t, "external")
	one := f.write(t, "Pile/Moby CD1/01.mp3", "track one")
	two := f.write(t, "Pile/Moby CD2/02.mp3", "track two")
	other := f.write(t, "Pile/Unrelated Novel - Someone Else.epub", "other bytes")

	dl := f.manualDownload(t)
	f.scanner.ImportFilesFromPath(context.Background(), dl, filepath.Join(f.intake, "Pile"), []string{one, two}, models.MediaTypeAudiobook)
	got, err := f.downloads.GetByID(context.Background(), dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportExternal {
		t.Fatalf("status = %q (%s), want %q", got.Status, got.ErrorMessage, models.StateImportExternal)
	}
	if srcs := got.HandedOffSources(); len(srcs) != 2 {
		t.Errorf("recorded sources = %v, want the two tracks", srcs)
	}

	items := f.scan(t, false)
	if _, ok := items[other]; !ok {
		t.Errorf("an unrelated epub in the tracks' common folder was hidden: %+v", items)
	}
	for p := range items {
		if p == one || p == two || p == filepath.Dir(one) || p == filepath.Dir(two) {
			t.Errorf("handed off track unit %q offered again", p)
		}
	}
}

// TestManualImportScan_DedicatedFolderHandOffStillHidden: a qBittorrent or
// SABnzbd download has a folder of its own, and the book inside it stays
// hidden after the hand-off.
func TestManualImportScan_DedicatedFolderHandOffStillHidden(t *testing.T) {
	f := newHandoffFixture(t, "external")
	book := f.write(t, mobyRel, "moby bytes")
	folder := filepath.Dir(book)

	f.importAndWait(t, folder, models.MediaTypeEbook, models.StateImportExternal)

	if it, ok := f.scan(t, false)[book]; ok {
		t.Fatalf("book in a handed off download folder offered again: %+v", it)
	}
	if it := f.scan(t, true)[book]; !it.HandedOff {
		t.Errorf("book = %+v, want labelled handed off", it)
	}
}
