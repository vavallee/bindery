package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Two accounts' copies can know the author under different primary ids: alice's
// has primary OL1 and secondary HC1, bob's the reverse. With tenancy off, a
// sync of bob's copy lists alice's Dune, whose author (OL1) is not credited
// there, so the "mis-parented" branch used to move it under bob's copy. The
// copies share ids, so the book stays put whatever the credited list says.
func TestFetchAuthorBooks_CopiesWithDifferentPrimariesNeverTradeBooks(t *testing.T) {
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
	mk := func(owner int64, primary, secondary string) *models.Author {
		a := &models.Author{ForeignID: primary, Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary", Monitored: true}
		if err := authorRepo.CreateForUser(ctx, a, owner); err != nil {
			t.Fatal(err)
		}
		if err := authorRepo.UpsertAuthorIdentifier(ctx, a.ID, secondary); err != nil {
			t.Fatal(err)
		}
		return a
	}
	alices, bobs := mk(alice.ID, "OL1", "HC1"), mk(bob.ID, "HC1", "OL1")
	dune := &models.Book{
		ForeignID: "OL-DUNE-W", AuthorID: alices.ID, Title: "Dune", SortTitle: "dune",
		Language: "eng", Status: models.BookStatusWanted, Monitored: true,
		Genres: []string{}, MetadataProvider: "openlibrary", OwnerUserID: alice.ID,
	}
	if err := bookRepo.Create(ctx, dune); err != nil {
		t.Fatal(err)
	}

	auth.SetEnforceTenancyForTests(t, false)
	stub := &stubMetaProvider{works: []models.Book{{
		ForeignID: "OL-DUNE-W", Title: "Dune", SortTitle: "dune",
		Language: "eng", Status: models.BookStatusWanted, Genres: []string{},
		MetadataProvider: "openlibrary", CreditedAuthorForeignIDs: []string{"HC1"},
	}}}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil,
		db.NewMetadataProfileRepo(database), &searcherSpy{})
	h.FetchAuthorBooks(bobs, false, "")

	got, err := bookRepo.GetByID(ctx, dune.ID)
	if err != nil || got == nil {
		t.Fatalf("re-read alice's Dune: %+v err=%v", got, err)
	}
	if got.AuthorID != alices.ID {
		t.Fatalf("alice's Dune is under author %d, want her copy %d (bob's is %d)", got.AuthorID, alices.ID, bobs.ID)
	}
}
