package recommender

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// --- hardFilter ---

func TestHardFilter_RemovesOwned(t *testing.T) {
	p := &UserProfile{
		OwnedForeignIDs:     map[string]bool{"OWN": true},
		DismissedForeignIDs: map[string]bool{},
		ExcludedAuthors:     map[string]bool{},
	}
	candidates := []models.RecommendationCandidate{
		{ForeignID: "OWN", Title: "Already owned", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "KEEP", Title: "Keep this", RatingsCount: 100, Rating: 4.0},
	}
	got := hardFilter(candidates, p)
	if len(got) != 1 || got[0].ForeignID != "KEEP" {
		t.Errorf("expected only KEEP, got %+v", got)
	}
}

func TestHardFilter_RemovesDismissed(t *testing.T) {
	p := &UserProfile{
		OwnedForeignIDs:     map[string]bool{},
		DismissedForeignIDs: map[string]bool{"DIS": true},
		ExcludedAuthors:     map[string]bool{},
	}
	candidates := []models.RecommendationCandidate{
		{ForeignID: "DIS", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "OK", RatingsCount: 100, Rating: 4.0},
	}
	got := hardFilter(candidates, p)
	if len(got) != 1 || got[0].ForeignID != "OK" {
		t.Errorf("expected only OK, got %+v", got)
	}
}

func TestHardFilter_RemovesExcludedAuthor(t *testing.T) {
	p := &UserProfile{
		OwnedForeignIDs:     map[string]bool{},
		DismissedForeignIDs: map[string]bool{},
		ExcludedAuthors:     map[string]bool{"bad author": true},
	}
	candidates := []models.RecommendationCandidate{
		{ForeignID: "A", AuthorName: "Bad Author", RatingsCount: 100, Rating: 4.0}, // case-insensitive match
		{ForeignID: "B", AuthorName: "Good Author", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "C", AuthorName: "", RatingsCount: 100, Rating: 4.0}, // no author → allowed
	}
	got := hardFilter(candidates, p)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.ForeignID == "A" {
			t.Error("excluded-author candidate should have been filtered")
		}
	}
}

func TestHardFilter_PopularityFilter(t *testing.T) {
	p := &UserProfile{
		OwnedForeignIDs:     map[string]bool{},
		DismissedForeignIDs: map[string]bool{},
		ExcludedAuthors:     map[string]bool{},
	}
	candidates := []models.RecommendationCandidate{
		{ForeignID: "A", Title: "No ratings at all", RatingsCount: 0, Rating: 0},
		{ForeignID: "B", Title: "Too few ratings", RatingsCount: 30, Rating: 4.5},
		{ForeignID: "C", Title: "Enough ratings but low score", RatingsCount: 50, Rating: 2.5},
		{ForeignID: "D", Title: "Enough ratings exactly 3.0", RatingsCount: 50, Rating: 3.0},
		{ForeignID: "E", Title: "Popular and well rated", RatingsCount: 200, Rating: 4.5},
		{ForeignID: "F", Title: "Popular but unrated in DB", RatingsCount: 200, Rating: 0.0},
	}
	got := hardFilter(candidates, p)

	wantPass := map[string]bool{"D": true, "E": true, "F": true}
	wantFilter := map[string]bool{"A": true, "B": true, "C": true}

	if len(got) != len(wantPass) {
		t.Fatalf("expected %d candidates, got %d: %+v", len(wantPass), len(got), got)
	}
	for _, c := range got {
		if wantFilter[c.ForeignID] {
			t.Errorf("candidate %q should have been filtered but was not", c.ForeignID)
		}
		if !wantPass[c.ForeignID] {
			t.Errorf("unexpected candidate %q in result", c.ForeignID)
		}
	}
}

func TestHardFilter_Dedupes(t *testing.T) {
	p := &UserProfile{
		OwnedForeignIDs:     map[string]bool{},
		DismissedForeignIDs: map[string]bool{},
		ExcludedAuthors:     map[string]bool{},
	}
	candidates := []models.RecommendationCandidate{
		{ForeignID: "X", Title: "first", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "X", Title: "dup", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "Y", RatingsCount: 100, Rating: 4.0},
	}
	got := hardFilter(candidates, p)
	if len(got) != 2 {
		t.Fatalf("expected 2 after dedup, got %d", len(got))
	}
	if got[0].ForeignID != "X" || got[0].Title != "first" {
		t.Errorf("first-wins dedup: got %+v", got[0])
	}
}

// --- shared DB-integrated fixtures ---

func seedSeries(t *testing.T, f profileFixtures, foreignID, title string) *models.Series {
	t.Helper()
	s := &models.Series{
		ForeignID: foreignID,
		Title:     title,
	}
	if err := f.series.Create(context.Background(), s); err != nil {
		t.Fatalf("create series: %v", err)
	}
	return s
}

// --- GenerateSeries ---

func TestGenerateSeries_NoStartedSeries(t *testing.T) {
	f := newProfileFixtures(t)
	p := &UserProfile{
		SeriesState:     map[int64]SeriesState{},
		OwnedForeignIDs: map[string]bool{},
	}
	got := GenerateSeries(context.Background(), f.books, f.series, p)
	if len(got) != 0 {
		t.Errorf("expected no candidates with empty series state, got %d", len(got))
	}
}

func TestGenerateSeries_NextInSequence(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Author", "OLA1", false)
	b1 := seedBook(t, f, a.ID, "OLB1", "Book 1", nil)
	b2 := seedBook(t, f, a.ID, "OLB2", "Book 2", nil)
	b3 := seedBook(t, f, a.ID, "OLB3", "Book 3", nil)

	s := seedSeries(t, f, "OLS1", "The Series")
	if err := f.series.LinkBook(ctx, s.ID, b1.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := f.series.LinkBook(ctx, s.ID, b2.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	if err := f.series.LinkBook(ctx, s.ID, b3.ID, "3", true); err != nil {
		t.Fatal(err)
	}

	// User owns book 1 only; expect book 2 as next-in-sequence.
	p := &UserProfile{
		SeriesState: map[int64]SeriesState{
			s.ID: {SeriesID: s.ID, SeriesTitle: s.Title, MaxPosition: 1},
		},
		OwnedForeignIDs: map[string]bool{b1.ForeignID: true},
	}
	got := GenerateSeries(ctx, f.books, f.series, p)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != b2.ForeignID {
		t.Errorf("expected next book %q, got %q", b2.ForeignID, got[0].ForeignID)
	}
	if got[0].RecType != models.RecTypeSeries {
		t.Errorf("RecType: got %q", got[0].RecType)
	}
	if got[0].SeriesID == nil || *got[0].SeriesID != s.ID {
		t.Error("SeriesID not populated")
	}
}

func TestGenerateSeries_FillGaps(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Author", "OLA1", false)
	b1 := seedBook(t, f, a.ID, "OLB1", "Book 1", nil)
	b2 := seedBook(t, f, a.ID, "OLB2", "Book 2", nil)
	b3 := seedBook(t, f, a.ID, "OLB3", "Book 3", nil)

	s := seedSeries(t, f, "OLS1", "Gappy Series")
	_ = f.series.LinkBook(ctx, s.ID, b1.ID, "1", true)
	_ = f.series.LinkBook(ctx, s.ID, b2.ID, "2", true)
	_ = f.series.LinkBook(ctx, s.ID, b3.ID, "3", true)

	// User owns books 1 and 3; book 2 is a gap and book 4 would be next (doesn't exist).
	p := &UserProfile{
		SeriesState: map[int64]SeriesState{
			s.ID: {
				SeriesID:         s.ID,
				SeriesTitle:      s.Title,
				MaxPosition:      3,
				MissingPositions: []float64{2},
			},
		},
		OwnedForeignIDs: map[string]bool{b1.ForeignID: true, b3.ForeignID: true},
	}
	got := GenerateSeries(ctx, f.books, f.series, p)
	// Next-in-sequence after 3 does not exist; gap-fill returns book 2.
	found := false
	for _, c := range got {
		if c.ForeignID == b2.ForeignID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gap-fill candidate for book 2, got %+v", got)
	}
}

// --- GenerateAuthorNew ---

func TestGenerateAuthorNew_MonitoredAuthor(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Monitored", "OLA_M", true)
	b1 := seedBook(t, f, a.ID, "OLB1", "Wanted Book", nil) // status=wanted via helper
	// Owned book should be filtered.
	seedBook(t, f, a.ID, "OLB2", "Owned Book", nil)

	p := &UserProfile{
		MonitoredAuthors: map[int64]bool{a.ID: true},
		OwnedForeignIDs:  map[string]bool{"OLB2": true},
	}
	got := GenerateAuthorNew(ctx, f.books, f.authors, p)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != b1.ForeignID {
		t.Errorf("expected %q, got %q", b1.ForeignID, got[0].ForeignID)
	}
	if got[0].RecType != models.RecTypeAuthorNew {
		t.Errorf("RecType: got %q", got[0].RecType)
	}
	if got[0].AuthorName != "Monitored" {
		t.Errorf("AuthorName: got %q", got[0].AuthorName)
	}
}

func TestGenerateAuthorNew_NoMonitoredAuthors(t *testing.T) {
	f := newProfileFixtures(t)
	p := &UserProfile{MonitoredAuthors: map[int64]bool{}}
	got := GenerateAuthorNew(context.Background(), f.books, f.authors, p)
	if len(got) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(got))
	}
}

func TestGenerateAuthorNew_SkipsNonWantedStatus(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Auth", "OLA", true)
	b := &models.Book{
		ForeignID:        "OLDOWN",
		AuthorID:         a.ID,
		Title:            "Downloaded",
		SortTitle:        "Downloaded",
		Status:           models.BookStatusDownloaded,
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := f.books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	p := &UserProfile{
		MonitoredAuthors: map[int64]bool{a.ID: true},
		OwnedForeignIDs:  map[string]bool{},
	}
	got := GenerateAuthorNew(ctx, f.books, f.authors, p)
	if len(got) != 0 {
		t.Errorf("downloaded books should not produce candidates, got %+v", got)
	}
}

// --- GenerateGenreSimilar ---

func TestGenerateGenreSimilar_SkipsStartedSeries(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Auth", "OLA", false)
	b1 := seedBook(t, f, a.ID, "OLB1", "Book 1", []string{"fantasy"})
	b2 := seedBook(t, f, a.ID, "OLB2", "Book 2", []string{"fantasy"})

	s := seedSeries(t, f, "OLS1", "Started Series")
	_ = f.series.LinkBook(ctx, s.ID, b1.ID, "1", true)
	_ = f.series.LinkBook(ctx, s.ID, b2.ID, "2", true)

	// User already started this series.
	p := &UserProfile{
		GenreWeights: map[string]float64{"fantasy": 1.0},
		SeriesState: map[int64]SeriesState{
			s.ID: {SeriesID: s.ID, MaxPosition: 1},
		},
		OwnedForeignIDs: map[string]bool{b1.ForeignID: true},
	}
	got := GenerateGenreSimilar(ctx, f.books, f.series, p)
	if len(got) != 0 {
		t.Errorf("should skip books in started series, got %+v", got)
	}
}

func TestGenreSimilar_UnstartedSeries(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Auth", "OLA", false)
	b1 := seedBook(t, f, a.ID, "OLB1", "Book 1", []string{"fantasy"})
	b2 := seedBook(t, f, a.ID, "OLB2", "Book 2", []string{"fantasy"})

	s := seedSeries(t, f, "OLS1", "Unstarted")
	_ = f.series.LinkBook(ctx, s.ID, b1.ID, "1", true)
	_ = f.series.LinkBook(ctx, s.ID, b2.ID, "2", true)

	p := &UserProfile{
		GenreWeights:    map[string]float64{"fantasy": 1.0},
		SeriesState:     map[int64]SeriesState{},
		OwnedForeignIDs: map[string]bool{},
	}
	got := GenerateGenreSimilar(ctx, f.books, f.series, p)
	if len(got) == 0 {
		t.Error("expected candidates from unstarted series")
	}
	for _, c := range got {
		if c.RecType != models.RecTypeGenreSimilar {
			t.Errorf("RecType: got %q", c.RecType)
		}
		if c.Reason == "" {
			t.Error("expected non-empty Reason")
		}
	}
}

// --- GenerateSerendipity ---

func TestGenerateSerendipity_FiltersOwned(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Auth", "OLA", false)
	owned := seedBook(t, f, a.ID, "OLOWN", "Owned", nil)
	unowned := seedBook(t, f, a.ID, "OLNEW", "Unowned", nil)

	s := seedSeries(t, f, "OLS1", "Series")
	_ = f.series.LinkBook(ctx, s.ID, owned.ID, "1", true)
	_ = f.series.LinkBook(ctx, s.ID, unowned.ID, "2", true)

	p := &UserProfile{
		GenreWeights:    map[string]float64{"fantasy": 1.0},
		OwnedForeignIDs: map[string]bool{owned.ForeignID: true},
	}
	got := GenerateSerendipity(ctx, f.books, f.series, p, 10)
	for _, c := range got {
		if c.ForeignID == owned.ForeignID {
			t.Errorf("owned book should not appear: %+v", c)
		}
		if c.RecType != models.RecTypeSerendipity {
			t.Errorf("RecType: got %q", c.RecType)
		}
	}
}

func TestGenerateSerendipity_RespectsCount(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	a := seedAuthor(t, f, "Auth", "OLA", false)
	for i, fid := range []string{"A", "B", "C", "D", "E"} {
		b := seedBook(t, f, a.ID, "OL"+fid, "T"+fid, []string{"horror"})
		s := seedSeries(t, f, "S"+fid, "Series")
		_ = f.series.LinkBook(ctx, s.ID, b.ID, "1", true)
		_ = i
	}

	p := &UserProfile{
		GenreWeights:    map[string]float64{"fantasy": 1.0},
		OwnedForeignIDs: map[string]bool{},
	}
	got := GenerateSerendipity(ctx, f.books, f.series, p, 2)
	if len(got) > 2 {
		t.Errorf("expected at most 2, got %d", len(got))
	}
}

// --- Engine.Run ---

func TestEngine_Run_Disabled(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	// Without the "recommendations.enabled" setting, Run is a no-op.
	e := New(f.books, f.authors, f.series, f.recs, f.settings)
	if err := e.Run(ctx, f.userID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	recs, err := f.recs.List(ctx, f.userID, "", 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("disabled run should produce no recs, got %d", len(recs))
	}
}

func TestEngine_Run_EnabledEmptyLibrary(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	if err := f.settings.Set(ctx, "recommendations.enabled", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	e := New(f.books, f.authors, f.series, f.recs, f.settings)
	if err := e.Run(ctx, f.userID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	recs, err := f.recs.List(ctx, f.userID, "", 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("empty library should yield 0 recs, got %d", len(recs))
	}
}

func TestEngine_Run_WithMonitoredAuthorProducesCandidate(t *testing.T) {
	f := newProfileFixtures(t)
	ctx := context.Background()

	if err := f.settings.Set(ctx, "recommendations.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	a := seedAuthor(t, f, "Mon", "OLA_M", true)
	// Wanted book from monitored author, not owned.
	seedBook(t, f, a.ID, "OLW", "Wanted", []string{"fantasy"})

	e := New(f.books, f.authors, f.series, f.recs, f.settings)
	if err := e.Run(ctx, f.userID); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recs, err := f.recs.List(ctx, f.userID, "", 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.ForeignID == "OLW" {
			found = true
		}
	}
	if !found {
		t.Errorf("wanted book from monitored author should appear as a recommendation")
	}
}

func TestEngine_WithClients(t *testing.T) {
	f := newProfileFixtures(t)
	e := New(f.books, f.authors, f.series, f.recs, f.settings)

	ol := &fakeSubjectFetcher{}
	hc := &fakeWishlistFetcher{}

	if ret := e.WithOLClient(ol); ret != e {
		t.Error("WithOLClient should return the engine")
	}
	if ret := e.WithHCClient(hc); ret != e {
		t.Error("WithHCClient should return the engine")
	}
	if e.olClient != ol {
		t.Error("olClient not wired")
	}
	if e.hcClient != hc {
		t.Error("hcClient not wired")
	}
}

// Ensure the package compiles with the db import path above.
var _ = db.OpenMemory
