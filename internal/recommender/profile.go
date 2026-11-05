package recommender

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// junkGenres are OpenLibrary subjects that add noise, not signal.
var junkGenres = map[string]bool{
	"accessible book":  true,
	"protected daisy":  true,
	"in library":       true,
	"large type books": true,
	"nonfiction":       true,
}

// BuildProfile analyses the whole library and constructs a UserProfile used
// for scoring recommendation candidates. It is BuildScopedProfile with scope
// 0, the instance wide profile an admin gets.
func BuildProfile(
	ctx context.Context,
	userID int64,
	books *db.BookRepo,
	authors *db.AuthorRepo,
	series *db.SeriesRepo,
	recs *db.RecommendationRepo,
	settings *db.SettingsRepo,
) (*UserProfile, error) {
	return BuildScopedProfile(ctx, userID, 0, books, authors, series, recs, settings)
}

// BuildScopedProfile builds the profile from the books and authors visible to
// scope (that user's own rows plus unowned ones), or from everything when
// scope is 0. Another user's books never count as owned, never shape the
// genre weights and never mark a series as started.
func BuildScopedProfile(
	ctx context.Context,
	userID int64,
	scope int64,
	books *db.BookRepo,
	authors *db.AuthorRepo,
	series *db.SeriesRepo,
	recs *db.RecommendationRepo,
	settings *db.SettingsRepo,
) (*UserProfile, error) {
	p := &UserProfile{
		Scope:               scope,
		GenreWeights:        make(map[string]float64),
		MonitoredAuthors:    make(map[int64]bool),
		AuthorBookCounts:    make(map[int64]int),
		SeriesState:         make(map[int64]SeriesState),
		OwnedForeignIDs:     make(map[string]bool),
		OwnedWorkKeys:       make(map[string]bool),
		AuthorNames:         make(map[int64]string),
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

	// Load authors before the book loop: the owned-work keys need each book's
	// author name, and the monitored set comes from the same list.
	allAuthors, err := authors.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range allAuthors {
		if scope != 0 && a.OwnerUserID != 0 && a.OwnerUserID != scope {
			continue
		}
		p.AuthorNames[a.ID] = a.Name
		if a.Monitored {
			p.MonitoredAuthors[a.ID] = true
		}
	}

	// Load all books to build the profile.
	listed, err := books.List(ctx)
	if err != nil {
		return nil, err
	}
	allBooks := listed[:0:0]
	for i := range listed {
		if p.visible(&listed[i]) {
			allBooks = append(allBooks, listed[i])
		}
	}
	p.TotalBooks = len(allBooks)
	p.LibraryMedianYear = medianReleaseYear(allBooks)

	// Genre frequency counts and per-genre document counts (for IDF).
	genreDocCount := make(map[string]int)

	for _, b := range allBooks {
		// Wanted counts as owned for suppression: recommending a book the
		// user has already decided they want is never useful (#1726).
		if b.Status == models.BookStatusImported || b.Status == models.BookStatusWanted {
			p.OwnedForeignIDs[b.ForeignID] = true
			for _, key := range ownedWorkKeys(b.Title, p.AuthorNames[b.AuthorID]) {
				p.OwnedWorkKeys[key] = true
			}
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

	// Compute TF-IDF genre weights, with the books as the documents. The IDF
	// divided by the number of distinct genres, so any genre on more books
	// than there are genres, the library's main genre most of all, weighed
	// negatively and dropped out of the top genres (#3172). Over the books it
	// is never negative, and still discounts a genre on nearly every book.
	if p.TotalBooks > 0 && totalUniqueGenres > 0 {
		for genre, docCount := range genreDocCount {
			tf := float64(docCount) / float64(p.TotalBooks)
			idf := math.Log(float64(p.TotalBooks) / float64(docCount))
			p.GenreWeights[genre] = tf * idf
		}
	}

	// Build series state from all series with their books.
	allSeries, err := series.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range allSeries {
		full, err := series.GetByIDForUser(ctx, s.ID, scope)
		if err != nil || full == nil || len(full.Books) < 2 {
			continue
		}

		var maxPos float64
		ownedPositions := make(map[float64]bool)
		allPositions := make(map[float64]bool)

		for _, sb := range full.Books {
			pos := parsePosition(sb.PositionInSeries)
			if pos <= 0 || !p.visible(sb.Book) {
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

// ownedWorkKeys returns the work-identity keys a book is owned under: the
// canonical title dedup key (#940) crossed with every normalized variant of
// the author name. Variants — not a single fold — because candidate author
// names are provider strings ("Card, Orson Scott", ASCII-ised umlauts) that
// need not equal the local author row; NormalizeAuthorNameWithVariants is the
// shared post-#1647 author comparison, and hand-rolling a different fold here
// is the drift class #1648 exists to prevent. Returns nil when either the
// title or the author name folds to nothing — the author dimension is
// mandatory, so such books are only matchable by ForeignID.
func ownedWorkKeys(title, authorName string) []string {
	dedup := indexer.CanonicalDedupKey(title)
	if dedup == "" {
		return nil
	}
	variants := textutil.NormalizeAuthorNameWithVariants(authorName)
	keys := make([]string, 0, len(variants))
	for _, v := range variants {
		keys = append(keys, dedup+"\x00"+v)
	}
	return keys
}

// IsOwned reports whether the user already has this work: first by exact
// ForeignID, then by work identity (title dedup key + author name). The
// second check is what catches the same work owned via a different provider
// (candidate "hc:ender-in-exile" vs owned "OL49485W", #1726). The key
// deliberately includes the author: a title-only match would silently
// suppress same-titled books by different authors, and a missing
// recommendation is harder to notice than a duplicate one.
func (p *UserProfile) IsOwned(foreignID, title, authorName string) bool {
	if p.OwnedForeignIDs[foreignID] {
		return true
	}
	for _, key := range ownedWorkKeys(title, authorName) {
		if p.OwnedWorkKeys[key] {
			return true
		}
	}
	return false
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
// positionEpsilon is the tolerance for comparing parsed series positions.
// Positions are small integers or short decimals (e.g. "2.5"), so any pair
// closer than this is treated as equal — avoiding exact float64 == pitfalls.
const positionEpsilon = 1e-9

// floatEqual reports whether a and b are within positionEpsilon of each other.
func floatEqual(a, b float64) bool {
	return math.Abs(a-b) < positionEpsilon
}

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
