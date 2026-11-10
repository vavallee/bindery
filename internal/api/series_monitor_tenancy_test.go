package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// tenancyMonitorFixture is the monitor fixture with the series and its owned
// volume belonging to one account and the catalogue author's row belonging to
// that account or to another.
func tenancyMonitorFixture(t *testing.T, authorIsOwners bool) (*SeriesHandler, *models.Series, *db.BookRepo, int64, *sql.DB) {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, true)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(60 * 24 * time.Hour)
	catalog := monitorCatalog(future)
	repo, books, authors := db.NewSeriesRepo(database), db.NewBookRepo(database), db.NewAuthorRepo(database)
	h := NewSeriesHandler(repo, books, authors, metadata.NewAggregator(&stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}).WithAudnexClient(nil), nil)

	authorOwner := bob.ID
	if authorIsOwners {
		authorOwner = alice.ID
	}
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	if err := authors.CreateForUser(ctx, author, authorOwner); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "hc-series:42", Title: "The Stormlight Archive"}
	if err := repo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID, HardcoverProviderID: "42", Confidence: 1, LinkedBy: "manual"}); err != nil {
		t.Fatal(err)
	}
	owned := &models.Book{ForeignID: "hc:the-way-of-kings", AuthorID: author.ID, Title: "The Way of Kings", SortTitle: "The Way of Kings",
		Status: models.BookStatusImported, Monitored: true, MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "hardcover", OwnerUserID: alice.ID}
	if err := books.Create(ctx, owned); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkBook(ctx, series.ID, owned.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	// Monitoring set by an admin, who has no account scope: the owner is
	// inferred from the books.
	if err := repo.SetMonitoring(ctx, series.ID, true, models.SeriesMonitorUpcoming, 0); err != nil {
		t.Fatal(err)
	}
	return h, series, books, alice.ID, database
}

// TestSeriesCheckAddsForTheSeriesOwner: under tenancy the new book belongs to
// the account whose series it is.
func TestSeriesCheckAddsForTheSeriesOwner(t *testing.T) {
	h, series, books, alice, _ := tenancyMonitorFixture(t, true)
	res, err := h.CheckSeries(context.Background(), series)
	if err != nil || res.Created != 1 {
		t.Fatalf("res = %+v, err = %v, want one book added", res, err)
	}
	got, _ := books.GetByForeignID(context.Background(), "hc:oathbringer")
	if got == nil || got.OwnerUserID != alice {
		t.Fatalf("new book = %+v, want it owned by account %d", got, alice)
	}
}

// TestSeriesCheckNeverAttachesToAnotherAccountsAuthor: the catalogue author is
// another account's row, so adding there would put the book in the wrong
// library. It used to add nothing; since authors are keyed per owner (#3555)
// the check creates the series owner's own row for the author and files the
// book there, leaving the other account's author alone.
func TestSeriesCheckNeverAttachesToAnotherAccountsAuthor(t *testing.T) {
	h, series, books, alice, database := tenancyMonitorFixture(t, false)
	ctx := context.Background()
	res, err := h.CheckSeries(ctx, series)
	if err != nil || res.Created != 1 {
		t.Fatalf("res = %+v, err = %v, want one book added under the owner's own author", res, err)
	}
	authors := db.NewAuthorRepo(database)
	theirs, _ := authors.GetByForeignID(ctx, "hc:brandon-sanderson")
	mine, _ := authors.GetByForeignIDForUser(ctx, "hc:brandon-sanderson", alice)
	if theirs == nil || mine == nil || mine.ID == theirs.ID || mine.OwnerUserID != alice {
		t.Fatalf("authors: other account's %+v, owner's %+v; want a separate row for account %d", theirs, mine, alice)
	}
	got, _ := books.GetByForeignID(ctx, "hc:oathbringer")
	if got == nil || got.AuthorID != mine.ID || got.OwnerUserID != alice {
		t.Fatalf("new book = %+v, want it account %d's, under author %d", got, alice, mine.ID)
	}
	if theirBooks, _ := books.ListByAuthor(ctx, theirs.ID); len(theirBooks) != 1 {
		t.Errorf("the other account's author gained books: %+v", theirBooks)
	}
}
