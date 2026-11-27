package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBookRepo_RefsByIDs: the narrow lookup returns the same id, title, author
// id and author name GetByIDs does for every book, across more ids than one
// statement carries, and tolerates repeated, missing and no ids.
func TestBookRepo_RefsByIDs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	var authorIDs []int64
	for i, name := range []string{"Ursula K. Le Guin", "Octavia E. Butler"} {
		a := &models.Author{
			ForeignID: fmt.Sprintf("OL-A%d", i), Name: name, SortName: name,
			MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatalf("create author: %v", err)
		}
		authorIDs = append(authorIDs, a.ID)
	}
	// More books than bookRefsChunk, so the lookup spans statements.
	total := bookRefsChunk + 7
	ids := make([]int64, 0, total+3)
	for i := 0; i < total; i++ {
		b := &models.Book{
			ForeignID: fmt.Sprintf("OL-B%d", i), AuthorID: authorIDs[i%2], Title: fmt.Sprintf("Book %d", i),
			SortTitle: fmt.Sprintf("book %d", i), Genres: []string{},
			MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatalf("create book: %v", err)
		}
		ids = append(ids, b.ID)
	}

	full, err := books.GetByIDs(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	// A repeated id and an id with no row are both tolerated.
	asked := append(append([]int64{}, ids...), ids[0], ids[total-1], 999999)
	got, err := books.RefsByIDs(ctx, asked)
	if err != nil {
		t.Fatalf("RefsByIDs: %v", err)
	}
	if len(got) != total {
		t.Fatalf("got %d refs, want %d", len(got), total)
	}
	if _, ok := got[999999]; ok {
		t.Error("a ref came back for an id with no book")
	}
	for _, id := range ids {
		b := full[id]
		want := BookRef{ID: b.ID, Title: b.Title, AuthorID: b.AuthorID, AuthorName: b.Author.Name}
		if got[id] != want {
			t.Fatalf("book %d: ref %+v, want %+v (what GetByIDs holds)", id, got[id], want)
		}
	}

	empty, err := books.RefsByIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no ids: got %v, err %v; want an empty map", empty, err)
	}
}

// A book whose author row is gone keeps its author id and gets an empty name,
// rather than dropping out of the result.
func TestBookRepo_RefsByIDs_AuthorRowGone(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	a := &models.Author{ForeignID: "OL-A", Name: "Gone", SortName: "Gone", MetadataProvider: "openlibrary"}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	b := &models.Book{ForeignID: "OL-B", AuthorID: a.ID, Title: "Orphan", SortTitle: "orphan", Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Remove the author without the cascade that would take the book too.
	if _, err := database.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM authors WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	got, err := books.RefsByIDs(ctx, []int64{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if want := (BookRef{ID: b.ID, Title: "Orphan", AuthorID: a.ID}); got[b.ID] != want {
		t.Fatalf("ref %+v, want %+v", got[b.ID], want)
	}
}

// A closed database surfaces as an error, not as an empty result.
func TestBookRepo_RefsByIDs_QueryError(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	if _, err := NewBookRepo(database).RefsByIDs(context.Background(), []int64{1}); err == nil {
		t.Fatal("want an error from a closed database")
	}
}
