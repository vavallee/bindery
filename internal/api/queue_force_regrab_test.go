package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// grabConflict is the 409 body a refused grab answers with.
type grabConflict struct {
	Error          string `json:"error"`
	ForceAvailable bool   `json:"forceAvailable"`
}

// TestQueueGrab_ForceRegrabsImportedRelease replays the half of #2289 the
// book delete fix (#2605) left open. schmitzkr's release was marked imported
// with nothing usable behind it, the book was still in the library, and every
// Grab answered "already imported". The only ways out were deleting the book
// or deleting the torrent in qBittorrent, which costs the seeding history on
// a ratio tracked tracker.
//
// The refusal must say it can be overridden, and a grab that sets force must
// go through, reuse the row and leave the book alone.
func TestQueueGrab_ForceRegrabsImportedRelease(t *testing.T) {
	h, database, downloads, clients, books, ctx := queueFixture(t)
	indexerURL, adds := regrabDownloadClient(t, clients)
	book := regrabBook(t, database, books, "force")

	imported := &models.Download{
		GUID:     "guid-2289-force",
		BookID:   &book.ID,
		Title:    "Stuck Release",
		NZBURL:   indexerURL + "/old.nzb",
		Status:   models.StateImported,
		Protocol: "usenet",
	}
	if err := downloads.Create(ctx, imported); err != nil {
		t.Fatal(err)
	}
	bookID := strconv.FormatInt(book.ID, 10)
	plain := `{"guid":"guid-2289-force","nzbUrl":"` + indexerURL + `/new.nzb","title":"Stuck Release","bookId":` + bookID + `}`

	rec := regrabPost(h, plain)
	if rec.Code != http.StatusConflict {
		t.Fatalf("an unforced grab of an imported release must still be refused; got %d: %s", rec.Code, rec.Body.String())
	}
	var conflict grabConflict
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if !conflict.ForceAvailable {
		t.Errorf("#2289: the refusal of an imported release must offer force, got %s", rec.Body.String())
	}
	if adds.Load() != 0 {
		t.Fatalf("a refused grab must not reach the download client, got %d adds", adds.Load())
	}

	forced := `{"guid":"guid-2289-force","nzbUrl":"` + indexerURL + `/new.nzb","title":"Stuck Release","bookId":` + bookID + `,"force":true}`
	rec = regrabPost(h, forced)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("#2289: a forced grab of an imported release must go through; got %d: %s", rec.Code, rec.Body.String())
	}
	if n := adds.Load(); n != 1 {
		t.Fatalf("expected the release sent to the download client once, got %d", n)
	}
	got, err := downloads.GetByGUID(ctx, "guid-2289-force")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.ID != imported.ID {
		t.Errorf("expected the forced grab to reuse row %d, got %d", imported.ID, got.ID)
	}
	if got.Status == models.StateImported || got.ImportedAt != nil {
		t.Errorf("expected the row to leave imported, got status=%q imported_at=%v", got.Status, got.ImportedAt)
	}
	if got.BookID == nil || *got.BookID != book.ID {
		t.Errorf("expected the row still linked to book %d, got %v", book.ID, got.BookID)
	}
	still, err := books.GetByID(ctx, book.ID)
	if err != nil || still == nil {
		t.Fatalf("the book must survive a forced grab: %v", err)
	}
}

// TestQueueGrab_ForceDoesNotOpenLiveWork pins what force must not do: a row
// that is in flight, or that the scanner or an external tool still owns, stays
// refused with force set, and its refusal does not offer force.
func TestQueueGrab_ForceDoesNotOpenLiveWork(t *testing.T) {
	for _, status := range []models.DownloadState{
		models.StateGrabbed,
		models.StateDownloading,
		models.StateCompleted,
		models.StateImporting,
		models.StateImportFailed,
		models.StateImportExternal,
		models.StateImportHeld,
	} {
		t.Run(string(status), func(t *testing.T) {
			h, database, downloads, clients, books, ctx := queueFixture(t)
			indexerURL, adds := regrabDownloadClient(t, clients)
			book := regrabBook(t, database, books, "live-"+string(status))
			dl := &models.Download{GUID: "live-force", BookID: &book.ID, Title: "T", Protocol: "usenet", Status: status}
			if err := downloads.Create(ctx, dl); err != nil {
				t.Fatal(err)
			}
			rec := regrabPost(h, `{"guid":"live-force","nzbUrl":"`+indexerURL+`/x.nzb","title":"T","force":true}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("force must not open a %s row; got %d: %s", status, rec.Code, rec.Body.String())
			}
			var conflict grabConflict
			if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
				t.Fatal(err)
			}
			if conflict.ForceAvailable {
				t.Errorf("the refusal of a %s row must not offer force", status)
			}
			if adds.Load() != 0 {
				t.Fatalf("nothing may reach the download client, got %d adds", adds.Load())
			}
		})
	}
}

// TestQueueGrab_ForceIsOwnRowsOnly keeps force inside tenancy: bob cannot
// force a re-grab of alice's imported release, and is not told he could.
func TestQueueGrab_ForceIsOwnRowsOnly(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	h, database, downloads, clients, books, ctx := queueFixture(t)
	indexerURL, adds := regrabDownloadClient(t, clients)
	alice, bob := regrabUsers(t, database)
	book := regrabOwnedBook(t, database, books, "alice-force", alice)
	dl := &models.Download{GUID: "alice-force", BookID: &book.ID, OwnerUserID: alice, Title: "T", Protocol: "usenet", Status: models.StateImported}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	registry := NewSearchResultRegistry()
	registry.remember([]newznab.SearchResult{{GUID: "alice-force", NZBURL: indexerURL + "/x.nzb", Title: "T"}})
	h.WithSearchResults(registry)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab",
		bytes.NewBufferString(`{"guid":"alice-force","nzbUrl":"`+indexerURL+`/x.nzb","title":"T","force":true}`))
	req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), bob), "user"))
	rec := httptest.NewRecorder()
	h.Grab(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("bob must not force alice's imported row; got %d: %s", rec.Code, rec.Body.String())
	}
	var conflict grabConflict
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.ForceAvailable {
		t.Error("a foreign row's refusal must not offer force")
	}
	if adds.Load() != 0 {
		t.Fatalf("nothing may reach the download client, got %d adds", adds.Load())
	}
}

// TestDownloadRepoRetryImportedClaimsOnlyImported is the SQL guard under the
// forced grab: only a row that is still imported is claimed.
func TestDownloadRepoRetryImportedClaimsOnlyImported(t *testing.T) {
	_, _, downloads, _, _, ctx := queueFixture(t)
	for i, status := range []models.DownloadState{models.StateDownloading, models.StateImportFailed, models.StateFailed, models.StateImported} {
		dl := &models.Download{GUID: "claim-" + strconv.Itoa(i), Title: "T", Protocol: "usenet", Status: status}
		if err := downloads.Create(ctx, dl); err != nil {
			t.Fatal(err)
		}
		ok, err := downloads.RetryImported(ctx, dl)
		if err != nil {
			t.Fatal(err)
		}
		if want := status == models.StateImported; ok != want {
			t.Errorf("RetryImported on a %s row = %v, want %v", status, ok, want)
		}
	}
}
