package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// queuePin is what the pin test compares for each listed row.
type queuePin struct {
	GUID   string
	Status models.DownloadState
	Owner  int64
	Book   string // "title / author name", empty for a row with no book ref
}

// TestQueueList_PinsRowsPerCaller pins what GET /queue returns for rows in
// each kind of state, owned by two users: live work, the three failed states
// however old, an import inside the 7 day window and one outside it, each with
// its book and author link. It holds for a user under tenancy, for an admin and
// with tenancy off, and was written to pass unchanged before and after the
// list query was split in two and the book links moved to a narrow lookup.
func TestQueueList_PinsRowsPerCaller(t *testing.T) {
	h, database, downloads, _, books, ctx := queueFixture(t)
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID: "OL-BUTLER", Name: "Octavia E. Butler", SortName: "Butler, Octavia",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-KINDRED", AuthorID: author.ID, Title: "Kindred", SortTitle: "kindred",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	day := 24 * time.Hour
	// Listed newest first, so each row's age is also its place in the response.
	fixture := []struct {
		guid   string
		status models.DownloadState
		owner  int64
		age    time.Duration
		book   bool
	}{
		{"alice-downloading", models.StateDownloading, alice.ID, 1 * time.Hour, true},
		{"bob-downloading", models.StateDownloading, bob.ID, 2 * time.Hour, false},
		{"alice-imported-yesterday", models.StateImported, alice.ID, 1 * day, true},
		{"bob-imported-yesterday", models.StateImported, bob.ID, 1*day + time.Hour, true},
		{"alice-imported-last-month", models.StateImported, alice.ID, 30 * day, true},
		{"bob-imported-last-month", models.StateImported, bob.ID, 31 * day, false},
		{"alice-import-failed", models.StateImportFailed, alice.ID, 100 * day, false},
		{"alice-import-blocked", models.StateImportBlocked, alice.ID, 101 * day, true},
		{"alice-failed", models.StateFailed, alice.ID, 200 * day, true},
		{"bob-failed", models.StateFailed, bob.ID, 201 * day, true},
	}
	for _, f := range fixture {
		d := &models.Download{GUID: f.guid, Title: f.guid, Status: models.StateGrabbed, Protocol: "usenet"}
		if f.book {
			d.BookID = &book.ID
		}
		if err := downloads.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-f.age)
		var imported any
		if f.status == models.StateImported {
			imported = at
		}
		if _, err := database.Exec(`UPDATE downloads SET status=?, added_at=?, imported_at=?, owner_user_id=? WHERE id=?`,
			f.status, at, imported, f.owner, d.ID); err != nil {
			t.Fatal(err)
		}
	}

	const linked = "Kindred / Octavia E. Butler"
	pin := func(guid string, status models.DownloadState, owner int64, hasBook bool) queuePin {
		p := queuePin{GUID: guid, Status: status, Owner: owner}
		if hasBook {
			p.Book = linked
		}
		return p
	}
	aliceRows := []queuePin{
		pin("alice-downloading", models.StateDownloading, alice.ID, true),
		pin("alice-imported-yesterday", models.StateImported, alice.ID, true),
		pin("alice-import-failed", models.StateImportFailed, alice.ID, false),
		pin("alice-import-blocked", models.StateImportBlocked, alice.ID, true),
		pin("alice-failed", models.StateFailed, alice.ID, true),
	}
	bobRows := []queuePin{
		pin("bob-downloading", models.StateDownloading, bob.ID, false),
		pin("bob-imported-yesterday", models.StateImported, bob.ID, true),
		pin("bob-failed", models.StateFailed, bob.ID, true),
	}
	everyone := []queuePin{
		aliceRows[0], bobRows[0], aliceRows[1], bobRows[1], aliceRows[2], aliceRows[3], aliceRows[4], bobRows[2],
	}

	list := func(req *http.Request) []queuePin {
		t.Helper()
		rec := httptest.NewRecorder()
		h.List(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var resp queueListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		out := make([]queuePin, len(resp.Items))
		for i, it := range resp.Items {
			out[i] = queuePin{GUID: it.GUID, Status: it.Status, Owner: it.OwnerUserID}
			if it.Book != nil {
				if it.Book.ID != book.ID || it.Book.AuthorID != author.ID {
					t.Errorf("%s: book ref ids %+v, want book %d by author %d", it.GUID, it.Book, book.ID, author.ID)
				}
				out[i].Book = it.Book.Title + " / " + it.Book.AuthorName
			}
		}
		return out
	}
	check := func(name string, got, want []queuePin) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
	asAdmin := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil)
		ctx := auth.WithUserRole(auth.WithUserID(r.Context(), alice.ID), "admin")
		return r.WithContext(ctx)
	}

	// Tenancy off: every caller sees every listed row.
	check("tenancy off, user", list(reqAsUser(http.MethodGet, "/api/v1/queue", bob.ID)), everyone)

	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	check("tenancy on, alice", list(reqAsUser(http.MethodGet, "/api/v1/queue", alice.ID)), aliceRows)
	check("tenancy on, bob", list(reqAsUser(http.MethodGet, "/api/v1/queue", bob.ID)), bobRows)
	check("tenancy on, admin", list(asAdmin()), everyone)
}

// seedQueueHistory inserts live rows that are downloading and old rows that
// were imported a year ago, every one linked to a book, in one transaction.
func seedQueueHistory(b *testing.B, database *sql.DB, bookID int64, live, old int) {
	b.Helper()
	tx, err := database.Begin()
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC()
	ins := func(prefix string, i int, status models.DownloadState, at time.Time, imported any) {
		if _, err := tx.Exec(
			`INSERT INTO downloads (guid, book_id, title, nzb_url, status, protocol, added_at, imported_at)
			 VALUES (?, ?, ?, 'https://indexer.example/get/1', ?, 'usenet', ?, ?)`,
			fmt.Sprintf("%s-%d", prefix, i), bookID, fmt.Sprintf("Release %s %d [EPUB]", prefix, i), status, at, imported,
		); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < old; i++ {
		at := now.Add(-365*24*time.Hour - time.Duration(i)*time.Minute)
		ins("old", i, models.StateImported, at, at)
	}
	for i := 0; i < live; i++ {
		ins("live", i, models.StateDownloading, now.Add(-time.Duration(i)*time.Minute), nil)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkQueueList_OldImports measures GET /queue with the same 60 live
// downloads and a growing number of imports old enough to be left out of the
// response. The response is identical in every case, so the time should be
// too: the poll must cost what it returns, not what the table holds.
//
//	go test ./internal/api/ -run '^$' -bench BenchmarkQueueList_OldImports
func BenchmarkQueueList_OldImports(b *testing.B) {
	for _, old := range []int{0, 10000, 40000} {
		b.Run(fmt.Sprintf("old=%d", old), func(b *testing.B) {
			database, err := db.OpenMemory()
			if err != nil {
				b.Fatal(err)
			}
			defer database.Close()
			ctx := b.Context()
			author := &models.Author{ForeignID: "OL-A", Name: "Author", SortName: "Author", MetadataProvider: "openlibrary"}
			if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
				b.Fatal(err)
			}
			books := db.NewBookRepo(database)
			book := &models.Book{ForeignID: "OL-B", AuthorID: author.ID, Title: "Book", SortTitle: "book", Genres: []string{}, MetadataProvider: "openlibrary"}
			if err := books.Create(ctx, book); err != nil {
				b.Fatal(err)
			}
			seedQueueHistory(b, database, book.ID, 60, old)
			h := NewQueueHandler(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), books, nil)

			b.ResetTimer()
			for b.Loop() {
				rec := httptest.NewRecorder()
				h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil))
				if rec.Code != http.StatusOK {
					b.Fatalf("status %d", rec.Code)
				}
			}
		})
	}
}
