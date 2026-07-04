package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// backupURLRequest builds a request carrying a chi {filename} URL param so the
// Restore/Delete handlers (which read chi.URLParam) see it.
func backupURLRequest(method, filename string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/system/backup/"+filename, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("filename", filename)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// --- backup Delete (handler line 215) ---

func TestBackupDelete_RemovesFileAndReturns204(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)

	// Stage a valid backup file directly in the backup dir.
	backupsDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	name := "bindery_20240102_030405.db"
	path := filepath.Join(backupsDir, name)
	if err := os.WriteFile(path, []byte("not really sqlite, but Delete only unlinks"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Delete(rec, backupURLRequest(http.MethodDelete, name))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body = %s, want 204", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected backup file to be deleted, stat err = %v", err)
	}
}

func TestBackupDelete_RejectsPathEscapeFilename(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)

	// A sentinel file one level above the backup dir. If the regex guard ever
	// regressed and a "../" filename were joined+removed, this would vanish.
	sentinel := filepath.Join(dataDir, "do_not_delete.db")
	if err := os.WriteFile(sentinel, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	for _, bad := range []string{
		"../do_not_delete.db",
		"bindery_2024.db",
		"bindery_20240102_030405.db.tmp",
		"evil.db",
		"",
	} {
		rec := httptest.NewRecorder()
		h.Delete(rec, backupURLRequest(http.MethodDelete, bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("filename %q: status = %d, want 400", bad, rec.Code)
		}
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel file should still exist after rejected deletes: %v", err)
	}
}

func TestBackupDelete_MissingFileReturns404(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)
	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Delete(rec, backupURLRequest(http.MethodDelete, "bindery_20240102_030405.db"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- backup Restore (handler line 184) ---

func TestBackupRestore_RejectsBadFilename(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)

	for _, bad := range []string{"../../etc/passwd", "random.db", "bindery_bad.db", ""} {
		rec := httptest.NewRecorder()
		req := backupURLRequest(http.MethodPost, bad)
		req.Header.Set("X-Confirm-Restore", "true")
		h.Restore(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("filename %q: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestBackupRestore_RequiresConfirmationHeader(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)

	// Valid filename, but no X-Confirm-Restore header: must 400 before touching
	// the filesystem.
	rec := httptest.NewRecorder()
	h.Restore(rec, backupURLRequest(http.MethodPost, "bindery_20240102_030405.db"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 without confirm header", rec.Code, rec.Body.String())
	}
}

func TestBackupRestore_MissingFileReturns404(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)

	rec := httptest.NewRecorder()
	req := backupURLRequest(http.MethodPost, "bindery_20240102_030405.db")
	req.Header.Set("X-Confirm-Restore", "true")
	h.Restore(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404 for absent backup", rec.Code, rec.Body.String())
	}
}

func TestBackupRestore_HappyPathCopiesBackupOverDBPath(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	h := NewBackupHandler(database, dbPath, dataDir)

	// Stage a known-content backup file. Restore is a byte copy from the backup
	// onto h.dbPath, so we can assert the destructive overwrite by content.
	backupsDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	name := "bindery_20240102_030405.db"
	const marker = "RESTORED-BACKUP-CONTENT"
	if err := os.WriteFile(filepath.Join(backupsDir, name), []byte(marker), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	// Pre-existing DB content the restore should clobber.
	if err := os.WriteFile(dbPath, []byte("STALE-LIVE-DB"), 0o600); err != nil {
		t.Fatalf("write live db: %v", err)
	}

	rec := httptest.NewRecorder()
	req := backupURLRequest(http.MethodPost, name)
	req.Header.Set("X-Confirm-Restore", "true")
	h.Restore(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read dbPath after restore: %v", err)
	}
	if string(got) != marker {
		t.Fatalf("dbPath content = %q, want backup content %q", string(got), marker)
	}
}

// --- authors_alias Merge (handler line 109) ---

func mergeRequest(targetID int64, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/"+strconv.FormatInt(targetID, 10)+"/merge", bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(targetID, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestAuthorAliasMerge_ReparentsBooksToTarget(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)

	target := &models.Author{ForeignID: "OL-CANON", Name: "Canonical Author", SortName: "Author, Canonical", Monitored: true}
	if err := authorRepo.Create(ctx, target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	source := &models.Author{ForeignID: "OL-DUP", Name: "Duplicate Author", SortName: "Author, Duplicate", Monitored: true}
	if err := authorRepo.Create(ctx, source); err != nil {
		t.Fatalf("create source: %v", err)
	}

	// Two books parented under the source (alias) author.
	b1 := &models.Book{ForeignID: "bk-1", AuthorID: source.ID, Title: "Book One", SortTitle: "book one"}
	b2 := &models.Book{ForeignID: "bk-2", AuthorID: source.ID, Title: "Book Two", SortTitle: "book two"}
	for _, b := range []*models.Book{b1, b2} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatalf("create book %s: %v", b.ForeignID, err)
		}
	}

	h := NewAuthorAliasHandler(authorRepo, aliasRepo)
	rec := httptest.NewRecorder()
	h.Merge(rec, mergeRequest(target.ID, `{"sourceId":`+strconv.FormatInt(source.ID, 10)+`}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}

	var result db.MergeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode merge result: %v", err)
	}
	if result.BooksReparented != 2 {
		t.Errorf("BooksReparented = %d, want 2", result.BooksReparented)
	}

	// Destructive effect: both books now point at the target, and the source
	// author row is gone.
	for _, id := range []int64{b1.ID, b2.ID} {
		var authorID int64
		if err := database.QueryRowContext(ctx, "SELECT author_id FROM books WHERE id = ?", id).Scan(&authorID); err != nil {
			t.Fatalf("read book %d author_id: %v", id, err)
		}
		if authorID != target.ID {
			t.Errorf("book %d author_id = %d, want target %d", id, authorID, target.ID)
		}
	}

	gone, err := authorRepo.GetByID(ctx, source.ID)
	if err != nil {
		t.Fatalf("GetByID source: %v", err)
	}
	if gone != nil {
		t.Errorf("source author should be deleted after merge, still present: %+v", gone)
	}

	// Source's name is preserved as an alias on the target.
	aliases, err := aliasRepo.ListByAuthor(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListByAuthor target: %v", err)
	}
	found := false
	for _, a := range aliases {
		if a.Name == source.Name {
			found = true
		}
	}
	if !found {
		t.Errorf("expected source name %q recorded as alias on target; aliases = %+v", source.Name, aliases)
	}
}

func TestAuthorAliasMerge_RejectsBadInput(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	target := &models.Author{ForeignID: "OL-T", Name: "T", SortName: "T", Monitored: true}
	if err := authorRepo.Create(ctx, target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	h := NewAuthorAliasHandler(authorRepo, aliasRepo)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"bad json", "not-json", http.StatusBadRequest},
		{"missing sourceId", `{}`, http.StatusBadRequest},
		{"same source and target", `{"sourceId":` + strconv.FormatInt(target.ID, 10) + `}`, http.StatusBadRequest},
		{"unknown source", `{"sourceId":999999}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Merge(rec, mergeRequest(target.ID, tc.body))
			if rec.Code != tc.want {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

// --- pending Grab (handler line 107) ---

// TestPendingGrab_DispatchesDownloadAndClearsPending exercises the full Grab
// path: it stands up a fake SABnzbd download client (httptest server), seeds a
// pending release whose ReleaseJSON carries a grabbable release, calls Grab,
// and asserts the download was actually dispatched to the client AND the
// pending row was removed.
func TestPendingGrab_DispatchesDownloadAndClearsPending(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	addCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "addfile" {
			addCalls++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"nzo-pending"},
		})
	}))
	defer srv.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	books := db.NewBookRepo(database)
	history := db.NewHistoryRepo(database)
	authors := db.NewAuthorRepo(database)
	pending := db.NewPendingReleaseRepo(database)

	host, port := testServerHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	author := &models.Author{ForeignID: "grab-author", Name: "Grab Author", SortName: "Grab Author", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{ForeignID: "grab-book", AuthorID: author.ID, Title: "Grab Book", SortTitle: "grab book", MediaType: models.MediaTypeEbook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}

	// The handler unmarshals ReleaseJSON into a grabRequest; it needs guid +
	// nzbUrl to satisfy the grab path.
	releaseJSON, _ := json.Marshal(map[string]any{
		"guid":     "pending-guid",
		"title":    "Pending Release Title",
		"nzbUrl":   srv.URL + "/release.nzb",
		"size":     123,
		"protocol": "usenet",
	})
	pr := &models.PendingRelease{
		BookID:      book.ID,
		MediaType:   models.MediaTypeEbook,
		Title:       "Pending Release Title",
		GUID:        "pending-guid",
		Protocol:    "usenet",
		Reason:      "delay",
		ReleaseJSON: string(releaseJSON),
	}
	if err := pending.Upsert(ctx, pr); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}
	all, err := pending.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pendingID int64
	for _, p := range all {
		if p.GUID == "pending-guid" {
			pendingID = p.ID
		}
	}
	if pendingID == 0 {
		t.Fatalf("pending release not inserted")
	}

	queue := NewQueueHandler(downloads, clients, books, history)
	h := NewPendingHandler(pending, queue, downloads, books)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/pending/"+strconv.FormatInt(pendingID, 10)+"/grab", nil), "id", strconv.FormatInt(pendingID, 10))
	h.Grab(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", rec.Code, rec.Body.String())
	}

	// Downstream effect: the download was actually dispatched to the client.
	if addCalls != 1 {
		t.Errorf("expected exactly one addfile dispatch to download client, got %d", addCalls)
	}
	dl, err := downloads.GetByGUID(ctx, "pending-guid")
	if err != nil || dl == nil {
		t.Fatalf("expected a download row for the grabbed release: %v", err)
	}

	// Destructive effect: the pending release is cleared after a successful grab.
	remaining, err := pending.GetByID(ctx, pendingID)
	if err != nil {
		t.Fatalf("GetByID pending: %v", err)
	}
	if remaining != nil {
		t.Errorf("expected pending release removed after grab, still present: %+v", remaining)
	}
}

func TestPendingGrab_MissingReleaseReturns404(t *testing.T) {
	h, _, _, _, _, _ := seedTwoUserPending(t)
	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/pending/999999/grab", nil), "id", "999999")
	h.Grab(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404", rec.Code, rec.Body.String())
	}
}
