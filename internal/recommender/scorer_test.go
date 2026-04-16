package recommender

import (
	"math"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestClamp(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{-0.5, 0},
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{2.0, 1},
	}
	for _, tt := range tests {
		if got := clamp(tt.in); got != tt.want {
			t.Errorf("clamp(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestGenreScore_EmptyProfile(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{}}
	c := models.RecommendationCandidate{Genres: []string{"fantasy"}}
	if got := genreScore(c, p); got != 0 {
		t.Errorf("empty profile should return 0, got %v", got)
	}
}

func TestGenreScore_EmptyCandidate(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{"fantasy": 0.5}}
	c := models.RecommendationCandidate{Genres: nil}
	if got := genreScore(c, p); got != 0 {
		t.Errorf("empty candidate genres should return 0, got %v", got)
	}
}

func TestGenreScore_PerfectMatch(t *testing.T) {
	p := &UserProfile{
		GenreWeights: map[string]float64{"fantasy": 1.0},
	}
	c := models.RecommendationCandidate{Genres: []string{"fantasy"}}
	got := genreScore(c, p)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("perfect overlap should give 1.0, got %v", got)
	}
}

func TestGenreScore_CaseInsensitive(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{"fantasy": 1.0}}
	c := models.RecommendationCandidate{Genres: []string{"  FANTASY  "}}
	got := genreScore(c, p)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("case-insensitive match should give 1.0, got %v", got)
	}
}

func TestGenreScore_NoOverlap(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{"fantasy": 1.0, "scifi": 1.0}}
	c := models.RecommendationCandidate{Genres: []string{"romance"}}
	if got := genreScore(c, p); got != 0 {
		t.Errorf("no overlap should give 0, got %v", got)
	}
}

func TestGenreScore_EmptyStringGenre(t *testing.T) {
	p := &UserProfile{GenreWeights: map[string]float64{"fantasy": 1.0}}
	c := models.RecommendationCandidate{Genres: []string{"", "   "}}
	if got := genreScore(c, p); got != 0 {
		t.Errorf("all-blank genres should return 0, got %v", got)
	}
}

func TestAuthorScore(t *testing.T) {
	aid := int64(42)
	tests := []struct {
		name string
		c    models.RecommendationCandidate
		p    *UserProfile
		want float64
	}{
		{
			name: "nil author",
			c:    models.RecommendationCandidate{},
			p:    &UserProfile{},
			want: 0,
		},
		{
			name: "monitored",
			c:    models.RecommendationCandidate{AuthorID: &aid},
			p:    &UserProfile{MonitoredAuthors: map[int64]bool{42: true}},
			want: 1.0,
		},
		{
			name: "3+ books",
			c:    models.RecommendationCandidate{AuthorID: &aid},
			p:    &UserProfile{AuthorBookCounts: map[int64]int{42: 5}},
			want: 0.7,
		},
		{
			name: "1 book",
			c:    models.RecommendationCandidate{AuthorID: &aid},
			p:    &UserProfile{AuthorBookCounts: map[int64]int{42: 1}},
			want: 0.4,
		},
		{
			name: "unknown",
			c:    models.RecommendationCandidate{AuthorID: &aid},
			p:    &UserProfile{AuthorBookCounts: map[int64]int{}},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorScore(tt.c, tt.p); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSeriesScore(t *testing.T) {
	sid := int64(7)
	profile := &UserProfile{
		SeriesState: map[int64]SeriesState{
			7: {SeriesID: 7, MaxPosition: 3, MissingPositions: []float64{2}},
		},
	}

	tests := []struct {
		name string
		c    models.RecommendationCandidate
		want float64
	}{
		{
			name: "nil series id",
			c:    models.RecommendationCandidate{},
			want: 0,
		},
		{
			name: "series not in state",
			c:    models.RecommendationCandidate{SeriesID: func() *int64 { x := int64(99); return &x }(), SeriesPos: "1"},
			want: 0,
		},
		{
			name: "invalid position",
			c:    models.RecommendationCandidate{SeriesID: &sid, SeriesPos: "abc"},
			want: 0,
		},
		{
			name: "next in sequence",
			c:    models.RecommendationCandidate{SeriesID: &sid, SeriesPos: "4"},
			want: 1.0,
		},
		{
			name: "fills gap",
			c:    models.RecommendationCandidate{SeriesID: &sid, SeriesPos: "2"},
			want: 0.5,
		},
		{
			name: "random position",
			c:    models.RecommendationCandidate{SeriesID: &sid, SeriesPos: "8"},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seriesScore(tt.c, profile); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommunityScore(t *testing.T) {
	// No ratings: should be 0
	c := models.RecommendationCandidate{Rating: 0, RatingsCount: 0}
	if got := communityScore(c); got != 0 {
		t.Errorf("zero ratings: got %v, want 0", got)
	}

	// Max rating + 1000 ratings ≈ 1.0
	c = models.RecommendationCandidate{Rating: 5.0, RatingsCount: 1000}
	got := communityScore(c)
	if got < 0.99 || got > 1.01 {
		t.Errorf("max-ratings score: got %v, want ~1.0", got)
	}

	// Should monotonically increase with count
	low := communityScore(models.RecommendationCandidate{Rating: 4.0, RatingsCount: 10})
	high := communityScore(models.RecommendationCandidate{Rating: 4.0, RatingsCount: 100})
	if high <= low {
		t.Errorf("more ratings should score higher: low=%v high=%v", low, high)
	}
}

func TestRecencyScore(t *testing.T) {
	// No date: neutral
	c := models.RecommendationCandidate{}
	if got := recencyScore(c); got != 0.5 {
		t.Errorf("no date: got %v, want 0.5", got)
	}

	// This year: 1.0
	thisYear := time.Date(time.Now().Year(), 6, 1, 0, 0, 0, 0, time.UTC)
	c = models.RecommendationCandidate{ReleaseDate: &thisYear}
	if got := recencyScore(c); got != 1.0 {
		t.Errorf("current year: got %v, want 1.0", got)
	}

	// 20 years ago: 0.3
	old := time.Date(time.Now().Year()-20, 1, 1, 0, 0, 0, 0, time.UTC)
	c = models.RecommendationCandidate{ReleaseDate: &old}
	if got := recencyScore(c); got != 0.3 {
		t.Errorf("20 years ago: got %v, want 0.3", got)
	}

	// Very old: clamped at 0.3
	ancient := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	c = models.RecommendationCandidate{ReleaseDate: &ancient}
	if got := recencyScore(c); got != 0.3 {
		t.Errorf("ancient: got %v, want 0.3", got)
	}

	// Future year: clamp to 0 years ago, should be 1.0.
	future := time.Date(time.Now().Year()+5, 1, 1, 0, 0, 0, 0, time.UTC)
	c = models.RecommendationCandidate{ReleaseDate: &future}
	if got := recencyScore(c); got != 1.0 {
		t.Errorf("future date: got %v, want 1.0", got)
	}
}

func TestScore_SeriesBonus(t *testing.T) {
	sid := int64(1)
	profile := &UserProfile{
		SeriesState: map[int64]SeriesState{
			1: {SeriesID: 1, MaxPosition: 1},
		},
	}
	c := models.RecommendationCandidate{
		RecType:   models.RecTypeSeries,
		SeriesID:  &sid,
		SeriesPos: "2",
	}
	got := Score(c, profile)
	if got <= 0.2 {
		t.Errorf("series next-in-sequence should score well (got %v)", got)
	}
	if got > 1.0 {
		t.Errorf("score should be clamped to <= 1.0, got %v", got)
	}
}

func TestScore_AuthorNewBonus(t *testing.T) {
	aid := int64(42)
	profile := &UserProfile{
		MonitoredAuthors: map[int64]bool{42: true},
	}
	c := models.RecommendationCandidate{
		RecType:  models.RecTypeAuthorNew,
		AuthorID: &aid,
	}
	withBonus := Score(c, profile)
	// Without monitored-author bonus
	c2 := c
	profile2 := &UserProfile{}
	without := Score(c2, profile2)
	if withBonus <= without {
		t.Errorf("author-new bonus should lift score: with=%v without=%v", withBonus, without)
	}
}

func TestScore_ClampedToOne(t *testing.T) {
	// Construct a candidate that maxes out every sub-score to exercise the clamp.
	aid := int64(1)
	sid := int64(1)
	now := time.Now()
	c := models.RecommendationCandidate{
		RecType:      models.RecTypeSeries,
		AuthorID:     &aid,
		SeriesID:     &sid,
		SeriesPos:    "2",
		Genres:       []string{"fantasy"},
		Rating:       5.0,
		RatingsCount: 10000,
		ReleaseDate:  &now,
	}
	profile := &UserProfile{
		GenreWeights:     map[string]float64{"fantasy": 1.0},
		MonitoredAuthors: map[int64]bool{1: true},
		SeriesState:      map[int64]SeriesState{1: {SeriesID: 1, MaxPosition: 1}},
	}
	got := Score(c, profile)
	if got > 1.0 || got < 0 {
		t.Errorf("Score must be in [0,1], got %v", got)
	}
}
