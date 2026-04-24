package recommender

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// junkGenres are OpenLibrary subjects that add noise, not signal.
var junkGenres = map[string]bool{
	"accessible book":  true,
	"protected daisy":  true,
	"in library":       true,
	"large type books": true,
	"nonfiction":       true,
}

// BuildProfile analyses the user's library and constructs a UserProfile used
// for scoring recommendation candidates.
func BuildProfile(
	ctx context.Context,
	userID int64,
	books *db.BookRepo,
	authors *db.AuthorRepo,
	series *db.SeriesRepo,
	recs *db.RecommendationRepo,
	settings *db.SettingsRepo,
) (*UserProfile, error) {
	p := &UserProfile{
		GenreWeights:        make(map[string]float64),
		MonitoredAuthors:    make(map[int64]bool),
		AuthorBookCounts:    make(map[int64]int),
		SeriesState:         make(map[int64]SeriesState),
		OwnedForeignIDs:     make(map[string]bool),
		DismissedForeignIDs: make(map[string]bool),
		ExcludedAuthors:     make(map[string]bool),
		PreferredLanguage:   "en",
	}

	// Load preferred language from settings.
	if settings != nil {
		if s, _ := settings.Get(ctx, "search.preferredLanguage"); s != nil && s.Value != "" {
			p.PreferredLanguage = s.Value
		}
	}

	// Load all books to build the profile.
	allBooks, err := books.List(ctx)
	if err != nil {
		return nil, err
	}
	p.TotalBooks = len(allBooks)
	p.LibraryMedianYear = medianReleaseYear(allBooks)

	// Genre frequency counts and per-genre document counts (for IDF).
	genreDocCount := make(map[string]int)

	for _, b := range allBooks {
		if b.Status == models.BookStatusDownloaded || b.Status == models.BookStatusImported {
			p.OwnedForeignIDs[b.ForeignID] = true
		}
		p.AuthorBookCounts[b.AuthorID]++

		seen := make(map[string]bool)
		for _, g := range b.Genres {
			g = strings.ToLower(strings.TrimSpace(g))
			if g == "" || junkGenres[g] {
				continue
			}
			if !seen[g] {
				genreDocCount[g]++
				seen[g] = true
			}
		}
	}
	totalUniqueGenres := len(genreDocCount)

	// Compute TF-IDF genre weights.
	if p.TotalBooks > 0 && totalUniqueGenres > 0 {
		for genre, docCount := range genreDocCount {
			tf := float64(docCount) / float64(p.TotalBooks)
			idf := math.Log(float64(totalUniqueGenres) / float64(docCount))
			p.GenreWeights[genre] = tf * idf
		}
	}

	// Build monitored authors set.
	allAuthors, err := authors.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range allAuthors {
		if a.Monitored {
			p.MonitoredAuthors[a.ID] = true
		}
	}

	// Build series state from all series with their books.
	allSeries, err := series.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range allSeries {
		full, err := series.GetByID(ctx, s.ID)
		if err != nil || full == nil || len(full.Books) < 2 {
			continue
		}

		var maxPos float64
		ownedPositions := make(map[float64]bool)
		allPositions := make(map[float64]bool)

		for _, sb := range full.Books {
			pos := parsePosition(sb.PositionInSeries)
			if pos <= 0 {
				continue
			}
			allPositions[pos] = true
			if sb.Book != nil && p.OwnedForeignIDs[sb.Book.ForeignID] {
				ownedPositions[pos] = true
				if pos > maxPos {
					maxPos = pos
				}
			}
		}

		var missing []float64
		for pos := range allPositions {
			if !ownedPositions[pos] && pos <= maxPos {
				missing = append(missing, pos)
			}
		}

		if maxPos > 0 {
			p.SeriesState[s.ID] = SeriesState{
				SeriesID:         s.ID,
				SeriesTitle:      s.Title,
				MaxPosition:      maxPos,
				MissingPositions: missing,
			}
		}
	}

	// Load dismissed foreign IDs.
	dismissed, err := recs.ListDismissedIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	p.DismissedForeignIDs = dismissed

	// Load excluded authors.
	excluded, err := recs.ListAuthorExclusions(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, name := range excluded {
		p.ExcludedAuthors[strings.ToLower(name)] = true
	}

	return p, nil
}

// medianReleaseYear returns the median publication year across all books that
// have a non-nil ReleaseDate. Returns 0 when no dated books exist.
func medianReleaseYear(books []models.Book) int {
	years := make([]int, 0, len(books))
	for _, b := range books {
		if b.ReleaseDate != nil {
			years = append(years, b.ReleaseDate.Year())
		}
	}
	if len(years) == 0 {
		return 0
	}
	sort.Ints(years)
	return years[len(years)/2]
}

// parsePosition converts a series position string like "1", "2.5" to float64.
// Returns 0 for empty or unparseable values.
func parsePosition(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
