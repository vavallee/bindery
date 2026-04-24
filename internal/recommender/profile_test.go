package recommender

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// profileFixtures holds repos and a user ID backing a fresh in-memory DB.
type profileFixtures struct {
	books    *db.BookRepo
	authors  *db.AuthorRepo
	series   *db.SeriesRepo
	recs     *db.RecommendationRepo
	settings *db.SettingsRepo
	userID   int64
}

func newProfileFixtures(t *testing.T) profileFixtures {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	users := db.NewUserRepo(database)
	u, err := users.Create(context.Background(), "alice", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	return profileFixtures{
		books:    db.NewBookRepo(database),
		authors:  db.NewAuthorRepo(database),
		series:   db.NewSeriesRepo(database),
		recs:     db.NewRecommendationRepo(database),
		settings: db.NewSettingsRepo(database),
		userID:   u.ID,
	}
}

func seedAuthor(t *testing.T, f profileFixtures, name, foreignID string, monitored bool) *models.Author {
	t.Helper()
	a := &models.Author{
		ForeignID:        foreignID,
		Name:             name,
		SortName:         name,
		MetadataProvider: "openlibrary",
		Monitored:        monitored,
	}
	if err := f.authors.Create(context.Background(), a); err != nil {
		t.Fatalf("create author: %v", err)
	}
	return a
}

func seedBook(t *testing.T, f profileFixtures, authorID int64, foreignID, title string, genres []string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID:        foreignID,
		AuthorID:         authorID,
		Title:            title,
		SortTitle:        title,
		Genres:           genres,
		Status:           models.BookStatusWanted,
		MetadataProvider: "openlibrary",
		Monitored:        true,
		RatingsCount:     100,
		AverageRating:    4.0,
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatalf("create book: %v", err)
	}
	return b
}

func seedImportedBook(t *testing.T, f profileFixtures, authorID int64, foreignID, title string, genres []string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID:        foreignID,
		AuthorID:         authorID,
		Title:            title,
		SortTitle:        title,
		Genres:           genres,
		Status:           models.BookStatusImported,
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatalf("create book: %v", err)
	}
	return b
}

func TestBuildProfile_Empty(t *testing.T) {
	f := newProfileFixtures(t)
	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if p.TotalBooks != 0 {
		t.Errorf("TotalBooks: want 0, got %d", p.TotalBooks)
	}
	if len(p.GenreWeights) != 0 {
		t.Errorf("GenreWeights should be empty, got %v", p.GenreWeights)
	}
	if p.PreferredLanguage != "en" {
		t.Errorf("PreferredLanguage default: want 'en', got %q", p.PreferredLanguage)
	}
}

func TestBuildProfile_GenreWeights(t *testing.T) {
	f := newProfileFixtures(t)
	a := seedAuthor(t, f, "Author A", "OL1A", false)
	seedBook(t, f, a.ID, "OL1W", "B1", []string{"Fantasy", "Adventure"})
	seedBook(t, f, a.ID, "OL2W", "B2", []string{"Fantasy"})
	// Junk genre should be skipped.
	seedBook(t, f, a.ID, "OL3W", "B3", []string{"Accessible Book", "Romance"})

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}

	if p.TotalBooks != 3 {
		t.Errorf("TotalBooks: want 3, got %d", p.TotalBooks)
	}

	// Junk genre filtered out.
	if _, ok := p.GenreWeights["accessible book"]; ok {
		t.Error("junk genre 'accessible book' should not appear in weights")
	}
	// Fantasy appears in 2/3 books so it's weighted.
	if p.GenreWeights["fantasy"] == 0 {
		t.Error("expected non-zero weight for 'fantasy'")
	}
	// All genres are lowercased.
	for g := range p.GenreWeights {
		if g == "" {
			t.Error("blank genre key")
		}
	}
}

func TestBuildProfile_OwnedAndAuthorCounts(t *testing.T) {
	f := newProfileFixtures(t)
	a := seedAuthor(t, f, "Prolific", "OL_PROLIFIC", true)
	b := seedAuthor(t, f, "One Hit", "OL_ONEHIT", false)
	// Imported books should be "owned"; wanted books should not be.
	seedImportedBook(t, f, a.ID, "OL1I", "B1 imported", []string{"fantasy"})
	seedImportedBook(t, f, a.ID, "OL2I", "B2 imported", []string{"fantasy"})
	seedBook(t, f, a.ID, "OL3W", "B3 wanted", []string{"fantasy"})
	seedBook(t, f, b.ID, "OL4W", "B4 wanted", []string{"romance"})

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !p.OwnedForeignIDs["OL1I"] || !p.OwnedForeignIDs["OL2I"] {
		t.Errorf("imported books missing from OwnedForeignIDs: %v", p.OwnedForeignIDs)
	}
	if p.OwnedForeignIDs["OL3W"] || p.OwnedForeignIDs["OL4W"] {
		t.Errorf("wanted books should not be in OwnedForeignIDs: %v", p.OwnedForeignIDs)
	}
	if p.AuthorBookCounts[a.ID] != 3 {
		t.Errorf("Prolific book count: want 3, got %d", p.AuthorBookCounts[a.ID])
	}
	if p.AuthorBookCounts[b.ID] != 1 {
		t.Errorf("OneHit book count: want 1, got %d", p.AuthorBookCounts[b.ID])
	}
	if !p.MonitoredAuthors[a.ID] {
		t.Errorf("expected Prolific (ID=%d) to be monitored", a.ID)
	}
	if p.MonitoredAuthors[b.ID] {
		t.Errorf("OneHit (ID=%d) should not be monitored", b.ID)
	}
}

func TestBuildProfile_PreferredLanguageFromSettings(t *testing.T) {
	f := newProfileFixtures(t)
	if err := f.settings.Set(context.Background(), "search.preferredLanguage", "de"); err != nil {
		t.Fatalf("set setting: %v", err)
	}

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if p.PreferredLanguage != "de" {
		t.Errorf("PreferredLanguage: want 'de', got %q", p.PreferredLanguage)
	}
}

func TestBuildProfile_DismissedAndExclusions(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	// Create a rec row, dismiss it.
	cand := models.RecommendationCandidate{
		ForeignID: "OLDISMISSED",
		RecType:   models.RecTypeAuthorNew,
		Title:     "Dismissed Book",
	}
	if err := f.recs.ReplaceBatch(ctx, f.userID, []models.RecommendationCandidate{cand}); err != nil {
		t.Fatalf("replace batch: %v", err)
	}
	recs, err := f.recs.List(ctx, f.userID, "", 10, 0)
	if err != nil {
		t.Fatalf("list recs: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 rec, got %d", len(recs))
	}
	if err := f.recs.Dismiss(ctx, f.userID, recs[0].ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// Exclude an author.
	if err := f.recs.AddAuthorExclusion(ctx, f.userID, "Bad Author"); err != nil {
		t.Fatalf("add exclusion: %v", err)
	}

	p, err := BuildProfile(ctx, f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !p.DismissedForeignIDs["OLDISMISSED"] {
		t.Errorf("dismissed foreign ID should appear: %v", p.DismissedForeignIDs)
	}
	// Excluded authors are stored lowercased.
	if !p.ExcludedAuthors["bad author"] {
		t.Errorf("excluded authors should include 'bad author', got %v", p.ExcludedAuthors)
	}
}

func TestParsePosition_ProfilePackage(t *testing.T) {
	// Covers parsePosition through a separate check in the profile package.
	if parsePosition("") != 0 {
		t.Error("empty should be 0")
	}
	if parsePosition("1.5") != 1.5 {
		t.Error("1.5 should parse")
	}
}
