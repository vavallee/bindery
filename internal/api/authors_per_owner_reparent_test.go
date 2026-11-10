package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Copies of one author made by two accounts under tenancy stay when tenancy is
// turned off (#3555). A catalogue sync of bob's copy then finds alice's Dune,
// filed under her copy with the same provider id. Before the fix the
// "duplicate author rows" branch of reparentMisattachedBook moved it under
// bob's copy: alice's next sync moved it back, and deleting bob's copy while
// it was parked there cascaded into her book. Two owners' copies are not
// duplicates, so the book stays put whatever the tenancy setting and whatever
// the provider says about authorship.
func TestFetchAuthorBooks_CopiesOfOneAuthorNeverTradeBooks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tenancy  bool
		credited []string
	}{
		{"tenancy off, credited", false, []string{"OL-HERBERT"}},
		{"tenancy off, authorship unknown", false, nil},
		{"tenancy on, credited", true, []string{"OL-HERBERT"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			ctx := context.Background()
			users := db.NewUserRepo(database)
			alice, err := users.Create(ctx, "alice", "h1")
			if err != nil {
				t.Fatal(err)
			}
			bob, err := users.Create(ctx, "bob", "h2")
			if err != nil {
				t.Fatal(err)
			}
			authorRepo := db.NewAuthorRepo(database)
			bookRepo := db.NewBookRepo(database)
			herbert := func(owner int64) *models.Author {
				a := &models.Author{
					ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank",
					MetadataProvider: "openlibrary", Monitored: true,
				}
				if err := authorRepo.CreateForUser(ctx, a, owner); err != nil {
					t.Fatalf("create herbert for %d: %v", owner, err)
				}
				return a
			}
			alices, bobs := herbert(alice.ID), herbert(bob.ID)
			dune := &models.Book{
				ForeignID: "OL-DUNE-W", AuthorID: alices.ID, Title: "Dune", SortTitle: "dune",
				Language: "eng", Status: models.BookStatusWanted, Monitored: true,
				Genres: []string{}, MetadataProvider: "openlibrary", OwnerUserID: alice.ID,
			}
			if err := bookRepo.Create(ctx, dune); err != nil {
				t.Fatal(err)
			}

			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			stub := &stubMetaProvider{works: []models.Book{{
				ForeignID: "OL-DUNE-W", Title: "Dune", SortTitle: "dune",
				Language: "eng", Status: models.BookStatusWanted, Genres: []string{},
				MetadataProvider: "openlibrary", CreditedAuthorForeignIDs: tc.credited,
			}}}
			h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil,
				db.NewMetadataProfileRepo(database), &searcherSpy{})
			h.FetchAuthorBooks(bobs, false, "")

			got, err := bookRepo.GetByID(ctx, dune.ID)
			if err != nil || got == nil {
				t.Fatalf("re-read alice's Dune: %+v err=%v", got, err)
			}
			if got.AuthorID != alices.ID || got.OwnerUserID != alice.ID {
				t.Fatalf("alice's Dune = author %d owner %d, want her copy %d (bob's is %d) and owner %d",
					got.AuthorID, got.OwnerUserID, alices.ID, bobs.ID, alice.ID)
			}

			// Deleting bob's copy must not take her book with it.
			if err := authorRepo.Delete(ctx, bobs.ID); err != nil {
				t.Fatalf("delete bob's copy: %v", err)
			}
			if still, _ := bookRepo.GetByID(ctx, dune.ID); still == nil {
				t.Fatal("deleting bob's copy deleted alice's Dune")
			}
		})
	}
}
