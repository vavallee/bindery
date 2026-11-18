package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// ownershipAdmin makes a real user row to act as the admin, since a download
// or book owner column is a foreign key.
func ownershipAdmin(t *testing.T, f *grabSecFixture) int64 {
	t.Helper()
	u, err := db.NewUserRepo(f.database).Create(f.ctx, "ownership-admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// ageRow backdates a download row past the scheduler's dead row cooldown.
func ageRow(t *testing.T, f *grabSecFixture, id int64) {
	t.Helper()
	longAgo := time.Now().UTC().Add(-models.DeadRegrabCooldown - time.Hour)
	if _, err := f.database.Exec("UPDATE downloads SET dead_at=?, added_at=? WHERE id=?", longAgo, longAgo, id); err != nil {
		t.Fatal(err)
	}
}

// TestQueueGrab_StaleFailedRowIsOnlyTheOwnersToTake covers a failed row older
// than the cooldown. Alice's row and a shared row (no owner) stay as they are
// when bob grabs the same release; alice reuses her own, and an admin, or any
// caller with tenancy off, keeps the old takeover.
func TestQueueGrab_StaleFailedRowIsOnlyTheOwnersToTake(t *testing.T) {
	cases := []struct {
		name      string
		tenancy   bool
		rowOwner  string // "alice" or "shared"
		caller    string // "alice", "bob", "admin"
		wantCode  int
		wantOwner string // owner after, "alice", "bob", "shared", "admin"
	}{
		{"bob on alice's row", true, "alice", "bob", http.StatusConflict, "alice"},
		{"bob on shared row", true, "shared", "bob", http.StatusConflict, "shared"},
		{"alice on her own row", true, "alice", "alice", http.StatusAccepted, "alice"},
		{"admin on alice's row", true, "alice", "admin", http.StatusAccepted, "admin"},
		{"admin on shared row", true, "shared", "admin", http.StatusAccepted, "admin"},
		{"tenancy off, bob on alice's row", false, "alice", "bob", http.StatusAccepted, "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			f := newGrabSecFixture(t)
			ids := map[string]int64{"alice": f.alice, "bob": f.bob, "admin": ownershipAdmin(t, f), "shared": 0}
			row := &models.Download{
				GUID: f.guid, OwnerUserID: ids[tc.rowOwner], Title: "Old Release",
				NZBURL: f.downloadURL, Status: models.StateFailed, Protocol: "usenet",
			}
			if err := f.downloads.Create(f.ctx, row); err != nil {
				t.Fatal(err)
			}
			ageRow(t, f, row.ID)
			f.searchAs(t, ids[tc.caller])

			role := auth.RoleUser
			if tc.caller == "admin" {
				role = auth.RoleAdmin
			}
			rec := f.grabAs(ids[tc.caller], role, map[string]any{
				"guid": f.guid, "title": "Lee Child - One Shot (epub)", "nzbUrl": f.downloadURL, "indexerId": f.indexerID,
			})
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			got, err := f.downloads.GetByID(f.ctx, row.ID)
			if err != nil || got == nil {
				t.Fatalf("reload row: %v", err)
			}
			if got.OwnerUserID != ids[tc.wantOwner] {
				t.Errorf("owner after = %d, want %d (%s)", got.OwnerUserID, ids[tc.wantOwner], tc.wantOwner)
			}
			if tc.wantCode == http.StatusConflict {
				if got.Title != "Old Release" || got.Status != models.StateFailed {
					t.Errorf("refused row changed: title=%q status=%s", got.Title, got.Status)
				}
				if strings.Contains(rec.Body.String(), "alice") || strings.Contains(rec.Body.String(), "shared") {
					t.Errorf("the refusal must not say whose row it is: %s", rec.Body.String())
				}
				if n := f.adds.Load(); n != 0 {
					t.Errorf("nothing may reach the download client; got %d add(s)", n)
				}
			} else if got.Status != models.StateDownloading || got.Title != "Lee Child - One Shot (epub)" {
				t.Errorf("claimed row not reset: title=%q status=%s", got.Title, got.Status)
			}
		})
	}
}

// TestQueueGrab_BookIDNeedsWriteAccess: a grab that names a book is held to
// the check the book routes make. A shared book (no owner) is not one a
// non-admin may change, so the grab answers 404 and leaves no download row;
// the owner, an admin, a grab with no book and tenancy off are unchanged.
func TestQueueGrab_BookIDNeedsWriteAccess(t *testing.T) {
	cases := []struct {
		name     string
		tenancy  bool
		book     string // "shared", "bob", "alice"
		caller   string
		noBook   bool
		wantCode int
	}{
		{"bob on shared book", true, "shared", "bob", false, http.StatusNotFound},
		{"bob on alice's book", true, "alice", "bob", false, http.StatusNotFound},
		{"bob on his own book", true, "bob", "bob", false, http.StatusAccepted},
		{"admin on shared book", true, "shared", "admin", false, http.StatusAccepted},
		{"bob with no book", true, "shared", "bob", true, http.StatusAccepted},
		{"tenancy off, bob on shared book", false, "shared", "bob", false, http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			f := newGrabSecFixture(t)
			ids := map[string]int64{"alice": f.alice, "bob": f.bob, "admin": ownershipAdmin(t, f), "shared": 0}
			book := regrabBook(t, f.database, f.books, "own-"+strings.ReplaceAll(tc.name, " ", "-"))
			if ids[tc.book] != 0 {
				if _, err := f.database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", ids[tc.book], book.ID); err != nil {
					t.Fatal(err)
				}
			}
			f.searchAs(t, ids[tc.caller])
			payload := map[string]any{
				"guid": f.guid, "title": "Lee Child - One Shot (epub)", "nzbUrl": f.downloadURL, "indexerId": f.indexerID,
			}
			if !tc.noBook {
				payload["bookId"] = book.ID
			}
			role := auth.RoleUser
			if tc.caller == "admin" {
				role = auth.RoleAdmin
			}
			rec := f.grabAs(ids[tc.caller], role, payload)
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			dl, err := f.downloads.GetByGUID(f.ctx, f.guid)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantCode == http.StatusNotFound && (dl != nil || f.adds.Load() != 0) {
				t.Errorf("a refused grab must leave no row and send nothing: %+v, adds=%d", dl, f.adds.Load())
			}
		})
	}
}

// TestBulkSearch_NeedsWriteAccess: the book bulk and wanted bulk "search"
// action use the write check every other bulk action uses, so bob's search over
// a mixed list queues his own book only; a shared book and alice's are "not
// found". Admin and tenancy off are unchanged.
func TestBulkSearch_NeedsWriteAccess(t *testing.T) {
	for _, endpoint := range []string{"book", "wanted"} {
		for _, tc := range []struct {
			name    string
			tenancy bool
			role    string
			caller  string
			want    map[string]bool // book key to searched
		}{
			{"bob", true, "user", "bob", map[string]bool{"bob": true, "shared": false, "alice": false}},
			{"alice", true, "user", "alice", map[string]bool{"bob": false, "shared": false, "alice": true}},
			{"admin", true, "admin", "admin", map[string]bool{"bob": true, "shared": true, "alice": true}},
			{"tenancy off", false, "user", "bob", map[string]bool{"bob": true, "shared": true, "alice": true}},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				auth.SetEnforceTenancyForTests(t, tc.tenancy)
				searchPaceInterval = 0
				t.Cleanup(func() { searchPaceInterval = 3 * time.Second })
				f := seedTwoUserBulk(t)
				spy := newMockBookSearcher()
				h := NewBulkHandler(f.authors, f.books, nil, spy)
				shared := mustCreateBook(t, f.books, t.Context(), &models.Book{
					ForeignID: "OL-SHARED", AuthorID: f.a1.ID, Title: "Shared Book", SortTitle: "shared book",
					Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
					Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
				})
				books := map[string]int64{"alice": f.b1.ID, "bob": f.b2.ID, "shared": shared.ID}
				callers := map[string]int64{"alice": f.u1, "bob": f.u2, "admin": 0}

				body := fmt.Sprintf(`{"ids":[%d,%d,%d],"action":"search"}`, books["bob"], books["shared"], books["alice"])
				handler := h.BooksBulk
				if endpoint == "wanted" {
					handler = h.WantedBulk
				}
				rec := postBulkAs(t, handler, body, callers[tc.caller], tc.role)
				if rec.Code != http.StatusOK {
					t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
				}
				wantN := 0
				for k, want := range tc.want {
					if got := resultOK(t, rec, books[k]); got != want {
						t.Errorf("%s book: ok=%v, want %v", k, got, want)
					}
					if want {
						wantN++
					}
				}
				seen := map[int64]bool{}
				for i := 0; i < wantN; i++ {
					seen[spy.waitForCall(t, 2*time.Second).ID] = true
				}
				spy.assertNoCall(t, 200*time.Millisecond)
				for k, want := range tc.want {
					if seen[books[k]] != want {
						t.Errorf("%s book searched=%v, want %v", k, seen[books[k]], want)
					}
				}
			})
		}
	}
}

// TestQueueGrab_PlainUserDoesNotReuseAnOrphanedImport: an imported row whose
// book was deleted is still its owner's row. A plain user's grab of the same
// release is refused and leaves it alone.
func TestQueueGrab_PlainUserDoesNotReuseAnOrphanedImport(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGrabSecFixture(t)
	aliceBook := regrabOwnedBook(t, f.database, f.books, "alice-orphan", f.alice)
	row := &models.Download{
		GUID: f.guid, BookID: &aliceBook.ID, OwnerUserID: f.alice, Title: "Alice Release",
		NZBURL: f.downloadURL, Status: models.StateImported, Protocol: "usenet",
	}
	if err := f.downloads.Create(f.ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := f.books.Delete(f.ctx, aliceBook.ID); err != nil {
		t.Fatal(err)
	}
	f.searchAs(t, f.bob)
	rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
		"guid": f.guid, "title": "x", "nzbUrl": f.downloadURL, "indexerId": f.indexerID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := f.downloads.GetByID(f.ctx, row.ID)
	if err != nil || got == nil || got.OwnerUserID != f.alice || got.Status != models.StateImported {
		t.Errorf("alice's row must be untouched: %+v, %v", got, err)
	}
}

func grabErrorBody(t *testing.T, body string) string {
	t.Helper()
	var m struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m.Error
}

// TestQueueGrab_RefusedBookMessages: both refusals are 404, but a shared book
// the caller can see says why, while a book the caller cannot see keeps the
// generic message that does not reveal it exists.
func TestQueueGrab_RefusedBookMessages(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGrabSecFixture(t)
	shared := regrabBook(t, f.database, f.books, "msg-shared")
	hidden := regrabOwnedBook(t, f.database, f.books, "msg-hidden", f.alice)
	f.searchAs(t, f.bob)
	for _, tc := range []struct {
		name string
		id   int64
		want string
	}{
		{"shared", shared.ID, "That book is shared, so only an admin can grab for it."},
		{"hidden", hidden.ID, "book not found"},
	} {
		rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
			"guid": f.guid, "title": "x", "nzbUrl": f.downloadURL, "indexerId": f.indexerID, "bookId": tc.id,
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
		if got := grabErrorBody(t, rec.Body.String()); got != tc.want {
			t.Errorf("%s: error = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestQueueRetry_OwnerRetriesRowOnNowSharedBook: a row bob owns whose book has
// since become shared still retries for bob, since the download is his.
func TestQueueRetry_OwnerRetriesRowOnNowSharedBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGrabSecFixture(t)
	book := regrabBook(t, f.database, f.books, "retry-shared")
	signed := newznab.SignDownloadURLFor(f.downloadURL, f.indexerURL+"/3/api", leakedAPIKey)
	row := &models.Download{
		GUID: "guid-retry-shared", BookID: &book.ID, OwnerUserID: f.bob, Title: "Bob Release",
		NZBURL: signed, Status: models.StateFailed, Protocol: "usenet",
	}
	if err := f.downloads.Create(f.ctx, row); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	rec := httptest.NewRecorder()
	f.queue.RetryDownload(rec, withURLParam(asRole(httptest.NewRequest(http.MethodPost, "/api/v1/queue/"+id+"/retry", nil), f.bob, auth.RoleUser), "id", id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("bob retrying his own row on a shared book: got %d: %s", rec.Code, rec.Body.String())
	}
}
