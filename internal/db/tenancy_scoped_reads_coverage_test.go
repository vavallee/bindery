package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestTenancyScopedReads_Coverage pins the owner scoping of the per-user read
// paths on downloads, history, books and authors. Alice and Bob each own one
// author and book, and one book is unowned (NULL owner), so every assertion
// separates "mine", "someone else's" and "nobody's".
func TestTenancyScopedReads_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	alice := seedUser(t, ctx, database, "scope-alice")
	bob := seedUser(t, ctx, database, "scope-bob")
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	mkOwnedAuthor := func(fid, name string, owner int64) *models.Author {
		t.Helper()
		a := &models.Author{ForeignID: fid, Name: name, SortName: name, MetadataProvider: "openlibrary", Monitored: true}
		if err := authors.CreateForUser(ctx, a, owner); err != nil {
			t.Fatal(err)
		}
		return a
	}
	mkOwnedBook := func(authorID int64, fid, title string, owner int64, status string) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: fid, AuthorID: authorID, Title: title, SortTitle: title, Status: status,
			Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	aliceAuthor := mkOwnedAuthor("OL-SC-A", "Alice Writer", alice)
	bobAuthor := mkOwnedAuthor("OL-SC-B", "Bob Writer", bob)
	nobodyAuthor := mkOwnedAuthor("OL-SC-N", "Nobody Writer", 0)
	aliceBook := mkOwnedBook(aliceAuthor.ID, "OL-SC-1W", "Alpha", alice, models.BookStatusWanted)
	bobBook := mkOwnedBook(bobAuthor.ID, "OL-SC-2W", "Bravo", bob, models.BookStatusWanted)
	nobodyBook := mkOwnedBook(nobodyAuthor.ID, "OL-SC-3W", "Charlie", 0, models.BookStatusWanted)

	bookIDs := func(bs []models.Book) map[int64]bool {
		out := map[int64]bool{}
		for _, b := range bs {
			out[b.ID] = true
		}
		return out
	}

	t.Run("books", func(t *testing.T) {
		page, total, err := books.ListPage(ctx, alice, 0, -5)
		if err != nil {
			t.Fatal(err)
		}
		ids := bookIDs(page)
		if total != 2 || !ids[aliceBook.ID] || !ids[nobodyBook.ID] || ids[bobBook.ID] {
			t.Fatalf("ListPage(alice) total=%d ids=%v; want alice's and the unowned book", total, ids)
		}
		page, total, err = books.ListPage(ctx, 0, 1, 1)
		if err != nil || total != 3 || len(page) != 1 || page[0].ID != bobBook.ID {
			t.Fatalf("ListPage(0,1,1) = %v total=%d err=%v; want the second title (Bravo)", page, total, err)
		}

		// Excluded books still appear in the IncludingExcluded variant.
		if err := books.SetExcluded(ctx, aliceBook.ID, true); err != nil {
			t.Fatal(err)
		}
		inc, err := books.ListByStatusIncludingExcludedAndUser(ctx, models.BookStatusWanted, alice)
		if err != nil {
			t.Fatal(err)
		}
		if ids := bookIDs(inc); len(ids) != 2 || !ids[aliceBook.ID] || !ids[nobodyBook.ID] {
			t.Fatalf("ListByStatusIncludingExcludedAndUser(alice) = %v", ids)
		}
		inc, err = books.ListByStatusIncludingExcludedAndUser(ctx, models.BookStatusWanted, 0)
		if err != nil || len(inc) != 3 {
			t.Fatalf("ListByStatusIncludingExcludedAndUser(0) = %d rows, %v", len(inc), err)
		}
		if err := books.SetExcluded(ctx, aliceBook.ID, false); err != nil {
			t.Fatal(err)
		}

		if got, err := books.GetByForeignIDVisibleTo(ctx, "OL-SC-2W", alice); err != nil || got != nil {
			t.Fatalf("GetByForeignIDVisibleTo(bob's book, alice) = %+v, %v; want nil", got, err)
		}
		if got, err := books.GetByForeignIDVisibleTo(ctx, "OL-SC-3W", alice); err != nil || got == nil || got.ID != nobodyBook.ID {
			t.Fatalf("GetByForeignIDVisibleTo(unowned, alice) = %+v, %v; want the unowned book", got, err)
		}
		if got, err := books.GetByForeignIDVisibleTo(ctx, "OL-SC-2W", 0); err != nil || got == nil || got.ID != bobBook.ID {
			t.Fatalf("GetByForeignIDVisibleTo(bob's book, 0) = %+v, %v; want it unscoped", got, err)
		}

		m, err := books.LibraryIDsByForeignIDsForUser(ctx, []string{"OL-SC-1W", "OL-SC-2W", "OL-SC-3W", "", "OL-SC-1W", "missing"}, alice)
		if err != nil {
			t.Fatal(err)
		}
		if len(m) != 2 || m["OL-SC-1W"] != aliceBook.ID || m["OL-SC-3W"] != nobodyBook.ID {
			t.Fatalf("LibraryIDsByForeignIDsForUser(alice) = %v", m)
		}
		if m, err := books.LibraryIDsByForeignIDsForUser(ctx, []string{"", ""}, alice); err != nil || len(m) != 0 {
			t.Fatalf("LibraryIDsByForeignIDsForUser(blank) = %v, %v; want empty without a query", m, err)
		}
		if m, err := books.LibraryIDsByForeignIDsForUser(ctx, []string{"OL-SC-2W"}, 0); err != nil || m["OL-SC-2W"] != bobBook.ID {
			t.Fatalf("LibraryIDsByForeignIDsForUser(0) = %v, %v", m, err)
		}
	})

	t.Run("authors", func(t *testing.T) {
		page, total, err := authors.ListPage(ctx, bob, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(page) != 2 || page[0].ID != bobAuthor.ID || page[1].ID != nobodyAuthor.ID {
			t.Fatalf("ListPage(bob) = %+v total=%d; want Bob Writer then Nobody Writer", page, total)
		}

		if err := authors.UpsertAuthorIdentifier(ctx, aliceAuthor.ID, "hc:alice-alt"); err != nil {
			t.Fatal(err)
		}
		// The same identifier on a second author is a typed conflict.
		err = authors.UpsertAuthorIdentifier(ctx, bobAuthor.ID, "hc:alice-alt")
		var conflict *AuthorIdentifierConflictError
		if !errors.As(err, &conflict) || conflict.AuthorID != aliceAuthor.ID || !errors.Is(err, ErrAuthorIdentifierConflict) {
			t.Fatalf("UpsertAuthorIdentifier conflict = %v; want AuthorIdentifierConflictError naming alice's author", err)
		}
		if (*AuthorIdentifierConflictError)(nil).Error() != ErrAuthorIdentifierConflict.Error() {
			t.Error("nil AuthorIdentifierConflictError should fall back to the sentinel text")
		}

		ids, err := authors.ListAuthorIdentifiers(ctx, aliceAuthor.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 {
			t.Fatalf("ListAuthorIdentifiers = %+v; want the primary id and hc:alice-alt", ids)
		}
		found := false
		for _, id := range ids {
			if id.ForeignID == "hc:alice-alt" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ListAuthorIdentifiers = %+v; missing hc:alice-alt", ids)
		}

		m, err := authors.LibraryIDsByAnyForeignIDsForUser(ctx, []string{" OL-SC-A ", "hc:alice-alt", "OL-SC-B", "OL-SC-N", "", "OL-SC-A"}, alice)
		if err != nil {
			t.Fatal(err)
		}
		if len(m) != 3 || m["OL-SC-A"] != aliceAuthor.ID || m["hc:alice-alt"] != aliceAuthor.ID || m["OL-SC-N"] != nobodyAuthor.ID {
			t.Fatalf("LibraryIDsByAnyForeignIDsForUser(alice) = %v; want alice's two ids and the unowned author", m)
		}
		if m, err := authors.LibraryIDsByAnyForeignIDsForUser(ctx, []string{"hc:alice-alt"}, bob); err != nil || len(m) != 0 {
			t.Fatalf("LibraryIDsByAnyForeignIDsForUser(bob) = %v, %v; alice's identifier must not leak", m, err)
		}
		if m, err := authors.LibraryIDsByAnyForeignIDsForUser(ctx, []string{"OL-SC-B"}, 0); err != nil || m["OL-SC-B"] != bobAuthor.ID {
			t.Fatalf("LibraryIDsByAnyForeignIDsForUser(0) = %v, %v", m, err)
		}
		if m, err := authors.LibraryIDsByAnyForeignIDsForUser(ctx, nil, alice); err != nil || len(m) != 0 {
			t.Fatalf("LibraryIDsByAnyForeignIDsForUser(nil) = %v, %v", m, err)
		}

		if a, err := authors.GetByAnyForeignIDForUser(ctx, "hc:alice-alt", alice); err != nil || a == nil || a.ID != aliceAuthor.ID {
			t.Fatalf("GetByAnyForeignIDForUser(alt, alice) = %+v, %v", a, err)
		}
		if a, err := authors.GetByAnyForeignIDForUser(ctx, "hc:alice-alt", bob); err != nil || a != nil {
			t.Fatalf("GetByAnyForeignIDForUser(alt, bob) = %+v, %v; want nil", a, err)
		}
		if a, err := authors.GetByAnyForeignIDForUser(ctx, "  ", alice); err != nil || a != nil {
			t.Fatalf("GetByAnyForeignIDForUser(blank) = %+v, %v", a, err)
		}

		if err := authors.DeleteAuthorIdentifier(ctx, aliceAuthor.ID, " "); err != nil {
			t.Fatal(err)
		}
		if err := authors.DeleteAuthorIdentifier(ctx, aliceAuthor.ID, "hc:alice-alt"); err != nil {
			t.Fatal(err)
		}
		if ids, _ := authors.ListAuthorIdentifiers(ctx, aliceAuthor.ID); len(ids) != 1 {
			t.Fatalf("identifiers after delete = %+v", ids)
		}
	})

	t.Run("downloads", func(t *testing.T) {
		repo := NewDownloadRepo(database)
		mk := func(guid string, owner int64, bookID *int64, status models.DownloadState) *models.Download {
			t.Helper()
			d := &models.Download{GUID: guid, Title: guid, NZBURL: "http://x/" + guid, Status: status, Protocol: "usenet", OwnerUserID: owner, BookID: bookID}
			if err := repo.Create(ctx, d); err != nil {
				t.Fatal(err)
			}
			return d
		}
		aID, bID := aliceBook.ID, bobBook.ID
		da := mk("sc-dl-a", alice, &aID, models.StateGrabbed)
		dbob := mk("sc-dl-b", bob, &bID, models.StateGrabbed)
		dn := mk("sc-dl-n", 0, nil, models.StateCompleted)

		list, err := repo.ListByUser(ctx, alice)
		if err != nil || len(list) != 1 || list[0].ID != da.ID {
			t.Fatalf("ListByUser(alice) = %+v, %v", list, err)
		}
		if list, _ := repo.ListByUser(ctx, 0); len(list) < 3 {
			t.Fatalf("ListByUser(0) = %d rows, want all", len(list))
		}
		list, err = repo.ListByStatusAndUser(ctx, models.StateGrabbed, bob)
		if err != nil || len(list) != 1 || list[0].ID != dbob.ID {
			t.Fatalf("ListByStatusAndUser(grabbed, bob) = %+v, %v", list, err)
		}
		if list, _ := repo.ListByStatusAndUser(ctx, models.StateCompleted, bob); len(list) != 0 {
			t.Fatalf("ListByStatusAndUser(completed, bob) = %+v; the unowned row is not bob's", list)
		}

		if owner, ok, err := repo.GetOwnerByID(ctx, dbob.ID); err != nil || !ok || owner != bob {
			t.Fatalf("GetOwnerByID(bob's) = %d,%v,%v", owner, ok, err)
		}
		if owner, ok, err := repo.GetOwnerByID(ctx, dn.ID); err != nil || !ok || owner != 0 {
			t.Fatalf("GetOwnerByID(unowned) = %d,%v,%v; want 0,true", owner, ok, err)
		}
		if _, ok, err := repo.GetOwnerByID(ctx, 999999); err != nil || ok {
			t.Fatalf("GetOwnerByID(missing) ok=%v err=%v", ok, err)
		}

		if err := repo.SetBookID(ctx, dn.ID, nobodyBook.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.IncrementImportRetryCount(ctx, dn.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.IncrementImportRetryCount(ctx, dn.ID); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetByID(ctx, dn.ID)
		if err != nil || got == nil || got.BookID == nil || *got.BookID != nobodyBook.ID || got.ImportRetryCount != 2 {
			t.Fatalf("after SetBookID+2x increment = %+v, %v", got, err)
		}
		// SetBookID onto a missing book violates the FK.
		if err := repo.SetBookID(ctx, dn.ID, 999999); err == nil {
			t.Error("SetBookID to a missing book should fail the foreign key")
		}

		// SetErrorWithStatus: missing row, invalid transition, then a valid one.
		if err := repo.SetErrorWithStatus(ctx, 999999, models.StateImportFailed, "x"); err == nil {
			t.Error("SetErrorWithStatus on a missing row should fail")
		}
		var inv models.ErrInvalidTransition
		if err := repo.SetErrorWithStatus(ctx, da.ID, models.StateImportBlocked, "x"); !errors.As(err, &inv) || inv.From != models.StateGrabbed {
			t.Fatalf("SetErrorWithStatus grabbed->importBlocked = %v; want ErrInvalidTransition from grabbed", err)
		}
		if err := repo.SetErrorWithStatus(ctx, dn.ID, models.StateImportFailed, "no match"); err != nil {
			t.Fatal(err)
		}
		if got, _ := repo.GetByID(ctx, dn.ID); got.Status != models.StateImportFailed || got.ErrorMessage != "no match" {
			t.Fatalf("after SetErrorWithStatus = %+v", got)
		}

		if err := repo.DeleteByBook(ctx, aliceBook.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := repo.GetByID(ctx, da.ID); got != nil {
			t.Fatalf("download for alice's book survived DeleteByBook: %+v", got)
		}
		if got, _ := repo.GetByID(ctx, dbob.ID); got == nil {
			t.Fatal("DeleteByBook removed another book's download")
		}
	})

	t.Run("history", func(t *testing.T) {
		repo := NewHistoryRepo(database)
		aID, bID := aliceBook.ID, bobBook.ID
		events := []*models.HistoryEvent{
			{BookID: &aID, EventType: models.HistoryEventGrabbed, SourceTitle: "h-a-grab"},
			{BookID: &aID, EventType: models.HistoryEventBookImported, SourceTitle: "h-a-import"},
			{BookID: &bID, EventType: models.HistoryEventGrabbed, SourceTitle: "h-b-grab"},
			{BookID: nil, EventType: models.HistoryEventGrabbed, SourceTitle: "h-orphan"},
		}
		base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		for i, e := range events {
			if err := repo.Create(ctx, e); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `UPDATE history SET created_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Minute), e.ID); err != nil {
				t.Fatal(err)
			}
		}
		titles := func(evs []models.HistoryEvent) []string {
			out := []string{}
			for _, e := range evs {
				out = append(out, e.SourceTitle)
			}
			return out
		}
		same := func(got []string, want ...string) bool {
			if len(got) != len(want) {
				return false
			}
			for i := range want {
				if got[i] != want[i] {
					return false
				}
			}
			return true
		}

		evs, err := repo.ListForUser(ctx, alice)
		if err != nil || !same(titles(evs), "h-orphan", "h-a-import", "h-a-grab") {
			t.Fatalf("ListForUser(alice) = %v, %v", titles(evs), err)
		}
		if evs, _ := repo.ListForUser(ctx, 0); len(evs) != 4 {
			t.Fatalf("ListForUser(0) = %v", titles(evs))
		}
		evs, err = repo.ListByBookAndUser(ctx, aliceBook.ID, bob)
		if err != nil || len(evs) != 0 {
			t.Fatalf("ListByBookAndUser(alice's book, bob) = %v, %v; want nothing", titles(evs), err)
		}
		evs, err = repo.ListByBookAndUser(ctx, aliceBook.ID, alice)
		if err != nil || !same(titles(evs), "h-a-import", "h-a-grab") {
			t.Fatalf("ListByBookAndUser(alice) = %v, %v", titles(evs), err)
		}
		if evs, _ := repo.ListByBookAndUser(ctx, bobBook.ID, 0); !same(titles(evs), "h-b-grab") {
			t.Fatalf("ListByBookAndUser(bob's book, 0) = %v", titles(evs))
		}
		evs, err = repo.ListByTypeAndUser(ctx, models.HistoryEventGrabbed, bob)
		if err != nil || !same(titles(evs), "h-orphan", "h-b-grab") {
			t.Fatalf("ListByTypeAndUser(grabbed, bob) = %v, %v", titles(evs), err)
		}
		if evs, _ := repo.ListByTypeAndUser(ctx, models.HistoryEventGrabbed, 0); len(evs) != 3 {
			t.Fatalf("ListByTypeAndUser(grabbed, 0) = %v", titles(evs))
		}

		if owner, ok, err := repo.GetOwnerByID(ctx, events[2].ID); err != nil || !ok || owner != bob {
			t.Fatalf("GetOwnerByID(bob's event) = %d,%v,%v", owner, ok, err)
		}
		if owner, ok, err := repo.GetOwnerByID(ctx, events[3].ID); err != nil || !ok || owner != 0 {
			t.Fatalf("GetOwnerByID(orphan) = %d,%v,%v; want 0,true", owner, ok, err)
		}
		if _, ok, err := repo.GetOwnerByID(ctx, 999999); err != nil || ok {
			t.Fatalf("GetOwnerByID(missing) ok=%v err=%v", ok, err)
		}
	})
}

// TestQueryScopeHelpers pins the SQL the scope helpers emit, including the
// "no existing WHERE" branch that the repos rarely take.
func TestQueryScopeHelpers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		where     string
		args      []any
		wantWhere string
		wantArgs  int
		including bool
	}{
		{"unscoped keeps input", "WHERE a = ?", []any{1}, "WHERE a = ?", 1, false},
		{"empty where strict", "", nil, "WHERE t.owner = ?", 1, false},
		{"append strict", "WHERE a = ?", []any{1}, "WHERE a = ? AND t.owner = ?", 2, false},
		{"empty where including null", "", nil, "WHERE (t.owner = ? OR t.owner IS NULL)", 1, true},
		{"append including null", "WHERE a = ?", []any{1}, "WHERE a = ? AND (t.owner = ? OR t.owner IS NULL)", 2, true},
	}
	for _, tc := range cases {
		user := int64(5)
		if tc.name == "unscoped keeps input" {
			user = 0
		}
		var w string
		var a []any
		if tc.including {
			w, a = QueryScopeForIncludingNull("t.owner", tc.where, user, tc.args...)
		} else {
			w, a = QueryScopeFor("t.owner", tc.where, user, tc.args...)
		}
		if w != tc.wantWhere || len(a) != tc.wantArgs {
			t.Errorf("%s: got %q %v, want %q with %d args", tc.name, w, a, tc.wantWhere, tc.wantArgs)
		}
		if user != 0 && a[len(a)-1] != user {
			t.Errorf("%s: user id must be the last arg, got %v", tc.name, a)
		}
	}
	if w, a := QueryScope("", 3); w != "WHERE owner_user_id = ?" || len(a) != 1 {
		t.Errorf("QueryScope = %q %v", w, a)
	}
	if w, a := QueryScopeForIncludingNull("t.owner", "WHERE x", 0); w != "WHERE x" || len(a) != 0 {
		t.Errorf("QueryScopeForIncludingNull unscoped = %q %v", w, a)
	}
}
