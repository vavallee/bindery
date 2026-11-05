package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A shared author can hold several accounts' books. The scoped variants must
// return only the asking user's rows plus unowned ones, and userID 0 must keep
// returning everything.
func TestScopedReadsOnSharedAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	authors, books, series := NewAuthorRepo(database), NewBookRepo(database), NewSeriesRepo(database)

	shared := &models.Author{ForeignID: "OL-S", Name: "Shared", SortName: "Shared", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	mk := func(fid, title string, owner int64, excluded bool) *models.Book {
		b := &models.Book{ForeignID: fid, AuthorID: shared.ID, Title: title, SortTitle: title, Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if excluded {
			if err := books.SetExcluded(ctx, b.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	aliceB := mk("A", "Same", alice.ID, false)
	aliceX := mk("AX", "Alice Excluded", alice.ID, true)
	bobB := mk("B", "Same", bob.ID, false)
	bobX := mk("BX", "Bob Excluded", bob.ID, true)
	open := mk("O", "Unowned", 0, false)

	idsOf := func(in []models.Book) map[int64]bool {
		m := map[int64]bool{}
		for _, b := range in {
			m[b.ID] = true
		}
		return m
	}
	check := func(name string, got map[int64]bool, want ...*models.Book) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %d rows", name, got, len(want))
		}
		for _, w := range want {
			if !got[w.ID] {
				t.Fatalf("%s: missing book %d (%s)", name, w.ID, w.Title)
			}
		}
	}

	all, err := books.ListIncludingExcludedAndUser(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("ListIncludingExcludedAndUser(alice)", idsOf(all), aliceB, aliceX, open)
	all, _ = books.ListIncludingExcludedAndUser(ctx, 0)
	check("ListIncludingExcludedAndUser(0)", idsOf(all), aliceB, aliceX, bobB, bobX, open)

	byAuthor, err := books.ListByAuthorIncludingExcludedAndUser(ctx, shared.ID, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("ListByAuthorIncludingExcludedAndUser(bob)", idsOf(byAuthor), bobB, bobX, open)
	byAuthor, _ = books.ListByAuthorIncludingExcludedAndUser(ctx, shared.ID, 0)
	check("ListByAuthorIncludingExcludedAndUser(0)", idsOf(byAuthor), aliceB, aliceX, bobB, bobX, open)

	// Duplicate scan: the thin rows follow the same rule, so bob's "Same" is
	// not a partner for alice's.
	scan, _, err := books.ListForDuplicateScan(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("ListForDuplicateScan(alice)", idsOf(scan), aliceB, aliceX, open)
	scan, _, _ = books.ListForDuplicateScan(ctx, 0)
	check("ListForDuplicateScan(0)", idsOf(scan), aliceB, aliceX, bobB, bobX, open)

	// Series by author, and series memberships for the duplicate scan.
	onlyBob, err := series.CreateManual(ctx, "Bob Only")
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := series.CreateManual(ctx, "Mixed")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []struct {
		s int64
		b *models.Book
	}{{onlyBob.ID, bobB}, {mixed.ID, aliceB}, {mixed.ID, bobX}} {
		if err := series.UpsertBookLink(ctx, l.s, l.b.ID, "1", true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := series.ListByAuthorAndUser(ctx, shared.ID, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != mixed.ID || len(got[0].Books) != 1 || got[0].Books[0].BookID != aliceB.ID {
		t.Fatalf("ListByAuthorAndUser(alice) = %+v, want only Mixed with alice's one book", got)
	}
	got, _ = series.ListByAuthorAndUser(ctx, shared.ID, 0)
	if len(got) != 2 {
		t.Fatalf("ListByAuthorAndUser(0) = %d series, want 2", len(got))
	}
	got, _ = series.ListByAuthor(ctx, shared.ID)
	if len(got) != 2 {
		t.Fatalf("ListByAuthor = %d series, want 2", len(got))
	}
	ms, err := series.ListBookSeriesMembershipsForOwner(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms[bobB.ID]) != 0 || len(ms[bobX.ID]) != 0 || len(ms[aliceB.ID]) != 1 {
		t.Fatalf("memberships for alice leak or miss: %v", ms)
	}
	ms, _ = series.ListBookSeriesMembershipsForOwner(ctx, 0)
	if len(ms[bobB.ID]) != 1 {
		t.Fatalf("unscoped memberships miss bob's: %v", ms)
	}
}
