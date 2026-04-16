package recommender

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestGenreToSubjectSlug(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Science Fiction", "science_fiction"},
		{"FANTASY", "fantasy"},
		{"  horror  ", "horror"},
		{"Young Adult", "young_adult"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := genreToSubjectSlug(tt.in); got != tt.want {
			t.Errorf("genreToSubjectSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParsePosition(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"1", 1},
		{"2.5", 2.5},
		{"  3  ", 3},
		{"not-a-number", 0},
	}
	for _, tt := range tests {
		if got := parsePosition(tt.in); got != tt.want {
			t.Errorf("parsePosition(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestBookToCandidate(t *testing.T) {
	aid := int64(5)
	b := &models.Book{
		ForeignID:     "OL1W",
		Title:         "Test",
		AuthorID:      aid,
		ImageURL:      "https://img",
		Description:   "desc",
		Genres:        []string{"fantasy"},
		AverageRating: 4.2,
		RatingsCount:  100,
		Language:      "eng",
		MediaType:     models.MediaTypeAudiobook,
	}
	c := bookToCandidate(b)
	if c.ForeignID != "OL1W" || c.Title != "Test" {
		t.Errorf("basic fields not copied: %+v", c)
	}
	if c.AuthorID == nil || *c.AuthorID != 5 {
		t.Errorf("AuthorID not copied: %v", c.AuthorID)
	}
	if c.Rating != 4.2 || c.RatingsCount != 100 {
		t.Errorf("rating fields not copied: %v / %d", c.Rating, c.RatingsCount)
	}
	if c.MediaType != models.MediaTypeAudiobook {
		t.Errorf("MediaType: want %q, got %q", models.MediaTypeAudiobook, c.MediaType)
	}

	// Empty MediaType defaults to ebook.
	b2 := &models.Book{ForeignID: "x", Title: "y"}
	if got := bookToCandidate(b2); got.MediaType != models.MediaTypeEbook {
		t.Errorf("default MediaType: got %q, want ebook", got.MediaType)
	}
}

func TestTopGenre(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{
		"fantasy": 0.9, "scifi": 0.5, "romance": 0.3,
	}}
	if got := topGenre(p); got != "fantasy" {
		t.Errorf("want 'fantasy', got %q", got)
	}

	// Empty profile
	empty := &UserProfile{GenreWeights: map[string]float64{}}
	if got := topGenre(empty); got != "" {
		t.Errorf("empty profile: want '', got %q", got)
	}
}

func TestTopNGenres(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{
		"a": 1.0, "b": 0.9, "c": 0.8, "d": 0.1, "e": 0.05,
	}}
	top3 := topNGenres(p, 3)
	if len(top3) != 3 {
		t.Fatalf("expected 3 genres, got %d", len(top3))
	}
	for _, g := range []string{"a", "b", "c"} {
		if !top3[g] {
			t.Errorf("missing expected genre %q", g)
		}
	}

	// Request more than available → return all
	all := topNGenres(p, 100)
	if len(all) != 5 {
		t.Errorf("expected 5 genres, got %d", len(all))
	}
}

func TestHasTopGenre(t *testing.T) {
	top := map[string]bool{"fantasy": true, "scifi": true}
	if !hasTopGenre([]string{"Fantasy"}, top) {
		t.Error("case-insensitive match should succeed")
	}
	if !hasTopGenre([]string{"romance", "  SCIFI  "}, top) {
		t.Error("trimmed/cased match should succeed")
	}
	if hasTopGenre([]string{"romance", "horror"}, top) {
		t.Error("no overlap should return false")
	}
	if hasTopGenre(nil, top) {
		t.Error("nil slice should return false")
	}
}

// --- GenerateGenrePopular ---

type fakeSubjectFetcher struct {
	called    []string // subjects requested
	responses map[string][]models.RecommendationCandidate
	err       error
}

func (f *fakeSubjectFetcher) GetSubjectBooks(_ context.Context, subject string, _ int) ([]models.RecommendationCandidate, error) {
	f.called = append(f.called, subject)
	if f.err != nil {
		return nil, f.err
	}
	return f.responses[subject], nil
}

func TestGenerateGenrePopular_NilClient(t *testing.T) {
	got := GenerateGenrePopular(context.Background(), nil, &UserProfile{GenreWeights: map[string]float64{"x": 1}}, 5, 20)
	if got != nil {
		t.Errorf("expected nil for nil client, got %v", got)
	}
}

func TestGenerateGenrePopular_EmptyProfile(t *testing.T) {
	f := &fakeSubjectFetcher{}
	got := GenerateGenrePopular(context.Background(), f, &UserProfile{GenreWeights: map[string]float64{}}, 5, 20)
	if got != nil {
		t.Errorf("expected nil for empty profile, got %v", got)
	}
	if len(f.called) != 0 {
		t.Errorf("fetcher should not have been called, got %v", f.called)
	}
}

func TestGenerateGenrePopular_Success(t *testing.T) {
	f := &fakeSubjectFetcher{
		responses: map[string][]models.RecommendationCandidate{
			"fantasy": {
				{ForeignID: "OL1W", Title: "Book 1"},
				{ForeignID: "OL2W", Title: "Book 2"},
			},
		},
	}
	profile := &UserProfile{
		GenreWeights: map[string]float64{"fantasy": 1.0},
	}
	got := GenerateGenrePopular(context.Background(), f, profile, 1, 20)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	for _, c := range got {
		if c.RecType != models.RecTypeGenrePopular {
			t.Errorf("RecType: want %q, got %q", models.RecTypeGenrePopular, c.RecType)
		}
		if c.Reason == "" {
			t.Error("expected non-empty Reason")
		}
	}
	if len(f.called) != 1 || f.called[0] != "fantasy" {
		t.Errorf("fetcher should be called with 'fantasy', got %v", f.called)
	}
}

func TestGenerateGenrePopular_FetchError(t *testing.T) {
	f := &fakeSubjectFetcher{err: errors.New("boom")}
	profile := &UserProfile{GenreWeights: map[string]float64{"fantasy": 1.0}}
	// Errors must be swallowed; we get an empty (but not nil-panic) result.
	got := GenerateGenrePopular(context.Background(), f, profile, 1, 20)
	if len(got) != 0 {
		t.Errorf("expected no candidates on error, got %d", len(got))
	}
}

// --- GenerateListCross ---

type fakeWishlistFetcher struct {
	called int
	books  []models.RecommendationCandidate
	err    error
}

func (f *fakeWishlistFetcher) GetUserWishlist(_ context.Context, _ int) ([]models.RecommendationCandidate, error) {
	f.called++
	if f.err != nil {
		return nil, f.err
	}
	return f.books, nil
}

func TestGenerateListCross_NilClient(t *testing.T) {
	got := GenerateListCross(context.Background(), nil, &UserProfile{}, 50)
	if got != nil {
		t.Errorf("expected nil for nil client, got %v", got)
	}
}

func TestGenerateListCross_FilterOwned(t *testing.T) {
	f := &fakeWishlistFetcher{
		books: []models.RecommendationCandidate{
			{ForeignID: "OL1W", Title: "Want 1"},
			{ForeignID: "OL2W", Title: "Own Already"}, // filtered
			{ForeignID: "OL3W", Title: "Want 3"},
		},
	}
	profile := &UserProfile{
		OwnedForeignIDs: map[string]bool{"OL2W": true},
	}
	got := GenerateListCross(context.Background(), f, profile, 50)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates after filtering, got %d", len(got))
	}
	for _, c := range got {
		if c.RecType != models.RecTypeListCross {
			t.Errorf("RecType: want %q, got %q", models.RecTypeListCross, c.RecType)
		}
		if c.ForeignID == "OL2W" {
			t.Error("owned book should have been filtered out")
		}
	}
}

func TestGenerateListCross_FetchError(t *testing.T) {
	f := &fakeWishlistFetcher{err: errors.New("nope")}
	got := GenerateListCross(context.Background(), f, &UserProfile{}, 50)
	if got != nil {
		t.Errorf("expected nil on fetch error, got %v", got)
	}
}

func TestGenerateListCross_EmptyResult(t *testing.T) {
	f := &fakeWishlistFetcher{books: []models.RecommendationCandidate{}}
	got := GenerateListCross(context.Background(), f, &UserProfile{}, 50)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d", len(got))
	}
}
