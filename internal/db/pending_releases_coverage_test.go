package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestPendingReleaseRepo_Coverage drives every PendingReleaseRepo method
// against one in memory database: upsert semantics on the guid key, the
// format and owner filters, first_seen ordering and each delete scope.
func TestPendingReleaseRepo_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	alice := seedUser(t, ctx, database, "pr-alice")
	bob := seedUser(t, ctx, database, "pr-bob")
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := mkAuthor(t, authors, ctx, "OL-PR-A")

	newBook := func(fid string, owner int64) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: fid, AuthorID: author.ID, Title: fid, SortTitle: fid,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
			Monitored: true, OwnerUserID: owner,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	aliceBook := newBook("OL-PR-1", alice)
	bobBook := newBook("OL-PR-2", bob)

	repo := NewPendingReleaseRepo(database)
	idx := int64(7)
	add := func(bookID int64, media, guid, reason string, indexer *int64, quality string) {
		t.Helper()
		pr := &models.PendingRelease{
			BookID: bookID, MediaType: media, Title: "T " + guid, IndexerID: indexer,
			GUID: guid, Protocol: "torrent", Size: 1024, AgeMinutes: 10, Quality: quality,
			CustomScore: 3, Reason: reason, ReleaseJSON: `{"guid":"` + guid + `"}`,
		}
		if err := repo.Upsert(ctx, pr); err != nil {
			t.Fatalf("Upsert %s: %v", guid, err)
		}
	}
	add(aliceBook.ID, "ebook", "g-1", "delay", &idx, "epub")
	add(aliceBook.ID, "audiobook", "g-2", "delay", nil, "")
	add(bobBook.ID, "ebook", "g-3", "delay", nil, "")

	// Pin first_seen so the DESC ordering is deterministic.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, guid := range []string{"g-1", "g-2", "g-3"} {
		if _, err := database.ExecContext(ctx, `UPDATE pending_releases SET first_seen = ? WHERE guid = ?`,
			base.Add(time.Duration(i)*time.Hour), guid); err != nil {
			t.Fatal(err)
		}
	}

	guids := func(prs []models.PendingRelease) []string {
		out := make([]string, 0, len(prs))
		for _, p := range prs {
			out = append(out, p.GUID)
		}
		return out
	}
	assertGUIDs := func(label string, prs []models.PendingRelease, err error, want ...string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		got := guids(prs)
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want %v", label, got, want)
			}
		}
	}

	all, err := repo.List(ctx)
	assertGUIDs("List", all, err, "g-3", "g-2", "g-1")

	// Upsert on an existing guid refreshes reason and age only.
	if err := repo.Upsert(ctx, &models.PendingRelease{
		BookID: bobBook.ID, MediaType: "audiobook", Title: "changed", GUID: "g-1",
		Protocol: "usenet", AgeMinutes: 99, Reason: "score", ReleaseJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	all, err = repo.List(ctx)
	assertGUIDs("List after re-upsert", all, err, "g-3", "g-2", "g-1")
	g1 := all[2]
	if g1.Reason != "score" || g1.AgeMinutes != 99 {
		t.Errorf("re-upsert reason/age = %q/%d, want score/99", g1.Reason, g1.AgeMinutes)
	}
	if g1.BookID != aliceBook.ID || g1.MediaType != "ebook" || g1.Title != "T g-1" || g1.Protocol != "torrent" {
		t.Errorf("re-upsert must not rewrite identity columns: %+v", g1)
	}
	if g1.IndexerID == nil || *g1.IndexerID != 7 || g1.Quality != "epub" {
		t.Errorf("indexer/quality = %v/%q, want 7/epub", g1.IndexerID, g1.Quality)
	}
	if all[1].IndexerID != nil || all[1].Quality != "" {
		t.Errorf("NULL indexer/quality should scan to nil/empty: %+v", all[1])
	}

	byBook, err := repo.ListByBook(ctx, aliceBook.ID)
	assertGUIDs("ListByBook", byBook, err, "g-2", "g-1")
	byFormat, err := repo.ListByBookAndMediaType(ctx, aliceBook.ID, "audiobook")
	assertGUIDs("ListByBookAndMediaType", byFormat, err, "g-2")

	forAlice, err := repo.ListForUser(ctx, alice)
	assertGUIDs("ListForUser(alice)", forAlice, err, "g-2", "g-1")
	forBob, err := repo.ListForUser(ctx, bob)
	assertGUIDs("ListForUser(bob)", forBob, err, "g-3")
	forAll, err := repo.ListForUser(ctx, 0)
	assertGUIDs("ListForUser(0)", forAll, err, "g-3", "g-2", "g-1")

	owner, ok, err := repo.GetOwnerByID(ctx, g1.ID)
	if err != nil || !ok || owner != alice {
		t.Errorf("GetOwnerByID = %d,%v,%v want %d,true,nil", owner, ok, err, alice)
	}
	if _, ok, err := repo.GetOwnerByID(ctx, 999999); err != nil || ok {
		t.Errorf("GetOwnerByID(missing) ok=%v err=%v, want false,nil", ok, err)
	}

	got, err := repo.GetByID(ctx, g1.ID)
	if err != nil || got == nil || got.GUID != "g-1" || got.ReleaseJSON != `{"guid":"g-1"}` {
		t.Fatalf("GetByID = %+v, %v", got, err)
	}
	if got, err := repo.GetByID(ctx, 999999); err != nil || got != nil {
		t.Errorf("GetByID(missing) = %+v, %v; want nil,nil", got, err)
	}

	// Missing book violates the FK.
	if err := repo.Upsert(ctx, &models.PendingRelease{
		BookID: 999999, MediaType: "ebook", Title: "x", GUID: "g-orphan", Protocol: "torrent", Reason: "r", ReleaseJSON: "{}",
	}); err == nil {
		t.Error("Upsert with a missing book should fail the foreign key")
	}

	// Each delete only removes its own scope.
	if err := repo.DeleteByBookAndMediaType(ctx, aliceBook.ID, "audiobook"); err != nil {
		t.Fatal(err)
	}
	all, err = repo.List(ctx)
	assertGUIDs("after DeleteByBookAndMediaType", all, err, "g-3", "g-1")
	if err := repo.DeleteByGUID(ctx, "g-3"); err != nil {
		t.Fatal(err)
	}
	all, err = repo.List(ctx)
	assertGUIDs("after DeleteByGUID", all, err, "g-1")
	add(aliceBook.ID, "audiobook", "g-4", "delay", nil, "")
	add(bobBook.ID, "ebook", "g-5", "delay", nil, "")
	if err := repo.DeleteByBook(ctx, aliceBook.ID); err != nil {
		t.Fatal(err)
	}
	all, err = repo.List(ctx)
	assertGUIDs("after DeleteByBook", all, err, "g-5")
	if err := repo.DeleteByID(ctx, all[0].ID); err != nil {
		t.Fatal(err)
	}
	all, err = repo.List(ctx)
	assertGUIDs("after DeleteByID", all, err)

	// Deleting the book cascades to its pending releases.
	add(bobBook.ID, "ebook", "g-6", "delay", nil, "")
	if _, err := database.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, bobBook.ID); err != nil {
		t.Fatal(err)
	}
	all, err = repo.List(ctx)
	assertGUIDs("after book delete", all, err)
}
