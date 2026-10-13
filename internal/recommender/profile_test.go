package recommender

import (
	"context"
	"fmt"
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
	// Imported AND wanted books both count as owned for suppression (#1726):
	// re-recommending a book the user already decided they want is useless.
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
	if !p.OwnedForeignIDs["OL3W"] || !p.OwnedForeignIDs["OL4W"] {
		t.Errorf("wanted books should be in OwnedForeignIDs (#1726): %v", p.OwnedForeignIDs)
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

// TestIsOwned_CrossProvider is the #1726 regression: a work owned under one
// provider's ForeignID must suppress the same work surfaced by another
// provider, matched by title dedup key + author — and the author dimension
// must keep same-titled books by different authors recommendable.
func TestIsOwned_CrossProvider(t *testing.T) {
	f := newProfileFixtures(t)
	a := seedAuthor(t, f, "Orson Scott Card", "OL_CARD", false)
	seedImportedBook(t, f, a.ID, "OL49485W", "Ender in Exile", []string{"science fiction"})

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}

	// Same work from Hardcover: different ForeignID, same title and author.
	if !p.IsOwned("hc:ender-in-exile", "Ender in Exile", "Orson Scott Card") {
		t.Error("same title+author from another provider should be owned")
	}
	// Provider spells the author last-first: still the same person.
	if !p.IsOwned("hc:ender-in-exile", "Ender in Exile", "Card, Orson Scott") {
		t.Error("last-first author spelling should still match")
	}
	// Same title, different author: a distinct work, must NOT be suppressed.
	if p.IsOwned("hc:other", "Ender in Exile", "Jane Doe") {
		t.Error("same title by a different author must not be owned")
	}
	// Exact ForeignID still matches regardless of title/author strings.
	if !p.IsOwned("OL49485W", "", "") {
		t.Error("exact ForeignID match should be owned")
	}
	// Missing author name must not fall back to title-only matching.
	if p.IsOwned("hc:ender-in-exile", "Ender in Exile", "") {
		t.Error("empty author name must not match on title alone")
	}
}

// TestHardFilter_CrossProviderOwned drives the same scenario through the real
// filter path candidates take.
func TestHardFilter_CrossProviderOwned(t *testing.T) {
	f := newProfileFixtures(t)
	a := seedAuthor(t, f, "Orson Scott Card", "OL_CARD", false)
	seedImportedBook(t, f, a.ID, "OL49485W", "Ender in Exile", []string{"science fiction"})

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}

	owned := models.RecommendationCandidate{
		ForeignID:    "hc:ender-in-exile",
		RecType:      models.RecTypeListCross,
		Title:        "Ender in Exile",
		AuthorName:   "Orson Scott Card",
		Rating:       4.2,
		RatingsCount: 500,
	}
	otherAuthor := models.RecommendationCandidate{
		ForeignID:    "hc:other-exile",
		RecType:      models.RecTypeListCross,
		Title:        "Ender in Exile",
		AuthorName:   "Jane Doe",
		Rating:       4.2,
		RatingsCount: 500,
	}

	got := hardFilter([]models.RecommendationCandidate{owned, otherAuthor}, p)
	if len(got) != 1 {
		t.Fatalf("hardFilter: want 1 survivor, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != "hc:other-exile" {
		t.Errorf("wrong survivor: %q", got[0].ForeignID)
	}
}

// TestHardFilter_WantedIsOwned pins the second #1726 fix: a wanted-status
// library book must suppress the matching candidate.
func TestHardFilter_WantedIsOwned(t *testing.T) {
	f := newProfileFixtures(t)
	a := seedAuthor(t, f, "Author A", "OL_A", false)
	seedBook(t, f, a.ID, "OL_WANTED", "Red God", []string{"fantasy"}) // status=wanted via helper

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}

	cand := models.RecommendationCandidate{
		ForeignID:    "OL_WANTED",
		RecType:      models.RecTypeListCross,
		Title:        "Red God",
		AuthorName:   "Author A",
		Rating:       4.0,
		RatingsCount: 500,
	}
	if got := hardFilter([]models.RecommendationCandidate{cand}, p); len(got) != 0 {
		t.Errorf("wanted book should be suppressed, got %+v", got)
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

// A library's main genre counts for it (#3172). With the distinct genres as
// the IDF numerator, a genre on more books than there are genres weighed
// negatively: here crime, on 30 of 40 books across 5 genres, came out about
// -1.34 and was missing from the top genres the genre searches use.
func TestBuildProfile_MainGenreWeighsPositively(t *testing.T) {
	f := newProfileFixtures(t)
	a := seedAuthor(t, f, "Kari Nordmann", "OL1A", false)
	n := 0
	for genre, count := range map[string]int{"Crime": 30, "Thriller": 5, "Romance": 3, "History": 1, "Poetry": 1} {
		for range count {
			n++
			seedBook(t, f, a.ID, fmt.Sprintf("OL%dW", n), fmt.Sprintf("Bok %d", n), []string{genre})
		}
	}

	p, err := BuildProfile(context.Background(), f.userID, f.books, f.authors, f.series, f.recs, f.settings)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	for genre, w := range p.GenreWeights {
		if w < 0 {
			t.Errorf("weight of %q = %.3f, want no negative weights", genre, w)
		}
	}
	if w := p.GenreWeights["crime"]; w <= 0 {
		t.Errorf("weight of the main genre = %.3f, want positive", w)
	}
	if !topNGenres(p, 3)["crime"] {
		t.Errorf("top 3 genres = %v, want the main genre among them (weights %v)", topNGenres(p, 3), p.GenreWeights)
	}
	// TF-IDF over the books: tf * ln(books / books with the genre) peaks for
	// a genre on about a third of the books, so thriller (5 of 40) edges out
	// crime (30 of 40), and both sit above romance (3 of 40). Checking the
	// order, not only the signs, catches a formula that merely avoids
	// negatives, such as abs(ln(genres / books with the genre)), which puts
	// crime first and thriller at 0 (#3173 review).
	w := p.GenreWeights
	if !(w["thriller"] > w["crime"] && w["crime"] > w["romance"] && w["romance"] > w["history"]) {
		t.Errorf("weights %v, want thriller > crime > romance > history", w)
	}
}
