package recommender

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Engine orchestrates the recommendation pipeline.
type Engine struct {
	books    *db.BookRepo
	authors  *db.AuthorRepo
	series   *db.SeriesRepo
	recs     *db.RecommendationRepo
	settings *db.SettingsRepo
	olClient SubjectBooksFetcher // optional; enables genre-popular candidates
	hcClient WishlistFetcher     // optional; enables list-cross candidates
}

// New creates a new recommendation engine.
func New(
	books *db.BookRepo,
	authors *db.AuthorRepo,
	series *db.SeriesRepo,
	recs *db.RecommendationRepo,
	settings *db.SettingsRepo,
) *Engine {
	return &Engine{
		books:    books,
		authors:  authors,
		series:   series,
		recs:     recs,
		settings: settings,
	}
}

// WithOLClient wires in an OpenLibrary client for genre-popular candidates.
// Must be called before the first Run.
func (e *Engine) WithOLClient(c SubjectBooksFetcher) *Engine {
	e.olClient = c
	return e
}

// WithHCClient wires in a Hardcover client for list-cross candidates.
// The client must already have a Bearer token set. Must be called before the first Run.
func (e *Engine) WithHCClient(c WishlistFetcher) *Engine {
	e.hcClient = c
	return e
}

// Run generates recommendations for the given user. It builds a taste profile,
// generates candidates from multiple sources, scores and ranks them, injects
// serendipity picks, and persists the top 100.
func (e *Engine) Run(ctx context.Context, userID int64) error {
	// Check if recommendations are enabled.
	if e.settings != nil {
		s, _ := e.settings.Get(ctx, "recommendations.enabled")
		if s == nil || s.Value != "true" {
			slog.Info("recommender: disabled, skipping")
			return nil
		}
	}

	slog.Info("recommender: building profile", "userId", userID)
	profile, err := BuildProfile(ctx, userID, e.books, e.authors, e.series, e.recs, e.settings)
	if err != nil {
		return err
	}

	var candidates []models.RecommendationCandidate

	// Always generate series and author-new candidates.
	series := GenerateSeries(ctx, e.books, e.series, profile)
	candidates = append(candidates, series...)
	slog.Info("recommender: series candidates", "count", len(series))

	authorNew := GenerateAuthorNew(ctx, e.books, e.authors, profile)
	candidates = append(candidates, authorNew...)
	slog.Info("recommender: author-new candidates", "count", len(authorNew))

	// Cold-start: skip genre scoring if < 20 books.
	if profile.TotalBooks >= 20 {
		genreSimilar := GenerateGenreSimilar(ctx, e.books, e.series, profile)
		candidates = append(candidates, genreSimilar...)
		slog.Info("recommender: genre-similar candidates", "count", len(genreSimilar))

		// Genre-popular: top 5 genres × OpenLibrary subjects API (5 calls).
		if e.olClient != nil {
			genrePopular := GenerateGenrePopular(ctx, e.olClient, profile, 5, 20)
			candidates = append(candidates, genrePopular...)
			slog.Info("recommender: genre-popular candidates", "count", len(genrePopular))
		}
	} else {
		slog.Info("recommender: cold start (< 20 books), skipping genre scoring")
	}

	// List cross-reference: books on user's external wishlist not in library.
	if e.hcClient != nil {
		listCross := GenerateListCross(ctx, e.hcClient, profile, 100)
		candidates = append(candidates, listCross...)
		slog.Info("recommender: list-cross candidates", "count", len(listCross))
	}

	// Score all candidates.
	for i := range candidates {
		candidates[i].Score = Score(candidates[i], profile)
	}

	// Sort by score descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	// Determine serendipity allocation.
	serendipityCount := 10
	scoredCount := 90
	if len(candidates) < 100 {
		serendipityCount = max(1, len(candidates)/20) // ~5%
		scoredCount = len(candidates)
	}

	// Truncate scored candidates.
	if len(candidates) > scoredCount {
		candidates = candidates[:scoredCount]
	}

	// Inject serendipity picks (only with enough books for genre data).
	if profile.TotalBooks >= 20 {
		serendipity := GenerateSerendipity(ctx, e.books, e.series, profile, serendipityCount)
		for i := range serendipity {
			serendipity[i].Score = Score(serendipity[i], profile)
		}
		candidates = append(candidates, serendipity...)
		slog.Info("recommender: serendipity candidates", "count", len(serendipity))
	}

	// Hard-filter: remove already-owned, dismissed, or excluded-author candidates.
	candidates = hardFilter(candidates, profile)

	// Take top 100.
	if len(candidates) > 100 {
		candidates = candidates[:100]
	}

	slog.Info("recommender: persisting", "count", len(candidates), "userId", userID)
	return e.recs.ReplaceBatch(ctx, userID, candidates)
}

// hardFilter removes candidates that should not be shown.
func hardFilter(candidates []models.RecommendationCandidate, p *UserProfile) []models.RecommendationCandidate {
	var filtered []models.RecommendationCandidate
	seen := make(map[string]bool)
	for _, c := range candidates {
		if p.OwnedForeignIDs[c.ForeignID] {
			continue
		}
		if p.DismissedForeignIDs[c.ForeignID] {
			continue
		}
		if c.AuthorName != "" && p.ExcludedAuthors[strings.ToLower(c.AuthorName)] {
			continue
		}
		if p.PreferredLanguage != "" && c.Language != "" && c.Language != p.PreferredLanguage {
			continue
		}
		// Suppress candidates with too few ratings (likely obscure editions or test entries).
		if c.RatingsCount < 50 {
			continue
		}
		// Suppress objectively poor books — only apply when there are enough ratings to trust the score.
		if c.RatingsCount >= 50 && c.Rating > 0 && c.Rating < 3.0 {
			continue
		}
		// Deduplicate by foreign ID.
		if seen[c.ForeignID] {
			continue
		}
		seen[c.ForeignID] = true
		filtered = append(filtered, c)
	}
	return filtered
}
