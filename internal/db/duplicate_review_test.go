package db

import (
	"context"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestListForDuplicateScanScopesByAuthorOwner(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := NewUserRepo(database)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	series := NewSeriesRepo(database)

	alice, err := users.Create(ctx, "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, owner int64) *models.Author {
		a := &models.Author{ForeignID: "OL-" + name, Name: name, SortName: name, MetadataProvider: "openlibrary"}
		var err error
		if owner != 0 {
			err = authors.CreateForUser(ctx, a, owner)
		} else {
			err = authors.Create(ctx, a)
		}
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	aliceAuthor, bobAuthor, shared := mk("Alice", alice.ID), mk("Bob", bob.ID), mk("Shared", 0)
	s := &models.Series{ForeignID: "S1", Title: "Saga"}
	if err := series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	for i, a := range []*models.Author{aliceAuthor, bobAuthor, shared} {
		b := &models.Book{ForeignID: "OL-B" + strconv.Itoa(i), AuthorID: a.ID, Title: a.Name + " Book", SortTitle: a.Name, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Description: "blurb"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := series.LinkBook(ctx, s.ID, b.ID, "1", false); err != nil {
			t.Fatal(err)
		}
	}

	all, names, err := books.ListForDuplicateScan(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || names[bobAuthor.ID] != "Bob" {
		t.Fatalf("unscoped: %d books, names %v", len(all), names)
	}
	if all[0].Description != "" || all[0].Title == "" {
		t.Errorf("thin row carries the wrong columns: %+v", all[0])
	}
	scoped, _, err := books.ListForDuplicateScan(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 || scoped[0].AuthorID != aliceAuthor.ID || scoped[1].AuthorID != shared.ID {
		t.Errorf("alice's scan = %+v, want her author and the unowned one", scoped)
	}
	ms, err := series.ListBookSeriesMembershipsForOwner(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Errorf("alice's memberships cover %d books, want 2", len(ms))
	}
}

func TestListDuplicateEvidenceBatchesBeyondOneChunk(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	editions := NewEditionRepo(database)
	a := &models.Author{ForeignID: "OL-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < duplicateEvidenceChunk+20; i++ {
		b := &models.Book{ForeignID: "OL-" + strconv.Itoa(i), AuthorID: a.ID, Title: "T" + strconv.Itoa(i), SortTitle: "T", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	last := ids[len(ids)-1]
	isbn, asin := "9780553418026", "B00X"
	if err := editions.Upsert(ctx, &models.Edition{ForeignID: "E1", BookID: last, Title: "e", ISBN13: &isbn, ASIN: &asin}); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, last, "ebook", "/nowhere/book.epub"); err != nil {
		t.Fatal(err)
	}

	got, err := books.ListDuplicateEvidence(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	ev := got[last]
	if len(ev.Files) != 1 || ev.Files[0].Format != "ebook" || len(ev.ISBNs) != 1 || ev.ISBNs[0] != isbn {
		t.Errorf("evidence for a book in the second chunk = %+v", ev)
	}
	if _, ok := got[ids[0]]; ok {
		t.Errorf("a book with nothing recorded should be absent, got %+v", got[ids[0]])
	}
}
