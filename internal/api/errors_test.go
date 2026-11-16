package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The not-found sweep (audit E6). Every handler below used to answer
// `err != nil || row == nil` with a 404, so a database fault read as "that
// row does not exist". The contract now: a missing row is 404 with the
// handler's own message, a store error is a generic 500. Each case runs the
// same request twice, once against an open database (missing id) and once
// against a closed one.

type lookupCase struct {
	name string
	// build returns the handler method under test, wired to d.
	build func(d *sql.DB) http.HandlerFunc
	// method/body drive the request; the id URL param is always "99999".
	method, body string
	notFound     string // expected 404 error text
}

func runLookupCases(t *testing.T, cases []lookupCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			do := func(d *sql.DB) *httptest.ResponseRecorder {
				req := httptest.NewRequest(c.method, "/x/99999", strings.NewReader(c.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				c.build(d)(rec, withURLParam(req, "id", "99999"))
				return rec
			}

			open, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = open.Close() })
			wantFixed(t, do(open), http.StatusNotFound, c.notFound)

			closed, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			_ = closed.Close()
			wantFixed(t, do(closed), http.StatusInternalServerError, msgServerError)
		})
	}
}

func TestNotFoundSweep_ConfigHandlers(t *testing.T) {
	runLookupCases(t, []lookupCase{
		{"delay profile update", func(d *sql.DB) http.HandlerFunc {
			return NewDelayProfileHandler(db.NewDelayProfileRepo(d)).Update
		}, http.MethodPut, `{}`, "delay profile not found"},
		{"metadata profile update", func(d *sql.DB) http.HandlerFunc {
			return NewMetadataProfileHandler(db.NewMetadataProfileRepo(d)).Update
		}, http.MethodPut, `{}`, "metadata profile not found"},
		{"import list update", func(d *sql.DB) http.HandlerFunc {
			return NewImportListHandler(db.NewImportListRepo(d), db.NewSettingsRepo(d), nil, nil).Update
		}, http.MethodPut, `{}`, "import list not found"},
	})
}

func TestNotFoundSweep_NotificationHandlers(t *testing.T) {
	newH := func(d *sql.DB) *NotificationHandler {
		return NewNotificationHandler(db.NewNotificationRepo(d), nil)
	}
	runLookupCases(t, []lookupCase{
		{"notification update", func(d *sql.DB) http.HandlerFunc { return newH(d).Update },
			http.MethodPut, `{}`, "notification not found"},
		{"notification test", func(d *sql.DB) http.HandlerFunc { return newH(d).Test },
			http.MethodPost, ``, "notification not found"},
	})
}

func TestNotFoundSweep_DownloadClientHandlers(t *testing.T) {
	newH := func(d *sql.DB) *DownloadClientHandler {
		return NewDownloadClientHandler(db.NewDownloadClientRepo(d))
	}
	runLookupCases(t, []lookupCase{
		{"download client get", func(d *sql.DB) http.HandlerFunc { return newH(d).Get },
			http.MethodGet, ``, "download client not found"},
		{"download client update", func(d *sql.DB) http.HandlerFunc { return newH(d).Update },
			http.MethodPut, `{}`, "download client not found"},
		{"download client test", func(d *sql.DB) http.HandlerFunc { return newH(d).Test },
			http.MethodPost, ``, "download client not found"},
		{"download client diagnose", func(d *sql.DB) http.HandlerFunc { return newH(d).Diagnose },
			http.MethodPost, ``, "download client not found"},
	})
}

func TestNotFoundSweep_IndexerHandlers(t *testing.T) {
	newH := func(d *sql.DB) *IndexerHandler {
		return NewIndexerHandler(db.NewIndexerRepo(d), db.NewBookRepo(d), db.NewAuthorRepo(d),
			db.NewMetadataProfileRepo(d), nil, db.NewSettingsRepo(d), db.NewBlocklistRepo(d))
	}
	runLookupCases(t, []lookupCase{
		{"indexer get", func(d *sql.DB) http.HandlerFunc { return newH(d).Get },
			http.MethodGet, ``, "indexer not found"},
		{"indexer update", func(d *sql.DB) http.HandlerFunc { return newH(d).Update },
			http.MethodPut, `{}`, "indexer not found"},
		{"indexer test", func(d *sql.DB) http.HandlerFunc { return newH(d).Test },
			http.MethodPost, ``, "indexer not found"},
		{"indexer search book", func(d *sql.DB) http.HandlerFunc { return newH(d).SearchBook },
			http.MethodGet, ``, "book not found"},
	})
}

func TestNotFoundSweep_ReleaseHandlers(t *testing.T) {
	runLookupCases(t, []lookupCase{
		{"pending grab", func(d *sql.DB) http.HandlerFunc {
			return NewPendingHandler(db.NewPendingReleaseRepo(d), nil, db.NewDownloadRepo(d), db.NewBookRepo(d)).Grab
		}, http.MethodPost, ``, "pending release not found"},
		{"history blocklist", func(d *sql.DB) http.HandlerFunc {
			return NewHistoryHandler(db.NewHistoryRepo(d), db.NewBlocklistRepo(d), db.NewBookRepo(d)).Blocklist
		}, http.MethodPost, ``, "history event not found"},
		{"file download", func(d *sql.DB) http.HandlerFunc {
			return NewFileHandler(db.NewBookRepo(d)).Download
		}, http.MethodGet, ``, "book not found"},
	})
}

func TestNotFoundSweep_GenreHandlers(t *testing.T) {
	author := func(d *sql.DB) *AuthorHandler {
		return NewAuthorHandler(db.NewAuthorRepo(d), nil, db.NewBookRepo(d), db.NewSeriesRepo(d), nil, db.NewSettingsRepo(d), db.NewMetadataProfileRepo(d), nil)
	}
	series := func(d *sql.DB) *SeriesHandler {
		return NewSeriesHandler(db.NewSeriesRepo(d), db.NewBookRepo(d), db.NewAuthorRepo(d), nil, nil)
	}
	runLookupCases(t, []lookupCase{
		{"author apply genres", func(d *sql.DB) http.HandlerFunc { return author(d).ApplyGenres },
			http.MethodPut, `{}`, "author not found"},
		{"series apply genres", func(d *sql.DB) http.HandlerFunc { return series(d).ApplyGenres },
			http.MethodPut, `{}`, "series not found"},
		{"series clear genres", func(d *sql.DB) http.HandlerFunc { return series(d).ClearGenres },
			http.MethodDelete, ``, "series not found"},
	})
}

// manual import keeps its documented 400 for a book that does not exist (the
// UI treats it as a bad selection), but a store fault on the book lookup is a
// 500. The download lookup runs first, so the fixture gives the handler a good
// download repo and a closed book repo.
func TestNotFoundSweep_MatchDownloadBookLookup(t *testing.T) {
	good, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = good.Close() })
	downloads := db.NewDownloadRepo(good)
	dl := &models.Download{GUID: "g1", Title: "t", Status: models.StateImportFailed, Protocol: "usenet"}
	if err := downloads.Create(context.Background(), dl); err != nil {
		t.Fatal(err)
	}
	body := `{"downloadId":` + strconv.FormatInt(dl.ID, 10) + `,"bookId":99999}`

	call := func(books *db.BookRepo) *httptest.ResponseRecorder {
		h := NewManualImportHandler(nil, downloads, books)
		rec := httptest.NewRecorder()
		h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/manualimport/match", strings.NewReader(body)))
		return rec
	}
	wantFixed(t, call(db.NewBookRepo(good)), http.StatusBadRequest, "book not found")

	closed, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	wantFixed(t, call(db.NewBookRepo(closed)), http.StatusInternalServerError, msgServerError)
}

// validateImport feeds Import, Reassign and ImportBatch. Same rule as above:
// a missing book stays the documented 400, a store fault is a 500 with the
// fixed text (and the batch endpoint reports that text per item).
func TestNotFoundSweep_ValidateImportBookLookup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "book.epub")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	open, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = open.Close() })
	closed, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()

	_, book, status, msg := NewManualImportHandler(nil, nil, db.NewBookRepo(open)).
		validateImport(context.Background(), file, 99999, "")
	if book != nil || status != http.StatusBadRequest || msg != "book not found" {
		t.Errorf("missing book: book=%v status=%d msg=%q", book, status, msg)
	}
	_, book, status, msg = NewManualImportHandler(nil, nil, db.NewBookRepo(closed)).
		validateImport(context.Background(), file, 99999, "")
	if book != nil || status != http.StatusInternalServerError || msg != msgServerError {
		t.Errorf("closed store: book=%v status=%d msg=%q", book, status, msg)
	}
}
