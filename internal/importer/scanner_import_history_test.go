package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestTryImportInternal_OneHistoryEventPerDownload is the regression test for
// issue #2764. A release carrying several ebook formats for one book wrote a
// bookImported history row per file, so a three format bundle produced three
// rows with the same release title and the same timestamp and nothing to tell
// them apart. One download is one import event, exactly as the bookImported
// webhook and the audiobook branch already treat it, and the row has to name
// the formats that landed because that is the part worth reading.
func TestTryImportInternal_OneHistoryEventPerDownload(t *testing.T) {
	libDir := t.TempDir()
	dlDir := t.TempDir()

	for _, name := range []string{"book.azw3", "book.epub", "book.mobi"} {
		if err := os.WriteFile(filepath.Join(dlDir, name), bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	historyRepo := db.NewHistoryRepo(database)

	author := &models.Author{
		ForeignID: "OLA-2764", Name: "Author 2764", SortName: "2764, Author",
		Monitored: true, MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-2764", AuthorID: author.ID,
		Title: "Multi Format Book", SortTitle: "Multi Format Book",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID: "guid-2764", Title: "Multi Format Book", BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, historyRepo, libDir, "", "", "", "")
	s.tryImportInternal(ctx, dl, dlDir, "", "", "", nil, nil)

	events, err := historyRepo.ListByType(ctx, models.HistoryEventBookImported)
	if err != nil {
		t.Fatalf("list history events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d bookImported history rows for one three format download, want 1: a multi format bundle is one import event, not one per file", len(events))
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("unmarshal history event data: %v", err)
	}
	if got := data["format"]; got != models.MediaTypeEbook {
		t.Errorf("history event format = %q, want %q", got, models.MediaTypeEbook)
	}
	if got := data["formats"]; got != "azw3, epub, mobi" {
		t.Errorf("history event formats = %q, want %q: the single row must still say which formats landed", got, "azw3, epub, mobi")
	}
	if got := data["fileCount"]; got != "3" {
		t.Errorf("history event fileCount = %q, want %q", got, "3")
	}
	if got := data["origin"]; got != "download" {
		t.Errorf("history event origin = %q, want download", got)
	}
	if got := data["guid"]; got != dl.GUID {
		t.Errorf("history event guid = %q, want %q", got, dl.GUID)
	}
	if got := data["downloadId"]; got != strconv.FormatInt(dl.ID, 10) {
		t.Errorf("history event downloadId = %q, want %d", got, dl.ID)
	}
	// path stays meaningful: with several formats sharing one folder the row
	// points at the folder, which is what the History page renders.
	path, ok := data["path"].(string)
	if !ok || path == "" || !strings.HasPrefix(path, libDir) {
		t.Errorf("history event path = %q, want a path under the library root %q", data["path"], libDir)
	}
	if filepath.Ext(path) != "" {
		t.Errorf("history event path = %q, want the shared folder rather than one of the three files", path)
	}
	paths, ok := data["paths"].([]any)
	if !ok || len(paths) != 3 {
		t.Fatalf("history event paths = %#v, want the three imported files", data["paths"])
	}
	seenPaths := make(map[string]bool, len(paths))
	for _, raw := range paths {
		importedPath, ok := raw.(string)
		if !ok {
			t.Fatalf("history event path entry = %#v, want string", raw)
		}
		seenPaths[filepath.Ext(importedPath)] = true
	}
	for _, ext := range []string{".azw3", ".epub", ".mobi"} {
		if !seenPaths[ext] {
			t.Errorf("history event paths = %#v, missing %s import", paths, ext)
		}
	}
}

func TestImportHistoryProvenanceDoesNotTreatSyntheticManualGUIDAsRelease(t *testing.T) {
	data := importHistoryProvenance(&models.Download{
		ID:   99,
		GUID: ManualDownloadGUIDPrefix + "import",
	}, map[string]any{"path": "/library/book.epub"})
	if got := data["origin"]; got != "manual" {
		t.Errorf("origin = %q, want manual", got)
	}
	for _, key := range []string{"guid", "downloadId", "indexerId", "downloadClientId"} {
		if got, exists := data[key]; exists {
			t.Errorf("manual import data[%q] = %q, want omitted", key, got)
		}
	}
}

func TestImportHistoryProvenanceKeepsKnownSourceIDs(t *testing.T) {
	indexerID, clientID := int64(17), int64(23)
	data := importHistoryProvenance(&models.Download{
		ID:               99,
		GUID:             "release-guid",
		IndexerID:        &indexerID,
		DownloadClientID: &clientID,
	}, map[string]any{"path": "/library/book.epub"})
	for key, want := range map[string]string{
		"origin":           "download",
		"guid":             "release-guid",
		"downloadId":       "99",
		"indexerId":        "17",
		"downloadClientId": "23",
	} {
		if got := data[key]; got != want {
			t.Errorf("data[%q] = %q, want %q", key, got, want)
		}
	}
}

// TestTryImportInternal_SingleFormatHistoryPathIsTheFile pins the other half of
// the path choice made for #2764: a single format download still records the
// file itself, so the History row reads exactly as it did before.
func TestTryImportInternal_SingleFormatHistoryPathIsTheFile(t *testing.T) {
	libDir := t.TempDir()
	dlDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dlDir, "book.epub"), bookSized("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	historyRepo := db.NewHistoryRepo(database)

	author := &models.Author{
		ForeignID: "OLA-2764b", Name: "Author 2764b", SortName: "2764b, Author",
		Monitored: true, MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-2764b", AuthorID: author.ID,
		Title: "Single Format Book", SortTitle: "Single Format Book",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID: "guid-2764b", Title: "Single Format Book", BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, historyRepo, libDir, "", "", "", "")
	s.tryImportInternal(ctx, dl, dlDir, "", "", "", nil, nil)

	events, err := historyRepo.ListByType(ctx, models.HistoryEventBookImported)
	if err != nil {
		t.Fatalf("list history events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d bookImported history rows, want 1", len(events))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("unmarshal history event data: %v", err)
	}
	path, ok := data["path"].(string)
	if !ok || filepath.Ext(path) != ".epub" {
		t.Errorf("history event path = %q, want the imported .epub file itself", data["path"])
	}
	if got := data["formats"]; got != "epub" {
		t.Errorf("history event formats = %q, want %q", got, "epub")
	}
}

// TestTryImportInternal_PartialImportWritesNoImportedRow pins the deliberate
// choice made for #2764 on the partial failure path: some files landed, some
// did not, the download is left failed and retryable, and failImport already
// records an importFailed row saying how many failed. No bookImported row is
// written for a download that is not imported. The retry then counts the
// already placed file through the idempotency guard and writes one row naming
// every format the download delivered.
func TestTryImportInternal_PartialImportWritesNoImportedRow(t *testing.T) {
	libDir := t.TempDir()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	historyRepo := db.NewHistoryRepo(database)

	author := &models.Author{
		ForeignID: "OLA-2764c", Name: "Author 2764c", SortName: "2764c, Author",
		Monitored: true, MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-2764c", AuthorID: author.ID,
		Title: "Partial Book", SortTitle: "Partial Book",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, historyRepo, libDir, "", "", "", "")

	newDownload := func(guid string) *models.Download {
		dl := &models.Download{
			GUID: guid, Title: "Partial Book", BookID: &book.ID,
			Status: models.StateCompleted,
		}
		if err := dlRepo.Create(ctx, dl); err != nil {
			t.Fatal(err)
		}
		return dl
	}
	writeFiles := func(names ...string) string {
		dir := t.TempDir()
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n), bookSized("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	importedRows := func() []models.HistoryEvent {
		rows, err := historyRepo.ListByType(ctx, models.HistoryEventBookImported)
		if err != nil {
			t.Fatalf("list history events: %v", err)
		}
		return rows
	}

	// Run 1: the epub alone lands cleanly and tells us the library folder.
	s.tryImportInternal(ctx, newDownload("guid-2764c-1"), writeFiles("book.epub"), "", "", "", nil, nil)
	rows := importedRows()
	if len(rows) != 1 {
		t.Fatalf("run 1: got %d bookImported rows, want 1", len(rows))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(rows[0].Data), &first); err != nil {
		t.Fatal(err)
	}
	epubPath, ok := first["path"].(string)
	if !ok {
		t.Fatalf("run 1: history path = %#v, want string", first["path"])
	}
	mobiPath := strings.TrimSuffix(epubPath, ".epub") + ".mobi"

	// Make the mobi's atomic promote fail while the epub is counted by the
	// idempotency guard: one file in, one file out, which is the partial
	// import case. A directory planted at the mobi's destination used to do
	// this; the #3560 collision suffix now imports beside it instead.
	restoreCommit := commitStagedRename
	refused := failCommitFor(t, ".mobi")

	// Run 2: partial.
	dl2 := newDownload("guid-2764c-2")
	s.tryImportInternal(ctx, dl2, writeFiles("book.epub", "book.mobi"), "", "", "", nil, nil)
	if *refused == 0 {
		t.Fatal("run 2: the mobi commit was never attempted, so the partial case was not exercised")
	}

	got2, err := dlRepo.GetByID(ctx, dl2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != models.StateImportFailed {
		t.Fatalf("run 2: download status = %q, want %q (a partial import stays retryable)", got2.Status, models.StateImportFailed)
	}
	if rows := importedRows(); len(rows) != 1 {
		t.Fatalf("run 2: got %d bookImported rows, want 1 (the partial run must not add one, only run 1's row exists)", len(rows))
	}
	failures, err := historyRepo.ListByType(ctx, models.HistoryEventImportFailed)
	if err != nil {
		t.Fatal(err)
	}
	var sawPartial bool
	for _, f := range failures {
		if strings.Contains(f.Data, "partial import") {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Error("run 2: no importFailed row mentioning the partial import, so the user has nothing telling them a file was left behind")
	}

	// Run 3: the obstacle is gone, the retry completes the download. The epub
	// is counted through the idempotency guard, so the single row names both
	// formats rather than only the one this run placed.
	commitStagedRename = restoreCommit
	s.tryImportInternal(ctx, newDownload("guid-2764c-3"), writeFiles("book.epub", "book.mobi"), "", "", "", nil, nil)

	rows = importedRows()
	if len(rows) != 2 {
		t.Fatalf("run 3: got %d bookImported rows total, want 2 (one per completed download)", len(rows))
	}
	var retry map[string]any
	for _, r := range rows {
		var d map[string]any
		if err := json.Unmarshal([]byte(r.Data), &d); err != nil {
			t.Fatal(err)
		}
		if d["fileCount"] == "2" {
			retry = d
		}
	}
	if retry == nil {
		t.Fatalf("run 3: no bookImported row covering both files; rows were %+v", rows)
	}
	if got := retry["formats"]; got != "epub, mobi" {
		t.Errorf("run 3: formats = %q, want %q (the file a previous attempt placed still counts)", got, "epub, mobi")
	}
	// The failed promote left nothing behind, so the retry lands the mobi at
	// its own name rather than a collision suffix beside a leftover.
	if _, err := os.Stat(mobiPath); err != nil {
		t.Errorf("run 3: mobi not at %s: %v", mobiPath, err)
	}
}
